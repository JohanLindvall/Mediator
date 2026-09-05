package server

import (
	"io"
	"log/slog"
	"strconv"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// One conversion, planned once for both converters.
//
// The pipe (/api/transcode) and the segmented converter (/api/hls) run the
// same ffmpeg over the same input with the same seek, the same hardware
// decision, the same picture copied or encoded and the same soundtrack; only
// what comes out the far end differs. Each used to spell all of that out for
// itself, and the two diverging once is how a fault got in — so the common
// part is built here, and each converter adds its own delivery.

// conversion is a planned run: the arguments up to the output, and the pipe
// feeding standard input where that is the only way to the bytes.
type conversion struct {
	args  []string
	stdin io.ReadCloser // nil where the input is a path or a URL
}

// close lets go of the pipe, where there was one.
func (c *conversion) close() {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
}

// planConversion works out everything the two converters share for a run
// from t seconds. copyVideo is the caller's decision, mustReencode already
// consulted; audio names the soundtrack as the URL did.
//
// -copyts keeps every stream's own timestamps through the seek, and
// make_zero then shifts them all by the same amount. Without the pair, a
// copied picture starting at the keyframe and a re-encoded soundtrack
// starting where the seek asked are each rebased to zero separately — which
// is to say the film goes out of sync by however far the keyframe was, and
// ten seconds is ordinary. The client asks for the keyframe itself, so
// normally there is nothing to shift; this is what keeps a seek honest when
// it could not measure one. A seek already done by byte position (a DVD
// title, see convertInput) gets no -ss: the stream simply starts where it
// was put.
func planConversion(ffmpeg string, it library.Item, t float64, copyVideo bool, audio string, log *slog.Logger) (*conversion, error) {
	input, byPosition, err := convertInput(it, t)
	if err != nil {
		return nil, err
	}
	c := &conversion{stdin: input.pipe}

	// Where this conversion runs. Decided before anything else, because it
	// changes the arguments on both sides of the input.
	onHardware := !copyVideo && hw.use(ffmpeg, it, log)

	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if onHardware {
		args = append(args, hw.input()...)
	}
	if t > 0 && !byPosition {
		args = append(args, "-ss", strconv.FormatFloat(t, 'f', 3, 64), "-copyts")
	}
	args = append(args, input.args...)
	// Which soundtrack, when the file carries more than one. Out of range is
	// nobody's choice, so the map is optional and ffmpeg simply produces a
	// picture — better than refusing to play the film.
	args = append(args, "-map", "0:v:0", "-map", audioMap(audio), "-sn", "-dn")
	switch {
	case copyVideo:
		args = append(args, "-c:v", "copy")
	case onHardware:
		// The filters and the encoder run where the frames already are.
		args = append(args, hw.encode(convertMaxWidth)...)
	default:
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
			"-vf", videoFilter(convertScale), "-pix_fmt", "yuv420p")
	}
	c.args = append(args,
		"-c:a", "aac", "-b:a", "160k", "-ac", "2",
		"-avoid_negative_ts", "make_zero",
	)
	return c, nil
}
