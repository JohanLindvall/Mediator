package server

import (
	"slices"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// A file can declare a pixel aspect ffmpeg refuses, and then nothing can be
// made from it: no still, no conversion, no segmented session. It is read
// off ffmpeg's own complaint, which names the parameter in every wording.
func TestAspectRefusedIsReadOffTheComplaint(t *testing.T) {
	said := "[graph 0 input from stream 0:0] Value -11.666667 for parameter 'pixel_aspect' out of range [0 - 1.797e+308]"
	if !aspectRefused(said) {
		t.Error("the complaint about the declared aspect was not recognised")
	}
	if !aspectRefused("Error setting option pixel_aspect to value -35/3.") {
		t.Error("the other wording of the same complaint was not recognised")
	}
	// Everything else a conversion can die of is left alone: a file that is
	// simply unreadable must not be copied through a repair for ever.
	for _, other := range []string{
		"", "No such file or directory", "Invalid data found when processing input",
		"Error opening output file", "moov atom not found",
	} {
		if aspectRefused(other) {
			t.Errorf("%q was taken for a refused aspect", other)
		}
	}
}

// Only the two codecs that have a filter for it are repaired, and the copy
// is a copy: no decoding, no re-encoding, into the one container that
// actually carries the correction.
func TestRepairCopiesRatherThanConverts(t *testing.T) {
	it := library.Item{Path: "/library/film.mp4", VCodec: "h264", Duration: 600_000}
	args := repairArgs(it, 60, repairSeconds, "/tmp/out.mkv")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-ss 60.000", "-i /library/film.mp4", "-c copy",
		"-bsf:v h264_metadata=sample_aspect_ratio=1/1", "-f matroska",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the copy is missing %q: %s", want, joined)
		}
	}
	if slices.Contains(args, "-vf") {
		t.Error("the repair filters the picture, which is the very thing that cannot be done")
	}
	// A conversion reads the whole film, where a still wants a moment of it.
	if slices.Contains(repairArgs(it, 60, 0, "pipe:1"), "-t") {
		t.Error("a conversion's copy was bounded to a few seconds")
	}
	if got := metadataFilter("hevc"); got != "hevc_metadata" {
		t.Errorf("hevc = %q", got)
	}
	for _, c := range []string{"", "vp9", "mpeg2video", "av1", "wmv2"} {
		if got := metadataFilter(c); got != "" {
			t.Errorf("%s was offered %q, which does not exist", c, got)
		}
	}
}

// The verdict is per file and per run, so one discovery spares the others:
// a tile made through the repair tells the converters what they would
// otherwise each find out by failing.
func TestBadAspectIsRememberedPerFile(t *testing.T) {
	var b badAspect
	it := library.Item{ID: "abc", ModTime: 5, Size: 9}
	other := library.Item{ID: "def", ModTime: 5, Size: 9}
	if b.has(it) {
		t.Error("a file nothing has judged was already marked")
	}
	b.note(it)
	if !b.has(it) || b.has(other) {
		t.Error("the mark did not stay on the file it was made for")
	}
	// A file replaced on disk is a different file, and is judged again.
	changed := it
	changed.Size = 10
	if b.has(changed) {
		t.Error("a file that changed on disk kept the old verdict")
	}
}
