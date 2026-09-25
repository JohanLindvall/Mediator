package server

import (
	"context"
	"io"
	"log/slog"
	"slices"
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

// quality is one rung of the bitrate ladder a viewer can choose from: a
// ceiling on the picture's rate, and the box the picture is scaled into so
// the bits go further. The zero value is no rung — the file's own rate, or
// the converter's ordinary output where a conversion was needed anyway.
//
// The ladder exists for the link, not the screen. Measured on the file that
// asked for it: a phone recording at 9.7 Mbit/s, watched over a link that
// delivered about eight, stalled throughout — and the same file converted
// came out at 3.3 Mbit/s at twice real time, the same picture size. Native
// playback sends the file's own rate whatever the link, and a converted 4K
// film needed *less* link than a native phone clip. Nothing here can measure
// the viewer's link, so the viewer is offered the choice.
type quality struct {
	kbps   int // the picture's ceiling, in kbit/s
	height int // the box it is scaled into: this tall, and 16:9 as wide
}

// qualityTiers is the ladder, top rung first. Three rungs, each half the
// one above: enough to reach a link of a few megabits from a film of ten,
// and few enough that the menu reads at a glance. A rung is named by its
// rate, since the rate is what it costs.
var qualityTiers = []quality{{6000, 1080}, {3000, 720}, {1500, 480}}

// parseQuality reads a rung off the query. Empty or "0" is no rung; anything
// else has to be a rung on the ladder exactly, since an arbitrary rate is a
// budget nobody set and a way to spend the encoder on nothing.
func parseQuality(q string) (quality, bool) {
	if q == "" || q == "0" {
		return quality{}, true
	}
	n, err := strconv.Atoi(q)
	if err != nil {
		return quality{}, false
	}
	for _, t := range qualityTiers {
		if t.kbps == n {
			return t, true
		}
	}
	return quality{}, false
}

// chosen says whether a rung was asked for.
func (q quality) chosen() bool { return q.kbps > 0 }

// width is the box's width for a 16:9 picture, even, which the encoders
// require. The box is what the scale fits the picture into either way up:
// a portrait clip is bounded by the height, a wide film by the width.
func (q quality) width() int { return (q.height*16/9 + 1) &^ 1 }

// rateCap is the encoder's ceiling for a rung: a rate to aim at and the same
// rate as the most it may ever spend, since a viewer who asked for three
// megabits has a link that carries about that and a burst above it is the
// stall they were trying to end.
func (q quality) rateCap() []string {
	k := strconv.Itoa(q.kbps) + "k"
	return []string{"-b:v", k, "-maxrate", k, "-bufsize", strconv.Itoa(q.kbps*2) + "k"}
}

// boxScale is the software scaler's rule for a rung: fit inside the box,
// keeping the picture's shape, at a size the encoder takes.
func (q quality) boxScale() string {
	return "scale=w='min(" + strconv.Itoa(q.width()) + ",iw)':h='min(" + strconv.Itoa(q.height) + ",ih)'" +
		":force_original_aspect_ratio=decrease:force_divisible_by=2"
}

// effectiveCopy is whether the picture may still be copied through once a
// rung is asked for: it may not. A copied picture is the file's own bits at
// the file's own rate, which is exactly what the viewer asked to have less
// of, so a rung is a re-encode however the file was going to be handled.
func effectiveCopy(copyVideo bool, q quality) bool {
	return copyVideo && !q.chosen()
}

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
// absolute keeps the film's own clock throughout — the input's timestamps
// carried through even from the start, no shifting to zero, and a
// re-encoded picture made to put a keyframe on every fourth second of that
// clock — for a segmented run that has to join the segments other runs
// made (hls.go). The pipe and a session without a table keep the older
// arrangement, where the stream's clock begins at the seek.
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
func planConversion(ctx context.Context, ffmpeg string, it library.Item, t float64, copyVideo bool, audio string, q quality, repair, absolute bool, log *slog.Logger) (*conversion, error) {
	copyVideo = effectiveCopy(copyVideo, q)
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
		args = append(args, "-ss", strconv.FormatFloat(t, 'f', 3, 64))
	}
	if (t > 0 && !byPosition) || absolute {
		args = append(args, "-copyts")
	}
	args = append(args, input.args...)
	// Which soundtrack, when the file carries more than one. Out of range is
	// nobody's choice, so the map is optional and ffmpeg simply produces a
	// picture — better than refusing to play the film.
	args = append(args, "-map", "0:v:0", "-map", audioMap(audio), "-sn", "-dn")
	if !copyVideo {
		// What comes out keeps the timing of what went in.
		//
		// ffmpeg's default for a file output is a *constant* rate, and where
		// the source does not declare one it takes the container's time base
		// for it — which for an ASF written in milliseconds is **1000 fps**.
		// Measured on such a file, a 276x246 webcam recording whose frames
		// are really about 32 a second: every frame duplicated thirty times,
		// 59,996 frames for a minute of film, the conversion crawling at
		// twice real time where it should manage hundreds, four times the
		// bytes it needed, and a browser asked to decode a thousand frames a
		// second. The viewer saw a spinner that never went away. The same
		// minute with the timing left alone: 1,811 frames, 25 times real
		// time, a third of the bytes.
		//
		// Nothing is lost where the source really is constant — there are no
		// duplicates to drop, and the output is what it always was. This
		// only ever removes frames ffmpeg invented.
		args = append(args, "-fps_mode", "vfr")
	}
	switch {
	case copyVideo:
		args = append(args, "-c:v", "copy")
	case onHardware:
		// The filters and the encoder run where the frames already are —
		// except the tone-map, which the engine cannot do and the processor
		// takes over for, after the engine has scaled the picture down.
		args = append(args, hw.encode(convertMaxWidth, q, hw.toneMap(toneCurve(ffmpeg, it.HDR)))...)
		if absolute {
			args = append(args, hwGridKeyframes()...)
		}
		args = append(args, convertColourArgs(it.HDR)...)
	default:
		// A wide-colour picture is brought back to ordinary colour on the
		// way through; see hdr.go for why a stream that keeps it is refused.
		// The scale comes first here too, for the same reason it does there.
		// A rung swaps the ordinary width cap for its own box, and puts a
		// ceiling on the rate that the quality target otherwise has none of.
		scale := convertScale
		if q.chosen() {
			scale = q.boxScale()
		}
		filters := scale
		if tm := toneCurve(ffmpeg, it.HDR); tm != "" {
			filters = scale + ",format=p010le," + tm
		}
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
			"-vf", videoFilter(filters), "-pix_fmt", "yuv420p")
		if absolute {
			// On the grid and nowhere else: a scene cut may not add a
			// keyframe of its own, or the muxer could take it for the
			// boundary a hair early.
			args = append(args, "-force_key_frames", gridKeyframeExpr, "-sc_threshold", "0", "-g", "1000")
		}
		if q.chosen() {
			args = append(args, q.rateCap()...)
		}
		args = append(args, convertColourArgs(it.HDR)...)
	}
	c.args = append(args, audioEncodeArgs(false)...)
	// The muxer shifts every stream by the same amount rather than rebasing
	// each to zero on its own — the belt for a seek whose keyframe could not
	// be measured. make_zero puts the first timestamp at zero, which is
	// exactly what an absolute clock must not have done to it (measured: a
	// run begun twenty minutes in came out starting at 0.083); with the
	// film's own clock kept there are no negative timestamps to shift, and
	// make_non_negative touches nothing.
	if absolute {
		c.args = append(c.args, "-avoid_negative_ts", "make_non_negative")
	} else {
		c.args = append(c.args, "-avoid_negative_ts", "make_zero")
	}
	return c, nil
}

