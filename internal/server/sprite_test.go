package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	"image/jpeg"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/rartest"
)

func TestSpriteEligible(t *testing.T) {
	// Archived video is eligible now: the sheet is ten seeks, which is what
	// the archived thumbnail already does over the loopback reader, and the
	// whole thing measured 164.5 MB and 2.9 s on a real member. What keeps
	// one off a hover is not what the item is but what the disk is doing —
	// see sheetWorthMaking — so this stays a case, with the opposite answer,
	// to record that the change was deliberate.
	archived := archivedItem(t, rartest.Payload(30_000), "Feature.mkv")
	archived.Duration = 60_000

	cases := []struct {
		name string
		it   library.Item
		want bool
	}{
		{"long video", library.Item{Kind: library.KindVideo, Duration: 60_000}, true},
		{"archived video", archived, true},
		{"exactly the minimum", library.Item{Kind: library.KindVideo, Duration: spriteMinDurationMs}, true},
		{"too short", library.Item{Kind: library.KindVideo, Duration: spriteMinDurationMs - 1}, false},
		{"unknown duration", library.Item{Kind: library.KindVideo}, false},
		{"image", library.Item{Kind: library.KindImage, Duration: 60_000}, false},
		{"audio", library.Item{Kind: library.KindAudio, Duration: 60_000}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := spriteEligible(c.it); got != c.want {
				t.Fatalf("spriteEligible = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSpriteSkipped(t *testing.T) {
	long := library.Item{ID: "v", Kind: library.KindVideo, Duration: 60_000, Path: "video.mp4"}
	cases := []struct {
		name   string
		it     library.Item
		ffmpeg string
	}{
		{"too short", library.Item{ID: "v", Kind: library.KindVideo, Duration: 1000}, "ffmpeg"},
		{"unknown duration", library.Item{ID: "v", Kind: library.KindVideo}, "ffmpeg"},
		{"not a video", library.Item{ID: "i", Kind: library.KindImage, Duration: 60_000}, "ffmpeg"},
		{"no ffmpeg", long, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			th := NewThumbnailer(nil, nil, testLog())
			th.ffmpeg = c.ffmpeg
			if _, err := th.Sprite(context.Background(), c.it); !errors.Is(err, ErrNoThumb) {
				t.Fatalf("err = %v, want ErrNoThumb", err)
			}
		})
	}
}

// The client places frames from the item's duration alone, so what is
// sampled has to be exactly the documented convention: frame i at
// duration*(i+0.5)/spriteFrames.
func TestSpriteOffsets(t *testing.T) {
	cases := []struct {
		name       string
		durationMs int64
		first      float64
		last       float64
	}{
		{"round minute", 60_000, 3, 57},
		{"the shortest sheet there is", spriteMinDurationMs, 1.5, 28.5},
		{"awkward length", 33_333, 1.66665, 31.66635},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			at := spriteOffsets(c.durationMs)
			if len(at) != spriteFrames {
				t.Fatalf("got %d offsets, want %d", len(at), spriteFrames)
			}
			if math.Abs(at[0]-c.first) > 0.001 || math.Abs(at[len(at)-1]-c.last) > 0.001 {
				t.Fatalf("offsets run %v..%v, want %v..%v", at[0], at[len(at)-1], c.first, c.last)
			}
			// Evenly spaced, or the client's arithmetic points at the wrong
			// moment of the film for every frame but the first.
			step := at[1] - at[0]
			for i := 2; i < len(at); i++ {
				if math.Abs((at[i]-at[i-1])-step) > 0.001 {
					t.Fatalf("uneven spacing at %d: %v", i, at)
				}
			}
		})
	}
}

// Each frame is its own seek, and the seek goes before the input: that is
// what makes it read a few megabytes around the timestamp instead of
// decoding the film up to it. The one-pass recipe this replaced took over
// five minutes on an 87-minute release, against its own two-minute timeout.
func TestSpriteFrameArgs(t *testing.T) {
	joined := strings.Join(spriteFrameArgs("in.mkv", "out.jpg", 12.5), " ")
	if !strings.Contains(joined, "-ss 12.500 -i in.mkv") {
		t.Fatalf("not an input seek: %s", joined)
	}
	if !strings.Contains(joined, "-frames:v 1") {
		t.Fatalf("not a single frame: %s", joined)
	}
	if !strings.Contains(joined, fmt.Sprintf("scale=%d:-2", spriteFrameWidth)) {
		t.Fatalf("not scaled to the sheet's frame width: %s", joined)
	}
	if strings.Contains(joined, "tile=") {
		t.Fatalf("tiling is ours now, not ffmpeg's: %s", joined)
	}
}

// writeClip writes a real (tiny) video of the given length, or skips the
// test: ffmpeg is optional at runtime, so the tests that need it are optional
// too.
func writeClip(t *testing.T, path string, seconds int) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=5:duration="+strconv.Itoa(seconds),
		"-c:v", "mpeg4", "-pix_fmt", "yuv420p", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build a test clip: %v: %s", err, out)
	}
}

