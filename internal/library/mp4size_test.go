package library

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The picture's size is written in the same box as the label the rewrap
// decision reads, a few bytes further on — so a listing can say how big a film
// is without a process per file. Checked against what ffprobe says about the
// very same file, at sizes with an odd number in them: a wrong offset reads
// plausible-looking rubbish, and only a real comparison catches that.
func TestVideoSampleInfoReadsThePictureSize(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if FFprobePath() == "" {
		t.Skip("ffprobe not installed")
	}
	dir := t.TempDir()
	for _, size := range []string{"322x178", "1920x1080", "640x480"} {
		path := filepath.Join(dir, "clip-"+size+".mp4")
		cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc=size="+size+":rate=5:duration=1",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-y", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("ffmpeg could not build a %s clip: %v: %s", size, err, out)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		format, w, h, _, _ := SampleInfo(Item{Path: path, Size: info.Size()})
		if format != "avc1" {
			t.Errorf("%s: label %q, want avc1", size, format)
		}
		// What ffprobe makes of the same file, which is the authority here.
		out, err := exec.Command(FFprobePath(), "-v", "error", "-select_streams", "v:0",
			"-show_entries", "stream=width,height", "-of", "csv=p=0", path).Output()
		if err != nil {
			t.Fatal(err)
		}
		want := strings.TrimSpace(string(out))
		if got := fmt.Sprintf("%d,%d", w, h); got != want {
			t.Errorf("%s: read %s, ffprobe says %s", size, got, want)
		}
		// And it is the size that was asked for, so a parser agreeing with a
		// probe about the wrong thing is caught too.
		if want != strings.ReplaceAll(size, "x", ",") {
			t.Errorf("%s: ffmpeg wrote %s", size, want)
		}
		_ = strconv.Itoa
	}
}

// Nothing is claimed about a file that is not one of these containers.
func TestVideoSampleInfoSaysNothingAboutOtherContainers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not a film.txt")
	if err := os.WriteFile(path, []byte("this is not an ISO base media file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if format, w, h := VideoSampleInfo(Item{Path: path, Size: info.Size()}); format != "" || w != 0 || h != 0 {
		t.Errorf("read %q %dx%d out of a text file", format, w, h)
	}
}

// The frame rate comes out of the table that says how long each sample lasts,
// which is the same walk again. Checked against ffprobe on rates that are not
// whole numbers, since those are where an arithmetic slip shows.
func TestSampleInfoReadsTheFrameRate(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if FFprobePath() == "" {
		t.Skip("ffprobe not installed")
	}
	dir := t.TempDir()
	for _, rate := range []string{"25", "30000/1001", "24000/1001", "60"} {
		path := filepath.Join(dir, "clip.mp4")
		cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc=size=160x120:rate="+rate+":duration=2",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-y", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("ffmpeg could not build a %s clip: %v: %s", rate, err, out)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, _, got := SampleInfo(Item{Path: path, Size: info.Size()})
		out, err := exec.Command(FFprobePath(), "-v", "error", "-select_streams", "v:0",
			"-show_entries", "stream=avg_frame_rate", "-of", "csv=p=0", path).Output()
		if err != nil {
			t.Fatal(err)
		}
		num, den, _ := strings.Cut(strings.TrimSpace(string(out)), "/")
		n, _ := strconv.ParseFloat(num, 64)
		d, _ := strconv.ParseFloat(den, 64)
		if d == 0 {
			t.Fatalf("%s: ffprobe said %q", rate, out)
		}
		want := n / d
		// Within a hundredth: the reading is exact arithmetic on the table,
		// and a mismatch means the table was read wrongly, not rounded.
		if got < want-0.01 || got > want+0.01 {
			t.Errorf("%s: read %.4f fps, ffprobe says %.4f", rate, got, want)
		}
	}
}
