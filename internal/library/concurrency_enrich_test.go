package library

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// indexOneTrack writes a file, indexes it, and hands back the library and the
// item as the index holds it.
func indexOneTrack(t *testing.T, name string, bytes int) (*Library, Item) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, bytes), 0o644); err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
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

// A reading belongs to the bytes it was read from. Enrichment snapshots an
// item, spends seconds reading the file — a probe's own ceiling is half a
// minute — and only then writes back; a download that finishes inside that
// window leaves the index holding the finished file under its final size and
// time, which is the one key that never changes again. Writing the partial
// file's duration, codecs and tags onto it there is permanent: the record is
// persisted and restored, and nothing re-reads a file whose identity matches.
func TestReadingOfReplacedBytesIsRefused(t *testing.T) {
	l, partial := indexOneTrack(t, "Harbour Lights.mp3", 4000)
	finished := grow(t, l, partial, 9000)

	stale := Probe{DurationMs: 720_000, VCodec: "theora", Probed: true}
	if l.applyReading(partial.ID, partial, tagMeta{title: "Half a song"}, stale) {
		t.Fatal("a reading of bytes that are gone was written down")
	}
	got, _ := l.Get(partial.ID)
	if got.Duration != 0 || got.VCodec != "" || got.Title != "" {
		t.Fatalf("the replaced file wears the old bytes' reading: %+v", got)
	}
	if got.probed || got.enriched {
		t.Fatal("the replaced file was marked as having been examined")
	}

	// And the same reading, taken from the file that is actually there, is
	// written: the guard is about identity, not about refusing work.
	if !l.applyReading(finished.ID, finished, tagMeta{title: "A whole song"}, stale) {
		t.Fatal("a reading of the current bytes was refused")
	}
	got, _ = l.Get(finished.ID)
	if got.Duration != 720_000 || got.Title != "A whole song" {
		t.Fatalf("the reading did not land: %+v", got)
	}
}

// The check before the write and the write itself are two turns of the lock,
// so the file can still be replaced in between. Looking again afterwards is
// what keeps the item from wearing a reading of bytes that went away while it
// was being written.
func TestAReadingLandingOnAReplacedFileIsForgotten(t *testing.T) {
	l, partial := indexOneTrack(t, "Harbour Lights.mp3", 4000)
	l.setProbe(partial.ID, Probe{DurationMs: 720_000, VCodec: "theora", Probed: true})
	grow(t, l, partial, 9000)

	if l.keptReading(partial.ID, partial) {
		t.Fatal("a reading written onto a replaced file was kept")
	}
	got, _ := l.Get(partial.ID)
	if got.Duration != 0 || got.VCodec != "" || got.probed {
		t.Fatalf("the replaced file kept the old bytes' reading: %+v", got)
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
