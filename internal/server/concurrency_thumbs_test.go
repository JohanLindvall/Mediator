package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/library"
)

// quietLog is a logger for a test that has nothing to say.
func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// noFFmpeg is a thumbnailer that will never start a process: what is under
// test here is what the deadlines and the caches decide, not what ffmpeg
// prints. The path has to be non-empty, or the video paths refuse before
// reaching the part being measured.
func noFFmpeg(t *testing.T) *Thumbnailer {
	t.Helper()
	th := NewThumbnailer(nil, nil, quietLog())
	th.ffmpeg = filepath.Join(t.TempDir(), "no-such-ffmpeg")
	return th
}

func aVideo(t *testing.T, durationMs int64) library.Item {
	t.Helper()
	path := filepath.Join(t.TempDir(), "feature.mkv")
	if err := os.WriteFile(path, []byte("not really a film"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return library.Item{
		ID: "0123456789abcdef", Name: "feature.mkv", Path: path,
		Kind: library.KindVideo, ModTime: info.ModTime().UnixMilli(),
		Size: info.Size(), Duration: durationMs,
	}
}

// The scrub sheet's own budget is not a verdict on the film. It used to be
// read only through the caller's context, so an expiry part way through the
// ten seeks came out as whatever had been gathered — a sheet with black cells,
// stored under a key that never rotates again and served immutable, or
// ErrNoThumb, negative-cached for the life of the process.
func TestSpriteBudgetIsNotAVerdict(t *testing.T) {
	th := noFFmpeg(t)
	it := aVideo(t, 10*60*1000)

	was := spriteTimeout
	spriteTimeout = time.Nanosecond
	defer func() { spriteTimeout = was }()

	_, err := th.Sprite(context.Background(), it)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the sheet's own deadline", err)
	}
	if errors.Is(err, ErrNoThumb) {
		t.Error("an expired budget was reported as a verdict on the film")
	}
	key := fmt.Sprintf("%s|%d|%d|%d", it.ID, it.ModTime, it.Size, spriteCacheWidth)
	if th.recentlyFailed(key) {
		t.Error("the sheet's deadline was negative-cached")
	}
}

// A plain video's tile has one budget for the whole item, as the archived one
// has always had: four offsets and the repair behind them each opened a fresh
// thirty seconds, so one tile could hold an ffmpeg slot for two and a half
// minutes. Its expiry is a deadline like any other and says nothing about the
// film.
func TestPlainVideoTileHasOneBudget(t *testing.T) {
	th := noFFmpeg(t)
	it := aVideo(t, 0)

	was := plainThumbItemTimeout
	plainThumbItemTimeout = time.Nanosecond
	defer func() { plainThumbItemTimeout = was }()

	_, err := th.Get(context.Background(), it, 200)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the item's own deadline", err)
	}
	key := fmt.Sprintf("%s|%d|%d|%d", it.ID, it.ModTime, it.Size, 200)
	if th.recentlyFailed(key) {
		t.Error("the item's deadline was negative-cached")
	}
}

// A picture is read under a budget too, and the read consults it. Without
// that, the single background slot was held for as long as a half-gigabyte
// photograph took to arrive over a mount that had gone slow.
func TestStillReadGivesUpWithItsBudget(t *testing.T) {
	th := NewThumbnailer(nil, nil, quietLog())
	dir := t.TempDir()
	path := filepath.Join(dir, "picture.jpg")
	if err := os.WriteFile(path, make([]byte, 1<<16), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	it := library.Item{
		ID: "fedcba9876543210", Name: "picture.jpg", Path: path,
		Kind: library.KindImage, ModTime: info.ModTime().UnixMilli(), Size: info.Size(),
	}

	was := stillThumbTimeout
	stillThumbTimeout = time.Nanosecond
	defer func() { stillThumbTimeout = was }()

	_, err = th.Get(context.Background(), it, 200)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the still's own deadline", err)
	}
	key := fmt.Sprintf("%s|%d|%d|%d", it.ID, it.ModTime, it.Size, 200)
	if th.recentlyFailed(key) {
		t.Error("the still's deadline was negative-cached")
	}
}

// A failure is remembered against a retry storm and forgotten afterwards. A
// mount that stops answering fails every item under it at once, and none of
// the key's components changes when it comes back: the files play again while
// their tiles stayed grey until the process was restarted.
func TestFailedTilesComeBackWhenTheMountDoes(t *testing.T) {
	th := NewThumbnailer(nil, nil, quietLog())
	it := aVideo(t, 1000)
	runs := 0
	gen := func(context.Context) ([]byte, error) {
		runs++
		return nil, fmt.Errorf("read %s: input/output error", it.Path)
	}

	if _, err := th.cached(context.Background(), it, 200, gen); err == nil {
		t.Fatal("the generation was supposed to fail")
	}
	if _, err := th.cached(context.Background(), it, 200, gen); !errors.Is(err, ErrNoThumb) {
		t.Fatalf("err = %v, want the remembered failure", err)
	}
	if runs != 1 {
		t.Fatalf("ran the generation %d times; the failure was not remembered at all", runs)
	}

	key := fmt.Sprintf("%s|%d|%d|%d", it.ID, it.ModTime, it.Size, 200)
	th.negMu.Lock()
	th.neg[key] = time.Now().Add(-negTTL - time.Minute)
	th.negMu.Unlock()

	if _, err := th.cached(context.Background(), it, 200, gen); err == nil {
		t.Fatal("the generation was supposed to fail")
	}
	if runs != 2 {
		t.Errorf("ran the generation %d times; the failure outlived its reason", runs)
	}
}

