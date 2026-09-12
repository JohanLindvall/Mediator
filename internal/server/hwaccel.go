package server

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// Converting on the graphics hardware rather than the processor.
//
// Software is fast enough for most of what needs converting, and hopelessly
// slow for the case that matters most: a phone's 4K video. Measured on a
// 24-second iPhone clip — 3840x2160, 60 fps, 10-bit HEVC at 54 Mbit/s — the
// software recipe produced 20 seconds of output in 49 s, which is 0.41 of
// real time. A conversion slower than playback can never catch up, so the
// player drains its buffer and stalls, over and over, for the whole film.
//
// The processor is not the problem and a faster preset does not help: at
// `ultrafast` it was still 0.61, and **decoding alone**, with no encoder in
// the chain at all, was 0.78. The cost is in decoding 4K60 10-bit HEVC, which
// no encoder setting can reduce.
//
// The same clip through the graphics hardware — decode, scale and encode
// without the frames ever leaving it — runs at 3.9. A DVD, MPEG-2 with a
// deinterlacer in the chain as well, runs at 10.5 where software manages
// about 3.
//
// This is for the **binary running on a real machine**. The container image
// carries no driver and is not given a device by default; it converts on the
// processor exactly as it always has, and everything here simply finds
// nothing.

// hwBackend is one way of converting on hardware: what to put before the
// input, what to put after it, and a way of finding out whether any of it
// works here.
//
// Every backend is written from its documentation and only one of them —
// VAAPI — has been measured on real hardware. That is safe because of how
// they are chosen: each is *proved* by running a real conversion through it
// before it is ever used, so a backend whose arguments are wrong fails its
// own test and is never picked. The cost of being wrong is that a machine
// converts on its processor, which is what it did before.
type hwBackend struct {
	name string
	// devices are the paths to try, one at a time. Empty means the backend
	// addresses its hardware some other way.
	devices func() []string
	// input goes before -i: which decoder, and where its frames live.
	input func(dev string) []string
	// encode is the filter chain and the encoder, for frames wherever this
	// backend leaves them.
	encode func(dev string, maxWidth int, tonemap string) []string
	// decodes are the pictures this hardware is asked to decode. Short on
	// purpose: the failure is silent and total — a DivX file through Intel's
	// video engine produced *no frames at all*, where software converts it
	// perfectly well — and those old codecs are the ones most likely to need
	// converting, so being wrong about one is worse than being slow about it.
	decodes map[string]bool
}

