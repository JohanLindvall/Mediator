package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// fakeConverter writes a stand-in converter: it fails while the marker file is
// absent and behaves once it appears — which is what a transient failure is.
// The last argument is the output path for both converters (the remuxer's
// .part file, the segmenter's playlist), so one script serves both.
func fakeConverter(t *testing.T, dir, marker string, sleep time.Duration) string {
	t.Helper()
	script := filepath.Join(dir, "ffmpeg")
	body := fmt.Sprintf(`#!/bin/sh
for a; do out="$a"; done
sleep %.2f
if [ ! -e %q ]; then
  echo "refusing, as instructed" >&2
  exit 1
fi
d=$(dirname "$out")
case "$out" in
*.m3u8)
  : > "$d/seg00000.ts"
  printf '#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n#EXT-X-PLAYLIST-TYPE:EVENT\n#EXTINF:4.000,\nseg00000.ts\n#EXT-X-ENDLIST\n' > "$out"
  ;;
*)
  printf 'converted' > "$out"
  ;;
esac
exit 0
`, sleep.Seconds(), marker)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// A failed conversion must not be the answer forever. The session used to
// stay in the map with its error, and every later ask for the same film at
// the same resume point got the cached failure back until the budget
// happened to evict it — a disk hiccup made a film unplayable on Safari for
// the life of the process. The Remuxer has always forgotten failures; now
// they agree.
func TestHLSFailureIsNotRemembered(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "now-behave")
	h := NewHLS(fakeConverter(t, dir, marker, 0), nil, NewScratch(t.TempDir(), 0), testLogger())
	defer h.Close()

	it := library.Item{
		ID: "aaaaaaaaaaaaaaaa", Kind: library.KindVideo,
		Path: filepath.Join(dir, "in.mkv"), ModTime: 1, Size: 1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := h.session(ctx, it, 0, true, ""); err == nil {
		t.Fatal("first ask should fail: the converter refused")
	}
	// The forget runs just after the waiters are released; give it a moment.
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		n := len(h.sessions)
		h.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed session still cached: %d in the map", n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := h.session(ctx, it, 0, true, "")
	if err != nil {
		t.Fatalf("second ask should be a fresh attempt, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "index.m3u8")); err != nil {
		t.Fatalf("fresh session has no playlist: %v", err)
	}
}

// The Remuxer's forget-on-failure is the behaviour the HLS fix mirrors, so
// it is pinned too — the two diverging once is how the bug got in.
func TestRemuxFailureIsNotRemembered(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "now-behave")
	r := NewRemuxer(fakeConverter(t, dir, marker, 0), NewScratch(t.TempDir(), 0), testLogger())
	defer func() { _ = r.Close() }()

	it := library.Item{
		ID: "bbbbbbbbbbbbbbbb", Kind: library.KindVideo, Name: "clip.flv",
		VCodec: "h264", ACodec: "aac",
		Path: filepath.Join(dir, "in.flv"), ModTime: 1, Size: 1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := r.File(ctx, it, "", remuxCopy); err == nil {
		t.Fatal("first ask should fail: the converter refused")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.entries)
		r.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed rewrap still cached: %d entries", n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := r.File(ctx, it, "", remuxCopy)
	if err != nil {
		t.Fatalf("second ask should be a fresh attempt, got %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || len(data) == 0 {
		t.Fatalf("fresh rewrap unreadable: %v", err)
	}
}

// Starting more sessions than may convert at once evicts the least recently
// wanted ones, and that path mutates session state other goroutines read —
// exactly what the race detector is in the build for. No assertion beyond
// everyone returning: the interleaving is the test.
func TestHLSConcurrentSessionsEvict(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "now-behave")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHLS(fakeConverter(t, dir, marker, 200*time.Millisecond), nil, NewScratch(t.TempDir(), 0), testLogger())
	defer h.Close()

	it := library.Item{
		ID: "cccccccccccccccc", Kind: library.KindVideo,
		Path: filepath.Join(dir, "in.mkv"), ModTime: 1, Size: 1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := range hlsConverting + 3 {
		wg.Add(1)
		go func(at float64) {
			defer wg.Done()
			_, _ = h.session(ctx, it, at, true, "")
		}(float64(i) * 10)
	}
	wg.Wait()
}

func TestHLSSegmentName(t *testing.T) {
	yes := []string{"seg00000.ts", "seg1.ts", "seg99999.ts"}
	no := []string{
		"", "index.m3u8", "session.key", "seg.ts", "segAB.ts", "SEG00000.TS",
		"seg00000.ts.tmp", "seg00000.mp4", "notseg00000.ts", "../seg00000.ts",
	}
	for _, name := range yes {
		if !hlsSegmentName(name) {
			t.Errorf("hlsSegmentName(%q) = false, want true", name)
		}
	}
	for _, name := range no {
		if hlsSegmentName(name) {
			t.Errorf("hlsSegmentName(%q) = true, want false", name)
		}
	}
}