// trimTo makes what is encoded begin at t on the film's clock, for a run
// whose input seek had to land earlier than that (gridSeek): the picture's
// chain and the soundtrack are each trimmed to t, which keeps their
// timestamps where a seek on the output side would start the clock again at
// nought — and a segmented run has to keep the film's clock to join the
// segments around it. sound says the file has a soundtrack to trim.
//
// It is the first frame through the trim that the forced keyframes count
// from, so a run trimmed to a grid point is on the grid again.
func (c *conversion) trimTo(t float64, sound bool) {
	at := strconv.FormatFloat(t, 'f', 3, 64)
	for i := 0; i+1 < len(c.args); i++ {
		if c.args[i] == "-vf" {
			c.args[i+1] = "trim=start=" + at + "," + c.args[i+1]
			break
		}
	}
	if !sound {
		return
	}
	for i, a := range c.args {
		if a == "-c:a" {
			c.args = slices.Insert(c.args, i, "-af", "atrim=start="+at)
			return
		}
	}
}

// gridKeyframeExpr makes the encoder put a keyframe on the first frame at
// or after every multiple of hlsSegmentSec on the output clock, and on the
// first frame of the run — which, the run seeking accurately to a grid
// point, is on the grid too. Verified on libx264 and h264_vaapi: one
// keyframe per segment, every segment the grid's length.
var gridKeyframeExpr = "expr:if(isnan(prev_forced_t),1,gte(t,(floor(prev_forced_t/" +
	strconv.Itoa(hlsSegmentSec) + ")+1)*" + strconv.Itoa(hlsSegmentSec) + "))"

// hwGridKeyframes is the same rule for the graphics engines, which take the
// forced frames through the same option; the long GOP keeps the engine
// from adding keyframes of its own between them, and NVENC has to be told
// that a forced frame is an IDR, or it writes one nothing can start on.
func hwGridKeyframes() []string {
	args := []string{"-force_key_frames", gridKeyframeExpr, "-g", "1000"}
	if engine, _ := hw.chosen(); engine != nil && engine.name == "cuda" {
		args = append(args, "-forced-idr", "1")
	}
	return args
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