// writeBlackClip is the same, holding nothing but black — what the front
// matter of a release looks like to the thumbnailer.
func writeBlackClip(t *testing.T, path string, seconds int) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:size=160x120:rate=5:duration="+strconv.Itoa(seconds),
		"-c:v", "mpeg4", "-pix_fmt", "yuv420p", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build a test clip: %v: %s", err, out)
	}
}

func testVideo(t *testing.T, seconds int) library.Item {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip.mp4")
	writeClip(t, path, seconds)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return library.Item{
		ID: "clip1", Name: "clip.mp4", Path: path, Kind: library.KindVideo,
		Size: info.Size(), ModTime: info.ModTime().UnixMilli(),
		Duration: int64(seconds) * 1000,
	}
}

func TestSpriteGeneratesAndCaches(t *testing.T) {
	it := testVideo(t, 40)
	store, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	data, err := NewThumbnailer(store, nil, testLog()).Sprite(context.Background(), it)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	// The clip is 4:3, scaled to the sheet's frame width, so a frame is
	// spriteFrameWidth by three quarters of it.
	wantH := spriteRows * spriteFrameWidth * 3 / 4
	if cfg.Width != spriteCols*spriteFrameWidth || cfg.Height != wantH {
		t.Fatalf("sheet is %dx%d, want %dx%d",
			cfg.Width, cfg.Height, spriteCols*spriteFrameWidth, wantH)
	}

	// Stored under the reserved width, so it neither replaces a thumbnail nor
	// is ever served as one.
	cached, ok := store.GetThumb(it.ID, it.ModTime, it.Size, spriteCacheWidth)
	if !ok || !bytes.Equal(cached, data) {
		t.Fatal("sheet not cached under the sprite key")
	}
	if _, ok := store.GetThumb(it.ID, it.ModTime, it.Size, defaultThumbWidth); ok {
		t.Fatal("sheet stored as a thumbnail too")
	}

	// A fresh thumbnailer serves it from the store with the source gone.
	if err := os.Remove(it.Path); err != nil {
		t.Fatal(err)
	}
	again, err := NewThumbnailer(store, nil, testLog()).Sprite(context.Background(), it)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, data) {
		t.Fatal("cached sheet differs from the generated one")
	}
}

func TestSpriteEndpoint(t *testing.T) {
	dir := t.TempDir()
	writeClip(t, filepath.Join(dir, "long.mp4"), 40)
	writeClip(t, filepath.Join(dir, "short.mp4"), 5)
	ts, lib := flagServer(t, dir)
	long := library.PathID(filepath.Join(dir, "long.mp4"))
	short := library.PathID(filepath.Join(dir, "short.mp4"))
	// The sheet is a function of the duration, which is read from the
	// container — the player has asked for the item before it asks for this.
	lib.EnrichNow(context.Background(), []string{long, short})

	res, err := http.Get(ts.URL + "/api/sprite/" + long)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content type %q", ct)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != spriteCols*spriteFrameWidth {
		t.Fatalf("sheet %dx%d", cfg.Width, cfg.Height)
	}

	// Nothing to scrub is a 404, which is what the client treats as "no
	// scrub strip" — the same answer as an id nobody indexed.
	for _, id := range []string{short, "deadbeefdeadbeef"} {
		res, err := http.Get(ts.URL + "/api/sprite/" + id)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("status %d for %s, want 404", res.StatusCode, id)
		}
	}
}

