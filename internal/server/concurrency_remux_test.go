package server

// What the rewrapper and the two verdicts beside it promise when more than
// one thing is happening at once: nothing waits on a child process behind a
// lock, a copy is always stoppable, a copy that lands at shutdown is kept
// rather than destroyed, bytes on their way to the disk are counted, and one
// film is looked at once.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// forgetFilters puts the two package-wide maps back as the test found them,
// since every test in this package shares them.
func forgetFilters(t *testing.T) {
	t.Helper()
	clear := func() {
		filtersMu.Lock()
		filterSets = map[string]map[string]bool{}
		filterRuns = map[string]chan struct{}{}
		filtersMu.Unlock()
	}
	saved := probeFilters
	clear()
	t.Cleanup(func() {
		probeFilters = saved
		clear()
	})
}

// The filter list is read with nothing held. It used to be read inside the
// package's only lock on the answer, so the slowest ffmpeg on the machine
// was the speed of every conversion plan at once — and because a reading
// that fails is deliberately not remembered, that queue was paid again by
// every later wide-colour film rather than once.
func TestFilterListIsReadOutsideTheLock(t *testing.T) {
	forgetFilters(t)
	held := false
	probeFilters = func(string) (map[string]bool, bool) {
		// Taking it here is what a second asker has to do to get past this
		// function at all. If it cannot, the reading owns it.
		if filtersMu.TryLock() {
			filtersMu.Unlock()
		} else {
			held = true
		}
		return map[string]bool{"zscale": true}, true
	}
	if !haveFilter("/nowhere/ffmpeg", "zscale") {
		t.Fatal("the filter the reading reported is missing")
	}
	if held {
		t.Error("the lock was held across the reading, so every other asker waited on the child process")
	}
}

// Asked twice, read once — and a reading that came back with nothing is not
// written down, so the next ask looks again rather than inheriting a silence
// as an answer.
func TestFilterListIsReadOnceAndSilenceIsNotRemembered(t *testing.T) {
	forgetFilters(t)
	runs, answer := 0, false
	probeFilters = func(string) (map[string]bool, bool) {
		runs++
		return map[string]bool{"zscale": true}, answer
	}
	if haveFilter("/nowhere/ffmpeg", "zscale") {
		t.Error("a reading that failed was taken for a yes")
	}
	if haveFilter("/nowhere/ffmpeg", "zscale") {
		t.Error("a reading that failed was remembered as a no")
	}
	if runs != 2 {
		t.Errorf("%d readings for two asks after a failure; each deserves its own", runs)
	}
	answer = true
	if !haveFilter("/nowhere/ffmpeg", "zscale") || !haveFilter("/nowhere/ffmpeg", "zscale") {
		t.Fatal("the answer that was read is not being served")
	}
	if runs != 3 {
		t.Errorf("%d readings; an answer is read once and remembered", runs)
	}
}

// A reading that comes apart still releases whoever is waiting on it. The
// publish used to be written out after the reading rather than deferred, so a
// reading that did not return normally — a panic in a handler, which the
// server above recovers at the cost of that one connection — left the channel
// open and the reading registered, and every later asker for that binary
// waited on a reading that was no longer running, for the life of the
// process. There is no context in hand to leave by.
func TestAFilterReadingThatComesApartReleasesItsWaiters(t *testing.T) {
	forgetFilters(t)
	const bin = "/nowhere/ffmpeg"
	var wait chan struct{}
	probeFilters = func(string) (map[string]bool, bool) {
		// Taken from inside the reading, which is the only moment it is
		// there to take: published, the leader removes it.
		filtersMu.Lock()
		wait = filterRuns[bin]
		filtersMu.Unlock()
		panic("the reading came apart")
	}
	func() {
		defer func() { _ = recover() }()
		haveFilter(bin, "zscale")
	}()
	if wait == nil {
		t.Fatal("the reading was never registered, so no waiter could have found it")
	}
	select {
	case <-wait:
	default:
		t.Error("the waiters were left on an open channel with nothing coming to close it")
	}
	filtersMu.Lock()
	_, still := filterRuns[bin]
	filtersMu.Unlock()
	if still {
		t.Error("the reading is still registered, so the next asker waits on one that is not running")
	}
}

// remuxItem is a film the rewrapper will take: a container no browser opens
// around streams all of them decode.
func remuxItem(id, name, dir string, size int64) library.Item {
	return library.Item{
		ID: id, Kind: library.KindVideo, Name: name,
		VCodec: "h264", ACodec: "aac",
		Path: filepath.Join(dir, name), ModTime: 1, Size: size,
	}
}

// blockingConverter is a stand-in ffmpeg that holds the copy open for longer
// than any test will wait, so a rewrap can be observed while it is in flight.
func blockingConverter(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "now-behave")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return fakeConverter(t, dir, marker, time.Minute)
}

// File publishes the entry with the means to stop it already on it. It used
// to be put there by the goroutine that runs ffmpeg, which may not have been
// scheduled at all: a Close landing in that window found a copy it could not
// stop, emptied the map and returned, and the rewrap went on writing a film
// into the scratch directory under a context nothing would ever cancel —
// which outlives the process that started it.
func TestFilePublishesAWayToStopTheCopy(t *testing.T) {
	dir := t.TempDir()
	r := NewRemuxer(blockingConverter(t), NewScratch(t.TempDir(), 0), testLogger())
	defer func() { _ = r.Close() }()

	gone, leave := context.WithCancel(context.Background())
	leave() // the caller has hung up; the work carries on for whoever asks next
	if _, err := r.File(gone, remuxItem("aaaaaaaaaaaaaaaa", "clip.flv", dir, 1), "", remuxCopy); err == nil {
		t.Fatal("a caller who has gone should not be made to wait")
	}

	var e *remuxEntry
	r.mu.Lock()
	for _, v := range r.entries {
		e = v
	}
	stoppable := e != nil && e.cancel != nil
	r.mu.Unlock()
	if e == nil {
		t.Fatal("File published nothing to wait on")
	}
	if !stoppable {
		t.Fatal("the entry was published with no way to stop the copy")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.done:
	case <-time.After(30 * time.Second):
		t.Fatal("Close did not stop the copy; it is still writing into the scratch directory")
	}
}

