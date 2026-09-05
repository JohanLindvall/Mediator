package library

// Duration probing. The common containers are parsed natively — cheap,
// header-only reads, so startup enrichment does not spawn a process per
// file. Everything else (mkv, webm, avi, wmv, ...) falls back to ffprobe
// when it is on PATH; without it those files simply get no duration.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxSaneDurationMs = int64(1000 * 60 * 60 * 1000) // 1000 hours

// Probe describes what a media file contains. Codecs and picture geometry
// are only filled in when ffprobe runs (the native parsers do not look at
// stream headers); an empty ACodec on a video therefore means "unknown",
// not "silent", and a zero Width means "not probed", not "no picture".
type Probe struct {
	DurationMs int64
	VCodec     string
	ACodec     string
	// Geometry of the first video stream. It comes out of the same ffprobe
	// call as the codecs — no probe of its own is worth a process spawn for
	// it — so it is known exactly where and when the codecs are.
	Width  int
	Height int
	FPS    float64
	// Tracks is every soundtrack the file carries, in ffmpeg's own order.
	Tracks []AudioTrack
	// Subs is every text subtitle stream, ditto. Bitmap subtitles are left
	// out at the parse: they cannot become WebVTT (see textSubCodecs).
	Subs []SubTrack

	// Probed records that ffprobe actually ran and this is everything it
	// had to say, empty fields included. It is the difference between "not
	// looked at yet" and "looked at, found nothing", which the individual
	// fields cannot express: plenty of containers give up their codecs over
	// a pipe and no duration at all (MPEG-TS and PS, live-muxed MKV), so a
	// missing duration is not evidence that nobody has looked. See
	// EnsureCodecs, which would otherwise re-probe on every request.
	Probed bool
}

// probePrefix and probePrefixVideo bound the FALLBACK probe: the piped
// prefix, used only when there is no loopback address to read the member
// through (see loopback.go). Over a pipe the header has to arrive before the
// input ends, and how much header there is cannot be known in advance —
// measured on a 1080p release stored as one member of a volume set, 30
// streams and a dozen font attachments push the first cluster to 10.6 MB, so
// a 4 MiB ceiling produced an empty document and the item got no duration and
// no codecs at all. Video therefore gets a much larger ceiling than audio.
// Both are ceilings, not reads: ffprobe stops when the header is complete.
//
// Guessing a prefix is exactly what the loopback path removes. Measured over
// 15 archived members, an ffprobe of the loopback URL returned the same
// duration and the same codecs as a 32 MiB piped prefix on every one of them,
// reading 5.2 MiB in 0.06 s — and it gets there by ranging to the end of the
// container for the index, which no prefix can ever contain.
const (
	probePrefix      = 4 << 20
	probePrefixVideo = 32 << 20
)

// FFprobePath is where ffprobe was found, or "" when it is not installed.
// Exported so that the parts of the server which have their own question to
// ask of a file — how deeply its frames are reordered, for one — go through
// the same lookup rather than repeating it.
func FFprobePath() string {
	ffprobeOnce.Do(func() { ffprobePath, _ = exec.LookPath("ffprobe") })
	return ffprobePath
}

// probeItem runs ffprobe over an item's content: by path for a plain file,
// and for archived content — which has no path — over this server's own
// loopback stream URL, falling back to a piped prefix when there is no
// loopback address. ctx bounds the run; the request that asked for it may go
// away first.
func probeItem(ctx context.Context, it Item) ffprobeResult {
	if !it.Archived() {
		return ffprobe(ctx, it.Path, nil)
	}
	if u := LoopbackURL(it); u != "" {
		return ffprobe(ctx, u, nil)
	}
	f, err := OpenItem(it)
	if err != nil {
		return ffprobeResult{}
	}
	defer f.Close()
	n := int64(probePrefix)
	if it.Kind == KindVideo {
		n = probePrefixVideo
	}
	return ffprobe(ctx, "pipe:0", io.LimitReader(f, n))
}

