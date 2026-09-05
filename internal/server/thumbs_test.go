package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/rartest"
)

func writePNG(t *testing.T, path string) library.Item {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 4))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return library.Item{
		ID: "img1", Name: filepath.Base(path), Path: path,
		Kind: library.KindImage, Size: info.Size(), ModTime: info.ModTime().UnixMilli(),
	}
}

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestThumbnailerWithStore(t *testing.T) {
	dir := t.TempDir()
	it := writePNG(t, filepath.Join(dir, "img.png"))
	store, err := blob.Open(filepath.Join(dir, "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	data, err := NewThumbnailer(store, nil, testLog()).Get(context.Background(), it, 360)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
		t.Fatalf("not a JPEG: % x", data[:min(len(data), 4)])
	}

	// Cached in the store: a fresh thumbnailer serves it even after the
	// source file is gone.
	if err := os.Remove(it.Path); err != nil {
		t.Fatal(err)
	}
	again, err := NewThumbnailer(store, nil, testLog()).Get(context.Background(), it, 360)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, data) {
		t.Fatal("cached thumbnail differs from generated one")
	}

	// Without a store the same request must regenerate — and fail now.
	if _, err := NewThumbnailer(nil, nil, testLog()).Get(context.Background(), it, 360); err == nil {
		t.Fatal("expected failure without store and source")
	}
}

func TestThumbnailerGatedWhileStreaming(t *testing.T) {
	// With playback active, generation serializes through the background
	// slot but must still complete.
	it := writePNG(t, filepath.Join(t.TempDir(), "img.png"))
	th := NewThumbnailer(nil, func() bool { return true }, testLog())
	data, err := th.Get(context.Background(), it, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
		t.Fatal("not a JPEG")
	}
	if th.Generating() {
		t.Fatal("generation counter did not return to zero")
	}
}

func TestThumbnailerWithoutStore(t *testing.T) {
	it := writePNG(t, filepath.Join(t.TempDir(), "img.png"))
	th := NewThumbnailer(nil, nil, testLog())
	for range 2 { // second call regenerates; no persistence involved
		data, err := th.Get(context.Background(), it, 100)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
			t.Fatal("not a JPEG")
		}
	}
}

// archivedVideo packs a real clip into a two-volume store-mode rar set and
// returns the virtual item the library indexes for it.
func archivedVideo(t *testing.T, seconds int) library.Item {
	t.Helper()
	// Matroska, not mp4: a pipe cannot be seeked, so a container whose index
	// sits at the end of the file is undecodable this way — which is also
	// why releases of this shape are the ones that matter here.
	src := filepath.Join(t.TempDir(), "src.mkv")
	writeClip(t, src, seconds)
	payload, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	rartest.WriteSet(t, dir, "release", "Release.mkv", payload, 2, false)
	lib := library.New([]string{dir}, testLog())
	lib.Scan(nil)
	res := lib.List(library.Query{})
	if res.Total != 1 {
		t.Fatalf("indexed %d items, want the archived clip", res.Total)
	}
	it := res.Items[0]
	if !it.Archived() || it.Kind != library.KindVideo {
		t.Fatalf("item is not an archived video: %+v", it)
	}
	return it
}

// A video inside a rar set has no path for ffmpeg to open, but its bytes are
// readable — so it gets a thumbnail like any other video.
func TestThumbnailerArchivedVideo(t *testing.T) {
	it := archivedVideo(t, 6)
	data, err := NewThumbnailer(nil, nil, testLog()).Get(context.Background(), it, 100)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 100 {
		t.Fatalf("thumbnail is %dx%d, want 100 wide", cfg.Width, cfg.Height)
	}
}

// A clip shorter than the first seek still gets its frame: the fallback to
// frame 0 is the same one that covers a seek running past a piped prefix.
func TestThumbnailerArchivedShortVideo(t *testing.T) {
	it := archivedVideo(t, 1)
	data, err := NewThumbnailer(nil, nil, testLog()).Get(context.Background(), it, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
		t.Fatal("not a JPEG")
	}
}

