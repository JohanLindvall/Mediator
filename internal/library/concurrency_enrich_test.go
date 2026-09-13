package library

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// indexOneFile writes a file, indexes it, and hands back the library and the
// item as the index holds it. The library has a database attached, since what
// enrichment writes down — the metadata record, the dirty set — is only
// observable where there is somewhere to write it.
func indexOneFile(t *testing.T, name string, bytes int) (*Library, Item) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, bytes), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	it, ok := l.Get(PathID(path))
	if !ok {
		t.Fatalf("indexing %s produced nothing", name)
	}
	return l, it
}

// grow rewrites the file larger and walks again, which is what the watcher's
// upsert does when a download finishes: the size and time move and
// forgetContent drops everything that was read from the old bytes.
func grow(t *testing.T, l *Library, it Item, bytes int) Item {
	t.Helper()
	if err := os.WriteFile(it.Path, make([]byte, bytes), 0o644); err != nil {
		t.Fatal(err)
	}
	// A walk within the same millisecond would leave the modification time
	// where it was, and then nothing has changed as far as the index knows.
	later := time.UnixMilli(it.ModTime).Add(2 * time.Second)
	if err := os.Chtimes(it.Path, later, later); err != nil {
		t.Fatal(err)
	}
	l.Scan(nil)
	fresh, ok := l.Get(it.ID)
	if !ok {
		t.Fatal("the file left the index when it grew")
	}
	if fresh.Size == it.Size && fresh.ModTime == it.ModTime {
		t.Fatal("the walk did not notice the file had changed")
	}
	return fresh
}

// replaceUnderIt does to the index exactly what upsert does when the file has
// changed on disk: the final size and time, and everything read from the old
// bytes forgotten.
func replaceUnderIt(l *Library, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if it, ok := l.items[id]; ok {
		it.Size += 5000
		it.ModTime += 2000
		it.forgetContent()
		l.markDirty(id)
	}
}

