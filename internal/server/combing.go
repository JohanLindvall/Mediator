// SPDX-License-Identifier: MIT

package server

// Pictures that are interlaced and say they are not.
//
// deinterlace.go handles the honest case: a disc's MPEG-2 flags every frame
// interlaced, and the deinterlacer works on exactly the flagged frames. But
// fields can be combed into frames that are flagged progressive — a capture
// of interlaced video re-encoded by something that never asked — and then
// nothing downstream knows: a browser plays the file as it is and shows the
// teeth on anything that moves, and a conversion's deinterlacer believes the
// flag and passes every frame through. Measured on such a file, 480 lines at
// 29.97 fps flagged progressive: ffmpeg's `idet` read 138 of 138 frames as
// bottom field first.
//
// So the picture is looked at rather than its flag. `idet` compares each
// frame's fields with each other and with the neighbouring frames' and says
// whether they belong to one moment or two, which is the question; a short
// stretch from a little way in answers it. Where it finds fields, the file is
// re-encoded rather than played or copied (mustReencode), and the conversion
// deinterlaces every frame in the order found (pictureFilter).
//
// A television cannot take the live conversion — it wants a length and
// ranges — so a cast hands it a deinterlaced file instead, made before the
// set is given the URL (castFieldsKind, remuxFields).

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

const (
	// combProbeFrames is how many frames are compared. Measured on a library
	// directory: the combed file read 138 of 138 interlaced; of thirty-three
	// progressive ones, none more than 21. A few seconds of picture
	// separates them by that margin, and costs a third of a second at 480
	// lines and under a second at 1080.
	combProbeFrames = 120
	// combProbeTimeout bounds the look, which runs on its own clock rather
	// than the asker's (Server.interlacing).
	combProbeTimeout = 20 * time.Second
	// combMaxHeight and combMaxFPS are the pictures worth asking about.
	// Interlacing is a standard-definition and 1080i habit, and its fields
	// are coded as frames at 25 or 29.97 a second: a taller picture or a
	// faster rate is progressive by construction, and is not decoded for
	// the question. A shape not read yet (zero) is asked.
	combMaxHeight = 1088
	combMaxFPS    = 31
	// combSampleCap is as far in as the look starts: a quarter of the way,
	// past titles and fades, but never further than this, so a long film's
	// look is not a long seek.
	combSampleCap = 300 * time.Second
)

// combCandidate says whether a picture could be interlaced at all.
func combCandidate(it library.Item) bool {
	return it.Kind == library.KindVideo && it.VCodec != "" &&
		it.Height <= combMaxHeight && it.FPS <= combMaxFPS
}

// interlacing is the combing verdict for a film: "tff" or "bff" where its
// frames are fields combed together, whatever it flags, and empty where they
// are not or nothing could be learnt.
//
// The look runs on its own clock and outlives whoever asked first: the item
// fetch asks with a budget of a second and a half shared with the probe and
// the frame-order look, which a 1080-line film's decode can overrun, and a
// look cut off with it would be repeated — and cut off again — on every
// open. Finished in the background, its verdict reaches the listing
// (stampReencode) for the next open. The asker waits only as long as its own
// context allows, and asks once: a look that ended without a verdict is not
// started again in the same ask.
func (s *Server) interlacing(ctx context.Context, it library.Item) string {
	if !combCandidate(it) {
		return ""
	}
	key := itemKey(it)
	looked := false
	for {
		v, known, wait := s.combing.claim(key)
		switch {
		case known:
			return v
		case wait == nil && looked:
			// The claim just made is released at once: this ask has had
			// its look, and it came back with nothing.
			s.combing.settle(key, "", false)
			return ""
		case wait == nil:
			looked = true
			go s.lookCombing(key, it)
			continue
		}
		select {
		case <-wait:
			looked = true
		case <-ctx.Done():
			return ""
		}
	}
}

// lookCombing runs the look and settles it from a defer, so that whoever
// waits on it is released however it ends.
func (s *Server) lookCombing(key string, it library.Item) {
	var (
		order    string
		answered bool
	)
	defer func() { s.combing.settle(key, order, answered) }()
	ctx, cancel := context.WithTimeout(context.Background(), combProbeTimeout)
	defer cancel()
	order, answered = combedPicture(ctx, it)
	if answered && order != "" {
		s.log.Info("picture is interlaced; deinterlacing it",
			"path", it.Rel, "fields", order)
	}
}

// idetTotals is idet's closing count over the frames it saw, judged on
// several frames at once rather than each alone (its steadier reading).
var idetTotals = regexp.MustCompile(`Multi frame detection: TFF:\s*(\d+)\s+BFF:\s*(\d+)\s+Progressive:\s*(\d+)`)

// combedPicture runs the look. answered says the picture was read and
// judged; without it the order means nothing, since no ffmpeg, an input
// seeked only by position or through a pipe, a timeout and a run that
// printed no count all leave the film as it was.
func combedPicture(ctx context.Context, it library.Item) (order string, answered bool) {
	ffmpeg := library.FFmpegPath()
	if ffmpeg == "" {
		return "", false
	}
	at := min(time.Duration(it.Duration)*time.Millisecond/4, combSampleCap)
	in, ok := timeSeekInput(it, at.Seconds())
	if !ok {
		return "", false
	}
	args := []string{"-nostdin", "-hide_banner", "-nostats", "-loglevel", "info"}
	if at > 0 {
		args = append(args, "-ss", strconv.FormatFloat(at.Seconds(), 'f', 3, 64))
	}
	args = append(args, in...)
	args = append(args, "-map", pictureMap, "-an", "-sn", "-dn",
		"-frames:v", strconv.Itoa(combProbeFrames), "-vf", "idet", "-f", "null", "-")
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	// As for the frame-order look: the budget bounds the wait for the pipe,
	// not only the child.
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil || ctx.Err() != nil {
		return "", false
	}
	// idet reports once per filter instance, and ffmpeg can build the graph
	// more than once; the counts add up.
	found := false
	var tff, bff, prog int
	for _, m := range idetTotals.FindAllSubmatch(errBuf.Bytes(), -1) {
		found = true
		t, _ := strconv.Atoi(string(m[1]))
		b, _ := strconv.Atoi(string(m[2]))
		p, _ := strconv.Atoi(string(m[3]))
		tff, bff, prog = tff+t, bff+b, prog+p
	}
	if !found {
		return "", false
	}
	return combVerdict(tff, bff, prog), true
}

// combVerdict is the decision, pure so it can be tested against the numbers
// real files produce without those files.
//
// Undetermined frames are left out: a still scene has two fields that agree
// whether they were taken together or not. Of the frames idet could decide,
// more than half must be fields, and enough of them must have been decided
// to mean anything. The margin is wide on both sides as measured: the
// combed file at 138 of 138, progressive files at 21 of 138 at the most —
// fine horizontal detail fools a field comparison now and then, and a
// threshold near that would re-encode films for nothing.
func combVerdict(tff, bff, progressive int) string {
	fields := tff + bff
	decided := fields + progressive
	if decided < 30 || fields*2 <= decided {
		return ""
	}
	if bff > tff {
		return "bff"
	}
	return "tff"
}
