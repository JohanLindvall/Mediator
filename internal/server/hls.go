package server

// Streaming straight from the converter, in the one shape iOS will accept.
//
// /api/transcode already streams from the converter: fragmented MP4 on
// ffmpeg's stdout, straight down the response. Every browser but Safari
// plays it. Safari opens a media URL with a byte-range request and will not
// play a resource that cannot answer one — and a conversion of unknown length
// never can, which is why a film that needs converting did not start on a
// phone at all.
//
// HLS answers the same question differently. The conversion is written as
// short segments, each one a finished file with a length of its own, listed
// in a playlist that grows as they appear. Playback begins after the first
// segment rather than after the last, so a file the converter takes a minute
// to work through starts in a couple of seconds — and every request involved
// is for an ordinary file, which is what Safari wanted all along. It is also
// Apple's own format, so on that platform it is the best-supported path there
// is; nothing else needs it, because everything else plays the pipe.
//
// A session is one conversion: one ffmpeg, one directory of segments, keyed
// by what was asked for. Seeking reopens at a new time exactly as the piped
// conversion does, so the clock, the resume point and the subtitle offsets
// are the ones that machinery already works out.
//
// The session is in the URL path, not in a query, and that is load-bearing.
// ffmpeg writes plain segment names into the playlist, and a player resolves
// those against the playlist's own URL — which drops the query. With the
// session identified by ?t= and ?mode=, every segment request arrived asking
// for a different conversion than the playlist described: the first one
// answered, having quietly started a second ffmpeg, and the rest did not line
// up. Playing the playlist URL therefore redirects to one under the session,
// where a relative name can only resolve to that session's own segments.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

const (
	// hlsSegmentSec is how much video goes in one segment. Short enough that
	// the first one is ready quickly, long enough that a two-hour film is
	// not thousands of files and requests.
	hlsSegmentSec = 4
	// hlsConverting bounds how many ffmpeg runs may be going at once, as the
	// piped converter bounds its own. It bounds *conversions*, not kept
	// files: when a new one starts, the least recently wanted still-running
	// conversion is stopped and what it produced is left where it is.
	hlsConverting = 2
	// hlsKeepFor protects a session from pruning after it was last asked
	// for. A player that has buffered ahead goes quiet for a while and is
	// still being watched; deleting its segments would end the film.
	hlsKeepFor = 2 * time.Minute
	// hlsIdle is how long a conversion keeps running with nobody asking for
	// it. What it produced is kept — going back to a film should not convert
	// it again — but there is no reason to spend the processor on the rest
	// of one nobody is watching. A player that has buffered ahead goes quiet
	// for a while, so this is well past a segment's worth of silence.
	hlsIdle = 90 * time.Second
	// hlsFirstWait bounds the wait for the opening segments. Copying the
	// video makes that a second or two; a full re-encode of something the
	// browser cannot decode at all is what takes the rest of this.
	hlsFirstWait = 60 * time.Second
)

// HLS runs the segmented conversions and hands out their files.
type HLS struct {
	ffmpeg  string
	log     *slog.Logger
	lib     *library.Library
	scratch *Scratch

	mu       sync.Mutex
	sessions map[string]*hlsSession // by what was asked for, so it is reused
	byID     map[string]*hlsSession // by the token in the path
	seq      int64
}

type hlsSession struct {
	key string
	id  string // opaque, and the path segment the player resolves against
	dir string
	// What this is a conversion of, and from where. The subtitle renditions
	// need both: the item to read the cues out of, and the start time to
	// rebase them onto this session's clock — which begins at the seek, not
	// at the start of the film.
	item  library.Item
	start float64
	last  time.Time // when a request last arrived for it
	// converting is true while ffmpeg is still working. A session that has
	// finished costs only disk, and disk is what the budget is for.
	converting bool
	cancel     context.CancelFunc
	ready      chan struct{} // closed once the playlist lists a segment
	err        error
	used       int64 // for eviction: the sequence number of the last request
}