// The scrub strip stays off for archived content: it samples the whole file,
// which here means reading every volume of the set for one small image.
func TestSpriteSkippedForArchived(t *testing.T) {
	it := archivedVideo(t, 6)
	it.Duration = 6000
	if spriteEligible(it) {
		t.Fatal("archived video should not be sprite-eligible")
	}
}

// A request the client hung up on says nothing about the item. Reporting it
// as a failed thumbnail negative-caches the item until restart, and since the
// grid aborts thumbnail fetches routinely — a cell recycled by scrolling, an
// overlay taking the screen — scrolling past a video at the wrong moment
// would leave it a grey tile for the life of the process.
func TestVideoFrameReportsCancellation(t *testing.T) {
	it := testVideo(t, 2) // skips unless ffmpeg is installed
	th := NewThumbnailer(nil, nil, testLog())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := th.videoFrame(ctx, it, "0", 100)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// plainVideo is a video file on disk that only a stand-in ffmpeg will read:
// what a thumbnail of it looks like is the fake's business.
func plainVideo(t *testing.T) library.Item {
	t.Helper()
	path := filepath.Join(t.TempDir(), "feature.mp4")
	if err := os.WriteFile(path, bytes.Repeat([]byte{7}, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return library.Item{
		ID: "plain1", Name: "feature.mp4", Path: path, Kind: library.KindVideo,
		Size: info.Size(), ModTime: info.ModTime().UnixMilli(), Duration: 90_000,
	}
}

// The plain-file path stores nothing torn either. It used to: it checked only
// the caller's context, so a frame its own deadline had interrupted was read
// back and written immutable, and every browser that saw it kept it.
func TestVideoThumbNeverStoresTornFrame(t *testing.T) {
	full := variedJPEG(t)
	it := plainVideo(t)
	store, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	th := NewThumbnailer(store, nil, testLog())
	fakeFFmpeg(t, th, false, full[:len(full)-32])

	if _, err := th.Get(context.Background(), it, 160); !errors.Is(err, ErrNoThumb) {
		t.Fatalf("err = %v, want ErrNoThumb", err)
	}
	if data, ok := store.GetThumb(it.ID, it.ModTime, it.Size, 160); ok {
		t.Fatalf("a torn frame was stored permanently (%d bytes)", len(data))
	}
}

// And a seek that ran out of time is a busy disk, not a film without a
// frame: it is not negative-cached, and the next ask reads the film.
func TestVideoThumbTimeoutIsNotNegativeCached(t *testing.T) {
	it := plainVideo(t)
	th := NewThumbnailer(nil, nil, testLog())
	fakeFFmpeg(t, th, true, variedJPEG(t))

	old := plainThumbTimeout
	plainThumbTimeout = 100 * time.Millisecond
	t.Cleanup(func() { plainThumbTimeout = old })

	if _, err := th.Get(context.Background(), it, 160); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
	if len(th.ffSem) != 0 {
		t.Fatal("an ffmpeg slot is still held after the run was killed")
	}
	plainThumbTimeout = old
	fakeFFmpeg(t, th, false, variedJPEG(t))
	data, err := th.Get(context.Background(), it, 160)
	if err != nil {
		t.Fatalf("the film was written off after a timeout: %v", err)
	}
	if !bytes.Equal(data, variedJPEG(t)) {
		t.Fatal("the retry did not return the frame ffmpeg produced")
	}
}

// A follower does not inherit the leader's cancellation. The grid scrolls a
// cell away and cancels the leader while the player, opening the same film,
// is waiting behind it; the player's request is live and must get a frame,
// not the 404 the leader's abandoned request would have got.
func TestFollowerOfCancelledLeaderLeadsTheNextAttempt(t *testing.T) {
	it := writePNG(t, filepath.Join(t.TempDir(), "img.png"))
	th := NewThumbnailer(nil, nil, testLog())

	var calls atomic.Int32
	joined := make(chan struct{})
	gen := func(ctx context.Context) ([]byte, error) {
		if calls.Add(1) == 1 {
			<-joined // the follower is waiting on us before we give up
			return nil, context.Canceled
		}
		return []byte("jpeg"), nil
	}
	leaderDone := make(chan error, 1)
	go func() {
		_, err := th.cached(context.Background(), it, 100, gen)
		leaderDone <- err
	}()
	// Wait until the leader's generation is registered, then join it.
	for {
		th.genMu.Lock()
		_, inflight := th.gen[fmt.Sprintf("%s|%d|%d|%d", it.ID, it.ModTime, it.Size, 100)]
		th.genMu.Unlock()
		if inflight {
			break
		}
		time.Sleep(time.Millisecond)
	}
	followerDone := make(chan struct{})
	var data []byte
	var ferr error
	go func() {
		defer close(followerDone)
		// Let the leader see us before it gives up.
		time.Sleep(20 * time.Millisecond)
		close(joined)
		data, ferr = th.cached(context.Background(), it, 100, gen)
	}()
	<-followerDone
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader err = %v, want its own cancellation", err)
	}
	if ferr != nil || string(data) != "jpeg" {
		t.Fatalf("follower got %q, %v; want the frame from its own attempt", data, ferr)
	}
	if calls.Load() != 2 {
		t.Fatalf("%d generations, want 2: the leader's and the follower's own", calls.Load())
	}
}

// The other half of the same guarantee: an error that is a cancellation must
// leave the item generatable, while a real failure stays negative-cached.
func TestCanceledGenerationIsNotNegativeCached(t *testing.T) {
	it := writePNG(t, filepath.Join(t.TempDir(), "img.png"))
	th := NewThumbnailer(nil, nil, testLog())

	calls := 0
	gen := func(context.Context) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, context.Canceled
		}
		return []byte("jpeg"), nil
	}
	if _, err := th.cached(context.Background(), it, 100, gen); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	data, err := th.cached(context.Background(), it, 100, gen)
	if err != nil {
		t.Fatalf("second attempt failed: %v — the canceled one was negative-cached", err)
	}
	if string(data) != "jpeg" {
		t.Fatalf("got %q", data)
	}

	// A genuine failure is remembered, so a broken file is not retried by
	// every cell that scrolls past it.
	fail := 0
	failing := func(context.Context) ([]byte, error) { fail++; return nil, ErrNoThumb }
	for range 3 {
		if _, err := th.cached(context.Background(), it, 200, failing); !errors.Is(err, ErrNoThumb) {
			t.Fatalf("err = %v, want ErrNoThumb", err)
		}
	}
	if fail != 1 {
		t.Fatalf("%d generation attempts after a failure, want 1 (negative cache)", fail)
	}
}

