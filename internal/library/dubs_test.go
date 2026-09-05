package library

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// buildDubbed writes a clip carrying several soundtracks, each labelled with
// its language, which is the shape a downloader produces for a video that
// ships automatic dubs beside its original.
func buildDubbed(t *testing.T, path string, langs ...string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=5:duration=2"}
	for i := range langs {
		// A different tone per track, so they are not the same bytes four
		// times over and a muxer cannot collapse them.
		args = append(args, "-f", "lavfi", "-i",
			"sine=frequency="+strconv.Itoa(300+100*i)+":duration=2")
	}
	args = append(args, "-map", "0:v")
	for i := range langs {
		args = append(args, "-map", strconv.Itoa(i+1)+":a")
	}
	args = append(args, "-c:v", "libvpx-vp9", "-b:v", "50k", "-c:a", "libopus")
	for i, l := range langs {
		args = append(args, "-metadata:s:a:"+strconv.Itoa(i), "language="+l)
	}
	args = append(args, "-y", path)
	if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build a dubbed clip: %v: %s", err, out)
	}
}

// A video that ships automatic dubs arrives as one file carrying every
// language as its own soundtrack, and the player can only offer the choice if
// all of them are read, in order, with their languages intact. Merging is
// what loses those tags, so the downloader labels them afterwards; this is the
// other end of that, and the reason it is worth a test of its own is that a
// probe reading only the first track looks exactly like a file with one.
func TestEveryDubIsRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a talk.webm")
	// Alphabetical, which is the order a downloader naming languages produces
	// and therefore the order these arrive in.
	want := []string{"ara", "eng", "spa", "por"}
	buildDubbed(t, path, want...)

	p := ProbeMedia(t.Context(), Item{Path: path, Kind: KindVideo})
	if len(p.Tracks) != len(want) {
		t.Fatalf("read %d soundtracks, want %d: %+v", len(p.Tracks), len(want), p.Tracks)
	}
	for i, lang := range want {
		got := p.Tracks[i]
		if got.Lang != lang {
			t.Errorf("soundtrack %d is %q, want %q", i, got.Lang, lang)
		}
		if got.Index != i {
			t.Errorf("soundtrack %d is numbered %d; the index is what -map is given", i, got.Index)
		}
	}
	// The first is the one a browser plays unaided, and a set picks for
	// itself — so which it is decides whether choosing costs a conversion.
	if !p.Tracks[0].Default {
		t.Error("no soundtrack is marked default; the player has nothing to fall back to")
	}
}
