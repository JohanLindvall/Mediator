package server

// Black borders that are in the file rather than around it.
//
// A portrait clip padded out to a landscape frame, a 4:3 film in a 16:9
// container, a phone video an app re-encoded: the picture is a rectangle
// inside a rectangle, and the bars are pixels like any other. The player can
// only fit what the file says its size is, so it letterboxes that frame into
// the window and the viewer gets black on all four sides with the picture
// small in the middle. Nothing is wrong; the file really is mostly black.
//
// ffmpeg can find the picture (`cropdetect`), and the answer is a property
// of the file, so it is worth finding once and keeping. What it costs is a
// few short reads at points across the film — not the opening, which is
// where the black frames and the title cards are, and which alone would
// crop a whole scene away.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

const (
	// cropDetectAt is how many frames each sample decodes: frames are cheap,
	// the seek is the cost. Where the samples fall is cropSamplePoints below
	// — never the very start, since a fade in from black is a picture with
	// no picture in it, and one sample there would crop the film to nothing.
	cropDetectAt = 8
	// cropKeepFraction is how much of the frame must be given back before
	// this is worth doing at all. A pixel or two at an edge is the encoder
	// rounding, not a border.
	cropKeepFraction = 0.97
	// cropTimeout bounds one detection: four short seeks and a handful of
	// frames, on a disk that may be busy with playback.
	cropTimeout = 45 * time.Second
)

var cropSamplePoints = []float64{0.10, 0.35, 0.60, 0.80}

// cropLine is what cropdetect prints: crop=W:H:X:Y.
var cropLine = regexp.MustCompile(`crop=(\d+):(\d+):(\d+):(\d+)`)

// cropRuns deduplicates detections in flight: two viewers opening the same
// film asked for eight ffmpeg seeks beside its playback, where one set of
// four answers both. Keyed like the thumbnails, by the file as it is.
var cropRuns = struct {
	mu       sync.Mutex
	inflight map[string]chan struct{}
}{inflight: map[string]chan struct{}{}}

// handleCrop answers where the picture actually is inside this file's frame.
// An empty box means there is nothing to trim, which is the ordinary case
// and is worth saying quickly.
func (s *Server) handleCrop(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok || it.Kind != library.KindVideo {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cropTimeout)
	defer cancel()
	key := fmt.Sprintf("%s|%d|%d", it.ID, it.ModTime, it.Size)
	for {
		if box, found := s.storedCrop(it); found {
			writeJSON(w, box)
			return
		}
		cropRuns.mu.Lock()
		if ch, running := cropRuns.inflight[key]; running {
			cropRuns.mu.Unlock()
			// Somebody is looking already; their answer will be in the store.
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				writeJSON(w, CropResponse{})
				return
			}
		}
		ch := make(chan struct{})
		cropRuns.inflight[key] = ch
		cropRuns.mu.Unlock()

		box := s.detectCrop(ctx, it)
		if ctx.Err() == nil {
			// Only an answer is written down. An interrupted run says nothing
			// about the file and must not be remembered as "no borders".
			s.storeCrop(it, box)
		}
		cropRuns.mu.Lock()
		delete(cropRuns.inflight, key)
		close(ch)
		cropRuns.mu.Unlock()
		writeJSON(w, box)
		return
	}
}