// hlsKeyFile names what a session's segments are a conversion of, so a later
// run can pick them up rather than converting the same film again.
const hlsKeyFile = "session.key"

// Adopt takes over the finished conversions a previous run left behind.
//
// Only finished ones: a playlist without its end marker is a conversion that
// was interrupted, and there is no way to carry on from where it stopped —
// the ffmpeg that knew where that was is gone. Those go, along with anything
// that is not a session at all.
func (h *HLS) Adopt() {
	base, err := h.scratch.Sub("hls")
	if err != nil {
		h.log.Warn("scratch: no place to keep conversions", "err", err)
		return
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	adopted, total := 0, int64(0)
	for _, e := range entries {
		dir := filepath.Join(base, e.Name())
		key, ok := completedSession(dir)
		if !ok {
			_ = os.RemoveAll(dir)
			continue
		}
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			_ = os.RemoveAll(dir)
			continue
		}
		done := make(chan struct{})
		close(done)
		h.mu.Lock()
		h.seq++
		s := &hlsSession{
			key: key, id: hex.EncodeToString(raw[:]), dir: dir,
			cancel: func() {}, ready: done, used: h.seq,
			// Wanted when it was last written, so the budget may take it
			// straight away rather than protecting it for a window it did
			// not earn.
			last: modTime(dir), converting: false,
		}
		h.sessions[key] = s
		h.byID[s.id] = s
		h.mu.Unlock()
		total += dirBytes(dir)
		adopted++
	}
	if adopted > 0 {
		h.scratch.Report("hls", total)
		h.log.Info("kept conversions from an earlier run", "sessions", adopted, "bytes", total)
	}
	h.account()
}

// completedSession reports what a directory holds, and whether it is a
// conversion that ran to the end.
func completedSession(dir string) (string, bool) {
	key, err := os.ReadFile(filepath.Join(dir, hlsKeyFile))
	if err != nil || len(key) == 0 {
		return "", false
	}
	playlist, err := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	if err != nil {
		return "", false
	}
	// The end marker is ffmpeg saying it wrote everything; without it the
	// conversion stopped somewhere and cannot be taken up again.
	if !strings.Contains(string(playlist), "#EXT-X-ENDLIST") {
		return "", false
	}
	return string(key), true
}

