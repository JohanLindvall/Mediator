package server

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// A demuxer whose seeks land on the keyframe after the time asked for, with
// keyframes every ten seconds at x2.56 — the shape of the Windows Media file
// this was found on.
func landsLate(seek float64) (float64, error) {
	for k := 2.56; ; k += 10 {
		if k >= seek {
			return k, nil
		}
	}
}

// And one that lands where it should: the keyframe at or before.
func landsEarly(seek float64) (float64, error) {
	best := 0.0
	for k := 2.56; k <= seek; k += 10 {
		best = k
	}
	return best, nil
}

func TestAGridSeekLandsAtOrBeforeThePoint(t *testing.T) {
	// Where the seek lands where it should, nothing changes.
	if got, err := gridSeek(328, landsEarly); err != nil || got != 328 {
		t.Errorf("ordinary file: seek %v, %v, want the point itself", got, err)
	}
	// Where it lands past it, earlier until it does not.
	got, err := gridSeek(328, landsLate)
	if err != nil {
		t.Fatal(err)
	}
	if landed, _ := landsLate(got); got >= 328 || landed > 328 {
		t.Errorf("late file: seek %v lands at %v, past the point", got, landed)
	}
	// Twice as far back each time, and never before the film's start.
	sparse := func(seek float64) (float64, error) {
		if seek > 0 {
			return 100, nil // one keyframe at the start and the next at 100
		}
		return 0, nil
	}
	if got, err := gridSeek(40, sparse); err != nil || got != 0 {
		t.Errorf("sparse keyframes: seek %v, %v, want the start", got, err)
	}
	// Nothing to go on is the old way, and says so.
	if got, err := gridSeek(328, func(float64) (float64, error) { return 0, errors.New("no packet") }); err == nil || got != 328 {
		t.Errorf("unreadable: seek %v, %v", got, err)
	}
}

// What framecrc prints, in the shape measured on the file: a time base, and
// a packet a line with its pts in that base.
func TestAFramecrcListingGivesTheFirstPacketsTime(t *testing.T) {
	out := []byte("#software: Lavf61.7.100\n#tb 0: 1/1000\n#media_type 0: video\n#codec_id 0: wmv2\n" +
		"#dimensions 0: 1280x720\n#sar 0: 1/1\n0,     332560,     332560,       40,    68761, 0x7a86db71\n")
	if got, ok := framecrcFirstPTS(out); !ok || got != 332.56 {
		t.Errorf("framecrcFirstPTS = %v, %v, want 332.56", got, ok)
	}
	if _, ok := framecrcFirstPTS([]byte("#tb 0: 1/1000\n")); ok {
		t.Error("a listing with no packet read as one")
	}
	if _, ok := framecrcFirstPTS([]byte("0, 10, 10, 40, 5, 0x0\n")); ok {
		t.Error("a packet with no time base read as a time")
	}
}

// The trim goes at the head of the picture's chain and in front of the
// soundtrack's encoder, and only where there is a soundtrack.
func TestARunIsTrimmedToThePoint(t *testing.T) {
	base := []string{"-ss", "318.000", "-copyts", "-i", "in", "-vf", "bwdif=mode=send_frame,scale=320:-2",
		"-c:a", "aac", "-b:a", "160k"}
	c := &conversion{args: slices.Clone(base)}
	c.trimTo(328, true)
	joined := strings.Join(c.args, " ")
	if !strings.Contains(joined, "-vf trim=start=328.000,bwdif") {
		t.Errorf("the picture is not trimmed first: %s", joined)
	}
	if !strings.Contains(joined, "-af atrim=start=328.000 -c:a aac") {
		t.Errorf("the soundtrack is not trimmed: %s", joined)
	}
	silent := &conversion{args: slices.Clone(base)}
	silent.trimTo(328, false)
	if slices.Contains(silent.args, "-af") {
		t.Error("a file with no soundtrack was given an audio filter")
	}
}