// Where a video's still comes from, and what happens when nobody knows how
// long the video is.
//
// A tenth of the way in, for the same reason archived video has always been:
// the opening of a film is its front matter, and for a television episode
// that is the distributor's logo almost without exception — which is what a
// shelf of shows looked like, Netflix and Paramount and Showtime one after
// another, none of them saying which programme it was.
func TestVideoSeeks(t *testing.T) {
	// A 40-minute episode: a tenth is 240 s, past any front matter.
	got := videoSeeks(40 * 60 * 1000)
	if len(got) < 3 || got[0] != "240.00" {
		t.Errorf("first seek %v, want a tenth of the way in", got)
	}
	// Whatever is tried, the last resort is the very first frame: it needs
	// nothing but the opening bytes, which is what a clip too short to seek
	// into, or an archived one fed a bounded prefix, can offer.
	if got[len(got)-1] != "0" {
		t.Errorf("the last resort is %q, want frame 0", got[len(got)-1])
	}

	// A two-minute clip: the floor keeps the seek out of the first seconds,
	// and the cap keeps it from running past the end.
	short := videoSeeks(2 * 60 * 1000)
	if short[0] != "60.00" {
		t.Errorf("a short clip seeks to %q, want its midpoint at most", short[0])
	}

	// Nobody has measured it, so there is no tenth to take.
	if got := videoSeeks(0); got[0] != "3" || got[1] != "0" {
		t.Errorf("with no duration: %v, want the old few seconds then frame 0", got)
	}
}