// videoEngines are tried in order. The first that proves itself is used.
var videoEngines = []hwBackend{
	{
		// Intel and AMD on Linux, through the kernel's render nodes.
		name:    "vaapi",
		devices: renderNodes,
		input: func(dev string) []string {
			return []string{
				"-hwaccel", "vaapi", "-hwaccel_device", dev,
				// The load-bearing half. Without it every frame is copied
				// back to system memory for the filters, and the copying
				// costs more than the hardware saved: the same clip measured
				// 5.5 times real time with the frames left where they were
				// and 0.37 with them brought back.
				"-hwaccel_output_format", "vaapi",
			}
		},
		encode: func(_ string, w int, tonemap string) []string {
			// The scale, and — for a wide-colour picture — a detour back to
			// the processor for the tone-map the engine cannot do (hdr.go).
			// The scale comes first, so what the processor is handed is a
			// 1080p frame rather than a 4K one.
			vf := "scale_vaapi=w='min(" + strconv.Itoa(w) + ",iw)':h=-2:format=nv12"
			if tonemap != "" {
				vf = "scale_vaapi=w='min(" + strconv.Itoa(w) + ",iw)':h=-2:format=p010," +
					"hwdownload,format=p010le," + tonemap + ",format=nv12,hwupload"
			}
			return []string{
				// No deinterlacer here, where the software chain has one.
				// Two reasons, and the second is why it was removed rather
				// than fixed: nothing that reaches the hardware can be
				// interlaced — interlacing is a standard-definition and
				// 1080i habit, and every one of those is far below the pixel
				// rate that sends work here — and `deinterlace_vaapi` fails
				// outright on this driver. Measured on a 1080x1920 60 fps
				// clip: "Error while filtering: Cannot allocate memory"
				// after 2.9 s and a truncated stream, where the same
				// conversion without it finishes in 2.7 s, which is four and
				// a half times real time. What the viewer saw was a spinner
				// that never resolved.
				"-vf", vf,
				"-c:v", "h264_vaapi",
				// A ceiling rather than a quality target. Everything that
				// reaches the hardware is demanding by definition — that is
				// what put it here — so what matters is that the stream stays
				// something a phone on a mobile connection can pull. Measured
				// on the 4K clip: 6.2 Mbit/s at this setting, against the
				// 11.5 software spent on the same content.
				"-rc_mode", "VBR", "-b:v", "6M", "-maxrate", "10M",
			}
		},
		decodes: map[string]bool{"h264": true, "hevc": true, "mpeg2video": true, "vp8": true, "vp9": true},
	},
	{
		// Intel again, through its own runtime rather than the kernel's
		// interface. Tried after VAAPI because VAAPI is the one measured
		// here; on a machine where only this works, this is what answers.
		name:    "qsv",
		devices: renderNodes,
		input: func(dev string) []string {
			return []string{"-hwaccel", "qsv", "-qsv_device", dev, "-hwaccel_output_format", "qsv"}
		},
		encode: func(_ string, w int, _ string) []string {
			// vpp_qsv does both jobs at once; deinterlace=2 is its adaptive
			// mode, which leaves progressive frames alone.
			return []string{
				"-vf", "vpp_qsv=w='min(" + strconv.Itoa(w) + ",iw)':h=-2:deinterlace=2:format=nv12",
				"-c:v", "h264_qsv", "-b:v", "6M", "-maxrate", "10M",
			}
		},
		decodes: map[string]bool{"h264": true, "hevc": true, "mpeg2video": true, "vp9": true},
	},
	{
		// NVIDIA, anywhere its driver is.
		name:    "cuda",
		devices: func() []string { return []string{""} },
		input: func(string) []string {
			return []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}
		},
		encode: func(_ string, w int, _ string) []string {
			return []string{
				"-vf", "yadif_cuda=deint=interlaced,scale_cuda=w='min(" + strconv.Itoa(w) + ",iw)':h=-2",
				"-c:v", "h264_nvenc", "-preset", "p4", "-b:v", "6M", "-maxrate", "10M",
			}
		},
		decodes: map[string]bool{"h264": true, "hevc": true, "mpeg2video": true, "vp8": true, "vp9": true, "av1": true},
	},
	{
		// Apple. The frames come back to system memory, so the filters stay
		// the software ones — the encoder is the part worth having, and the
		// decoder is hardware either way.
		name:    "videotoolbox",
		devices: func() []string { return []string{""} },
		input:   func(string) []string { return []string{"-hwaccel", "videotoolbox"} },
		encode: func(_ string, w int, _ string) []string {
			return []string{
				"-vf", videoFilter("scale=w='min(" + strconv.Itoa(w) + ",iw)':h=-2"),
				"-pix_fmt", "yuv420p",
				"-c:v", "h264_videotoolbox", "-b:v", "6M", "-maxrate", "10M",
			}
		},
		decodes: map[string]bool{"h264": true, "hevc": true, "mpeg2video": true, "vp9": true},
	},
}

// renderNodes are the kernel's per-graphics-card doors, in a fixed order so a
// machine with two cards always picks the same one. Only render nodes: the
// card node drives the display and wants privileges a media server has no
// business holding.
func renderNodes() []string {
	found, _ := filepath.Glob("/dev/dri/renderD*")
	slices.Sort(found)
	return found
}

// hwProbeBudget bounds one backend's self-test. It is a handful of tiny
// frames; hardware that cannot manage that in this long is not worth waiting
// for on every conversion.
const hwProbeBudget = 20 * time.Second

// hw is the machine's, not any one server's, so a single search answers for
// every path that converts.
var hw hwaccel

type hwaccel struct {
	once sync.Once
	// The answer, guarded: the search runs in a goroutine at startup and
	// /api/info may ask what it found before it has finished looking.
	mu     sync.Mutex
	engine *hwBackend
	device string
	// searched is set once the search has finished, found or not. A
	// conversion asked for before then goes to the processor rather than
	// waiting on the search: proving each engine is a real encode, up to
	// twenty seconds apiece, and the first film opened after a start was
	// blocking behind all of it.
	searched atomic.Bool
	// tonemap says the engine has been shown to carry frames out to the
	// processor for the colour work and back (hwProveToneMap).
	tonemap atomic.Bool
}

// chosen is what the search settled on, or nil while it is still looking or
// found nothing.
func (h *hwaccel) chosen() (*hwBackend, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.engine, h.device
}

