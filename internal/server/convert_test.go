package server

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// Both converters run this one plan and add only their delivery, so the seek,
// the soundtrack and the codec decision are pinned here once for the pair.
func TestPlanConversion(t *testing.T) {
	it := library.Item{ID: "f", Name: "film.mkv", Path: "/m/film.mkv", Kind: library.KindVideo, VCodec: "h264"}
	log := testLogger()

	full, err := planConversion(context.Background(), "ffmpeg", it, 0, false, "", quality{}, false, false, log)
	if err != nil {
		t.Fatal(err)
	}
	defer full.close()
	joined := strings.Join(full.args, " ")
	if full.stdin != nil {
		t.Error("a plain file was handed over on a pipe")
	}
	if strings.Contains(joined, "-ss ") {
		t.Errorf("a conversion from the start seeks: %s", joined)
	}
	for _, want := range []string{
		"-i /m/film.mkv", "-map 0:v:0 -map 0:a:0? -sn -dn",
		"-c:v libx264", "-vf " + videoFilter(convertScale),
		"-c:a aac -b:a 160k -ac 2 -avoid_negative_ts make_zero",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.HasSuffix(joined, "pipe:1") || strings.Contains(joined, "-f ") {
		t.Errorf("the plan chose a delivery, which is the caller's: %s", joined)
	}

	// The soundtrack conversion copies the picture, seeks to the keyframe
	// with the file's own clock kept, and takes the soundtrack it was asked
	// for.
	sound, err := planConversion(context.Background(), "ffmpeg", it, 61.5, true, "2", quality{}, false, false, log)
	if err != nil {
		t.Fatal(err)
	}
	defer sound.close()
	joined = strings.Join(sound.args, " ")
	for _, want := range []string{"-ss 61.500 -copyts", "-c:v copy", "-map 0:a:2?"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "libx264") {
		t.Errorf("a soundtrack conversion re-encodes the picture: %s", joined)
	}
	// The seek goes before the input, where it is an input seek.
	if strings.Index(joined, "-ss ") > strings.Index(joined, "-i ") {
		t.Errorf("the seek follows the input, which decodes everything before it: %s", joined)
	}
}

// The deinterlacer's mode has to be said: ffmpeg's default is send_field,
// which emits a frame per field and so doubles the frame rate of everything
// that passes through — a progressive file included, since it is passed
// through untouched and then emitted twice. Measured on a 25 fps broadcast
// before this was pinned, the conversion came out at 50 fps.
func TestDeinterlacerKeepsOneFramePerFrame(t *testing.T) {
	if !strings.Contains(deinterlacer, "mode=send_frame") {
		t.Errorf("the deinterlacer does not name its mode (%q), so ffmpeg's default doubles the frame rate", deinterlacer)
	}
	// And it still only touches what the container flags, which is what
	// makes it safe to apply to every conversion.
	if !strings.Contains(deinterlacer, "deint=interlaced") {
		t.Errorf("the deinterlacer would process progressive frames too: %q", deinterlacer)
	}
	// Before any scale, whatever else the picture needs.
	got := videoFilter("scale=w=1280:h=-2")
	if !strings.HasPrefix(got, deinterlacer+",") {
		t.Errorf("the deinterlacer is not first: %q", got)
	}
}

// A re-encode keeps the timing of what it was given. ffmpeg's default for a
// file output is a constant rate, and where the source declares none it
// takes the container's time base for one — which for an ASF written in
// milliseconds is a thousand frames a second, every real frame duplicated
// thirty times.
func TestAReencodeKeepsTheSourcesTiming(t *testing.T) {
	log := testLogger()
	it := library.Item{ID: "abc", Kind: library.KindVideo, Path: "/x/clip.wmv", VCodec: "wmv3"}
	full, err := planConversion(context.Background(), "ffmpeg", it, 0, false, "", quality{}, false, false, log)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(full.args, "-fps_mode") {
		t.Errorf("a re-encode does not say what to do about the frame rate: %v", full.args)
	}
	// A copy has no frame rate to decide: the frames are the file's own.
	copied, err := planConversion(context.Background(), "ffmpeg", it, 0, true, "", quality{}, false, false, log)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(copied.args, "-fps_mode") {
		t.Errorf("a copy was told how to pace frames it is not encoding: %v", copied.args)
	}
}