// detectCrop looks at a few points across the film and takes the box that
// holds all of them — the union, not the average. A dark scene detects as
// smaller than it is, and cropping to that would cut the picture; taking the
// largest of what was found means a borderless moment simply cancels the
// crop, which is the safe way to be wrong.
func (s *Server) detectCrop(ctx context.Context, it library.Item) CropResponse {
	ffmpeg := s.thumbs.FFmpegPath()
	if ffmpeg == "" || it.Duration <= 0 {
		return CropResponse{}
	}
	input, headers := library.LoopbackURL(it), ""
	if input == "" {
		if it.Archived() {
			return CropResponse{} // no path to read it by, and no loopback
		}
		input = it.Path
	} else {
		headers = library.LoopbackHeaderArg()
	}

	seconds := float64(it.Duration) / 1000
	// The frame the borders are a fraction of: what the library read out of
	// the header where it has, and only otherwise what ffmpeg prints about
	// its input, which is a log line and not a contract.
	box := CropResponse{FrameW: it.Width, FrameH: it.Height}
	for _, at := range cropSamplePoints {
		if ctx.Err() != nil {
			return CropResponse{}
		}
		// Each seek takes an ffmpeg slot, as every still does: four of them
		// beside a film that is playing were the thumbnailer's whole reason
		// for a gate, and this path had none.
		if err := s.thumbs.acquire(ctx, s.thumbs.ffSem); err != nil {
			return CropResponse{}
		}
		// The size is read off the log, so the log has to be there whatever
		// the build's default level is.
		args := []string{"-hide_banner", "-nostdin", "-loglevel", "info",
			"-ss", strconv.FormatFloat(at*seconds, 'f', 2, 64)}
		if headers != "" {
			args = append(args,
				"-rw_timeout", strconv.Itoa(int(archiveSeekReadTimeout/time.Microsecond)),
				"-headers", headers)
		}
		args = append(args, "-i", input,
			"-vf", "cropdetect=24:2:0", "-frames:v", strconv.Itoa(cropDetectAt),
			"-an", "-sn", "-f", "null", "-")
		cmd := exec.CommandContext(ctx, ffmpeg, args...)
		var errBuf strings.Builder
		cmd.Stderr = &errBuf
		cmd.WaitDelay = 5 * time.Second
		_ = cmd.Run() // a sample that fails is a sample, not a failure
		<-s.thumbs.ffSem
		out := errBuf.String()
		box = box.union(lastCrop(out))
		if box.FrameW == 0 {
			box.FrameW, box.FrameH = frameSize(out)
		}
	}
	if !box.worthwhile() {
		return CropResponse{}
	}
	return box
}

// lastCrop reads the final answer cropdetect settled on in this run.
func lastCrop(out string) CropResponse {
	m := cropLine.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return CropResponse{}
	}
	last := m[len(m)-1]
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	return CropResponse{W: n(last[1]), H: n(last[2]), X: n(last[3]), Y: n(last[4])}
}

// frameSize reads the picture's dimensions out of the line ffmpeg prints
// about its input, which saves probing the file a second time to learn what
// the borders are a fraction of.
var streamSize = regexp.MustCompile(`Video: .* (\d{2,5})x(\d{2,5})`)

func frameSize(out string) (w, h int) {
	m := streamSize.FindStringSubmatch(out)
	if m == nil {
		return 0, 0
	}
	w, _ = strconv.Atoi(m[1])
	h, _ = strconv.Atoi(m[2])
	return w, h
}

// union is the smallest box holding both. An empty box contributes nothing,
// which is what makes a failed sample harmless.
func (c CropResponse) union(o CropResponse) CropResponse {
	if o.W <= 0 || o.H <= 0 {
		return c
	}
	if c.W <= 0 || c.H <= 0 {
		return o
	}
	x0, y0 := min(c.X, o.X), min(c.Y, o.Y)
	x1, y1 := max(c.X+c.W, o.X+o.W), max(c.Y+c.H, o.Y+o.H)
	return CropResponse{
		X: x0, Y: y0, W: x1 - x0, H: y1 - y0,
		FrameW: max(c.FrameW, o.FrameW), FrameH: max(c.FrameH, o.FrameH),
	}
}

// worthwhile reports whether the borders are worth removing at all.
func (c CropResponse) worthwhile() bool {
	return c.W > 0 && c.H > 0 && c.FrameW > 0 && c.FrameH > 0 &&
		float64(c.W*c.H) < cropKeepFraction*float64(c.FrameW*c.FrameH)
}

// storedCrop reads what was found for this file before, if anything was. The
// database is left unaware of what a crop means: it keeps bytes stamped with
// the file they describe, as it does for thumbnails.
func (s *Server) storedCrop(it library.Item) (CropResponse, bool) {
	store := s.thumbs.Store()
	if store == nil {
		return CropResponse{}, false
	}
	raw, ok := store.GetCrop(it.ID, it.ModTime, it.Size)
	if !ok {
		return CropResponse{}, false
	}
	var c CropResponse
	if json.Unmarshal(raw, &c) != nil {
		return CropResponse{}, false
	}
	return c, true
}

// storeCrop remembers it, including an empty answer: "there are no borders
// here" costs the same several seconds to find out as the other kind, and
// is just as worth not finding out twice.
func (s *Server) storeCrop(it library.Item, c CropResponse) {
	store := s.thumbs.Store()
	if store == nil {
		return
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return
	}
	if err := store.PutCrop(it.ID, it.ModTime, it.Size, raw); err != nil {
		s.log.Warn("could not store the detected borders", "path", it.Rel, "err", err)
	}
}