// Sprites go through the machinery thumbnails use: one run per key however
// many requests arrive, a failure remembered so it is not retried per
// request, and a cancellation remembered as nothing at all.
func TestCachedSharesAndRemembers(t *testing.T) {
	it := library.Item{ID: "x", ModTime: 1, Size: 2}
	th := NewThumbnailer(nil, nil, testLog())

	var mu sync.Mutex
	calls := 0
	inflight := make(chan struct{})
	release := make(chan struct{})
	gen := func(context.Context) ([]byte, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		close(inflight)
		<-release
		return []byte("sheet"), nil
	}
	share := func(wg *sync.WaitGroup) {
		defer wg.Done()
		data, err := th.cached(context.Background(), it, spriteCacheWidth, gen)
		if err != nil {
			t.Error(err)
			return
		}
		if string(data) != "sheet" {
			t.Errorf("follower got %q", data)
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go share(&wg)
	<-inflight // the leader is generating; only now can the rest join it
	for range 3 {
		wg.Add(1)
		go share(&wg)
	}
	time.Sleep(50 * time.Millisecond) // let them reach the shared entry
	close(release)
	wg.Wait()
	if calls != 1 {
		t.Fatalf("%d generations, want 1", calls)
	}

	failed := 0
	fail := func(context.Context) ([]byte, error) { failed++; return nil, ErrNoThumb }
	for range 2 {
		if _, err := th.cached(context.Background(), it, 64, fail); !errors.Is(err, ErrNoThumb) {
			t.Fatalf("err = %v, want ErrNoThumb", err)
		}
	}
	if failed != 1 {
		t.Fatalf("%d attempts after a failure, want 1 (negative cache)", failed)
	}

	gaveUp := 0
	cancel := func(context.Context) ([]byte, error) { gaveUp++; return nil, context.Canceled }
	for range 2 {
		if _, err := th.cached(context.Background(), it, 128, cancel); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	}
	// A cancellation says nothing about the item, so it must be retried.
	if gaveUp != 2 {
		t.Fatalf("%d attempts after a cancellation, want 2", gaveUp)
	}
}

// Whether a pointer may pay for a sheet that does not exist yet. This used to
// turn on whether the content was archived, which measured wrong: a DVD title
// is archived by the same definition as a rar member and its sheet costs a
// third as long, MPEG-2 keyframes being dense. What every one of those reads
// has in common is the disk, and the disk is what playback needs.
func TestSheetWorthMaking(t *testing.T) {
	for _, c := range []struct {
		name                     string
		hover, stored, streaming bool
		want                     bool
	}{
		{"a hover, nothing playing", true, false, false, true},
		{"a hover while something plays", true, false, true, false},
		{"a hover for one already made", true, true, true, true},
		{"the player asking, while it plays", false, false, true, true},
		{"the player asking, nothing playing", false, false, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := sheetWorthMaking(c.hover, c.stored, c.streaming); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// A seek that produced no frame leaves its cell black and every later frame
// where it belongs. The frames used to be appended as they came back, so one
// gap slid every following frame one cell to the left — into a cell the
// client reads as an earlier moment of the film.
func TestSpriteKeepsAGapInItsCell(t *testing.T) {
	solid := func(c color.RGBA) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, 8, 6))
		stddraw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, stddraw.Src)
		return img
	}
	red := color.RGBA{220, 30, 30, 255}
	green := color.RGBA{30, 200, 30, 255}
	frames := make([]image.Image, spriteFrames)
	frames[0] = solid(red)
	// frames[1] is the seek that failed.
	frames[2] = solid(green)

	data, err := tileSprite(frames)
	if err != nil {
		t.Fatal(err)
	}
	sheet, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	// The middle of each of the first three cells, top row.
	at := func(cell int) color.RGBA {
		r, g, b, _ := sheet.At(cell*8+4, 3).RGBA()
		return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 255}
	}
	near := func(got, want color.RGBA) bool {
		d := func(a, b uint8) int { return max(int(a)-int(b), int(b)-int(a)) }
		return d(got.R, want.R) < 40 && d(got.G, want.G) < 40 && d(got.B, want.B) < 40
	}
	if got := at(0); !near(got, red) {
		t.Errorf("cell 0 is %v, want the first frame", got)
	}
	if got := at(1); !near(got, color.RGBA{0, 0, 0, 255}) {
		t.Errorf("cell 1 is %v, want black for the frame that was not taken", got)
	}
	if got := at(2); !near(got, green) {
		t.Errorf("cell 2 is %v, want the third frame in its own cell", got)
	}
	// Nothing at all is not a sheet.
	if _, err := tileSprite(make([]image.Image, spriteFrames)); !errors.Is(err, ErrNoThumb) {
		t.Errorf("an empty sheet answered %v, want ErrNoThumb", err)
	}
}
