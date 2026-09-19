//go:build linux

package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// next takes the backend's next event, or fails after the budget.
func next(t *testing.T, b fsBackend, within time.Duration) (fsEvent, bool) {
	t.Helper()
	select {
	case ev, ok := <-b.Events():
		return ev, ok
	case err := <-b.Errors():
		t.Fatalf("an error where an event was expected: %v", err)
	case <-time.After(within):
	}
	return fsEvent{}, false
}

// The whole reason this backend exists: a file being written is not an
// event. Measured before it was written, a downloader making 464,000 write
// calls a second — three files, 46 bytes a call — overflowed the kernel's
// queue eight times a second and cost this process a core for hours. So
// nothing is said about a file between its creation and the moment a writer
// closes it, and then one thing is said.
func TestInotifyReportsClosesNotWrites(t *testing.T) {
	dir := t.TempDir()
	b, err := newFSBackend()
	if err != nil {
		t.Skipf("no inotify here: %v", err)
	}
	defer b.Close()
	if err := b.Add(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "clip.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := next(t, b, 2*time.Second)
	if !ok || ev.Name != path || !ev.Op.Has(fsCreate) {
		t.Fatalf("after creating the file: %+v (%v), want its creation", ev, ok)
	}
	for range 200 {
		if _, err := f.Write([]byte("forty-six bytes, give or take, per call......")); err != nil {
			t.Fatal(err)
		}
	}
	if ev, ok := next(t, b, 300*time.Millisecond); ok {
		t.Fatalf("two hundred writes produced an event: %+v", ev)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	ev, ok = next(t, b, 2*time.Second)
	if !ok || ev.Name != path || !ev.Op.Has(fsWrite) {
		t.Fatalf("after the writer closed the file: %+v (%v), want a write", ev, ok)
	}
	// The rest of what a watch has to say: a stamp, a move, a removal.
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if ev, ok := next(t, b, 2*time.Second); !ok || ev.Name != path || !ev.Op.Has(fsChmod) {
		t.Fatalf("after a stamp: %+v (%v), want attributes changed", ev, ok)
	}
	moved := filepath.Join(dir, "film.mp4")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	got := map[string]fsOp{}
	for range 2 {
		ev, ok := next(t, b, 2*time.Second)
		if !ok {
			break
		}
		got[ev.Name] |= ev.Op
	}
	if !got[path].Has(fsRemove) || !got[moved].Has(fsCreate) {
		t.Fatalf("after a move: %v, want the old name gone and the new one appeared", got)
	}
	if err := os.Remove(moved); err != nil {
		t.Fatal(err)
	}
	if ev, ok := next(t, b, 2*time.Second); !ok || ev.Name != moved || !ev.Op.Has(fsRemove) {
		t.Fatalf("after a removal: %+v (%v), want it gone", ev, ok)
	}
	// A directory that appears is reported as one, as a file is.
	sub := filepath.Join(dir, "Season 2")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if ev, ok := next(t, b, 2*time.Second); !ok || ev.Name != sub || !ev.Op.Has(fsCreate) {
		t.Fatalf("after a directory appeared: %+v (%v)", ev, ok)
	}
}

// A watch taken off reports nothing more, a watch on a file is refused, and
// the same directory twice is one watch.
func TestInotifyWatchesAreDirectoriesAndRemovable(t *testing.T) {
	dir := t.TempDir()
	b, err := newFSBackend()
	if err != nil {
		t.Skipf("no inotify here: %v", err)
	}
	defer b.Close()
	if err := b.Add(dir); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(dir); err != nil {
		t.Fatal(err)
	}
	if got := b.WatchList(); len(got) != 1 || got[0] != dir {
		t.Fatalf("watching one directory twice lists %v", got)
	}
	file := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(file); err == nil {
		t.Fatal("a watch on a file was accepted")
	}
	// Drain what the file's arrival said.
	for {
		if _, ok := next(t, b, 300*time.Millisecond); !ok {
			break
		}
	}
	if err := b.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if len(b.WatchList()) != 0 {
		t.Fatalf("still watching %v", b.WatchList())
	}
	if err := os.WriteFile(filepath.Join(dir, "other.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ev, ok := next(t, b, 300*time.Millisecond); ok {
		t.Fatalf("a removed watch reported %+v", ev)
	}
	if err := b.Remove(dir); err == nil {
		t.Fatal("removing a watch that is not there was accepted")
	}
	// A directory that goes takes its watch with it, and says so first.
	sub := filepath.Join(dir, "gone")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(sub); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	if ev, ok := next(t, b, 2*time.Second); !ok || ev.Name != sub || !ev.Op.Has(fsRemove) {
		t.Fatalf("a watched directory's removal: %+v (%v)", ev, ok)
	}
	waitFor(t, "the kernel to drop the watch", func() bool { return len(b.WatchList()) == 0 })
	if err := b.Add(dir); err != nil {
		t.Fatal(err)
	}
	// Close wakes the reader out of the kernel and closes both channels,
	// and an Add afterwards is refused rather than made on a descriptor
	// that may by then be somebody else's.
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-b.Events():
		if ok {
			t.Fatal("an event after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the events channel was not closed")
	}
	if err := b.Add(dir); err == nil {
		t.Fatal("an Add after Close was accepted")
	}
	if err := b.Close(); err != nil {
		t.Fatal("a second Close failed")
	}
}
