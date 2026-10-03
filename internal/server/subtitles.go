package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// Text subtitles are ordinarily kilobytes. Bound both sidecar reads and
// ffmpeg output before decoding or conversion can multiply their memory use.
const subtitleMaxBytes = 16 << 20

// handleSubs lists a video's subtitles: the sidecar files found next to it,
// and the text streams carried inside it, as one list under one numbering.
func (s *Server) handleSubs(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// The embedded tracks come from the probe that runs when a video is
	// opened; this listing is part of that opening, so it is the moment the
	// probe exists for — and without it a film asked about cold would deny
	// the captions it carries.
	it = s.probed(r.Context(), it)
	subs := s.lib.Subtitles(it)
	if subs == nil {
		subs = []library.Subtitle{}
	}
	writeJSON(w, SubtitlesResponse{Subs: subs})
}

// subtitleData reads one of an item's subtitles by its combined index: the
// bytes of a sidecar as they are on disk, or an embedded stream already
// extracted to WebVTT. The name that comes back is what the converters key
// their decoding on — a sidecar's own, or a .vtt name for the extraction,
// which has nothing left to decode.
func (s *Server) subtitleData(ctx context.Context, it library.Item, index int) (data []byte, name string, err error) {
	if path, ok := s.lib.SubtitlePath(it, index); ok {
		info, err := os.Stat(path)
		if err != nil {
			return nil, path, err
		}
		if !info.Mode().IsRegular() || info.Size() > subtitleMaxBytes {
			return nil, path, fmt.Errorf("subtitle is not a regular file of at most %d bytes", subtitleMaxBytes)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, path, err
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(ctxReader{ctx, f}, subtitleMaxBytes+1))
		if len(data) > subtitleMaxBytes {
			return nil, path, fmt.Errorf("subtitle exceeds %d bytes", subtitleMaxBytes)
		}
		return data, path, err
	}
	if stream, ok := s.lib.EmbeddedSubStream(it, index); ok {
		data, err = s.extractEmbSub(ctx, it, stream)
		return data, "embedded.vtt", err
	}
	return nil, "", fmt.Errorf("no subtitle %d", index)
}

// handleSubFile serves one subtitle converted to WebVTT, the only format
// <track> accepts.
func (s *Server) handleSubFile(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	it = s.probed(r.Context(), it)
	// Resolved through the index, so a subtitle outside the roots is not
	// reachable even if something odd is sitting in the directory list.
	// Past the sidecars the index names a stream inside the file itself,
	// which is served extracted (embsubs.go) through the same conversions
	// and the same ?shift= as a sidecar — one list, one numbering, and
	// nothing downstream knows which kind it picked.
	data, path, err := s.subtitleData(r.Context(), it, index)
	if err != nil {
		if r.Context().Err() == nil {
			s.log.Debug("subtitle unavailable", "path", it.Rel, "index", index, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	// A television reads the sidecar itself and wants SubRip; the browser
	// takes only WebVTT. Same file, same list, one parameter apart.
	if r.URL.Query().Get("format") == "srt" {
		srt, err := ToSRT(path, data)
		if err != nil {
			s.log.Debug("subtitle conversion failed", "path", path, "err", err)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(srt)
		return
	}
	vtt, err := ToVTT(path, data)
	if err != nil {
		s.log.Debug("subtitle conversion failed", "path", path, "err", err)
		http.NotFound(w, r)
		return
	}
	// ?shift= rebases the cues onto a transcoded stream, whose clock starts at
	// the keyframe it was opened at rather than at the start of the film.
	if shift := mediaSeconds(r.URL.Query().Get("shift")); shift > 0 {
		vtt = shiftVTT(vtt, shift)
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// Sidecars change independently of the video, and shifting changes the
	// representation too. Validate the actual output, never the film's mtime.
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sha256.Sum256(vtt)))
	http.ServeContent(w, r, "subtitles.vtt", time.Time{}, bytes.NewReader(vtt))
}