// ProbeMedia returns duration and, for videos, the codecs. Codec names let
// the player decide whether the browser can play the file directly or needs
// the server to convert it.
//
// A video whose duration the native parser already read is NOT probed for
// codecs here: that would be one process spawn per file across the whole
// library, purely for a value that only matters when the file is opened.
// EnsureCodecs fills them in at that moment instead.
//
// ctx belongs to whoever asked. Enrichment routes through here from
// EnrichNow and EnrichSoon as well as from the background sweep, so a
// hardcoded background context would let one probe outlive the deadline the
// request set for it (measured: a 200 ms deadline returned after 5.01 s).
func ProbeMedia(ctx context.Context, it Item) Probe {
	p := Probe{DurationMs: nativeDuration(it)}
	if it.Kind != KindVideo {
		if p.DurationMs == 0 {
			switch {
			case !it.Archived():
				p.DurationMs = sane(ffprobe(ctx, it.Path, nil).durationMs)
			case LoopbackURL(it) != "":
				// A member in a container nothing here parses natively: the
				// loopback URL is a seekable view of it, and the probe reads
				// its tail the way it does a film's.
				p.DurationMs = sane(probeItem(ctx, it).durationMs)
			}
		}
		return p
	}
	if p.DurationMs > 0 {
		return p // codecs on demand, see EnsureCodecs
	}

	// ffprobe is needed for the duration anyway; codecs come for free in
	// the same call.
	out := probeItem(ctx, it)
	p.VCodec, p.ACodec = out.vcodec, out.acodec
	p.Tracks = out.tracks
	p.Subs = out.subs
	p.Width, p.Height, p.FPS = out.width, out.height, out.fps
	p.DurationMs = sane(out.durationMs)
	// Only an answer counts as having looked. A run that never happened —
	// no ffprobe on PATH, the member unreadable, the caller's deadline —
	// leaves the item exactly as unexamined as it was.
	p.Probed = out.answered
	return p
}

// ProbeDuration returns the playing time of a media item in milliseconds,
// or 0 when it cannot be determined.
func ProbeDuration(ctx context.Context, it Item) int64 {
	return ProbeMedia(ctx, it).DurationMs
}

// nativeDuration parses the duration out of the container in pure Go, which
// works for archived items too — all the parsers need is random access.
func nativeDuration(it Item) int64 {
	// Where the container itself said, that is the answer — a DVD does, in
	// its own information file, and it is the only one that knows: an MPEG-2
	// program stream carries no duration and ffprobe's estimate from the
	// timestamps was out by a factor of two on a measured disc.
	if it.stored != nil && it.stored.durationMs > 0 {
		return it.stored.durationMs
	}
	var parse func(io.ReaderAt, int64) int64
	switch strings.ToLower(filepath.Ext(it.Name)) {
	case ".mp4", ".m4a", ".m4b", ".m4v", ".mov", ".3gp":
		parse = mp4Duration
	case ".mp3":
		parse = mp3Duration
	case ".flac":
		parse = flacDuration
	case ".ogg", ".oga", ".opus":
		parse = oggDuration
	case ".wav":
		parse = wavDuration
	}
	if parse == nil {
		return 0
	}
	f, err := OpenItem(it)
	if err != nil {
		return 0
	}
	defer f.Close()
	return sane(parse(f, it.Size))
}

func sane(ms int64) int64 {
	if ms < 0 || ms > maxSaneDurationMs {
		return 0
	}
	return ms
}

// --- ISO base media (mp4, m4a, m4v, mov, 3gp) --------------------------------

// mp4Duration walks the box tree to moov/mvhd (which may sit at the end of
// the file) and reads timescale + duration from it. The walk is the one the
// sample-entry reading uses (mp4box.go); this file used to carry a second
// copy of it.
func mp4Duration(f io.ReaderAt, size int64) int64 {
	moovS, moovE, ok := child(f, 0, size, "moov")
	if !ok {
		return 0
	}
	mvhdS, _, ok := child(f, moovS, moovE, "mvhd")
	if !ok {
		return 0
	}
	return mp4Mvhd(f, mvhdS)
}

func mp4Mvhd(f io.ReaderAt, pos int64) int64 {
	var b [32]byte
	if _, err := f.ReadAt(b[:], pos); err != nil {
		return 0
	}
	var scale, dur uint64
	if b[0] == 1 { // version 1: 64-bit creation/modification times
		scale = uint64(binary.BigEndian.Uint32(b[20:24]))
		dur = binary.BigEndian.Uint64(b[24:32])
	} else {
		scale = uint64(binary.BigEndian.Uint32(b[12:16]))
		dur = uint64(binary.BigEndian.Uint32(b[16:20]))
		if dur == 0xFFFFFFFF { // "unknown"
			return 0
		}
	}
	if scale == 0 || dur > math.MaxUint64/1000 {
		return 0 // a corrupt 64-bit duration would wrap into a plausible number
	}
	return int64(dur * 1000 / scale)
}

// --- MP3 ----------------------------------------------------------------------

