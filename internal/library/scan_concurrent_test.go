package library

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Scan serializes. The initial walk, the periodic rescan and a change of
// directories can all ask for one at once, and before the lock existed each
// walk cleared byInode mid-flight and reconciled against its own idea of
// what it had seen: a hard-linked pair could be dropped as duplicates of
// each other, and files one walk indexed vanished under the other's
// reconciliation. The hard link is the sensitive fixture — dedup is the
// state the interleaving corrupted — and the race detector watches the rest.
func TestScanSerializesConcurrentCalls(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "clip one.mp4"), "vvvv")
	writeFile(t, filepath.Join(root, "song one.mp3"), "aaaa")
	if err := os.Link(filepath.Join(root, "clip one.mp4"), filepath.Join(root, "clip one again.mp4")); err != nil {
		t.Skipf("no hard links here: %v", err)
	}

	lib := New([]string{root}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	for range 5 {
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				lib.Scan(nil)
			}()
		}
		wg.Wait()
		// The pair counts once, the track once: two items, every time.
		if got := lib.Size(); got != 2 {
			t.Fatalf("after concurrent scans: %d items, want 2", got)
		}
	}
}