// cropServer is a server with nowhere to put anything but the blob database:
// what is under test is which answers reach it.
func cropServer(t *testing.T) *Server {
	t.Helper()
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Server{thumbs: NewThumbnailer(db, nil, quietLog()), log: quietLog()}
}

func freshCropKey(t *testing.T) library.Item {
	t.Helper()
	return library.Item{
		ID:   fmt.Sprintf("crop%012d", time.Now().UnixNano()%1e12),
		Kind: library.KindVideo, ModTime: 1, Size: 2, Duration: 60000,
		Path: "/nowhere/feature.mkv", Rel: "feature.mkv",
	}
}

// A run that never looked is not an answer. The store is keyed by the file as
// it is, which for a stable file never changes again, so an empty box written
// by a detection that had no duration to place its samples by — or by a build
// with no ffmpeg — was a permanent "this film has no borders".
func TestUnmeasuredCropIsNotWrittenDown(t *testing.T) {
	s := cropServer(t)
	it := freshCropKey(t)

	box := s.cropAnswer(context.Background(), it, func(context.Context) (CropResponse, bool) {
		return CropResponse{}, false // nothing measured this file
	})
	if box != (CropResponse{}) {
		t.Fatalf("box = %+v, want nothing", box)
	}
	if _, found := s.storedCrop(it); found {
		t.Fatal("a detection that never looked was remembered as an answer")
	}

	// A real look that found nothing to trim is the other thing entirely, and
	// is worth not finding out twice.
	s.cropAnswer(context.Background(), it, func(context.Context) (CropResponse, bool) {
		return CropResponse{}, true
	})
	if _, found := s.storedCrop(it); !found {
		t.Fatal("a measured 'no borders here' was not remembered")
	}
}

// The leader's answer travels in the dedup entry rather than through the
// store. Without that the close of the channel said nothing at all: with
// -db off, or after a write that failed, every waiter missed in the store,
// found the entry already gone, and ran the same four seeks again.
func TestCropWaiterTakesTheLeadersAnswer(t *testing.T) {
	s := &Server{thumbs: NewThumbnailer(nil, nil, quietLog()), log: quietLog()} // -db off
	it := freshCropKey(t)
	key := fmt.Sprintf("%s|%d|%d", it.ID, it.ModTime, it.Size)
	answer := CropResponse{X: 80, Y: 45, W: 160, H: 90, FrameW: 320, FrameH: 180}

	// A leader that has just finished, exactly as a waiter finds it.
	done := make(chan struct{})
	close(done)
	cropRuns.mu.Lock()
	cropRuns.inflight[key] = &cropRun{done: done, box: answer, ok: true}
	cropRuns.mu.Unlock()
	defer func() {
		cropRuns.mu.Lock()
		delete(cropRuns.inflight, key)
		cropRuns.mu.Unlock()
	}()

	got := s.cropAnswer(context.Background(), it, func(context.Context) (CropResponse, bool) {
		t.Error("the waiter ran a detection of its own")
		return CropResponse{}, true
	})
	if got != answer {
		t.Errorf("box = %+v, want the leader's %+v", got, answer)
	}

}

// A leader whose request went away writes nothing down. An interrupted run
// says nothing about the file, and the entry it leaves says so too, so the
// next caller looks again rather than inheriting its silence.
func TestInterruptedCropIsNotWrittenDown(t *testing.T) {
	s := cropServer(t)
	it := freshCropKey(t)
	gone, cancel := context.WithCancel(context.Background())
	cancel()

	s.cropAnswer(gone, it, func(context.Context) (CropResponse, bool) {
		return CropResponse{X: 80, Y: 45, W: 160, H: 90, FrameW: 320, FrameH: 180}, true
	})
	if _, found := s.storedCrop(it); found {
		t.Error("a detection nobody was waiting for was remembered as an answer")
	}
}

// Nothing measured here either, and the second word says so: no duration to
// place the samples by, and a build with no ffmpeg at all.
func TestDetectCropSaysWhenItDidNotLook(t *testing.T) {
	s := &Server{thumbs: NewThumbnailer(nil, nil, quietLog()), log: quietLog()}
	it := freshCropKey(t)
	it.Duration = 0
	if box, measured := s.detectCrop(context.Background(), it); measured || box != (CropResponse{}) {
		t.Errorf("a film nothing had measured answered %+v, measured=%v", box, measured)
	}
}