var (
	mp3RatesMPEG1   = [3]int64{44100, 48000, 32000}
	mp3BitratesV1L3 = [16]int64{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	mp3BitratesV2L3 = [16]int64{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
)

// mp3Duration finds the first MPEG frame (skipping ID3v2) and derives the
// duration from a Xing/Info/VBRI header when present, or the frame bitrate
// (CBR estimate) otherwise.
func mp3Duration(f io.ReaderAt, size int64) int64 {
	start := int64(0)
	var id3 [10]byte
	if _, err := f.ReadAt(id3[:], 0); err == nil && bytes.Equal(id3[:3], []byte("ID3")) {
		tagSize := int64(id3[6]&0x7f)<<21 | int64(id3[7]&0x7f)<<14 |
			int64(id3[8]&0x7f)<<7 | int64(id3[9]&0x7f)
		start = 10 + tagSize
		if id3[5]&0x10 != 0 {
			start += 10 // footer
		}
	}
	end := size
	var tag [3]byte
	if size >= 128 {
		if _, err := f.ReadAt(tag[:], size-128); err == nil && string(tag[:]) == "TAG" {
			end = size - 128 // trailing ID3v1
		}
	}
	if end-start < 4 {
		return 0
	}
	buf := make([]byte, min(64<<10, end-start))
	n, _ := f.ReadAt(buf, start)
	buf = buf[:n]
	for i := 0; i+4 <= len(buf); i++ {
		if buf[i] != 0xFF || buf[i+1]&0xE0 != 0xE0 {
			continue
		}
		if ms := mp3FromFrame(buf[i:], end-start-int64(i)); ms > 0 {
			return ms
		}
	}
	return 0
}

// mp3FromFrame interprets b as the start of a Layer III frame; audioBytes is
// the remaining stream length from this frame on.
func mp3FromFrame(b []byte, audioBytes int64) int64 {
	if len(b) < 4 {
		return 0
	}
	ver := b[1] >> 3 & 3   // 0: MPEG2.5, 2: MPEG2, 3: MPEG1
	layer := b[1] >> 1 & 3 // 1: Layer III
	brIdx := b[2] >> 4
	srIdx := b[2] >> 2 & 3
	if ver == 1 || layer != 1 || brIdx == 0 || brIdx == 15 || srIdx == 3 {
		return 0
	}
	mpeg1 := ver == 3
	rate := mp3RatesMPEG1[srIdx]
	if ver == 2 {
		rate /= 2
	}
	if ver == 0 {
		rate /= 4
	}
	samplesPerFrame := int64(576)
	if mpeg1 {
		samplesPerFrame = 1152
	}
	mono := b[3]>>6 == 3

	// Xing/Info sits after the side-info block, VBRI at a fixed 32 bytes.
	xing := 4 + 32
	if mpeg1 && mono {
		xing = 4 + 17
	} else if !mpeg1 {
		if mono {
			xing = 4 + 9
		} else {
			xing = 4 + 17
		}
	}
	if xing+12 <= len(b) {
		if id := string(b[xing : xing+4]); id == "Xing" || id == "Info" {
			if binary.BigEndian.Uint32(b[xing+4:xing+8])&1 != 0 {
				frames := int64(binary.BigEndian.Uint32(b[xing+8 : xing+12]))
				if frames > 0 {
					return frames * samplesPerFrame * 1000 / rate
				}
			}
		}
	}
	if 54 <= len(b) && string(b[36:40]) == "VBRI" {
		frames := int64(binary.BigEndian.Uint32(b[50:54]))
		if frames > 0 {
			return frames * samplesPerFrame * 1000 / rate
		}
	}
	var kbps int64
	if mpeg1 {
		kbps = mp3BitratesV1L3[brIdx]
	} else {
		kbps = mp3BitratesV2L3[brIdx]
	}
	if kbps == 0 {
		return 0
	}
	return audioBytes * 8 / kbps // bytes*8 bits / (kbps*1000) s = bytes*8/kbps ms
}

// --- FLAC -----------------------------------------------------------------------

func flacDuration(f io.ReaderAt, size int64) int64 {
	var magic [4]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil || string(magic[:]) != "fLaC" {
		return 0
	}
	pos := int64(4)
	for pos+4 < size {
		var bh [4]byte
		if _, err := f.ReadAt(bh[:], pos); err != nil {
			return 0
		}
		blockLen := int64(bh[1])<<16 | int64(bh[2])<<8 | int64(bh[3])
		if bh[0]&0x7f == 0 { // STREAMINFO
			if blockLen < 34 {
				return 0
			}
			var si [18]byte
			if _, err := f.ReadAt(si[:], pos+4); err != nil {
				return 0
			}
			rate := int64(si[10])<<12 | int64(si[11])<<4 | int64(si[12])>>4
			samples := int64(si[13]&0x0f)<<32 | int64(si[14])<<24 |
				int64(si[15])<<16 | int64(si[16])<<8 | int64(si[17])
			if rate == 0 {
				return 0
			}
			return samples * 1000 / rate
		}
		if bh[0]&0x80 != 0 { // last metadata block
			return 0
		}
		pos += 4 + blockLen
	}
	return 0
}

// --- Ogg (vorbis, opus) -----------------------------------------------------------

// oggDuration reads the codec sample rate from the first page and the total
// sample count from the granule position of the last page.
func oggDuration(f io.ReaderAt, size int64) int64 {
	head := make([]byte, 512)
	n, _ := f.ReadAt(head, 0)
	head = head[:n]
	if len(head) < 28 || string(head[:4]) != "OggS" {
		return 0
	}
	body := 27 + int(head[26])
	if body+16 > len(head) {
		return 0
	}
	var rate, preSkip int64
	switch {
	case bytes.HasPrefix(head[body:], []byte("OpusHead")):
		rate = 48000 // opus granules are always 48 kHz
		preSkip = int64(binary.LittleEndian.Uint16(head[body+10 : body+12]))
	case bytes.HasPrefix(head[body:], []byte("\x01vorbis")):
		rate = int64(binary.LittleEndian.Uint32(head[body+12 : body+16]))
	default:
		return 0
	}
	if rate <= 0 {
		return 0
	}
	tail := make([]byte, min(64<<10, size))
	tn, _ := f.ReadAt(tail, size-int64(len(tail)))
	tail = tail[:tn]
	granule := int64(-1)
	for i := 0; i+14 <= len(tail); i++ {
		if tail[i] == 'O' && tail[i+1] == 'g' && tail[i+2] == 'g' &&
			tail[i+3] == 'S' && tail[i+4] == 0 {
			if g := int64(binary.LittleEndian.Uint64(tail[i+6 : i+14])); g > granule {
				granule = g
			}
		}
	}
	if granule <= preSkip {
		return 0
	}
	return (granule - preSkip) * 1000 / rate
}

// --- WAV ---------------------------------------------------------------------------

func wavDuration(f io.ReaderAt, size int64) int64 {
	var hdr [12]byte
	if _, err := f.ReadAt(hdr[:], 0); err != nil ||
		string(hdr[:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return 0
	}
	pos := int64(12)
	var byteRate, dataLen int64
	for pos+8 <= size {
		var ch [8]byte
		if _, err := f.ReadAt(ch[:], pos); err != nil {
			return 0
		}
		chunkLen := int64(binary.LittleEndian.Uint32(ch[4:8]))
		switch string(ch[:4]) {
		case "fmt ":
			var fm [12]byte
			if _, err := f.ReadAt(fm[:], pos+8); err != nil {
				return 0
			}
			byteRate = int64(binary.LittleEndian.Uint32(fm[8:12]))
		case "data":
			dataLen = chunkLen
		}
		if byteRate > 0 && dataLen > 0 {
			return dataLen * 1000 / byteRate
		}
		pos += 8 + chunkLen + chunkLen&1
	}
	return 0
}

// --- ffprobe -------------------------------------------------------------------------

var (
	ffprobeOnce sync.Once
	ffprobePath string
)

// ffprobeResult is what one ffprobe run tells us about a file.
type ffprobeResult struct {
	vcodec     string
	acodec     string
	width      int
	height     int
	fps        float64
	durationMs int64
	tracks     []AudioTrack
	subs       []SubTrack
	// answered records that ffprobe ran, exited and printed a document we
	// could parse. It is not "we found something": a container may answer
	// with codecs and no duration, or with nothing at all, and that is still
	// an answer. It is the difference between that and never having asked —
	// no ffprobe on PATH, an unreadable member, a deadline — which must not
	// be written down as a verdict (see Probe.Probed).
	answered bool
}

// ffprobe timeouts, which are a ceiling on top of ctx and never an extension
// of it. Reading a header by path or over the loopback stream is nearly
// instant (measured: 0.06 s and 5.2 MiB for an archived 2160p member). The
// piped fallback first has to pull a multi-megabyte prefix out of the volume
// set, across as many volumes as the header spans, off a disk that may be
// busy with playback — hence the longer ceiling. A request-scoped caller
// passes its own, shorter deadline and gets the process killed with it.
const (
	ffprobeTimeout     = 30 * time.Second
	ffprobePipeTimeout = 60 * time.Second
)

// ffprobe inspects an input: a file path, this server's own loopback stream
// URL (for archived content, which has no path of its own), or a stream fed
// to standard input when stdin is non-nil (the fallback for the same, when
// there is no loopback address). Missing ffprobe, or any failure, yields a
// zero result whose answered field stays false.
func ffprobe(ctx context.Context, path string, stdin io.Reader) ffprobeResult {
	probe := FFprobePath()
	if probe == "" {
		return ffprobeResult{}
	}
	timeout := ffprobeTimeout
	if stdin != nil {
		timeout = ffprobePipeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"-v", "error"}
	if strings.HasPrefix(path, "http://") {
		// Reading ourselves: say so, or handleStream would count this as
		// playback and the background-work gate would throttle against it.
		args = append(args, "-headers", LoopbackHeaderArg())
	}
	// Geometry rides along with the codecs: the fields cost nothing extra
	// here, and a second invocation for them would be a process per video.
	args = append(args,
		"-show_entries",
		"stream=codec_type,codec_name,width,height,avg_frame_rate,channels:"+
			"stream_tags=language,title:stream_disposition=default,comment:format=duration",
		"-of", "json", path)
	cmd := exec.CommandContext(ctx, probe, args...)
	cmd.Stdin = stdin
	// A reader that is not an *os.File is copied to the child on a goroutine,
	// and Wait blocks on that goroutine — so a read wedged on a slow volume
	// would hold this enrichment worker long after the process was killed.
	// WaitDelay gives up on the copy shortly after the process is gone.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		// A non-zero exit with a document printed is ffprobe having read
		// the streams and then failed on something after them — a file cut
		// short — and the document is worth having if it parses. Anything
		// else that went wrong (no process, a pipe that broke) is no answer.
		var exit *exec.ExitError
		if !errors.As(err, &exit) || len(out) == 0 {
			return ffprobeResult{}
		}
	}
	if ctx.Err() != nil {
		return ffprobeResult{} // killed, not answered
	}

	var parsed struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			FrameRate string `json:"avg_frame_rate"`
			Channels  int    `json:"channels"`
			Tags      struct {
				Language string `json:"language"`
				Title    string `json:"title"`
			} `json:"tags"`
			Disposition struct {
				Default int `json:"default"`
				Comment int `json:"comment"`
			} `json:"disposition"`
			// Subtitle streams reuse CodecName and Tags; nothing more.
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if json.Unmarshal(out, &parsed) != nil {
		return ffprobeResult{}
	}
	res := ffprobeResult{answered: true}
	subStreams := 0
	for _, st := range parsed.Streams {
		switch st.CodecType {
		case "video":
			if res.vcodec == "" {
				res.vcodec = st.CodecName
				res.width, res.height = saneDimension(st.Width), saneDimension(st.Height)
				res.fps = parseRate(st.FrameRate)
			}
		case "subtitle":
			if textSubCodecs[st.CodecName] {
				res.subs = append(res.subs, SubTrack{
					Stream: subStreams,
					Codec:  st.CodecName,
					Lang:   cleanTag(st.Tags.Language),
					Title:  cleanTag(st.Tags.Title),
				})
			}
			// The ordinal counts every subtitle stream, text or not: it is
			// ffmpeg's s:<n>, and ffmpeg counts them all.
			subStreams++
		case "audio":
			if res.acodec == "" {
				res.acodec = st.CodecName
			}
			// Every soundtrack, in the order ffmpeg will number them: a film
			// with a commentary, or dubbed into three languages, is one file
			// with a choice in it, and the choice is only offerable if the
			// list survives the probe that was run anyway.
			res.tracks = append(res.tracks, AudioTrack{
				Index:    len(res.tracks),
				Codec:    st.CodecName,
				Lang:     cleanTag(st.Tags.Language),
				Title:    cleanTag(st.Tags.Title),
				Channels: st.Channels,
				Default:  st.Disposition.Default == 1,
				Comment:  st.Disposition.Comment == 1,
			})
		}
	}
	if sec, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil && sec > 0 {
		res.durationMs = int64(sec * 1000)
	}
	return res
}

// maxSaneDimension rejects nonsense geometry from a damaged header; nothing
// the player has to know is bigger than this.
const maxSaneDimension = 100000

func saneDimension(v int) int {
	if v <= 0 || v > maxSaneDimension {
		return 0
	}
	return v
}

// parseRate turns ffprobe's "num/den" frame rate into frames per second, 0
// when it says nothing (audio streams report "0/0"). Two decimals: the value
// is shown, not computed with, and 30000/1001 reads better as 29.97.
func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		return 0
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d <= 0 || n <= 0 {
		return 0
	}
	fps := math.Round(n/d*100) / 100
	if fps > 1000 {
		return 0
	}
	return fps
}
