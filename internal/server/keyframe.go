package server

import (
	"context"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// A transcode that copies the video stream cannot begin anywhere but a
// keyframe: ffmpeg seeks to the last one at or before the time asked for, so
// the stream really starts up to a whole group of pictures early. Ten seconds
// is an ordinary spacing for a 4K web release, and a client that assumes the
// stream starts where it asked is then wrong by that much — in the position
// readout, in the resume point, and in every subtitle cue, whose times are
// absolute in the file while the stream's clock restarts at zero.
//
// So the client asks first, and corrects for the difference itself: it seeks
// the remaining fraction inside the stream, and offsets what it displays and
// what it hands the subtitle track.

// streamStart reports where a copied stream seeking to t really begins.
//
// It performs that very seek, with the same tool and the same options as the
// transcode itself, and reads the timestamp of the first packet to come out
// (-copyts keeps the file's own clock, and does not affect where the seek
// lands). Deriving the answer instead from a keyframe listing does not work:
// ffmpeg's seek is conservative in ways an index scan does not reproduce —
// asking for a time a few milliseconds past a keyframe rewinds to the one
// before it — and being wrong here means being wrong by a whole GOP. Reading
// one packet is also the cheaper of the two by an order of magnitude.
//
// t is returned unchanged when there is nothing better to say: no ffmpeg or
// ffprobe, an unreadable file, or an answer that makes no sense.
func streamStart(ctx context.Context, ffmpegBin, path string, t float64) float64 {
	probe := library.FFprobePath()
	if ffmpegBin == "" || probe == "" || t <= 0 {
		return max(t, 0)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	ff := exec.CommandContext(ctx, ffmpegBin,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-copyts", "-ss", strconv.FormatFloat(t, 'f', 3, 64), "-i", path,
		"-map", "0:v:0", "-c:v", "copy", "-frames:v", "1",
		"-f", "matroska", "pipe:1")
	ff.WaitDelay = 2 * time.Second
	out, err := ff.StdoutPipe()
	if err != nil {
		return t
	}
	fp := exec.CommandContext(ctx, probe, "-v", "error",
		"-select_streams", "v:0", "-show_entries", "packet=pts_time",
		"-of", "csv=p=0", "pipe:0")
	fp.Stdin = out
	fp.WaitDelay = 2 * time.Second

	if err := ff.Start(); err != nil {
		return t
	}
	pts, perr := fp.Output()
	out.Close()
	_ = ff.Wait() // it is writing into a pipe nobody reads any more
	if perr != nil {
		return t
	}
	if start, ok := firstPTS(string(pts), t); ok {
		return start
	}
	return t
}

// firstPTS reads the timestamp of the first packet out of ffprobe's csv. A
// value outside [0, t] is not a seek landing point — a container whose own
// clock does not start at zero would produce one — and is rejected so the
// caller can fall back to assuming an accurate seek.
func firstPTS(out string, t float64) (float64, bool) {
	for _, line := range strings.Split(out, "\n") {
		v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(line, ",")), 64)
		if err != nil {
			continue
		}
		if v < 0 || v > t {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// handleKeyframe answers where a copy-mode transcode of ?t= would begin.
// Content inside an archive is fed to ffmpeg through a pipe, which cannot be
// seeked by timestamp, so there is nothing to look up: the answer is the
// request, and the client corrects nothing.
func (s *Server) handleKeyframe(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok || it.Kind != library.KindVideo {
		http.NotFound(w, r)
		return
	}
	t, _ := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	if t < 0 || t > 1e7 {
		t = 0
	}
	start := t
	if !it.Archived() {
		start = streamStart(r.Context(), s.thumbs.FFmpegPath(), it.Path, t)
	}
	// A property of the file and the time asked for, not of the session, so
	// repeating a seek — or reloading the page on one — is free.
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSON(w, KeyframeResponse{Start: start})
}
