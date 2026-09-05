package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// A stream that puts more B-frames between its references than its own
// parameter set declares plays correctly only where something buffers more
// generously than the declaration asks. A browser that takes it at its word
// drops the frames that arrive behind what it has already shown, which is a
// film at three quarters of its frame rate with a limp — and a copy preserves
// it exactly, which is why it has to be found before one is made.
func TestReorderUnderstatedIsFound(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if library.FFprobePath() == "" {
		t.Skip("ffprobe not installed")
	}
	dir := t.TempDir()

	// Ordinary modern encodes, which the first version of the detector
	// accused wholesale: a B-pyramid puts three consecutive B-frames under a
	// declaration of two, and that is honest — the middle frame is itself a
	// reference. Being wrong here re-encodes every frame of an innocent film.
	for name, args := range map[string][]string{
		"a modern capture.avi": {"-c:v", "libx264", "-preset", "ultrafast", "-bf", "3", "-x264-params", "b-adapt=0"},
		"a plain capture.avi":  {"-c:v", "libx264", "-preset", "ultrafast", "-bf", "0"},
	} {
		path := filepath.Join(dir, name)
		cmd := exec.Command(ffmpeg, append([]string{"-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc=size=160x120:rate=25:duration=4",
			"-pix_fmt", "yuv420p"}, append(args, "-y", path)...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("ffmpeg could not build %s: %v: %s", name, err, out)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		it := library.Item{
			ID: name, Name: name, Path: path, Kind: library.KindVideo, VCodec: "h264",
			Size: info.Size(), ModTime: info.ModTime().UnixMilli(),
		}
		understated, answered := reorderUnderstated(t.Context(), it)
		if !answered {
			t.Errorf("%s was not judged at all", name)
		} else if understated {
			t.Errorf("%s was accused of lying about its reordering", name)
		}
	}
}

// The decision itself, against the numbers real files produced. The film
// that found the fault declared one and ran three; the modern encodes that
// found the false positive declare two and run three, honestly.
func TestReorderVerdict(t *testing.T) {
	for _, c := range []struct {
		why                            string
		declared, longestRun, inverted int
		want                           bool
	}{
		{"the film that found the fault: declared 1, three B-frames between references", 1, 3, 0, true},
		{"a declaration of none with any B-frame at all is the same lie", 0, 1, 0, true},
		{"an ordinary B-pyramid: three under a declaration of two is honest", 2, 3, 0, false},
		{"deeper pyramids on higher settings, still honest", 4, 7, 0, false},
		{"no B-frames is nobody's lie", 0, 0, 0, false},
		{"frames emitted with timestamps running backwards are the fault seen directly,\n" +
			"whatever the declaration says", 2, 3, 5, true},
		{"but a single inversion is an edit-list oddity, not a lie", 2, 3, 1, false},
	} {
		if got := reorderVerdict(c.declared, c.longestRun, c.inverted); got != c.want {
			t.Errorf("reorderVerdict(%d, %d, %d) = %v; want %v — %s",
				c.declared, c.longestRun, c.inverted, got, c.want, c.why)
		}
	}
}

// Nothing is accused without being read, and a file that cannot be read is
// left exactly as it was — no answer is not an accusation.
func TestReorderSaysNothingAboutWhatItCannotRead(t *testing.T) {
	missing := library.Item{
		ID: "abc", Name: "gone.avi", Path: filepath.Join(t.TempDir(), "gone.avi"),
		Kind: library.KindVideo, VCodec: "h264",
	}
	if understated, answered := reorderUnderstated(t.Context(), missing); understated || answered {
		t.Error("a file that could not be read was accused, or counted as judged")
	}
	// And an item whose streams are not known yet is nobody's answer.
	s := &Server{log: testLog()}
	if s.mustReencode(t.Context(), library.Item{Kind: library.KindVideo}) {
		t.Error("an unprobed item was accused")
	}
	if s.mustReencode(t.Context(), library.Item{Kind: library.KindAudio, VCodec: "h264"}) {
		t.Error("music was asked about its picture")
	}
}

// Asked twice, read once: the answer is remembered for the run, which is what
// keeps this to the same shape of cost EnsureCodecs already pays.
func TestReorderIsRememberedForTheRun(t *testing.T) {
	s := &Server{log: testLog()}
	it := library.Item{ID: "abc", ModTime: 1, Size: 2, Kind: library.KindVideo, VCodec: "h264"}
	s.reorder.put(it.ID+"|1|2", true)
	if !s.mustReencode(t.Context(), it) {
		t.Fatal("the remembered answer was not used")
	}
	// A file that changed under us is a different file, not a stale answer.
	changed := it
	changed.ModTime = 99
	if s.mustReencode(t.Context(), changed) {
		t.Error("the answer outlived the file it was about")
	}
}

// A look that could not finish is not a verdict, and must not be remembered
// as one: with the short budget an item fetch gives it, a film would
// otherwise be excused for the life of the process — for the conversions
// with their own budget as much as for the next open.
func TestReorderNoAnswerIsNotCached(t *testing.T) {
	ts, srv, _ := serverUnderTest(t, t.TempDir())
	_ = ts
	it := library.Item{ID: "gone", Name: "gone.mp4", Path: filepath.Join(t.TempDir(), "gone.mp4"),
		Kind: library.KindVideo, VCodec: "h264"}
	for range 2 {
		if srv.mustReencode(context.Background(), it) {
			t.Fatal("a file nobody could read was accused")
		}
	}
	if _, cached := srv.reorder.get("gone|0|0"); cached {
		t.Fatal("no answer was remembered as an acquittal")
	}
}

// The item endpoint says when a film cannot be played as it is, so the
// player converts it rather than handing the file to the element — which
// is where two MP4s from one muxer stuttered on a phone, their reorder
// depth declared as two against runs of three and six.
func TestItemSaysWhenACopyWouldStutter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	// A real clip, so the open-time probe finds a codec: nothing is judged
	// before its streams are known, and the verdict is keyed by the file.
	writeClip(t, path, 2)
	ts, srv, lib := serverUnderTest(t, dir)
	id := library.PathID(path)
	it, _ := lib.Get(id)

	fetch := func() library.Item {
		t.Helper()
		res, err := http.Get(ts.URL + "/api/item/" + id)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var got library.Item
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := fetch(); got.Reencode {
		t.Fatal("an unjudged film was accused")
	}
	// What the look would have said, had ffprobe been able to read a film.
	srv.reorder.put(fmt.Sprintf("%s|%d|%d", id, it.ModTime, it.Size), true)
	if got := fetch(); !got.Reencode {
		t.Fatal("the item did not say the picture must be re-encoded")
	}
}
