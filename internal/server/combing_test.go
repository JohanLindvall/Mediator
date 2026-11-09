// SPDX-License-Identifier: MIT

package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// The verdict against the counts real files gave: the combed one at 138 of
// 138 bottom first, the progressive ones of the same directory at 21 of 138
// at the most, and two that idet could mostly not decide.
func TestCombVerdict(t *testing.T) {
	for _, c := range []struct {
		tff, bff, prog int
		want           string
		why            string
	}{
		{0, 138, 0, "bff", "the combed file as measured"},
		{116, 0, 0, "tff", "top first, as a synthetic merge of two frames reads"},
		{0, 21, 117, "", "the worst progressive file: fine detail fools some frames"},
		{6, 10, 121, "", "a progressive file with a few misreadings each way"},
		{0, 0, 138, "", "plainly progressive"},
		{0, 0, 61, "", "progressive with half the frames undecided (left out)"},
		{10, 10, 0, "", "too few decided frames to mean anything"},
		{40, 29, 69, "", "half is not more than half"},
		{40, 30, 69, "tff", "more than half, top first leading"},
	} {
		if got := combVerdict(c.tff, c.bff, c.prog); got != c.want {
			t.Errorf("combVerdict(%d, %d, %d) = %q; want %q — %s", c.tff, c.bff, c.prog, got, c.want, c.why)
		}
	}
}

// A judged picture is deinterlaced on every frame in the order found, still
// before any scale; an unjudged one keeps the deinterlacer that believes the
// file's flags.
func TestPictureFilterFollowsTheVerdict(t *testing.T) {
	if got := pictureFilter("", "scale=320:-2"); got != videoFilter("scale=320:-2") {
		t.Errorf("no verdict changed the chain: %q", got)
	}
	for _, order := range []string{"tff", "bff"} {
		got := pictureFilter(order, "scale=320:-2")
		first, rest, _ := strings.Cut(got, ",")
		if !strings.HasPrefix(first, "bwdif=") || !strings.Contains(first, "deint=all") ||
			!strings.Contains(first, "parity="+order) || !strings.Contains(first, "mode=send_frame") {
			t.Errorf("%s: the deinterlacer is not first, total and in order: %q", order, got)
		}
		if rest != "scale=320:-2" {
			t.Errorf("%s: the rest of the chain moved: %q", order, got)
		}
	}
}

// The look against pictures built for it: two moments merged into each
// frame's fields and flagged progressive, which is the file this is for, and
// the same source left progressive. testsrc and not testsrc2, whose fine
// moving detail idet reads as fields even in a progressive encode.
func TestCombedPictureIsFound(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	for _, c := range []struct {
		name, rate, vf string
		want           string
	}{
		{"combed.mp4", "60", "tinterlace=mode=merge,setfield=prog", "tff"},
		{"progressive.mp4", "30", "null", ""},
	} {
		path := filepath.Join(dir, c.name)
		cmd := exec.CommandContext(t.Context(), ffmpeg, "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc=size=320x240:rate="+c.rate+":duration=5",
			"-vf", c.vf, "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-y", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("ffmpeg could not build %s: %v: %s", c.name, err, out)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		it := library.Item{ID: c.name, Name: c.name, Path: path, Kind: library.KindVideo,
			VCodec: "h264", Size: info.Size(), ModTime: info.ModTime().UnixNano(), Duration: 5000}
		order, answered := combedPicture(t.Context(), it)
		if !answered {
			t.Fatalf("%s: no answer", c.name)
		}
		if order != c.want {
			t.Errorf("%s: found %q; want %q", c.name, order, c.want)
		}
	}
}

// Asked through the server, the verdict is remembered, condemns a copy and
// reaches the listing; a picture no look is worth is never decoded.
func TestInterlacingIsRemembered(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	_, srv, _ := serverUnderTest(t, t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "combed.mp4")
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=60:duration=5",
		"-vf", "tinterlace=mode=merge,setfield=prog", "-c:v", "libx264", "-preset", "ultrafast",
		"-pix_fmt", "yuv420p", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build the clip: %v: %s", err, out)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	it := library.Item{ID: "c", Name: "combed.mp4", Path: path, Kind: library.KindVideo,
		VCodec: "h264", Size: info.Size(), ModTime: info.ModTime().UnixNano(), Duration: 5000}
	if got := srv.interlacing(t.Context(), it); got != "tff" {
		t.Fatalf("interlacing = %q; want tff", got)
	}
	if v, ok := srv.combing.get(itemKey(it)); !ok || v != "tff" {
		t.Errorf("the verdict was not remembered: %q %v", v, ok)
	}
	if !srv.mustReencode(t.Context(), it) {
		t.Error("an interlaced picture was allowed to be copied")
	}
	items := []library.Item{it}
	srv.stampReencode(items)
	if !items[0].Reencode {
		t.Error("the listing did not carry the verdict")
	}

	tall := it
	tall.ID, tall.Height, tall.FPS = "tall", 2160, 24
	if got := srv.interlacing(t.Context(), tall); got != "" {
		t.Errorf("a 2160-line picture was judged %q", got)
	}
	if _, ok := srv.combing.get(itemKey(tall)); ok {
		t.Error("a picture too tall to be interlaced was looked at")
	}
}
