package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// The frame under the pointer on the seek bar (GET /api/frame/{id}?t=&w=).
//
// The scrub sheet (Sprite) is ten frames, which is a tour of a film and not a
// way to find a moment in it: across two hours that is a picture every twelve
// minutes. What the bar wants is the frame at the exact moment the pointer or
// the finger is on, so this takes that one frame, then and there, to the
// millisecond: an input seek to the keyframe at or before the moment and a
// decode forward from it (frameArgs, accurate), which is the same frame a
// player shows there to within one frame's length — which is as close as a
// moment of film goes.
//
// Nothing is kept on this side. A frame is wanted for as long as the pointer
// is on its pixel, the bar maps pixels to moments differently at every width,
// and a store of them would be a database growing with every hover. What the
// same moment costs twice is the browser's business: the answer is the same
// for the same file, so it is served immutable and the URL carries the file's
// time as the thumbnails' does.
//
// What it costs is the thing to watch, since it is taken while the film plays
// from the same disk: a few megabytes read around the moment, and a decode of
// up to a keyframe interval. The page keeps one request in flight and drops
// the ones the pointer has left behind — a request dropped kills its ffmpeg,
// the context being the request's own — and frameSem bounds it across every
// viewer. It is not counted as playback: it is the viewer using the
// interface, which is the top of this app's priority order and is marked as
// such for every request anyway.

// seekFrameTimeout bounds one frame: a keyframe interval of 4K decoded on a
// machine that is converting the same film. Well past what a frame costs,
// which is what makes it a bound rather than a budget; the page has usually
// moved on long before.
const seekFrameTimeout = 20 * time.Second

// The widths a frame may be asked for. The page asks for what its preview
// box is on its screen — a fifth of the player, at the display's density —
// and anything else is clamped here rather than refused: a width is not a
// thing to fail over, and a frame of a thousand pixels a hover is not a
// thing to make.
const (
	seekFrameMinWidth     = 96
	seekFrameMaxWidth     = 960
	seekFrameDefaultWidth = 320
)

// seekFrameWidth reads the width a frame is asked for.
func seekFrameWidth(s string) int {
	w, err := strconv.Atoi(s)
	if err != nil || w <= 0 {
		return seekFrameDefaultWidth
	}
	return min(max(w, seekFrameMinWidth), seekFrameMaxWidth)
}

// seekFrameAt reads the moment asked for, in milliseconds, and keeps it
// inside the film: a seek to the very end, or past an end the pointer can
// reach and the stream cannot, decodes nothing. ok is false for a moment
// that is not a number.
func seekFrameAt(s string, durationMs int64) (float64, bool) {
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil || ms < 0 {
		return 0, false
	}
	// The last frame is a frame's length before the end; half a second is
	// short of anything a pointer can tell apart on a bar and long enough to
	// land on a picture.
	if durationMs > 0 && ms > durationMs-500 {
		ms = max(durationMs-500, 0)
	}
	return float64(ms) / 1000, true
}

// seekFrameSpec is where the frame at a moment is read from: the file by
// path; content inside another file over this server's own stream URL, by
// time; and a DVD title over the same URL by position, since its clock is not
// continuous and a seek by time lands wherever the estimate says
// (library.SeekByte) — that frame is the first one where the title is
// entered, which is near the moment rather than at it, and is the best a disc
// can do. Nothing is read over a pipe, which reaches a moment only by reading
// everything before it: no loopback address, no frame.
func seekFrameSpec(it library.Item, at float64, width int) (frameSpec, bool) {
	spec := frameSpec{
		seek:     strconv.FormatFloat(at, 'f', 3, 64),
		width:    width,
		quality:  5,
		accurate: true,
	}
	if !it.Archived() {
		spec.input = it.Path
		return spec, true
	}
	url := library.LoopbackURL(it)
	if url == "" {
		return frameSpec{}, false
	}
	spec.input, spec.loopback = url, true
	if off, ok := library.SeekByte(it, at); ok {
		// -seekable 0 is half of this, as in convertInput: told the input can
		// be seeked, the demuxer rewinds to the start and the offset is undone.
		spec.pre = []string{"-seekable", "0", "-offset", strconv.FormatInt(off, 10)}
		spec.seek = ""
	}
	return spec, true
}

// Frame takes one frame of a video at a moment (seconds, to the millisecond),
// width pixels wide. ErrNoThumb where there is no frame to be had — no
// ffmpeg, nothing to seek in, or a moment the stream has nothing at — and the
// context's own error where the page gave up on it first, which it does
// every time the pointer moves on.
func (t *Thumbnailer) Frame(ctx context.Context, it library.Item, at float64, width int) ([]byte, error) {
	if t.ffmpeg == "" || it.Kind != library.KindVideo {
		return nil, ErrNoThumb
	}
	spec, ok := seekFrameSpec(it, at, width)
	if !ok {
		return nil, ErrNoThumb
	}
	if err := t.acquire(ctx, t.frameSem); err != nil {
		return nil, err
	}
	defer func() { <-t.frameSem }()
	cctx, cancel := context.WithTimeout(ctx, seekFrameTimeout)
	defer cancel()
	data, err := t.runFrame(ctx, cctx, nil, func(out string) []string {
		spec.out = out
		return frameArgs(spec)
	})
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, ErrNoThumb
	}
	return data, nil
}

// handleFrame serves the frame at a moment of a video, for the seek bar.
func (s *Server) handleFrame(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok || it.Kind != library.KindVideo {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	at, ok := seekFrameAt(q.Get("t"), it.Duration)
	if !ok {
		http.Error(w, "t is a moment in milliseconds", http.StatusBadRequest)
		return
	}
	data, err := s.thumbs.Frame(r.Context(), it, at, seekFrameWidth(q.Get("w")))
	if err != nil {
		// The page dropping a request the pointer has left is the ordinary
		// case and says nothing; anything else is worth a line.
		if !errors.Is(err, ErrNoThumb) && r.Context().Err() == nil {
			s.log.Debug("seek frame failed", "path", it.Rel, "at", at, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	serveImmutableJPEG(w, r, it, data)
}
