package server

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"

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
	// hardware says the graphics engine was asked to carry this one, so a
	// caller whose run failed knows whether to write the hardware off for
	// this file and try again on the processor (hwRefused).
	hardware bool
	args     []string
	stdin    io.ReadCloser // nil where the input is a path or a URL
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
func planConversion(ctx context.Context, ffmpeg string, it library.Item, t float64, copyVideo bool, audio string, repair bool, log *slog.Logger) (*conversion, error) {
	input, byPosition, err := convertInput(it, t)
	if err != nil {
		return nil, err
	}
	// A file whose declared pixel aspect ffmpeg refuses is read through the
	// copy that rewrites it (aspect.go) rather than directly: the graph is
	// configured from what the stream says, so without this there is no
	// conversion to be had at all. The copy does the seek, so the conversion
	// asks for none — it reads a pipe, and a pipe seeks nowhere anyway.
	if repair && repairable(ffmpeg, it) {
		pipe, rerr := startRepair(ctx, ffmpeg, it, t)
		if rerr != nil {
			return nil, rerr
		}
		if input.pipe != nil {
			_ = input.pipe.Close()
		}
		input = convertSource{args: []string{"-i", "pipe:0"}, pipe: pipe}
		byPosition = true
	}
	c := &conversion{stdin: input.pipe}

	// Where this conversion runs. Decided before anything else, because it
	// changes the arguments on both sides of the input.
	onHardware := !copyVideo && hw.use(it, log)
	c.hardware = onHardware

	args := ffmpegBase()
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
		// The filters and the encoder run where the frames already are —
		// except the tone-map, which the engine cannot do and the processor
		// takes over for, after the engine has scaled the picture down.
		args = append(args, hw.encode(convertMaxWidth, hw.toneMap(toneCurve(ffmpeg, it.HDR)))...)
		args = append(args, convertColourArgs(it.HDR)...)
	default:
		// A wide-colour picture is brought back to ordinary colour on the
		// way through; see hdr.go for why a stream that keeps it is refused.
		// The scale comes first here too, for the same reason it does there.
		filters := convertScale
		if tm := toneCurve(ffmpeg, it.HDR); tm != "" {
			filters = convertScale + ",format=p010le," + tm
		}
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
			"-vf", videoFilter(filters), "-pix_fmt", "yuv420p")
		args = append(args, convertColourArgs(it.HDR)...)
	}
	c.args = append(args, audioEncodeArgs(false)...)
	c.args = append(c.args, "-avoid_negative_ts", "make_zero")
	return c, nil
}

// audioEncodeArgs is the soundtrack every conversion here makes: stereo AAC
// at 160 kbit/s, which every browser and every set decodes. fast is the
// coder the sound-fix file uses — it is waited on for the whole encode, and
// measured over a television episode the fast coder took it from 61 s to
// 37 s — where a live conversion keeps the default, having only to stay
// ahead of playback. One spelling, so the same film sounds the same
// whichever route it took.
func audioEncodeArgs(fast bool) []string {
	args := []string{"-c:a", "aac"}
	if fast {
		args = append(args, "-aac_coder", "fast")
	}
	return append(args, "-b:a", "160k", "-ac", "2")
}

// newestOf picks, among entries keyed "<id>|…", the one most recently
// wanted: the item's latest conversion, which a soundtrack change or a seek
// makes a second of. The readout is about that one; the first found in a
// map is whichever. Both converters keep such a map and both used to spell
// this loop.
func newestOf[T any](entries map[string]T, id string, used func(T) int64) (best T, found bool) {
	var at int64 = -1
	for key, cand := range entries {
		if strings.HasPrefix(key, id+"|") && used(cand) > at {
			best, at, found = cand, used(cand), true
		}
	}
	return best, found
}