// modTime is when a session was last written to.
func modTime(dir string) time.Time {
	fi, err := os.Stat(dir)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// NewHLS builds the segmented converter. Without ffmpeg it declines
// everything, and the piped converter stays the only route.
func NewHLS(ffmpeg string, lib *library.Library, scratch *Scratch, log *slog.Logger) *HLS {
	return &HLS{
		ffmpeg: ffmpeg, lib: lib, scratch: scratch, log: log,
		sessions: map[string]*hlsSession{},
		byID:     map[string]*hlsSession{},
	}
}

// Close stops every conversion and removes what they wrote.
func (h *HLS) Close() {
	// converting is read and written under the lock everywhere, so the
	// decision is taken here and only the I/O happens outside it.
	type closing struct {
		s    *hlsSession
		drop bool
	}
	h.mu.Lock()
	all := make([]closing, 0, len(h.sessions))
	for _, s := range h.sessions {
		all = append(all, closing{s, s.converting})
		s.converting = false
	}
	h.sessions = map[string]*hlsSession{}
	h.byID = map[string]*hlsSession{}
	h.mu.Unlock()
	for _, c := range all {
		c.s.cancel()
		if c.drop {
			// Interrupted: nothing can carry on from where it stopped, and
			// a half-written playlist is not worth keeping for a later run.
			_ = os.RemoveAll(c.s.dir)
		}
		// Finished: left where it is, so the next run finds it rather than
		// converting the same film again.
	}
}

// stopConverting ends the ffmpeg but keeps everything it wrote. A player that
// comes back can still watch what was produced, and going back to a film it
// has already converted costs nothing. Caller must hold h.mu: converting is
// read under it by the reaper, the converting cap and the accounting.
func (s *hlsSession) stopConverting() {
	s.cancel()
	s.converting = false
}

// discard ends the conversion and takes its files with it. Only the budget
// does this: files are kept until the space is needed.
func (s *hlsSession) discard() {
	s.cancel()
	_ = os.RemoveAll(s.dir)
}

// handleHLSStart begins (or rejoins) a conversion and serves its playlist,
// with the segment names rewritten to carry the session.
//
// It does not redirect, and that is deliberate. ffmpeg writes plain names,
// and a player resolves those against the playlist's URL — but *which* URL
// is not agreed: Safari uses the one it was finally served from, Chrome the
// one it asked for. A redirect therefore worked in one and not the other,
// where every segment request arrived without the session and was answered
// 404. Serving the playlist where it was asked for and naming the segments
// relative to *that* leaves nothing to disagree about.
func (s *Server) handleHLSStart(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok || it.Kind != library.KindVideo {
		http.NotFound(w, r)
		return
	}
	if s.hls == nil || s.hls.ffmpeg == "" {
		http.Error(w, "conversion unavailable (no ffmpeg)", http.StatusNotImplemented)
		return
	}
	t, _ := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	if t < 0 || t > 1e7 {
		t = 0
	}
	copyVideo := r.URL.Query().Get("mode") == "audio"
	// The same fault the pipe has: a soundtrack-only conversion copies the
	// picture through, and a stream that reorders further than it declares
	// has to be re-encoded whatever was asked for (reorder.go).
	if copyVideo && s.mustReencode(r.Context(), it) {
		copyVideo = false
	}

	// The subtitle renditions below need the probed listing, and a film
	// asked about cold would otherwise deny the captions it carries inside
	// itself. This is the moment the probe exists for: something is opening.
	it = s.probed(r.Context(), it)

	sess, err := s.hls.session(r.Context(), it, t, copyVideo, r.URL.Query().Get("a"))
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("hls session", "path", it.Rel, "err", err)
		}
		// A file the disk would not hand over is the same answer the stream
		// gives, in the same words, so the player says why rather than
		// sending it on to a converter that will fail at the same open.
		var pe *fs.PathError
		if errors.As(err, &pe) {
			http.Error(w, "file unavailable: "+openFault(err), http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "could not start the conversion: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Where the film has subtitles, what is served here is a **master**
	// playlist naming them as renditions, with the media playlist beside
	// them — all under the session's own path, so the relative names resolve
	// exactly as the segments always have, signed prefix and all. This is
	// what puts subtitles on an AirPlay receiver: AirPlay hands over a URL
	// and nothing else, so the captions have to be *inside* what the URL
	// describes — and it is Safari's native subtitle path generally, one
	// menu in fullscreen, inline and on the receiver alike. The session and
	// its segments are untouched: one conversion serves every choice, and
	// the choice picks which rendition is marked DEFAULT.
	if subs := s.lib.Subtitles(it); len(subs) > 0 {
		body := masterPlaylist(sess.id, it, subs, r.URL.Query().Get("sub"))
		defer s.lib.StartStream()()
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "index.m3u8", time.Now(), bytes.NewReader(body))
		return
	}

	body, err := os.ReadFile(filepath.Join(sess.dir, "index.m3u8"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body = settledPlaylist(qualifySegments(body, sess.id))

	// Serving a segment is serving media: thumbnailing and enrichment yield
	// to it exactly as they do for the file and for the pipe.
	defer s.lib.StartStream()()
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	// Never cached: the playlist is the one thing here that changes, and a
	// stale copy is a player that stops at whatever it last listed.
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "index.m3u8", time.Now(), bytes.NewReader(body))
}

// qualifySegments puts the session in front of every segment name, so the
// name a player resolves says which conversion it belongs to whichever URL
// it resolves against.
func qualifySegments(body []byte, id string) []byte {
	lines := bytes.Split(body, []byte("\n"))
	for i, line := range lines {
		if bytes.HasSuffix(bytes.TrimSpace(line), []byte(".ts")) {
			lines[i] = append([]byte(id+"/"), bytes.TrimSpace(line)...)
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

// handleHLSFile serves one file of a running conversion.
func (s *Server) handleHLSFile(w http.ResponseWriter, r *http.Request) {
	if s.hls == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("file")
	sess := s.hls.byToken(r.PathValue("sid"))
	if sess == nil {
		// Reaped, evicted, or never ours. A player that comes back to a
		// session that has gone is told so plainly rather than being handed
		// somebody else's segments.
		http.NotFound(w, r)
		return
	}
	// The master's children live under the session path too, resolved
	// against the master exactly as segments are (hlssubs.go).
	if name == "media.m3u8" || hlsSubName(name) {
		s.handleHLSChild(w, r, sess, name)
		return
	}
	// Only what a playlist can name: the segments ffmpeg writes. The session
	// directory also holds the playlist itself (served by the start
	// endpoint) and the key file, which is internal bookkeeping and
	// nobody's to fetch.
	if !hlsSegmentName(name) {
		http.NotFound(w, r)
		return
	}

	// Serving a segment is serving media: thumbnailing and enrichment yield
	// to it exactly as they do for the file and for the pipe.
	defer s.lib.StartStream()()

	w.Header().Set("Content-Type", "video/mp2t")
	// A segment never changes, but it lives only as long as its session, so
	// it is not for keeping either.
	w.Header().Set("Cache-Control", "no-store")

	f, err := os.Open(filepath.Join(sess.dir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// Progress reports how much of this item has been converted, and whether a
// conversion is running.
//
// Counted in segments, which is what there is: each one is a known number of
// seconds, so the total against the item's length is how far the conversion
// has reached. That is not how much has been *watched* — it is how much is
// ready to watch, which is the thing worth showing while waiting.
//
// Of the item's sessions — a film seeked twice, or asked for in two
// languages, has several — it is the most recently asked for that answers:
// the one the player waiting on this readout started. The first found used
// to answer, which in a map is whichever, and could describe a conversion
// nobody was watching.
func (h *HLS) Progress(id string, durationMs int64) (float64, bool) {
	h.mu.Lock()
	var dir string
	var used int64 = -1
	for key, s := range h.sessions {
		if strings.HasPrefix(key, id+"|") && s.used > used {
			dir, used = s.dir, s.used
		}
	}
	h.mu.Unlock()
	if dir == "" || durationMs <= 0 {
		return 0, dir != ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, true
	}
	segs := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ts") {
			segs++
		}
	}
	done := float64(segs*hlsSegmentSec) / (float64(durationMs) / 1000)
	return min(done, 1), true
}

// settledPlaylist says a finished conversion is a finished thing.
//
// ffmpeg writes EVENT throughout, and leaves it that way after adding the end
// marker. EVENT means "segments are only ever appended", so a player goes on
// treating it as a live event: it shows LIVE where the clock should be, and a
// running time of what has been converted rather than of the film — which for
// something already converted in full is simply wrong. VOD is the type for a
// complete presentation, and the end marker is what says it is complete.
func settledPlaylist(body []byte) []byte {
	if !bytes.Contains(body, []byte("#EXT-X-ENDLIST")) {
		return body // still growing, and EVENT is the honest description
	}
	return bytes.Replace(body,
		[]byte("#EXT-X-PLAYLIST-TYPE:EVENT"),
		[]byte("#EXT-X-PLAYLIST-TYPE:VOD"), 1)
}

// hlsSegmentName reports whether a request names one of the segments ffmpeg
// writes ("seg00000.ts"): a fixed prefix, digits, and the one extension.
func hlsSegmentName(name string) bool {
	rest, ok := strings.CutPrefix(name, "seg")
	if !ok {
		return false
	}
	rest, ok = strings.CutSuffix(rest, ".ts")
	if !ok || rest == "" {
		return false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// byToken finds a live session and marks it as still wanted, which is what
// keeps the reaper away from a player that is quietly working through it.
func (h *HLS) byToken(id string) *hlsSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.byID[id]
	if s == nil {
		return nil
	}
	h.seq++
	s.used = h.seq
	s.last = time.Now()
	return s
}

// session returns the conversion for what was asked for, starting it if this
// is the first ask, and waits until there is something to play.
func (h *HLS) session(ctx context.Context, it library.Item, t float64, copyVideo bool, audio string) (*hlsSession, error) {
	mode := "full"
	if copyVideo {
		mode = "audio"
	}
	// The soundtrack is part of what a session is: two viewers watching the
	// same film in different languages are watching two conversions.
	key := fmt.Sprintf("%s|%d|%d|%.3f|%s|%s", it.ID, it.ModTime, it.Size, t, mode, audio)

	h.mu.Lock()
	if s, ok := h.sessions[key]; ok {
		h.seq++
		s.used = h.seq
		s.last = time.Now()
		h.mu.Unlock()
		return h.await(ctx, s)
	}
	h.mu.Unlock()

	// The directory, the token and the key file are made before the lock is
	// retaken: MkdirTemp and WriteFile are disk I/O, and a scratch disk busy
	// with a conversion was stalling every segment request behind them.
	dir, err := h.scratch.Temp("hls", "s-")
	if err != nil {
		return nil, err
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	// The key goes in beside the segments: a later run finds a directory of
	// files and needs to be told which conversion they are.
	if err := os.WriteFile(filepath.Join(dir, hlsKeyFile), []byte(key), 0o644); err != nil {
		h.log.Debug("hls: could not record the session key", "dir", dir, "err", err)
	}
	// The conversion outlives the request that started it: a player asks for
	// the playlist, goes away to fetch a segment, and comes back. Tying the
	// ffmpeg to one request would kill it between the two.
	cctx, cancel := context.WithCancel(context.Background())

	h.mu.Lock()
	if s, ok := h.sessions[key]; ok {
		// Somebody else made the same session while the directory was being
		// prepared; theirs is the one everybody waits on.
		h.seq++
		s.used = h.seq
		s.last = time.Now()
		h.mu.Unlock()
		cancel()
		_ = os.RemoveAll(dir)
		return h.await(ctx, s)
	}
	h.seq++
	s := &hlsSession{
		key: key, id: hex.EncodeToString(raw[:]), dir: dir, item: it, start: t,
		cancel: cancel, ready: make(chan struct{}), used: h.seq, last: time.Now(),
	}
	s.converting = true
	h.sessions[key] = s
	h.byID[s.id] = s
	stop := h.limitConvertingLocked()
	for _, old := range stop {
		old.stopConverting() // holds h.mu, as stopConverting requires
	}
	h.mu.Unlock()
	for _, old := range stop {
		h.log.Info("stopped converting, keeping what it produced", "dir", old.dir)
	}

	go h.run(cctx, s, it, t, copyVideo, audio)
	go h.reap(cctx, s)
	return h.await(ctx, s)
}

// await blocks until the session has something to play, or the caller leaves.
func (h *HLS) await(ctx context.Context, s *hlsSession) (*hlsSession, error) {
	// A timer that is stopped, not time.After: that one lived on for its
	// full minute after every playlist request that was answered sooner.
	limit := time.NewTimer(hlsFirstWait)
	defer limit.Stop()
	select {
	case <-s.ready:
		if s.err != nil {
			return nil, s.err
		}
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-limit.C:
		return nil, errors.New("the conversion produced nothing in time")
	}
}

// limitConvertingLocked keeps the number of running conversions down without
// touching what any of them produced. The least recently wanted one stops;
// its segments stay, and a player still watching that part still can.
// Called with the lock held.
func (h *HLS) limitConvertingLocked() []*hlsSession {
	var running []*hlsSession
	for _, s := range h.sessions {
		if s.converting {
			running = append(running, s)
		}
	}
	if len(running) <= hlsConverting {
		return nil
	}
	slices.SortFunc(running, func(a, b *hlsSession) int { return cmp.Compare(a.used, b.used) })
	return running[:len(running)-hlsConverting]
}

// reap stops converting a film nobody is watching. What was converted stays:
// going back to it should not do the work again, and the only thing that
// removes files is the budget needing the space.
func (h *HLS) reap(ctx context.Context, s *hlsSession) {
	t := time.NewTicker(hlsIdle / 3)
	defer t.Stop()
	last := int64(-1)
	quiet := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.mu.Lock()
			cur := s.used
			_, live := h.sessions[s.key]
			converting := s.converting
			h.mu.Unlock()
			if !live || !converting {
				return // discarded by the budget, or finished on its own
			}
			h.account()
			if cur == last {
				quiet++
			} else {
				quiet = 0
				last = cur
			}
			if quiet >= 3 {
				h.log.Info("nobody is watching; stopping the conversion and keeping what it produced",
					"dir", s.dir)
				h.mu.Lock()
				s.stopConverting()
				h.mu.Unlock()
				return
			}
		}
	}
}

// account measures what the sessions are holding and drops the least recently
// wanted until the converters are within their shared budget.
//
// A session being played is the most recently wanted, so it goes last — and a
// single one larger than the whole budget is left alone rather than killed
// under whoever is watching it. That is a budget the operator set too small
// for one film, and stopping playback is not the way to say so.
func (h *HLS) account() {
	// Measuring is a ReadDir per session, and doing it under the lock made
	// every segment request wait on the disk the conversions are writing to.
	// The sizes are a moment stale by the time the lock is retaken, which is
	// fine: the budget is approximate by nature.
	type aged struct {
		s     *hlsSession
		used  int64
		bytes int64
	}
	h.mu.Lock()
	order := make([]aged, 0, len(h.sessions))
	for _, s := range h.sessions {
		order = append(order, aged{s: s, used: s.used})
	}
	h.mu.Unlock()

	var total int64
	for i := range order {
		order[i].bytes = dirBytes(order[i].s.dir)
		total += order[i].bytes
	}
	h.scratch.Report("hls", total)
	if h.scratch.Excess() <= 0 || len(order) < 2 {
		return
	}

	// Files are kept until the space is needed; this is where it is needed.
	slices.SortFunc(order, func(a, b aged) int { return cmp.Compare(a.used, b.used) })
	now := time.Now()
	var dropped []*hlsSession
	h.mu.Lock()
	for _, a := range order {
		if h.scratch.Excess() <= 0 || len(h.sessions) < 2 {
			break
		}
		// A session that arrived after the snapshot has no business being
		// evicted on its account.
		if _, live := h.sessions[a.s.key]; !live {
			continue
		}
		// A session asked for recently is being watched, whatever the budget
		// says: a player that has buffered ahead goes quiet, and deleting
		// its segments would end the film it is in the middle of.
		if now.Sub(a.s.last) < hlsKeepFor {
			continue
		}
		total -= a.bytes
		delete(h.sessions, a.s.key)
		delete(h.byID, a.s.id)
		dropped = append(dropped, a.s)
		h.scratch.Report("hls", total)
	}
	h.mu.Unlock()
	for _, s := range dropped {
		h.log.Info("discarded a conversion to stay within the scratch budget", "dir", s.dir)
		s.discard()
	}
}

// dirBytes is what one session is holding.
func dirBytes(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range entries {
		if fi, err := e.Info(); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// forget removes a session that ended in an error, so the next ask starts
// fresh. A failure negative-cached for the life of the process made a film
// unplayable at that resume point on Safari — the same key answered with the
// same cached error forever — where the Remuxer already forgets failures for
// exactly this reason. Waiters that saw the error have their answer; the map
// entry is what must not outlive it.
func (h *HLS) forget(s *hlsSession) {
	h.mu.Lock()
	if cur, ok := h.sessions[s.key]; ok && cur == s {
		delete(h.sessions, s.key)
		delete(h.byID, s.id)
	}
	s.converting = false
	h.mu.Unlock()
	// Nothing playable was produced (failIfEmpty guarantees it), so the
	// directory holds only the key file and whatever ffmpeg half-wrote.
	s.discard()
}

// run is the conversion itself: the plan both converters share (convert.go)
// delivered as segments.
func (h *HLS) run(ctx context.Context, s *hlsSession, it library.Item, t float64, copyVideo bool, audio string) {
	plan, err := planConversion(h.ffmpeg, it, t, copyVideo, audio, h.log)
	if err != nil {
		s.fail(err)
		h.forget(s)
		return
	}
	defer plan.close()
	args := append(plan.args,
		"-f", "hls",
		"-hls_time", strconv.Itoa(hlsSegmentSec),
		// Every segment stays listed and on disk: this is a file being
		// converted, not a broadcast, so a player is entitled to go back to
		// what it has already been given.
		"-hls_list_size", "0",
		"-hls_flags", "independent_segments",
		"-hls_playlist_type", "event",
		"-hls_segment_filename", filepath.Join(s.dir, "seg%05d.ts"),
		filepath.Join(s.dir, "index.m3u8"),
	)

	cmd := exec.CommandContext(ctx, h.ffmpeg, args...)
	if plan.stdin != nil {
		cmd.Stdin = plan.stdin
	}
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	cmd.WaitDelay = 5 * time.Second

	if err := cmd.Start(); err != nil {
		s.fail(err)
		h.forget(s)
		return
	}
	go h.watchFirst(ctx, s)
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		h.log.Warn("hls conversion ended", "path", it.Rel, "err", err,
			"ffmpeg", strings.TrimSpace(errBuf.String()))
		// Only a failure that produced nothing is a failure to the caller;
		// one that stopped part way leaves a playable prefix behind.
		s.failIfEmpty(err)
	}
	h.mu.Lock()
	s.converting = false
	h.mu.Unlock()
	s.finish()
	if s.err != nil {
		// The waiters have their error; a fresh ask deserves a fresh
		// attempt rather than this one, cached.
		h.forget(s)
	}
}

// watchFirst opens the gate as soon as the playlist names a segment that
// exists. Serving the playlist before that gives the player a document with
// nothing in it, which it treats as the end of the stream.
func (h *HLS) watchFirst(ctx context.Context, s *hlsSession) {
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.ready:
			return
		case <-t.C:
			body, err := os.ReadFile(filepath.Join(s.dir, "index.m3u8"))
			if err != nil || !strings.Contains(string(body), ".ts") {
				continue
			}
			// The name is in the playlist; the file has to be there too, or
			// the player asks for something that is still being written.
			for _, line := range strings.Split(string(body), "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasSuffix(line, ".ts") {
					continue
				}
				if _, err := os.Stat(filepath.Join(s.dir, line)); err == nil {
					s.finish()
					return
				}
				break
			}
		}
	}
}

func (s *hlsSession) fail(err error) {
	s.err = err
	s.finish()
}

// failIfEmpty records an error only when nothing playable was produced.
func (s *hlsSession) failIfEmpty(err error) {
	select {
	case <-s.ready:
		return // something was already playable
	default:
		s.err = err
	}
}

func (s *hlsSession) finish() {
	select {
	case <-s.ready:
	default:
		close(s.ready)
	}
}