// hwPixelRate is how much picture a second the processor is left to handle
// on its own: a little over a 1080p60 stream.
//
// Hardware is not simply better, and the measurements say so plainly. On a
// DVD, software converts at three times real time and spends **0.8 Mbit/s**;
// the graphics hardware manages ten times real time and spends 4.3 — five
// times the bytes, for speed nobody needed, down a connection somebody is
// watching over. Fixed-function encoders are less efficient than x264 by
// that sort of margin, and at a matched bitrate they are visibly worse
// (measured on one clip: SSIM 0.945 against 0.964).
//
// So the hardware is for the case software cannot do at all. The same
// measurements bound it: 4K60 — 497 million pixels a second — converted at
// 0.41 of real time, which is a film that stalls for ever, while a DVD's 10
// million converts at three times. This threshold puts 4K of any frame rate,
// and 1080p above sixty, on the hardware, and leaves everything else where
// the picture is better.
const hwPixelRate = 120_000_000

// use reports whether this conversion should go to the hardware: the picture
// has to be one the hardware decodes, something has to work here, and
// software has to be the wrong tool for it.
//
// Anything unknown answers no. An empty codec means nobody has probed the
// file, and a missing frame rate or size means the same — none of this is a
// decision to guess at, and guessing wrong costs either a stalling film or a
// stream five times larger than it needed to be.
func (h *hwaccel) use(it library.Item, log *slog.Logger) bool {
	if !hwWorthIt(it) || !h.searched.Load() {
		return false
	}
	if hwRefused.has(it) {
		// Said once per conversion rather than never: a film converting on
		// the processor when the machine has an engine is the kind of thing
		// somebody comes to the log to understand.
		log.Debug("converting on the processor: the hardware failed this file before", "path", it.Rel)
		return false
	}
	engine, _ := h.chosen()
	return engine != nil && engine.decodes[it.VCodec]
}

// toneMap is the tone-map chain to splice into a hardware conversion, or
// nothing where the engine has not been shown to carry frames out to the
// processor and back (see hwProveToneMap). Without it a wide-colour film
// converts with its colours left as they are and described as ordinary —
// wrong on screen, but a stream that plays, which is the better of the two
// failures and the same one a build without zscale accepts.
func (h *hwaccel) toneMap(curve string) string {
	if curve == "" || !h.tonemap.Load() {
		return ""
	}
	return curve
}

// hwWorthIt reports whether this picture is more than the processor should be
// asked to convert.
func hwWorthIt(it library.Item) bool {
	if it.Width <= 0 || it.Height <= 0 || it.FPS <= 0 {
		return false
	}
	return float64(it.Width*it.Height)*it.FPS > hwPixelRate
}

// input and encode are the chosen backend's arguments. Only meaningful after
// use has answered yes.
func (h *hwaccel) input() []string {
	engine, device := h.chosen()
	return engine.input(device)
}

func (h *hwaccel) encode(w int, tonemap string) []string {
	engine, device := h.chosen()
	return engine.encode(device, w, tonemap)
}

// find works out what this machine can do, once.
//
// Every candidate is proved by running a real conversion through it: frames
// generated, put wherever the backend wants them, filtered and encoded. Every
// cheaper question has a wrong answer available to it — the device node exists
// on machines with no driver, the driver loads on hardware that cannot encode,
// and ffmpeg lists encoders for hardware it has never seen.
func (h *hwaccel) find(ffmpeg string, log *slog.Logger) {
	h.once.Do(func() {
		defer h.searched.Store(true)
		if ffmpeg == "" {
			return
		}
		for i := range videoEngines {
			e := &videoEngines[i]
			for _, dev := range e.devices() {
				if dev != "" {
					if _, err := os.Stat(dev); err != nil {
						continue
					}
				}
				if err := hwProve(ffmpeg, e, dev); err != nil {
					log.Debug("this hardware will not convert", "engine", e.name, "device", dev, "err", err)
					continue
				}
				h.mu.Lock()
				h.engine, h.device = e, dev
				h.mu.Unlock()
				log.Info("converting on the graphics hardware", "engine", e.name, "device", dev)
				// And whether it can hand frames out to the processor for
				// the colour work and take them back, which the base proof
				// never exercises: a chain that cannot would fail every
				// wide-colour film on the first frame.
				if !haveFilter(ffmpeg, "zscale") {
					log.Info("wide-colour films convert with their colour left as it is: this ffmpeg has no zscale")
				} else if err := hwProveToneMap(ffmpeg, e, dev); err != nil {
					log.Warn("wide-colour films convert with their colour left as it is: the hardware cannot carry frames out and back",
						"engine", e.name, "device", dev, "err", err)
				} else {
					h.tonemap.Store(true)
				}
				return
			}
		}
		log.Info("converting on the processor: no graphics hardware would")
	})
}