// A copy that finishes as the server is shutting down is kept and is not
// offered. It used to be published to every waiter with a nil error and the
// path, and then deleted — so a caller was handed a film that was being
// unlinked underneath it, and a finished rewrap that the next run would have
// adopted was thrown away after costing the whole length of the film.
func TestARewrapLandingAtShutdownIsKeptAndNotOffered(t *testing.T) {
	dir := t.TempDir()
	scratch := NewScratch(t.TempDir(), 0)
	r := NewRemuxer(blockingConverterThatFinishes(t), scratch, testLogger())
	sub, err := scratch.Sub("remux")
	if err != nil {
		t.Fatal(err)
	}
	it := remuxItem("bbbbbbbbbbbbbbbb", "trailer.flv", dir, 1)
	key := "trailer"
	e := &remuxEntry{path: filepath.Join(sub, "trailer.mp4"), want: it.Size, done: make(chan struct{})}
	r.mu.Lock()
	r.entries[key] = e
	r.pending += e.want
	r.mu.Unlock()

	// The shutdown lands while the copy is in its last moments: it is past
	// the point where cancelling it would do anything.
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	work, stop := context.WithCancel(context.Background())
	defer stop()
	r.produce(work, stop, it, 0, remuxCopy, key, e)

	if _, err := r.wait(context.Background(), e); !errors.Is(err, ErrNoRemux) {
		t.Errorf("a waiter released after Close was answered %v; nothing is holding that file for it", err)
	}
	if _, err := os.Stat(e.path); err != nil {
		t.Errorf("the finished rewrap was deleted at shutdown: %v", err)
	}
}

// blockingConverterThatFinishes writes its output and exits, which is the
// ordinary case.
func blockingConverterThatFinishes(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "now-behave")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return fakeConverter(t, dir, marker, 0)
}

// A copy being written is bytes on the disk, so it counts towards the
// budget. It used to count nowhere until it finished, and two admissions in
// the same minute each measured a total that left the other out: with two
// evictable copies of 3 units beside a budget of 10, the second film of 5
// freed nothing and the directory went to 12 where counting what was coming
// frees one and stays at 9.
func TestRewrapsInFlightCountTowardsTheBudget(t *testing.T) {
	dir := t.TempDir()
	const unit = 1 << 20
	scratch := NewScratch(t.TempDir(), 10*unit)
	r := NewRemuxer(blockingConverter(t), scratch, testLogger())
	defer func() { _ = r.Close() }()

	sub, err := scratch.Sub("remux")
	if err != nil {
		t.Fatal(err)
	}
	old := make([]string, 2)
	for i := range old {
		old[i] = filepath.Join(sub, "old"+string(rune('a'+i))+".mp4")
		if err := os.WriteFile(old[i], make([]byte, 3*unit), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Under the lock, as anything touching these fields has to be: nothing
	// else is running yet, but a setup written the other way is the pattern
	// the race detector exists to catch and stops being harmless the moment
	// somebody reorders it.
	r.mu.Lock()
	for i := range old {
		done := make(chan struct{})
		close(done)
		r.entries["old"+string(rune('a'+i))] = &remuxEntry{
			path: old[i], size: 3 * unit, used: int64(i + 1),
			// Last wanted long enough ago to be prunable.
			last: time.Now().Add(-2 * remuxKeepFor), done: done,
		}
	}
	r.total = 6 * unit
	r.seq = 2
	r.mu.Unlock()

	gone, leave := context.WithCancel(context.Background())
	leave()
	for i, id := range []string{"cccccccccccccccc", "dddddddddddddddd"} {
		if _, err := r.File(gone, remuxItem(id, "film.flv", dir, 5*unit), "", remuxCopy); err == nil {
			t.Fatalf("ask %d should have returned to a caller who had gone", i)
		}
	}
	for i, path := range old {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("old copy %d is still on disk: the second admission did not count the first, which is in flight", i)
		}
	}
}

// One film is looked at once, and a look that could not finish leaves
// nothing behind. Opening a film asks this from several places at the same
// moment, and each of them used to run its own ffprobe over the same opening
// — against the disk the playback it is preparing is about to read from.
func TestReorderLookIsSharedAndAnUnfinishedOneIsNotRemembered(t *testing.T) {
	var c reorderCache
	if _, known, wait := c.claim("film"); known || wait != nil {
		t.Fatal("the first asker takes the job")
	}
	_, known, wait := c.claim("film")
	if known || wait == nil {
		t.Fatal("a second asker should be handed the look in flight, not a second ffprobe")
	}
	released := make(chan struct{})
	go func() {
		<-wait
		close(released)
	}()
	c.settle("film", false, false) // the budget ran out; nothing was judged
	<-released
	if _, ok := c.get("film"); ok {
		t.Error("a look that could not finish was written down as a verdict")
	}

	if _, _, wait := c.claim("film"); wait != nil {
		t.Fatal("with nothing in flight and nothing known, the next asker takes the job")
	}
	c.settle("film", true, true)
	if v, ok := c.get("film"); !ok || !v {
		t.Error("the verdict was not remembered")
	}
	if v, known, wait := c.claim("film"); !known || !v || wait != nil {
		t.Error("a film already judged is answered rather than looked at again")
	}
}