// openWindow arranges for the file to be replaced the first time enrichment
// reaches one of its two windows — before the check, or after the write —
// which is the only way to put a download finishing inside one on purpose
// rather than once in a thousand arrivals. It hands back a function reporting
// how often the window was reached at all: a guard that has been deleted
// never opens its window, and a test that then passed would be pinning
// nothing.
func openWindow(t *testing.T, l *Library, install func(func(string)), extra func(id string)) func() int {
	t.Helper()
	var mu sync.Mutex
	seen := 0
	install(func(id string) {
		mu.Lock()
		seen++
		first := seen == 1
		mu.Unlock()
		if !first {
			return
		}
		replaceUnderIt(l, id)
		if extra != nil {
			extra(id)
		}
	})
	t.Cleanup(func() { install(nil) })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

// countWrites counts the commits without disturbing any of them.
func countWrites(t *testing.T) func() int {
	t.Helper()
	var mu sync.Mutex
	seen := 0
	beforeWrite = func(string) {
		mu.Lock()
		seen++
		mu.Unlock()
	}
	t.Cleanup(func() { beforeWrite = nil })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

// openReadWindow replaces the file between the read and the check.
func openReadWindow(t *testing.T, l *Library) func() int {
	return openWindow(t, l, func(f func(string)) { afterRead = f }, nil)
}

// openWriteWindow replaces it between the check and the write, which is where
// the two being separate turns of the lock lets the reading land after all.
func openWriteWindow(t *testing.T, l *Library, extra func(id string)) func() int {
	return openWindow(t, l, func(f func(string)) { beforeWrite = f }, extra)
}

// indexOneClip builds a real (tiny) video and indexes it, so that what
// enrichment reads is an actual duration and an actual codec: a file of zeros
// yields nothing, and a test whose reading is empty cannot tell a guard that
// refused the write from a write that had nothing in it.
func indexOneClip(t *testing.T, name string) (*Library, Item) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	buildClip(t, path, 1) // skips where ffmpeg or ffprobe is missing
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	it, ok := l.Get(PathID(path))
	if !ok {
		t.Fatal("indexing the clip produced nothing")
	}
	return l, it
}

// pendingMeta is what enrichment has queued for the next bulk write.
func (l *Library) pendingMeta(id string) (blob.Meta, bool) {
	l.metaPendMu.Lock()
	defer l.metaPendMu.Unlock()
	m, ok := l.metaPending[id]
	return m, ok
}

// A reading belongs to the bytes it was read from. enrichOne snapshots an
// item, spends as long as the file takes to read — a probe's own ceiling is
// half a minute — and only then writes back. A download that finishes inside
// that window leaves the index holding the finished file under its final size
// and time, which is the one key that never changes again: the partial file's
// duration, codecs and tags written there are permanent, the record being
// persisted and restored and nothing ever re-reading a file that says it has
// been read.
func TestEnrichmentRefusesAReadingOfBytesThatAreGone(t *testing.T) {
	l, it := indexOneClip(t, "Harbour Lights.mp4")
	windows := openReadWindow(t, l)

	l.enrichOne(context.Background(), it.ID)

	if windows() == 0 {
		t.Fatal("nothing asked whether the file was still the one that had been read")
	}
	got, _ := l.Get(it.ID)
	if got.enriched {
		t.Fatal("a file nobody read was marked examined, which nothing ever undoes")
	}
	if got.Duration != 0 || got.VCodec != "" || got.probed {
		t.Fatalf("the replaced file wears the old bytes' reading: %+v", got)
	}
	if m, ok := l.pendingMeta(it.ID); ok {
		t.Fatalf("a record of the old bytes was queued under the new file's key: %+v", m)
	}
}

// The check before the write and the write itself are two turns of the lock,
// so the file can still be replaced in between — and then the reading has
// already landed. Looking again afterwards is what takes it off.
func TestAReadingThatLandedOnAReplacedFileIsForgotten(t *testing.T) {
	l, it := indexOneClip(t, "Harbour Lights.mp4")
	windows := openWriteWindow(t, l, nil)

	l.enrichOne(context.Background(), it.ID)

	if windows() == 0 {
		t.Fatal("the reading was never looked at again after it was written")
	}
	got, _ := l.Get(it.ID)
	if got.Duration != 0 || got.VCodec != "" || got.probed {
		t.Fatalf("the replaced file kept the old bytes' reading: %+v", got)
	}
	if got.enriched {
		t.Fatal("a file nobody read was marked examined, which nothing ever undoes")
	}
}

// forgetContent changes what is persisted, so the item has to be marked dirty
// again. The writes a moment earlier did mark it — and the persist loop may
// have flushed in between, taking the dirty bit with it, which is exactly the
// window this stands in: without it the polluted record survives the restart,
// which is the whole of what is being prevented.
func TestForgettingAPollutedReadingIsWrittenDown(t *testing.T) {
	l, it := indexOneFile(t, "Harbour Lights.mp3", 4000)
	l.setProbe(it.ID, Probe{DurationMs: 720_000, VCodec: "theora", Probed: true})
	replaceUnderIt(l, it.ID)
	// The persist loop has flushed since that write: the record is on disk
	// and the dirty bit is gone with it.
	l.mu.Lock()
	delete(l.dirty, it.ID)
	l.mu.Unlock()

	if l.keptReading(it.ID, it) {
		t.Fatal("a reading written onto a replaced file was kept")
	}
	l.mu.RLock()
	_, dirty := l.dirty[it.ID]
	l.mu.RUnlock()
	if !dirty {
		t.Fatal("the item that had its reading taken off was not queued for writing again")
	}
}

// What the container declares is not the file's to forget. A DVD says how
// long its title is and nothing else can, so upsertStored puts it back after
// its own forgetContent — and neither setMeta nor setProbe may restore it
// afterwards (declaresDuration). Forgetting it here would leave a disc title
// with no playing time at all until the next full rescan.
func TestForgettingKeepsWhatTheContainerDeclares(t *testing.T) {
	l := quietLib(t.TempDir())
	e := &storedEntry{name: "Feature.vob", size: 4000, durationMs: 3_042_800}
	l.upsertStored(filepath.Join(t.TempDir(), "disc.iso"), e, time.UnixMilli(1000))

	var snap Item
	for _, it := range l.List(Query{}).Items {
		snap = it
	}
	if snap.ID == "" || snap.Duration != e.durationMs {
		t.Fatalf("the title was not indexed with the length the disc declares: %+v", snap)
	}
	replaceUnderIt(l, snap.ID)

	if l.keptReading(snap.ID, snap) {
		t.Fatal("a reading written onto a replaced title was kept")
	}
	got, _ := l.Get(snap.ID)
	if got.Duration != e.durationMs {
		t.Fatalf("the disc's own length was forgotten with the rest: %+v", got)
	}
}

// The cache-hit branch commits what the record already holds before it goes
// looking for the picture's size — a probe that can run for half a minute —
// and again afterwards. Committing the two together withheld the cached
// title, performer and playing time for the whole of that probe, which is a
// reading of something else entirely: a tile does not wait on a frame rate.
func TestTheCachedReadingLandsBeforeTheShapeIsLookedFor(t *testing.T) {
	l, it := indexOneClip(t, "Harbour Lights.mp4")
	if err := l.metaDB.PutMeta(it.ID, it.ModTime, it.Size, blob.Meta{
		MTime: it.ModTime, Size: it.Size, Duration: 61_000,
		Title: "Harbour Lights", Artist: "The Invented Band",
		// Written before there was anywhere to keep the picture's shape,
		// which is what sends this down the reading path at all.
		Shape: 0,
	}); err != nil {
		t.Fatal(err)
	}
	commits := countWrites(t)

	l.enrichOne(context.Background(), it.ID)

	if n := commits(); n < 2 {
		t.Fatalf("the cached reading was committed %d time(s): the record's own title and "+
			"playing time waited on a reading of the picture's size", n)
	}
}

// EnsureCodecs has the same window and a worse consequence: `probed` is
// sticky and is exactly what forgetContent clears, so a late answer written
// onto a replaced file re-arms it carrying the old file's soundtracks and
// captions — a menu of languages the file does not hold, and a conversion
// mapping a stream that is not there.
func TestEnsureCodecsRefusesAProbeOfBytesThatAreGone(t *testing.T) {
	l, it := indexOneClip(t, "Harbour Lights.mp4")
	windows := openReadWindow(t, l)

	l.EnsureCodecs(context.Background(), it.ID)

	if windows() == 0 {
		t.Fatal("nothing asked whether the file was still the one that had been probed")
	}
	got, _ := l.Get(it.ID)
	if got.probed {
		t.Fatal("a replaced file was marked probed, which spends its one second chance on bytes nobody read")
	}
	if got.VCodec != "" || got.Duration != 0 {
		t.Fatalf("the replaced file wears the old bytes' probe: %+v", got)
	}
	if m, ok := l.pendingMeta(it.ID); ok {
		t.Fatalf("a record of the old bytes was queued under the new file's key: %+v", m)
	}
}

// Every watcher event arms a timer of its own. Resetting the one in the map
// cannot tell a timer that is still waiting from one that has already fired
// and is merely blocked on the mutex — re-arming the second put a timer
// nobody was tracking back on the clock.
func TestEveryEventArmsItsOwnTimer(t *testing.T) {
	defer withQuiet(t, time.Hour)()
	l := quietLib(t.TempDir())

	l.enrichAfterQuiet("nothing-here")
	first := l.pendingRead("nothing-here")
	l.enrichAfterQuiet("nothing-here")
	second := l.pendingRead("nothing-here")

	if first == nil || second == nil {
		t.Fatal("the read was not armed")
	}
	if first == second {
		t.Fatal("the second event re-armed the first timer instead of arming its own")
	}
	if first.Stop() {
		t.Fatal("the timer the second event replaced was left running")
	}
}

// And a timer that fires after the map has moved on gives way. It used to
// delete whatever entry it found, which orphaned the live timer that owned
// it: the next event armed a second one for the same file, and from then on
// the reads landed while the writer was still going.
func TestAFiredTimerLeavesTheLiveOneAlone(t *testing.T) {
	defer withQuiet(t, time.Hour)()
	l := quietLib(t.TempDir())

	l.enrichAfterQuiet("nothing-here")
	stale := l.pendingRead("nothing-here")
	l.enrichAfterQuiet("nothing-here")
	live := l.pendingRead("nothing-here")

	// Fire the one the second event replaced. It has no entry of its own any
	// more, so it must do nothing at all.
	// It fires at once, so this is a wait for the wrong thing to happen
	// rather than a race: the old callback deleted the entry it found the
	// moment it got the mutex.
	stale.Reset(0)
	deadline := time.Now().Add(250 * time.Millisecond)
	for l.pendingRead("nothing-here") == live && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if l.pendingRead("nothing-here") != live {
		t.Fatal("a timer that had already been replaced deleted the live entry")
	}
}

// The watcher's reads stand down for playback like every other reader in this
// tier, and — unlike the sweep's wait — the standing down is bounded: the
// waiter holds one of a handful of process-wide slots, so an evening's
// viewing must not stop the watcher reading anything at all.
func TestTheWatchersReadStandsDownForPlayback(t *testing.T) {
	l := quietLib(t.TempDir())
	was := enrichBusyWait
	enrichBusyWait = 60 * time.Millisecond
	defer func() { enrichBusyWait = was }()

	start := time.Now()
	l.standDownForPlayback()
	if waited := time.Since(start); waited > 20*time.Millisecond {
		t.Fatalf("waited %v with nothing playing", waited)
	}

	done := l.StartStream()
	defer done()
	start = time.Now()
	l.standDownForPlayback()
	waited := time.Since(start)
	if waited < 40*time.Millisecond {
		t.Fatalf("read went ahead after %v while a film was being served", waited)
	}
	if waited > 2*time.Second {
		t.Fatalf("waited %v, which is not a bound", waited)
	}
}

// And the read the watcher actually arms is the one that stands down: the
// gate belongs on that path, not merely in a function beside it.
func TestTheArmedReadWaitsForTheFilmToFinish(t *testing.T) {
	l, it := indexOneFile(t, "Harbour Lights.mp3", 4000)
	defer withQuiet(t, time.Millisecond)()
	was := enrichBusyWait
	enrichBusyWait = time.Minute
	defer func() { enrichBusyWait = was }()

	done := l.StartStream()
	l.enrichAfterQuiet(it.ID)
	time.Sleep(200 * time.Millisecond)
	if got, _ := l.Get(it.ID); got.enriched {
		done()
		t.Fatal("the watcher read the file while a film was being served")
	}

	done()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := l.Get(it.ID); got.enriched {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the read never went ahead after the film stopped")
}

// withQuiet shortens (or lengthens) the debounce for one test.
func withQuiet(t *testing.T, d time.Duration) func() {
	t.Helper()
	was := enrichQuiet
	enrichQuiet = d
	return func() { enrichQuiet = was }
}

// pendingRead is the timer currently armed for an id, if any.
func (l *Library) pendingRead(id string) *time.Timer {
	l.enrichDebMu.Lock()
	defer l.enrichDebMu.Unlock()
	return l.enrichDeb[id]
}

// A worker that gives up while waiting for playback to go quiet is already
// holding an id nobody else can see, and the feeder has counted it — so the
// pass has not got to the end of its list, whatever the count says.
func TestAPassThatDroppedAnItemIsNotComplete(t *testing.T) {
	l := quietLib(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	took := make(chan struct{})
	var once sync.Once
	busy := func() bool {
		once.Do(func() { close(took) })
		return true
	}
	go func() {
		<-took // the worker has the id and is waiting for the disk
		cancel()
	}()

	if l.enrichAll(ctx, []string{"nothing-here"}, busy) {
		t.Fatal("a pass that abandoned the item it held reported it had got to the end")
	}
}

// A run a deadline killed is not a file that had nothing to say. The two come
// back looking exactly alike, and only one of them may be written down.
func TestAKilledProbeSaysItWasCutShort(t *testing.T) {
	if FFprobePath() == "" {
		t.Skip("ffprobe not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mkv")
	if err := os.WriteFile(path, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := ffprobe(ctx, path, nil)
	if out.answered {
		t.Fatal("a probe that never ran reported an answer")
	}
	if !out.cutShort {
		t.Fatal("a probe a deadline killed read as a file with nothing to say")
	}
}

// And the ceiling ffprobe installs of its own is the half of this that the
// caller cannot see: it fires on a disk that has gone slow while the caller's
// context is perfectly alive, and the empty result is otherwise
// indistinguishable from a container with nothing in it.
func TestAProbeStoppedByItsOwnCeilingSaysSo(t *testing.T) {
	if FFprobePath() == "" {
		t.Skip("ffprobe not installed")
	}
	// A named pipe with nobody writing to it is a file whose open never
	// returns — which is what a device that has stopped answering looks like
	// from here, and the one way to reach the ceiling without waiting it out.
	path := filepath.Join(t.TempDir(), "clip.mkv")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("no named pipes here: %v", err)
	}
	was := ffprobeTimeout
	ffprobeTimeout = 200 * time.Millisecond
	defer func() { ffprobeTimeout = was }()

	ctx := context.Background()
	out := ffprobe(ctx, path, nil)
	if ctx.Err() != nil {
		t.Fatal("the caller's context was the thing that expired")
	}
	if out.answered {
		t.Fatal("a probe that was killed reported an answer")
	}
	if !out.cutShort {
		t.Fatal("a probe its own ceiling killed read as a file with nothing to say")
	}
}

// Being stopped is not a fact about the file, so nothing is written down —
// and the file is not read again in this run either. needsEnrich stays true,
// so every listing page holding it would otherwise ask for it again, which on
// a disk that has gone slow is half a minute of ffprobe per page for the life
// of the process. The next start gets to try.
func TestAFileWhoseProbeWasCutShortIsLeftAloneThisRun(t *testing.T) {
	l, it := indexOneFile(t, "Harbour Lights.mkv", 4000)
	enrichCutShort.note(it)

	l.enrichOne(context.Background(), it.ID)
	got, _ := l.Get(it.ID)
	if got.enriched {
		t.Fatal("a file whose probe was cut short was written off as examined")
	}
	if _, ok := l.pendingMeta(it.ID); ok {
		t.Fatal("a reading nobody was allowed to finish was queued for the database")
	}

	// And the memory is of those bytes, not of that name: the file changing
	// on disk is a different file, and it is read.
	fresh := grow(t, l, it, 9000)
	l.enrichOne(context.Background(), fresh.ID)
	if got, _ = l.Get(fresh.ID); !got.enriched {
		t.Fatal("the file was not looked at after it changed on disk")
	}
}
