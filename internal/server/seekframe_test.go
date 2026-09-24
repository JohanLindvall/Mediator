package server

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

func TestASeekFrameIsAskedForInBounds(t *testing.T) {
	for in, want := range map[string]int{
		"": 320, "abc": 320, "0": 320, "-5": 320,
		"50": seekFrameMinWidth, "480": 480, "5000": seekFrameMaxWidth,
	} {
		if got := seekFrameWidth(in); got != want {
			t.Errorf("seekFrameWidth(%q) = %d, want %d", in, got, want)
		}
	}
	for _, c := range []struct {
		in   string
		dur  int64
		want float64
		ok   bool
	}{
		{"1234", 10_000, 1.234, true},
		{"0", 10_000, 0, true},
		// The very end has no frame after it; half a second short of it does.
		{"9999", 10_000, 9.5, true},
		{"99999", 10_000, 9.5, true},
		// A film nobody has measured is taken at its word.
		{"99999", 0, 99.999, true},
		{"-1", 10_000, 0, false},
		{"soon", 10_000, 0, false},
	} {
		got, ok := seekFrameAt(c.in, c.dur)
		if ok != c.ok || got != c.want {
			t.Errorf("seekFrameAt(%q, %d) = %v, %v, want %v, %v", c.in, c.dur, got, ok, c.want, c.ok)
		}
	}
}

// The frame is the moment's, decoded forward from the keyframe, and a DVD
// title's is read from where the disc puts the moment rather than by time.
func TestASeekFrameIsTakenAtTheMoment(t *testing.T) {
	plain := library.Item{Kind: library.KindVideo, Path: "/m/film.mkv"}
	spec, ok := seekFrameSpec(plain, 1.234, 320)
	if !ok || spec.input != "/m/film.mkv" || spec.seek != "1.234" || !spec.accurate || spec.loopback || spec.pre != nil {
		t.Fatalf("plain file: %+v, %v", spec, ok)
	}
	args := frameArgs(spec)
	if slices.Contains(args, "-noaccurate_seek") {
		t.Error("the frame at a moment must be decoded to, not the keyframe before it")
	}
	if i, j := slices.Index(args, "-ss"), slices.Index(args, "-i"); i < 0 || i > j {
		t.Errorf("the seek belongs in front of the input: %v", args)
	}
	// With no time to seek to, the input options go in front of the input
	// and no -ss is written at all.
	byPos := frameArgs(frameSpec{input: "http://x/stream", pre: []string{"-seekable", "0", "-offset", "4096"}, width: 320, quality: 5})
	if slices.Contains(byPos, "-ss") {
		t.Errorf("a seek by position has no -ss: %v", byPos)
	}
	if i, j := slices.Index(byPos, "-offset"), slices.Index(byPos, "-i"); i < 0 || i > j {
		t.Errorf("the offset belongs in front of the input: %v", byPos)
	}
}

// A clip whose brightness is its own clock — luma twenty times the second —
// with one keyframe at the start: the frame at five seconds is about a
// hundred bright. The clip is written in video range and the frame comes back
// in full range, so the brightness read back is (Y - 16) * 255 / 219, and one
// frame of the clip is 0.93 of it. Measured: the exact frame lands within 0.6
// of the moment's brightness, and ffmpeg without the accurate seek lands
// three frames early — 2.8 off — which the tolerance here is set between.
func TestTheFrameIsTheMomentsToTheFrame(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	path := filepath.Join(t.TempDir(), "clock.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:size=64x48:rate=25:duration=10",
		"-vf", "geq=lum='min(255,T*20)':cb=128:cr=128",
		// One keyframe, at the start: the encoder takes a brightness ramp for
		// a run of scene cuts and makes most frames keyframes unless told not
		// to, and then a keyframe and the moment are the same frame.
		"-c:v", "mpeg4", "-q:v", "2", "-g", "250", "-sc_threshold", "1000000000", "-bf", "0",
		"-pix_fmt", "yuv420p", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build the clip: %v: %s", err, out)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	it := library.Item{ID: "clock", Kind: library.KindVideo, Path: path, Size: info.Size(), ModTime: info.ModTime().UnixMilli(), Duration: 10_000}
	th := NewThumbnailer(nil, nil, testLog())
	luma := func(at float64) float64 {
		t.Helper()
		data, err := th.Frame(context.Background(), it, at, 96)
		if err != nil {
			t.Fatalf("frame at %v: %v", at, err)
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if w := img.Bounds().Dx(); w != 96 {
			t.Errorf("frame is %d wide, want the 96 asked for", w)
		}
		y, ok := img.(*image.YCbCr)
		if !ok {
			t.Fatalf("frame is a %T", img)
		}
		sum := 0
		for _, v := range y.Y {
			sum += int(v)
		}
		return float64(sum) / float64(len(y.Y))
	}
	for _, at := range []float64{2.52, 5.0, 8.04} {
		got, want := luma(at), (at*20-16)*255/219
		if got < want-1.5 || got > want+1.5 {
			t.Errorf("frame at %.2fs is %.1f bright, want about %.1f — the moment's own frame", at, got, want)
		}
	}
}

// Through the handler: a frame for a film, 400 for a moment that is not one,
// and 404 for what is not a film.
func TestTheSeekFrameEndpoint(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	writeClip(t, filepath.Join(dir, "clip.mp4"), 12)
	if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("not a picture"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, lib := flagServer(t, dir)
	var film, still string
	for _, it := range lib.List(library.Query{Limit: 10}).Items {
		switch it.Kind {
		case library.KindVideo:
			film = it.ID
		case library.KindImage:
			still = it.ID
		}
	}
	if film == "" || still == "" {
		t.Fatal("the fixture did not index")
	}
	get := func(path string) *http.Response {
		t.Helper()
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	res := get("/api/frame/" + film + "?t=6000&w=128")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("frame: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	cfg, err := jpeg.DecodeConfig(res.Body)
	if err != nil || cfg.Width != 128 {
		t.Errorf("frame: %v, %d wide, want 128", err, cfg.Width)
	}
	if res := get("/api/frame/" + film + "?t=later"); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a moment that is not a number: %d", res.StatusCode)
	}
	if res := get("/api/frame/" + still + "?t=1000"); res.StatusCode != http.StatusNotFound {
		t.Errorf("a picture has no moments: %d", res.StatusCode)
	}
	if res := get("/api/frame/nosuch?t=1000"); res.StatusCode != http.StatusNotFound {
		t.Errorf("no such film: %d", res.StatusCode)
	}
}
