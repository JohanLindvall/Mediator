package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// waitFor polls until cond holds or the budget runs out. The watcher is
// asynchronous by nature; a fixed sleep is either flaky or slow.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func liveWatcher(t *testing.T, root string) *Library {
	t.Helper()
	l := quietLib(root)
	l.Scan(nil)
	w, err := NewWatcher(l)
	if err != nil {
		t.Skipf("no watcher available here: %v", err)
	}
	w.AddDir(root)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	return l
}

// A file created in a watched directory is indexed without waiting for a
// rescan. This is the ordinary path, and it is here so the two below are
// read as what they are: the cases it does not cover.
func TestWatcherIndexesANewFile(t *testing.T) {
	root := t.TempDir()
	l := liveWatcher(t, root)
	if err := os.WriteFile(filepath.Join(root, "clip.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the new file", func() bool { return l.List(Query{Limit: 5}).Total == 1 })
}

// The window this exists for: a directory is created and filled before the
// watch on it can be installed. A torrent client preallocating a release
// makes the directory and then every one of its files four milliseconds
// later, and those creations are reported to nobody.
//
// walkNew's immediate walk cannot cover that — at that instant the directory
// is empty — so it is walked again as its contents settle. Here the settle
// walk is called directly: the timing is the operating system's, and a test
// that waited on it would be a test of the clock.
func TestRewalkFindsWhatArrivedInTheWindow(t *testing.T) {
	root := t.TempDir()
	l := quietLib(root)
	l.Scan(nil)
	w, err := NewWatcher(l)
	if err != nil {
		t.Skipf("no watcher available here: %v", err)
	}
	dir := filepath.Join(root, "Larkspur.Nights.2004.1080p.WEB-GRP")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The walk that happens the moment the directory appears finds nothing,
	// which is exactly the situation.
	w.rewalk(dir)
	if n := l.List(Query{Limit: 5}).Total; n != 0 {
		t.Fatalf("an empty directory produced %d items", n)
	}
	// The contents arrive in the window nothing is listening to.
	if err := os.WriteFile(filepath.Join(dir, "film.mkv"), []byte("xxx"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.rewalk(dir)
	if n := l.List(Query{Limit: 5}).Total; n != 1 {
		t.Fatalf("the settle walk found %d items, want the film", n)
	}
}

// The outstanding re-walks are bounded: a tree moved in wholesale is one
// Create per directory, and each would otherwise schedule its own timers.
func TestSettlesAreBounded(t *testing.T) {
	l := quietLib(t.TempDir())
	w, err := NewWatcher(l)
	if err != nil {
		t.Skipf("no watcher available here: %v", err)
	}
	for i := 0; i < maxSettles; i++ {
		if !w.claimSettle() {
			t.Fatalf("refused a slot at %d, before the cap", i)
		}
	}
	if w.claimSettle() {
		t.Error("granted a slot past the cap")
	}
	w.releaseSettle()
	if !w.claimSettle() {
		t.Error("a released slot was not reusable")
	}
}

// Every tool that preserves timestamps stamps them after the copy, and the
// mtime is part of what an item is here — it keys the metadata cache, the
// grid cell and the thumbnail URL. Those events used to be dropped.
func TestWatcherNoticesAStampedTime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(path, []byte("xxxx"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := liveWatcher(t, root)
	before := l.List(Query{Limit: 5}).Items
	if len(before) != 1 {
		t.Fatalf("the file was not indexed to begin with: %+v", before)
	}
	// The source's own time, put back after the copy.
	stamp := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the stamped mtime", func() bool {
		items := l.List(Query{Limit: 5}).Items
		return len(items) == 1 && items[0].ModTime == stamp.UnixMilli()
	})
}

// fakeBackend reports what a test says.
type fakeBackend struct {
	events chan fsEvent
	errs   chan error
	once   sync.Once
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{events: make(chan fsEvent), errs: make(chan error)}
}

func (f *fakeBackend) Add(string) error       { return nil }
func (f *fakeBackend) Remove(string) error    { return nil }
func (f *fakeBackend) WatchList() []string    { return nil }
func (f *fakeBackend) Events() <-chan fsEvent { return f.events }
func (f *fakeBackend) Errors() <-chan error   { return f.errs }
func (f *fakeBackend) Close() error {
	f.once.Do(func() {
		close(f.events)
		close(f.errs)
	})
	return nil
}

// A run of overflows is one loss, reported once it ends: a burst that lasted
// three hours used to be a warning eight times a second, and nothing walked
// afterwards to find what the burst had hidden.
func TestLostEventsAreReportedOncePerBurstAndAnsweredWithAWalk(t *testing.T) {
	old := overflowQuiet
	overflowQuiet = 100 * time.Millisecond
	t.Cleanup(func() { overflowQuiet = old })

	l := quietLib(t.TempDir())
	fb := newFakeBackend()
	w := newWatcherOn(l, fb)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)

	// Three overflows in quick succession, inside one burst.
	for range 3 {
		fb.errs <- errEventOverflow
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-w.Lost():
		t.Fatal("a loss was announced while the burst was still going")
	default:
	}
	select {
	case <-w.Lost():
	case <-time.After(2 * time.Second):
		t.Fatal("the burst ended and nothing was announced")
	}
	select {
	case <-w.Lost():
		t.Fatal("one burst was announced twice")
	case <-time.After(200 * time.Millisecond):
	}
	// A later burst is a later loss.
	fb.errs <- errEventOverflow
	select {
	case <-w.Lost():
	case <-time.After(2 * time.Second):
		t.Fatal("a second burst was not announced")
	}
	// Any other error is only logged, and never announced as a loss.
	fb.errs <- errors.New("something else")
	select {
	case <-w.Lost():
		t.Fatal("an ordinary error was announced as lost events")
	case <-time.After(300 * time.Millisecond):
	}
}
