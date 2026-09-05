package server

import (
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// Both converters run this one plan and add only their delivery, so the seek,
// the soundtrack and the codec decision are pinned here once for the pair.
func TestPlanConversion(t *testing.T) {
	it := library.Item{ID: "f", Name: "film.mkv", Path: "/m/film.mkv", Kind: library.KindVideo, VCodec: "h264"}
	log := testLogger()

	full, err := planConversion("ffmpeg", it, 0, false, "", log)
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
	sound, err := planConversion("ffmpeg", it, 61.5, true, "2", log)
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
