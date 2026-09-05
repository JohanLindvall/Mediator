package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// The arithmetic of a border: the union grows to hold every sample and an
// empty sample changes nothing, a box is worth applying only where it gives
// back more than the encoder's rounding, and cropdetect's last word is the
// one read.
func TestCropArithmetic(t *testing.T) {
	a := CropResponse{X: 80, Y: 45, W: 160, H: 90, FrameW: 320, FrameH: 180}
	b := CropResponse{X: 60, Y: 45, W: 160, H: 100}
	u := a.union(b)
	if u.X != 60 || u.Y != 45 || u.W != 180 || u.H != 100 || u.FrameW != 320 || u.FrameH != 180 {
		t.Errorf("union = %+v, want the smallest box holding both", u)
	}
	if got := a.union(CropResponse{}); got != a {
		t.Errorf("an empty sample changed the box: %+v", got)
	}
	if got := (CropResponse{}).union(a); got != a {
		t.Errorf("the first sample was not taken: %+v", got)
	}
	if !a.worthwhile() {
		t.Error("a picture in a quarter of its frame is not worth cropping?")
	}
	if (CropResponse{X: 1, Y: 1, W: 318, H: 178, FrameW: 320, FrameH: 180}).worthwhile() {
		t.Error("a pixel of rounding at each edge was taken for a border")
	}
	if (CropResponse{W: 160, H: 90}).worthwhile() {
		t.Error("a box in a frame of unknown size was worth applying")
	}

	out := "frame=1 crop=300:170:10:5\nframe=8 crop=160:90:80:45\n"
	if got := lastCrop(out); got != (CropResponse{X: 80, Y: 45, W: 160, H: 90}) {
		t.Errorf("lastCrop = %+v, want the last line", got)
	}
	if got := lastCrop("nothing here"); got != (CropResponse{}) {
		t.Errorf("lastCrop of nothing = %+v", got)
	}
	w, h := frameSize("  Stream #0:0: Video: h264 (High), yuv420p(progressive), 320x180 [SAR 1:1], 25 fps")
	if w != 320 || h != 180 {
		t.Errorf("frameSize = %dx%d, want 320x180", w, h)
	}
}

// A clip padded into a larger frame answers the picture inside it, in the
// frame's own coordinates, and the answer is kept.
func TestCropFindsThePictureInsideTheFrame(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "padded.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x90:rate=25:duration=4",
		"-vf", "pad=320:180:80:45:black", "-pix_fmt", "yuv420p",
		"-c:v", "libx264", "-preset", "ultrafast", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not make the clip: %v: %s", err, out)
	}
	ts, _, lib := serverUnderTest(t, dir)
	items := lib.List(library.Query{Limit: 5}).Items
	if len(items) != 1 {
		t.Fatalf("indexed %d items, want the clip", len(items))
	}
	// The samples fall at fractions of the length, so the length has to be
	// known: the player asks for the item, which reads it, before it asks
	// for the borders.
	lib.EnrichNow(context.Background(), []string{items[0].ID})
	if it, _ := lib.Get(items[0].ID); it.Duration <= 0 {
		t.Fatal("the clip's length was not read")
	}
	res, err := http.Get(ts.URL + "/api/crop/" + items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var box CropResponse
	if err := json.NewDecoder(res.Body).Decode(&box); err != nil {
		t.Fatal(err)
	}
	if box.W < 150 || box.W > 170 || box.H < 80 || box.H > 100 || box.X < 70 || box.X > 90 || box.Y < 40 || box.Y > 50 {
		t.Errorf("crop = %+v, want about the 160x90 picture at 80,45", box)
	}
	if box.FrameW != 320 || box.FrameH != 180 {
		t.Errorf("frame = %dx%d, want 320x180", box.FrameW, box.FrameH)
	}
}