// hwProve runs one real conversion through a backend.
//
// The generated frames start in system memory, which is not where a decoded
// frame would be, so they are put where the backend expects them first: the
// device is opened by name, the filter chain is asked for from there, and
// hwupload does the moving. A backend that leaves frames in system memory
// anyway (Apple's) uploads nothing and simply encodes.
func hwProve(ffmpeg string, e *hwBackend, dev string) error {
	args, upload := hwProbeInput(e, dev)
	args = append(args, "-vf", "format=nv12"+upload)
	// The encoder alone: the backend's own filter chain is for frames coming
	// out of a decoder, and this is proving the device and the encoder.
	encode := e.encode(dev, 320, "")
	for i, a := range encode {
		if a == "-vf" || (i > 0 && encode[i-1] == "-vf") {
			continue
		}
		args = append(args, a)
	}
	return hwProbeRun(ffmpeg, args)
}

// hwProveToneMap runs the wide-colour chain through a backend that has
// already proved itself: frames uploaded to the engine, scaled there, handed
// back to the processor for the tone-map, and taken up again for the
// encoder. That round trip is the part the base proof never touches, and
// it is where a driver refuses (a surface format it will not write, an
// upload it will not take after a download).
//
// What it cannot prove is the real run's device selection: the proof names
// the device for the filters, where a conversion leaves ffmpeg to take the
// decoder's — measured to work on the engine this was written against. A
// chain that failed there anyway fails the conversion, which hwRefused then
// keeps off the hardware for that film.
func hwProveToneMap(ffmpeg string, e *hwBackend, dev string) error {
	args, upload := hwProbeInput(e, dev)
	// The generated frames are ten-bit, as a wide-colour decode's are.
	args = append(args, "-vf", "format=p010le"+upload)
	encode := e.encode(dev, 320, tonemapSoftware)
	for i, a := range encode {
		if a == "-vf" && i+1 < len(encode) {
			// Onto the end of the upload rather than as a second -vf, which
			// would replace the first: one chain, uploaded first.
			args[len(args)-1] += "," + encode[i+1]
			continue
		}
		if i > 0 && encode[i-1] == "-vf" {
			continue
		}
		args = append(args, a)
	}
	return hwProbeRun(ffmpeg, args)
}

// hwProbeInput is the front of every proof: the device opened by name, the
// filters told to use it, and five generated frames. It answers the upload
// filter as well, since that differs by backend and the caller splices it
// onto its own chain.
func hwProbeInput(e *hwBackend, dev string) (args []string, upload string) {
	args = ffmpegBase()
	switch e.name {
	case "vaapi":
		args = append(args, "-init_hw_device", "vaapi=hw:"+dev, "-filter_hw_device", "hw")
		upload = ",hwupload"
	case "qsv":
		args = append(args, "-init_hw_device", "qsv=hw:"+dev, "-filter_hw_device", "hw")
		upload = ",hwupload=extra_hw_frames=8"
	case "cuda":
		args = append(args, "-init_hw_device", "cuda=hw", "-filter_hw_device", "hw")
		upload = ",hwupload_cuda"
	}
	return append(args, "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25", "-frames:v", "5"), upload
}

// hwProbeRun runs one proof to a null output under the probe's budget.
func hwProbeRun(ffmpeg string, args []string) error {
	args = append(args, "-f", "null", "-")
	ctx, cancel := context.WithTimeout(context.Background(), hwProbeBudget)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput()
	if err != nil {
		return &hwProbeError{err: err, out: strings.TrimSpace(string(out))}
	}
	return nil
}

type hwProbeError struct {
	err error
	out string
}

func (e *hwProbeError) Error() string {
	if e.out == "" {
		return e.err.Error()
	}
	return e.err.Error() + ": " + e.out
}

// hwRefused is what the hardware has failed on, for the run.
//
// The failure of a graphics engine is silent and total — no frames, or a
// stream that stops half way — and it is not always something a list of
// codecs can predict: a driver refuses a size, runs out of surfaces, or
// simply gives up. So a conversion that fails while the hardware was
// carrying it puts the file here, and the next attempt converts it on the
// processor, which is slower and always works. Per file and per run, like
// the other verdicts here; a restart tries the hardware again, at the cost
// of one failed attempt.
var hwRefused perFileVerdict
