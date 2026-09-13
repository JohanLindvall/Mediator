package library

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// subsOf reports the sidecars the index holds for a directory.
func subsOf(l *Library, dir string) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return slices.Clone(l.subsByDir[dir])
}

// A sidecar the watcher indexed while the walk was elsewhere must survive
// that walk's reconciliation. The sweep used to consult nothing but what the
// walk itself saw, so a caption that landed after the walk passed its
// directory was deleted while it lay on disk — and with no change event to
// say so, a player that had just been offered it went on offering a listing
// that no longer had it.
//
// The walk's own directory callback is the interleaving point: it runs on
// the scan's goroutine, inside the walk, once per directory and in name
// order — so acting on it for "b" is acting after "a" has been walked and
// before the reconciliation runs.
func TestScanKeepsASidecarAddedWhileTheWalkWasElsewhere(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "harbour.mkv"), "vvvv")
	if err := os.MkdirAll(filepath.Join(root, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := quietLib(root)
	l.Scan(nil)

	srt := filepath.Join(root, "a", "harbour.en.srt")
	l.Scan(func(dir string) {
		if filepath.Base(dir) != "b" {
			return
		}
		// What the watcher does the moment a caption lands, in the window
		// between the walk leaving "a" and the reconciliation below.
		writeFile(t, srt, "1\n00:00:01,000 --> 00:00:02,000\nhello\n")
		l.AddFile(srt)
	})

	if got := subsOf(l, filepath.Join(root, "a")); !slices.Contains(got, srt) {
		t.Fatalf("the sidecar was swept although it is on disk: %v", got)
	}
}

// The other half of the same rule: one that really has gone still goes.
func TestScanDropsASidecarThatIsGone(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a")
	writeFile(t, filepath.Join(dir, "harbour.mkv"), "vvvv")
	srt := filepath.Join(dir, "harbour.en.srt")
	writeFile(t, srt, "1\n00:00:01,000 --> 00:00:02,000\nhello\n")
	l := quietLib(root)
	l.Scan(nil)
	if got := subsOf(l, dir); len(got) != 1 {
		t.Fatalf("the sidecar was not indexed to begin with: %v", got)
	}
	if err := os.Remove(srt); err != nil {
		t.Fatal(err)
	}
	l.Scan(nil)
	if got := subsOf(l, dir); len(got) != 0 {
		t.Fatalf("a deleted sidecar is still listed: %v", got)
	}
}

// Content that lives inside another file is reconciled by asking about the
// container, never about its own path. The path is the container's with the
// member's name after a NUL, which the syscall layer refuses outright — so
// the rescue that keeps a path the walk did not report could never fire for
// a rar member or a DVD title, and every one of them the walk had not
// reported was dropped. That is the ordinary state of a container the
// watcher indexed after the walk passed its directory, and of one whose
// parse failed this time round.
func TestScanKeepsMembersWhoseContainerIsStillThere(t *testing.T) {
	root := t.TempDir()
	container := filepath.Join(root, "holder.bin")
	writeFile(t, container, "not media, and not walked as any")
	l := quietLib(root)
	l.Scan(nil)

	// What a parse of the container produces: a member the walk itself
	// never reports, since nothing in the walk knows how to read this one.
	member := &storedEntry{
		name: "Harbour.Lights.S01E01.mkv", size: 10,
		segs: []storedSeg{{path: container, off: 0, n: 10}},
	}
	paths, _ := l.indexStored(container, []*storedEntry{member}, time.Now())
	if len(paths) != 1 {
		t.Fatalf("the member was not indexed: %v", paths)
	}
	l.Scan(nil)
	if _, ok := l.Get(PathID(paths[0])); !ok {
		t.Fatal("the member was dropped although its container is on disk")
	}

	// And when the container really goes, so does the member.
	if err := os.Remove(container); err != nil {
		t.Fatal(err)
	}
	l.Scan(nil)
	if _, ok := l.Get(PathID(paths[0])); ok {
		t.Fatal("the member outlived its container")
	}
}

// An item indexed after the walk began is not dropped by that walk. The disk
// is asked about the missing paths before the write lock is taken — one
// syscall each, and then the wait — so the answer it gives can be about a
// file that is not the one now in the index: a path deleted and written
// again under the same name comes back as a fresh item, and the stat that
// landed in the gap between condemned it.
//
// The file is removed here only to force the stat to fail, which is what the
// race produces; keeping such an item for one cycle is the deliberate cost,
// and the next walk drops it.
func TestScanKeepsAnItemIndexedAfterTheWalkBegan(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "harbour.mkv"), "vvvv")
	if err := os.MkdirAll(filepath.Join(root, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := quietLib(root)
	l.Scan(nil)

	late := filepath.Join(root, "a", "larkspur.mkv")
	l.Scan(func(dir string) {
		if filepath.Base(dir) != "b" {
			return
		}
		// The index stamps FirstSeen in milliseconds, so let the clock move
		// off the one the walk started on before the watcher's item is made.
		time.Sleep(2 * time.Millisecond)
		writeFile(t, late, "vvvv")
		l.AddFile(late)
		if err := os.Remove(late); err != nil {
			t.Fatal(err)
		}
	})
	if _, ok := l.Get(PathID(late)); !ok {
		t.Fatal("an item indexed after the walk began was dropped by it")
	}
	l.Scan(nil)
	if _, ok := l.Get(PathID(late)); ok {
		t.Fatal("the next walk should have dropped it")
	}
}

// Reading a container and reconciling what it holds is one read-modify-write
// and nothing used to hold the two together, so two reads of one container —
// a watcher event and a settle timer, which is the ordinary case while a
// release lands — could finish in the wrong order and let the older
// membership drop what the newer one had just indexed.
func TestContainerReadsSerialize(t *testing.T) {
	release := enterContainer("/m/set.rar")
	in, out := make(chan struct{}), make(chan struct{})
	go func() {
		second := enterContainer("/m/set.rar")
		close(in)
		second()
		close(out)
	}()
	select {
	case <-in:
		t.Fatal("two readings of one container were let in at once")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-out:
	case <-time.After(5 * time.Second):
		t.Fatal("the second reading never got in")
	}
	containers.mu.Lock()
	n := len(containers.held)
	containers.mu.Unlock()
	if n != 0 {
		t.Errorf("%d container locks left behind", n)
	}
	// Two containers are not each other's business.
	a, b := enterContainer("/m/one.rar"), enterContainer("/m/two.rar")
	a()
	b()
}

// A container's members are read once its writer goes quiet, and one
// container's worth at a time. A watcher event on any volume of a set
// reparses the whole set and reports every member as changed, so a goroutine
// per event put an unbounded number of readers — an ffprobe apiece, over the
// loopback stream — on the disk that playback is reading from.
func TestContainerReadsCollapseToOnePendingPass(t *testing.T) {
	was := containerQuiet
	containerQuiet = 10 * time.Millisecond
	defer func() { containerQuiet = was }()

	l := quietLib(t.TempDir())
	for range 20 {
		l.readMembersSoon("/m/set.rar", nil)
	}
	containerReads.mu.Lock()
	n := len(containerReads.m)
	containerReads.mu.Unlock()
	if n != 1 {
		t.Fatalf("a burst of events left %d pending reads, want 1", n)
	}
	waitFor(t, "the pass to run and let go", func() bool {
		containerReads.mu.Lock()
		defer containerReads.mu.Unlock()
		return len(containerReads.m) == 0
	})
}

// Watches are installed by two things that outlive a change of directories:
// a settle timer armed minutes ago, and a walk that took its root list
// before the change. Reset has run by then, so anything they install
// afterwards is never removed again.
func TestAddDirRefusesWhatIsNotOurs(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	l := quietLib(root)
	w, err := NewWatcher(l)
	if err != nil {
		t.Skipf("no watcher available here: %v", err)
	}
	defer w.fsw.Close()
	w.AddDir(elsewhere)
	if slices.Contains(w.fsw.WatchList(), elsewhere) {
		t.Error("watched a directory outside the roots")
	}
	w.AddDir(root)
	if !slices.Contains(w.fsw.WatchList(), root) {
		t.Error("refused a root of its own")
	}
}

// And the timers themselves are stopped when the directories change, rather
// than left to fire minutes later over a tree that is no longer ours.
func TestResetStopsTheSettleWalks(t *testing.T) {
	root := t.TempDir()
	l := quietLib(root)
	w, err := NewWatcher(l)
	if err != nil {
		t.Skipf("no watcher available here: %v", err)
	}
	defer w.fsw.Close()
	for range 3 {
		if !w.claimSettle() {
			t.Fatal("refused a slot well inside the cap")
		}
		w.armSettle(time.Hour, filepath.Join(root, "arrival"))
	}
	w.mu.Lock()
	settles, timers := w.settles, len(w.timers)
	w.mu.Unlock()
	if settles != 3 || timers != 3 {
		t.Fatalf("armed %d timers holding %d slots, want 3 and 3", timers, settles)
	}
	w.Reset()
	w.mu.Lock()
	settles, timers = w.settles, len(w.timers)
	w.mu.Unlock()
	if settles != 0 || timers != 0 {
		t.Fatalf("after a change of directories: %d timers holding %d slots", timers, settles)
	}
}
