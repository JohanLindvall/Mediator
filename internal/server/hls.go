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
// short segments, each one a finished file with a length of its own, named
// in a playlist. Playback begins after the first segment rather than after
// the last, and every request involved is for an ordinary file, which is
// what Safari wanted all along. It is also Apple's own format, so on that
// platform it is the best-supported path there is; nothing else needs it,
// because everything else plays the pipe.
//
// **The playlist is the whole film from the first request.** A playlist
// that grows as the conversion runs is a live event to a player: it shows
// LIVE where the clock should be, a running time of what has been converted
// so far, and a seek bar that reaches no further — which on a phone's own
// fullscreen player, the one place a converted film is watched there, is
// the whole of the interface. So the segments are decided before any is
// made (hlstable.go): where each begins and how long it lasts, listed with
// the end marker from the first request. The player then has the film's
// length and seeks anywhere in it natively, and the server makes the
// segments as they are asked for — from wherever the player has got to,
// which after a seek is the segment it landed on. A session is one film in
// one form (mode, soundtrack, rung), one directory, one table, and any
// number of conversions over its life, each producing a run of segments and
// each stopped when the player asks for something a fresh start would reach
// sooner (hlsWaitFor). What has been made is kept and served; what has not
// is made on request, with the request waiting for it.
//
// Every cut is made where the table said, and checked. A re-encoded picture
// is told where to put its keyframes; a copied one is cut on the file's own
// keyframes, where a run begins on whichever the demuxer lands on (landing)
// and the muxer is told the table's times from there. The muxer's own list
// of what it wrote (`-segment_list`, csv) says when a segment is finished —
// a line is written only once the file is closed — and every line is judged
// against the table (verify): a cut anywhere else ends the session rather
// than serving a playlist that has become a lie.
//
// A film whose keyframes cannot be read cheaply — a copy from a container
// with no index this reads, or content reachable only through a pipe — keeps
// the older shape: one conversion from the seek, a playlist that grows, and
// the player told so (X-Media-Timeline) so that it keeps its own clock the
// way it always did. The session is in the URL path either way, and that is
// load-bearing: a player resolves segment names against the playlist's own
// URL, which drops the query, so a session identified by the query answered
// the playlist and then every segment request arrived asking for a different
// conversion.

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
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
	// hlsFirstWait bounds the wait for a segment that is being made: the
	// opening ones of a conversion, or the one a seek landed on. Copying the
	// video makes that a second or two; a full re-encode of something the
	// browser cannot decode at all is what takes the rest of this.
	hlsFirstWait = 60 * time.Second
	// hlsListPoll is how often a running conversion's list is read for the
	// segments it has finished.
	hlsListPoll = 200 * time.Millisecond
	// hlsProbeBudget bounds the landing probe: one seek and one packet.
	hlsProbeBudget = 15 * time.Second
	// hlsRunFailures is how many conversions may end without producing a
	// segment a request was waiting for before the session gives up on it.
	// One is the hardware being written off and the processor taking over;
	// two is a file that will not convert.
	hlsRunFailures = 2
	// hlsRunTail is how far past a stopping boundary a run is told to read,
	// so the segment before it is cut where the table says and the stub
	// after it is thrown away.
	hlsRunTail = 0.2
)

// errHLSClosed is what a conversion asked for during shutdown is told. There
// is nothing left to run it: the context that would cancel it hangs off
// Background, and the maps holding the only handle on it have been emptied.
var errHLSClosed = errors.New("the server is shutting down")

// errNoSuchSegment is a request for a segment the table does not have.
var errNoSuchSegment = errors.New("no such segment")

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
	// closed is set by Close and refused by session: a conversion started
	// after the shutdown snapshot is one nothing will ever cancel, its
	// context hanging off Background and the only cancel path being the map
	// Close has already emptied. The ffmpeg then outlives the process and
	// writes into a scratch directory nothing sweeps until a later run's
	// Adopt. The Remuxer has always refused the same way.
	closed bool
}

type hlsSession struct {
	key string
	id  string // opaque, and the path segment the player resolves against
	dir string
	// What this is a conversion of, and from where. The subtitle renditions
	// need both: the item to read the cues out of, and the start time to
	// rebase them onto this session's clock — which for a session with a
	// table is the film's own, and for one without begins at the seek.
	item  library.Item
	start float64
	// q is the rung of the bitrate ladder this session was made at, for
	// the master playlist to declare honestly; copyVideo and audio are the
	// rest of what it is a conversion of, for the runs it starts.
	q         quality
	copyVideo bool
	audio     string
	last      time.Time // when a request last arrived for it
	// converting is true while an ffmpeg is working for this session. A
	// session that has finished costs only disk, and disk is what the
	// budget is for.
	converting bool
	cancel     context.CancelFunc
	ready      chan struct{} // closed once there is something to play
	// stateMu guards done and err. The two are written by the conversion
	// and by the watcher that opens the gate, from different goroutines,
	// and read by every waiter the moment the gate opens: a plain field was
	// a race between "nothing playable, record the error" and the watcher
	// finding the first segment — and two goroutines closing ready.
	stateMu sync.Mutex
	done    bool
	err     error
	used    int64 // for eviction: the sequence number of the last request
	// bytes is what the directory held when it was last measured. It is
	// kept here, under h.mu, so the figure the budget reads can be summed
	// from the sessions that are live at the moment it is written rather
	// than from a snapshot taken before the lock was dropped.
	bytes int64

	// The rest belongs to a session with a table, and is guarded by sm —
	// taken after h.mu where both are held, never before it.
	table *hlsTable
	sm    sync.Mutex
	// have names the file holding each finished segment, by index. Files
	// are the runs' own (run<n>-seg<k>.ts), so a later run never writes
	// over what an earlier one made and is serving.
	have map[int]string
	run  *hlsRun       // the conversion in flight, or nil
	wake chan struct{} // closed and replaced whenever have or run changes
	// broken is set when a run cut somewhere the table did not say, or
	// runs kept ending without producing what was asked: the session
	// answers every segment with it from then on, and the player moves on.
	broken error
	// failures counts, per segment, the runs that ended without making it.
	failures map[int]int
	// startMu serialises stopping one run and starting the next, so the
	// old run's files are cleared before the new run's are named.
	startMu    sync.Mutex
	manifestMu sync.Mutex
}

// hlsRun is one ffmpeg over a session's table: from a segment, up to where
// segments already exist or the film ends.
type hlsRun struct {
	seq    int64
	ctx    context.Context
	cancel context.CancelFunc
	exited chan struct{} // closed once the run has ended and cleaned up
	// fileStart is the index of the first file the muxer writes, which is
	// where the demuxer landed; partial says that file begins inside its
	// segment and is thrown away. until is where the run stops, exclusive.
	fileStart int
	partial   bool
	until     int
	list      string // the muxer's list, by name in the session directory
	hardware  bool
	state     hlsRunState
	seen      int   // lines of the list already read
	reached   bool  // it produced everything it was asked for
	err       error // what ended it, if not the end of its work
}

// hlsKeyFile names what a session's segments are a conversion of, so a later
// run can pick them up rather than converting the same film again.
const hlsKeyFile = "session.key"

// hlsManifest lists, for a session with a table, the segments finished and
// the file holding each — the record a later run adopts, and the one thing
// that says a file on disk is whole.
const hlsManifest = "done.txt"

// Adopt takes over the conversions a previous run left behind.
//
// A session with a table is taken as far as its manifest goes: every segment
// it lists is whole, since a line is written only once the muxer has closed
// the file, and anything else in the directory is what a conversion was in
// the middle of. A session without one is kept only when its playlist has
// its end marker — a conversion that was interrupted cannot be carried on
// from where it stopped, the ffmpeg that knew where that was being gone.
// Directories that are neither go.
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
		var have map[int]string
		if !ok {
			key, have, ok = adoptTabled(dir)
		}
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
		size := dirBytes(dir)
		h.mu.Lock()
		h.seq++
		s := &hlsSession{
			key: key, id: hex.EncodeToString(raw[:]), dir: dir,
			cancel: func() {}, ready: done, used: h.seq, bytes: size,
			// Wanted when it was last written, so the budget may take it
			// straight away rather than protecting it for a window it did
			// not earn.
			last: modTime(dir), converting: false,
			have: have, wake: make(chan struct{}), failures: map[int]int{},
		}
		h.sessions[key] = s
		h.byID[s.id] = s
		h.mu.Unlock()
		total += size
		adopted++
	}
	if adopted > 0 {
		h.scratch.Report("hls", total)
		h.log.Info("kept conversions from an earlier run", "sessions", adopted, "bytes", total)
	}
	h.account()
}

// completedSession reports what a directory holds, and whether it is a
// conversion without a table that ran to the end.
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

// adoptTabled reads a tabled session's manifest and clears the directory
// down to what it names: the key, and the whole segments.
func adoptTabled(dir string) (string, map[int]string, bool) {
	key, err := os.ReadFile(filepath.Join(dir, hlsKeyFile))
	if err != nil || !strings.Contains(string(key), "|"+hlsVODField+"|") {
		return "", nil, false
	}
	have := readManifest(dir)
	keep := map[string]bool{hlsKeyFile: true, hlsManifest: true}
	for _, name := range have {
		keep[name] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, false
	}
	for _, e := range entries {
		if !keep[e.Name()] {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	return string(key), have, true
}

// readManifest is the segments a manifest says are whole, checked against
// the disk: a line naming a file that is not there, or is empty, is a
// segment that is not there.
func readManifest(dir string) map[int]string {
	have := map[int]string{}
	f, err := os.Open(filepath.Join(dir, hlsManifest))
	if err != nil {
		return have
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var k int
		var name string
		if _, err := fmt.Sscanf(sc.Text(), "%d %s", &k, &name); err != nil || k < 0 {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.Size() > 0 && filepath.Base(name) == name {
			have[k] = name
		}
	}
	return have
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

// Close stops every conversion and removes what an interrupted one without a
// table wrote. A tabled session keeps its files: the manifest says which are
// whole, and the next run adopts exactly those.
func (h *HLS) Close() {
	// converting is read and written under the lock everywhere, so the
	// decision is taken here and only the I/O happens outside it.
	type closing struct {
		s    *hlsSession
		drop bool
	}
	h.mu.Lock()
	h.closed = true
	all := make([]closing, 0, len(h.sessions))
	for _, s := range h.sessions {
		all = append(all, closing{s, s.converting && s.table == nil})
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
			// A removal that fails is said out loud rather than swallowed:
			// cancel only signals the ffmpeg, so a directory can gain a
			// segment between the readdir and the rmdir, and what is left
			// then is a directory no session names any more.
			if err := os.RemoveAll(c.s.dir); err != nil {
				h.log.Warn("could not remove a stopped conversion", "dir", c.s.dir, "err", err)
			}
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
//
// What could not be removed is handed back rather than dropped. cancel only
// *signals* the ffmpeg, so a directory can gain a segment between the readdir
// and the rmdir; what is left then is a directory no session names any more,
// which no later measurement can see and nothing but a later run's Adopt will
// clear. That is worth a line in the log.
func (s *hlsSession) discard() error {
	s.cancel()
	return os.RemoveAll(s.dir)
}

// hlsTimelineHeader tells the player whose clock the stream keeps: "film"
// for a tabled session, whose segments carry the film's own timestamps and
// whose playlist is the whole film, and "session" for one that begins at
// the seek. The player cannot read a playlist's shape from the element it
// hands the URL to, and the two want different arithmetic from it.
const hlsTimelineHeader = "X-Media-Timeline"

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
	// A rung on the bitrate ladder is a re-encode whatever the mode asked
	// for; see effectiveCopy.
	q, ok := parseQuality(r.URL.Query().Get("q"))
	if !ok {
		http.Error(w, "unknown quality", http.StatusBadRequest)
		return
	}
	copyVideo = effectiveCopy(copyVideo, q)
	// The same fault the pipe has: a soundtrack-only conversion copies the
	// picture through, and a stream that reorders further than it declares
	// has to be re-encoded whatever was asked for (reorder.go).
	if copyVideo && s.mustReencode(r.Context(), it) {
		copyVideo = false
	}

	// The subtitle renditions below need the probed listing, and a film
	// asked about cold would otherwise deny the captions it carries inside
	// itself. This is the moment the probe exists for: something is opening.
	// The table needs the film's length from the same probe.
	it = s.probed(r.Context(), it)

	sess, err := s.hls.session(r.Context(), it, t, copyVideo, r.URL.Query().Get("a"), q)
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
	timeline, startAt := "session", 0.0
	if sess.table != nil {
		timeline, startAt = "film", t
	}
	w.Header().Set(hlsTimelineHeader, timeline)

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
		body := masterPlaylist(sess.id, it, subs, r.URL.Query().Get("sub"), copyVideo, sess.q, startAt)
		defer s.lib.StartStream()()
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "index.m3u8", time.Now(), bytes.NewReader(body))
		return
	}

	var body []byte
	if sess.table != nil {
		body = sess.table.playlist(sess.id+"/", startAt)
	} else {
		body, err = os.ReadFile(filepath.Join(sess.dir, "index.m3u8"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		body = settledPlaylist(qualifySegments(body, sess.id))
	}

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

// handleHLSFile serves one file of a conversion.
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
	// Only what a playlist can name: the segments. The session directory
	// also holds the playlist itself (served by the start endpoint), the
	// muxer's lists and the key file, which are internal bookkeeping and
	// nobody's to fetch.
	if !hlsSegmentName(name) {
		http.NotFound(w, r)
		return
	}

	// Serving a segment is serving media: thumbnailing and enrichment yield
	// to it exactly as they do for the file and for the pipe.
	defer s.lib.StartStream()()

	path := filepath.Join(sess.dir, name)
	if sess.table != nil {
		// Made on request where it has not been made yet — this is where a
		// seek is answered — and the request waits for it.
		var err error
		path, err = s.hls.segment(r.Context(), sess, hlsSegmentIndex(name))
		switch {
		case err == nil:
		case errors.Is(err, errNoSuchSegment):
			http.NotFound(w, r)
			return
		case r.Context().Err() != nil:
			return // the player left
		default:
			s.log.Warn("hls segment", "path", sess.item.Rel, "segment", name, "err", err)
			http.Error(w, "the conversion could not produce this segment: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
	}

	w.Header().Set("Content-Type", "video/mp2t")
	// A segment never changes, but it lives only as long as its session, so
	// it is not for keeping either.
	w.Header().Set("Cache-Control", "no-store")

	f, err := os.Open(path)
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
// Counted in segments, which is what there is: a tabled session knows how
// many it has of how many, and one without counts its files against the
// item's length. That is not how much has been *watched* — it is how much
// is ready to watch, which is the thing worth showing while waiting.
//
// Of the item's sessions — a film asked for in two languages, or at two
// rungs, has several — it is the most recently asked for that answers:
// the one the player waiting on this readout started. The first found used
// to answer, which in a map is whichever, and could describe a conversion
// nobody was watching.
func (h *HLS) Progress(id string, durationMs int64) (float64, bool) {
	h.mu.Lock()
	s, found := newestOf(h.sessions, id, func(s *hlsSession) int64 { return s.used })
	dir := ""
	if found {
		dir = s.dir
	}
	h.mu.Unlock()
	if dir == "" {
		return 0, false
	}
	if s.table != nil {
		s.sm.Lock()
		n, got := s.table.n(), len(s.have)
		s.sm.Unlock()
		if n <= 0 {
			return 0, true
		}
		return min(float64(got)/float64(n), 1), true
	}
	if durationMs <= 0 {
		return 0, true
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

// hlsSegmentName reports whether a request names a segment ("seg00000.ts"):
// a fixed prefix, digits, and the one extension.
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

// tableFor decides how a film will be cut, or that it cannot be decided:
// a re-encode keeps the grid, a copy takes the file's own keyframes where
// the container has an index this reads (library.Keyframes). Nil is a
// session of the older shape, one conversion from the seek.
//
// Two things rule a table out whatever the container. Content read through
// a pipe cannot be seeked, so a run cannot start at a segment; and a disc
// title is seeked by byte position because its clock is not continuous
// (library.SeekByte), where a table is a promise about that clock.
func (h *HLS) tableFor(it library.Item, copyVideo bool) *hlsTable {
	end := float64(it.Duration) / 1000
	if end <= 0 || library.SeeksByByte(it) {
		return nil
	}
	if it.Archived() && library.LoopbackURL(it) == "" {
		return nil
	}
	if !copyVideo {
		return hlsGrid(end)
	}
	keys, ok := library.Keyframes(it)
	if !ok {
		return nil
	}
	return hlsTableFromKeys(keys, end)
}

// session returns the conversion for what was asked for, starting it if this
// is the first ask. A tabled session answers at once and begins converting
// where the viewer asked to start; one without waits until there is
// something to play.
func (h *HLS) session(ctx context.Context, it library.Item, t float64, copyVideo bool, audio string, q quality) (*hlsSession, error) {
	table := h.tableFor(it, copyVideo)
	keyT := t
	if table != nil {
		keyT = -1
	}
	key := hlsKey(it, keyT, copyVideo, audio, q)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errHLSClosed
	}
	if s, ok := h.sessions[key]; ok {
		h.seq++
		s.used = h.seq
		s.last = time.Now()
		h.mu.Unlock()
		if table != nil {
			// Adopted from an earlier run: it knows what it holds and not
			// what it is of, until the first ask says.
			s.sm.Lock()
			if s.table == nil {
				s.table, s.item, s.copyVideo, s.audio, s.q = table, it, copyVideo, audio, q
			}
			s.sm.Unlock()
			h.prewarm(s, t)
			return s, nil
		}
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
	if h.closed {
		// Shutdown happened while the directory was being prepared. Starting
		// here would leave an ffmpeg nothing can reach: the snapshot Close
		// cancelled has been taken, and this session would be in neither map
		// it holds.
		h.mu.Unlock()
		cancel()
		_ = os.RemoveAll(dir)
		return nil, errHLSClosed
	}
	if s, ok := h.sessions[key]; ok {
		// Somebody else made the same session while the directory was being
		// prepared; theirs is the one everybody waits on.
		h.seq++
		s.used = h.seq
		s.last = time.Now()
		h.mu.Unlock()
		cancel()
		_ = os.RemoveAll(dir)
		if table != nil {
			h.prewarm(s, t)
			return s, nil
		}
		return h.await(ctx, s)
	}
	h.seq++
	s := &hlsSession{
		key: key, id: hex.EncodeToString(raw[:]), dir: dir, item: it, start: t, q: q,
		copyVideo: copyVideo, audio: audio,
		cancel: cancel, ready: make(chan struct{}), used: h.seq, last: time.Now(),
		table: table, have: map[int]string{}, wake: make(chan struct{}), failures: map[int]int{},
	}
	if table != nil {
		// Nothing to wait for: the playlist is the whole film, and the
		// segments are made as they are asked for.
		s.start = 0
		cancel()
		s.cancel = func() {}
		close(s.ready)
		h.sessions[key] = s
		h.byID[s.id] = s
		h.mu.Unlock()
		go h.reap(s)
		h.prewarm(s, t)
		return s, nil
	}
	s.converting = true
	h.sessions[key] = s
	h.byID[s.id] = s
	stop := h.limitConvertingLocked(s)
	for _, old := range stop {
		old.stopConverting() // holds h.mu, as stopConverting requires
	}
	h.mu.Unlock()
	for _, old := range stop {
		h.log.Info("stopped converting, keeping what it produced", "dir", old.dir)
	}

	// The watcher that opens the gate on the first segment is the
	// session's, started once: an attempt that failed and is being made
	// again must not leave a second watcher reading the dead attempt's
	// playlist beside the new one.
	go h.watchFirst(cctx, s)
	go h.run(cctx, s, it, t, copyVideo, audio, q)
	go h.reap(s)
	return h.await(ctx, s)
}

// hlsVODField is what stands where the start time would in the key of a
// session with a table: it begins nowhere in particular, being the whole
// film.
const hlsVODField = "vod"

// hlsKey is what a session is: the film, the file it was when the session
// was made, where it starts (or that it is the whole film), what is
// converted, which soundtrack — and which rung of the bitrate ladder. The
// soundtrack is part of it because two viewers watching one film in
// different languages are watching two conversions, and the rung for the
// same reason: a viewer who moved down the ladder must not be handed the
// session made before they did. The rung is last, so a key written before
// there was one still reads back (hlsSessionItem takes the start from the
// fourth field).
func hlsKey(it library.Item, t float64, copyVideo bool, audio string, q quality) string {
	mode := "full"
	if copyVideo {
		mode = "audio"
	}
	start := hlsVODField
	if t >= 0 {
		start = fmt.Sprintf("%.3f", t)
	}
	return fmt.Sprintf("%s|%d|%d|%s|%s|%s|q%d", it.ID, it.ModTime, it.Size, start, mode, audio, q.kbps)
}

// await blocks until the session has something to play, or the caller leaves.
func (h *HLS) await(ctx context.Context, s *hlsSession) (*hlsSession, error) {
	// A timer that is stopped, not time.After: that one lived on for its
	// full minute after every playlist request that was answered sooner.
	limit := time.NewTimer(hlsFirstWait)
	defer limit.Stop()
	select {
	case <-s.ready:
		if err := s.failure(); err != nil {
			return nil, err
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
// its segments stay, and a player still watching that part still can. The
// session that is starting is never among them, whatever its age.
// Called with the lock held.
func (h *HLS) limitConvertingLocked(starting *hlsSession) []*hlsSession {
	var running []*hlsSession
	for _, s := range h.sessions {
		if s.converting && s != starting {
			running = append(running, s)
		}
	}
	if len(running) < hlsConverting {
		return nil
	}
	slices.SortFunc(running, func(a, b *hlsSession) int { return cmp.Compare(a.used, b.used) })
	return running[:len(running)-hlsConverting+1]
}

// reap stops converting a film nobody is watching. What was converted stays:
// going back to it should not do the work again, and the only thing that
// removes files is the budget needing the space. A session without a table
// is done with once its one conversion ends; one with a table may start
// another on the next seek, so its reaper lives as long as it does.
func (h *HLS) reap(s *hlsSession) {
	t := time.NewTicker(hlsIdle / 3)
	defer t.Stop()
	last := int64(-1)
	quiet := 0
	for range t.C {
		h.mu.Lock()
		cur := s.used
		live := h.sessions[s.key] == s
		converting := s.converting
		h.mu.Unlock()
		if !live || (!converting && s.table == nil) {
			return // discarded by the budget, or finished on its own
		}
		if !converting {
			last, quiet = cur, 0
			continue
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
			quiet = 0
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
//
// What is published is summed from the sessions that are live when the lock
// is retaken, never from the snapshot's own running total. Two accountants
// overlap easily — the measuring is a ReadDir per session on the disk the
// conversions are writing to — and the one whose snapshot was older used to
// overwrite the other's corrected figure with a total that still counted
// sessions it had since evicted. A stale measurement is only a moment out; a
// stale *total* said gigabytes were on a disk that no longer held them, and
// the Remuxer prunes its own rewraps on that same shared figure.
func (h *HLS) account() {
	// Measuring is a ReadDir per session, and doing it under the lock made
	// every segment request wait on the disk the conversions are writing to.
	// The sizes are a moment stale by the time the lock is retaken, which is
	// fine: the budget is approximate by nature.
	h.mu.Lock()
	order := make([]*hlsSession, 0, len(h.sessions))
	for _, s := range h.sessions {
		order = append(order, s)
	}
	h.mu.Unlock()

	sizes := make([]int64, len(order))
	for i, s := range order {
		sizes[i] = dirBytes(s.dir)
	}

	now := time.Now()
	var dropped []*hlsSession
	h.mu.Lock()
	for i, s := range order {
		if h.sessions[s.key] == s {
			s.bytes = sizes[i]
		}
	}
	h.reportLocked()
	if h.scratch.Excess() > 0 && len(h.sessions) >= 2 {
		// Files are kept until the space is needed; this is where it is needed.
		slices.SortFunc(order, func(a, b *hlsSession) int { return cmp.Compare(a.used, b.used) })
		for _, s := range order {
			if h.scratch.Excess() <= 0 || len(h.sessions) < 2 {
				break
			}
			// The session under this key has to be the one that was
			// measured, and not merely *a* session under it. A key vacated
			// and taken again while the directories were being read left the
			// guard satisfied by the newcomer, which was then deleted from
			// the map on the strength of the old one's age and bytes — a
			// live conversion, still reachable by its token and still
			// writing, that no later measurement could see, no reaper
			// managed and Close never cancelled. forget has always compared
			// identity; this is the same test in the places that missed it.
			if h.sessions[s.key] != s {
				continue
			}
			// A session asked for recently is being watched, whatever the budget
			// says: a player that has buffered ahead goes quiet, and deleting
			// its segments would end the film it is in the middle of.
			if now.Sub(s.last) < hlsKeepFor {
				continue
			}
			h.dropLocked(s)
			dropped = append(dropped, s)
			h.reportLocked()
		}
	}
	h.mu.Unlock()
	for _, s := range dropped {
		h.log.Info("discarded a conversion to stay within the scratch budget", "dir", s.dir)
		if err := s.discard(); err != nil {
			h.log.Warn("could not remove a discarded conversion", "dir", s.dir, "err", err)
		}
	}
}

// reportLocked publishes what the live sessions hold between them.
//
// Summed here rather than carried along: every route that changes what HLS
// holds ends in this one place, so the shared figure cannot be left
// describing a session that has been discarded. What it sums is what each
// live session was last *measured* to hold, which is a moment stale by
// nature and is nothing at all for a session younger than its reaper's first
// tick — so a caller that can afford a measurement takes one (account) and
// the others accept the approximation the budget has always been. It used to
// be written only from the reap ticker, which returns the moment its session
// stops converting — so a conversion that finished inside one tick was never
// counted at all, and with nothing converting anywhere the figure stood
// still at whatever was last measured while the disk went on filling.
// Called with the lock held.
func (h *HLS) reportLocked() {
	var total int64
	for _, s := range h.sessions {
		total += s.bytes
	}
	h.scratch.Report("hls", total)
}

// dropLocked takes a session out of both maps, and only where each still
// names it: the token map and the key map are reached by different routes,
// and removing an entry either of them no longer holds is how the two come to
// disagree. Called with the lock held.
func (h *HLS) dropLocked(s *hlsSession) {
	if h.sessions[s.key] == s {
		delete(h.sessions, s.key)
	}
	if h.byID[s.id] == s {
		delete(h.byID, s.id)
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
	h.dropLocked(s)
	s.converting = false
	// What it was holding leaves the shared figure with it, or the budget
	// goes on counting bytes that are about to be deleted and the Remuxer
	// frees rewraps to make room for them.
	h.reportLocked()
	h.mu.Unlock()
	// Nothing playable was produced (failIfEmpty guarantees it), so the
	// directory holds only the key file and whatever ffmpeg half-wrote.
	if err := s.discard(); err != nil {
		h.log.Warn("could not remove a failed conversion", "dir", s.dir, "err", err)
	}
	// And then the figure is measured rather than merely reduced. What was
	// published a moment ago is a sum of what each session was last measured
	// to hold, and a session younger than its reaper's first tick has been
	// measured at nothing — so a forget in that window would leave the
	// budget told that a conversion writing to the disk right now is not
	// there. Measuring is a ReadDir per session, which a failed conversion
	// can afford; it is the segment requests that could not.
	h.account()
}

// ---- tabled sessions ----------------------------------------------------

// broadcastLocked wakes everything waiting on the session. Called with sm
// held.
func (s *hlsSession) broadcastLocked() {
	close(s.wake)
	s.wake = make(chan struct{})
}

// runStateLocked is the conversion in flight as a request may judge it.
// Called with sm held.
func (s *hlsSession) runStateLocked() *hlsRunState {
	if s.run == nil {
		return nil
	}
	st := s.run.state
	return &st
}

// prewarm starts converting at the moment the viewer asked for, so the
// first segment request finds it under way rather than starting it.
func (h *HLS) prewarm(s *hlsSession, t float64) {
	k := s.table.at(t)
	s.sm.Lock()
	_, have := s.have[k]
	wait := hlsWaitFor(s.runStateLocked(), k, time.Now())
	s.sm.Unlock()
	if have || wait {
		return
	}
	if err := h.restartAt(s, k); err != nil {
		h.log.Warn("hls: could not begin converting", "path", s.item.Rel, "segment", k, "err", err)
	}
}

// segment answers with the file holding segment k, making it first where it
// has not been made: waiting on the conversion that will reach it, or
// stopping that one and starting at k where a fresh start would be sooner.
func (h *HLS) segment(ctx context.Context, s *hlsSession, k int) (string, error) {
	limit := time.NewTimer(hlsFirstWait)
	defer limit.Stop()
	for {
		s.sm.Lock()
		if s.broken != nil {
			err := s.broken
			s.sm.Unlock()
			return "", err
		}
		if name, ok := s.have[k]; ok {
			s.sm.Unlock()
			return filepath.Join(s.dir, name), nil
		}
		if k < 0 || k >= s.table.n() {
			s.sm.Unlock()
			return "", errNoSuchSegment
		}
		wake := s.wake
		wait := hlsWaitFor(s.runStateLocked(), k, time.Now())
		s.sm.Unlock()
		if !wait {
			if err := h.restartAt(s, k); err != nil {
				return "", err
			}
			continue
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return "", ctx.Err()
		case <-limit.C:
			return "", errors.New("the conversion did not reach the segment in time")
		}
	}
}

// restartAt stops the conversion in flight, if any, and starts one at k.
// Serialised per session: the old run's files are cleared before the new
// one is named, and two requests deciding to restart at once start one.
func (h *HLS) restartAt(s *hlsSession, k int) error {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.sm.Lock()
	old := s.run
	_, have := s.have[k]
	wait := hlsWaitFor(s.runStateLocked(), k, time.Now())
	s.sm.Unlock()
	if have || wait {
		return nil // somebody got here first
	}
	if old != nil {
		if !old.state.ended {
			old.cancel()
		}
		<-old.exited
		// A run that ended without making a segment it was asked for is
		// counted against that segment, whatever ended it — an error, or
		// an end with nothing to show — and the second such is a file that
		// will not convert: the session says so rather than trying for
		// ever. A run that was stopped is not one of these; a run that
		// began behind this segment and never reached it is.
		if old.state.ended && old.ctx.Err() == nil && k >= old.fileStart && k < old.until {
			err := old.err
			if err == nil {
				err = fmt.Errorf("the conversion ended without producing segment %d", k)
			}
			s.sm.Lock()
			s.failures[k]++
			tooMany := s.failures[k] >= hlsRunFailures
			if tooMany && s.broken == nil {
				s.broken = err
				s.broadcastLocked()
			}
			s.sm.Unlock()
			if tooMany {
				return err
			}
		}
	}
	return h.startRun(s, k)
}

// startRun begins a conversion at segment k.
func (h *HLS) startRun(s *hlsSession, k int) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errHLSClosed
	}
	h.seq++
	seq := h.seq
	cctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.converting = true
	stop := h.limitConvertingLocked(s)
	for _, old := range stop {
		old.stopConverting()
	}
	h.mu.Unlock()
	for _, old := range stop {
		h.log.Info("stopped converting, keeping what it produced", "dir", old.dir)
	}

	r := &hlsRun{
		seq: seq, ctx: cctx, cancel: cancel, exited: make(chan struct{}),
		fileStart: k, list: fmt.Sprintf("list-%d.csv", seq),
		state: hlsRunState{next: k, started: time.Now()},
	}
	s.sm.Lock()
	r.until = s.table.n()
	for j := k + 1; j < s.table.n(); j++ {
		if _, ok := s.have[j]; ok {
			r.until = j
			break
		}
	}
	r.state.until = r.until
	s.run = r
	s.broadcastLocked()
	s.sm.Unlock()
	go h.runTable(s, r, k)
	return nil
}

// runTable is one conversion over the table, from segment k: the probe
// that says where the demuxer will land, the ffmpeg, the reading of its
// list as it writes, and the clearing up.
func (h *HLS) runTable(s *hlsSession, r *hlsRun, k int) {
	ctx := r.ctx
	tb := s.table
	defer h.endRun(s, r)

	// Where the run begins. A re-encode seeks accurately and starts on the
	// grid point itself. A copy starts where the demuxer lands, which is a
	// keyframe at or before the boundary; the muxer is told the table's
	// cuts from there, and whatever it writes before the first whole
	// segment is thrown away.
	seek, landed := tb.starts[k], tb.starts[k]
	trimAt := 0.0
	if tb.grid && k > 0 {
		// A re-encode seeks accurately to the grid point — which assumes the
		// demuxer lands at or before it, and not every one does (gridSeek).
		// Where this one lands past it, the run seeks earlier and trims.
		early, err := gridSeek(tb.starts[k], func(at float64) (float64, error) {
			return h.seekLanding(ctx, s.item, at)
		})
		switch {
		case err != nil:
			h.log.Debug("hls: where a seek lands could not be read; seeking to the boundary", "path", s.item.Rel, "at", tb.starts[k], "err", err)
		case early < tb.starts[k]:
			seek, trimAt = early, tb.starts[k]
			h.log.Debug("hls: this file's seeks land past where they are asked; seeking earlier and trimming",
				"path", s.item.Rel, "at", tb.starts[k], "seek", early)
		}
	}
	if !tb.grid && k > 0 {
		var err error
		seek, landed, err = h.landing(ctx, s.item, tb.starts[k])
		if err != nil {
			if ctx.Err() == nil {
				r.err = fmt.Errorf("finding where the seek lands: %w", err)
				h.log.Warn("hls: landing probe failed", "path", s.item.Rel, "at", tb.starts[k], "err", err)
			}
			return
		}
		r.fileStart = tb.at(landed)
		r.partial = landed > tb.starts[r.fileStart]+hlsLandTol
	}
	var times []float64
	if !tb.grid {
		for j := r.fileStart + 1; j < r.until; j++ {
			times = append(times, tb.starts[j]-landed-0.0005)
		}
	}
	to := 0.0
	if r.until < tb.n() {
		to = tb.starts[r.until] + hlsRunTail
	}

	for attempt := 1; ; attempt++ {
		outcome := h.attemptTable(ctx, s, r, seek, trimAt, times, to)
		if outcome == attemptAgain && attempt < hlsMaxAttempts && r.state.produced == 0 {
			h.clearRun(s, r)
			r.seen = 0
			continue
		}
		break
	}
}

// attemptTable is one ffmpeg over a run.
func (h *HLS) attemptTable(ctx context.Context, s *hlsSession, r *hlsRun, seek, trimAt float64, times []float64, to float64) attemptOutcome {
	it := s.item
	repaired := aspects.has(it)
	plan, err := planConversion(ctx, h.ffmpeg, it, seek, s.copyVideo, s.audio, s.q, repaired, true, h.log)
	if err != nil {
		r.err = err
		return attemptDone
	}
	defer plan.close()
	if trimAt > 0 {
		plan.trimTo(trimAt, it.ACodec != "")
	}
	r.hardware = plan.hardware
	pattern := fmt.Sprintf("run%d-seg%%05d.ts", r.seq)
	args := append(plan.args, hlsTableArgs(s.dir, r.list, pattern, r.fileStart, times, s.table.grid, to)...)

	cmd := exec.CommandContext(ctx, h.ffmpeg, args...)
	if plan.stdin != nil {
		cmd.Stdin = plan.stdin
	}
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		r.err = err
		return attemptDone
	}
	stop := make(chan struct{})
	followed := make(chan struct{})
	go func() {
		defer close(followed)
		h.follow(ctx, s, r, stop)
	}()
	werr := cmd.Wait()
	close(stop)
	<-followed
	h.readList(s, r) // whatever it closed on the way out
	if werr == nil || ctx.Err() != nil || r.reached {
		// Finished, stopped, or done with what it was asked for: an error on
		// the way out of a run that produced everything is not one — the
		// graphics engine complains at the flush of a run cut short by -to,
		// measured, and writing the hardware off for that would send every
		// later run of the film to the processor.
		return attemptDone
	}
	h.log.Warn("hls conversion ended", "path", it.Rel, "err", werr,
		"ffmpeg", strings.TrimSpace(errBuf.String()))
	if startOverWithAspect(errBuf.String(), repaired, repairable(h.ffmpeg, it)) {
		aspects.note(it)
		h.log.Info("converting again with the declared aspect put right", "path", it.Rel)
		return attemptAgain
	}
	if plan.hardware {
		// A run the graphics engine was carrying is written off for this
		// file: half a conversion is a viewer watching a spinner, and the
		// processor always works. Where nothing was written the run goes
		// again on it at once; otherwise the next request starts one.
		hwRefused.note(it)
		h.log.Info("converting on the processor from now on", "path", it.Rel)
		if r.state.produced == 0 {
			return attemptAgain
		}
	}
	r.err = werr
	return attemptDone
}

// hlsTableArgs is the delivery for a tabled run: transport-stream segments
// keeping the file's own clock, cut where the table says, named per run,
// and listed as each is closed.
func hlsTableArgs(dir, list, pattern string, startNumber int, times []float64, grid bool, to float64) []string {
	args := []string{
		"-f", "segment",
		"-segment_format", "mpegts",
		// The muxer's own clock would otherwise start every segment file
		// near zero; the timestamps have to be the film's, so a run begun
		// anywhere joins the segments around it.
		"-segment_format_options", "mpegts_copyts=1",
		"-segment_start_number", strconv.Itoa(startNumber),
		// The list is written whole to a temporary file and renamed, so a
		// reader never sees half a line.
		"-segment_list", filepath.Join(dir, list),
		"-segment_list_type", "csv",
		"-segment_list_flags", "+live",
		"-segment_list_size", "0",
	}
	if grid {
		// Cumulative from the first packet, which the accurate seek puts on
		// the grid point itself; the delta takes the forced keyframe on the
		// next grid point even when it lands a frame early of the count.
		args = append(args, "-segment_time", strconv.Itoa(hlsSegmentSec), "-segment_time_delta", "0.06")
	} else {
		// Every cut named; past the last named one the muxer cuts nowhere.
		// Named even where there is none to make — a run over the film's
		// last segment — because with no times at all the muxer falls back
		// to its own two-second default and cuts where the table did not.
		parts := make([]string, 0, len(times)+1)
		for _, t := range times {
			parts = append(parts, strconv.FormatFloat(t, 'f', 4, 64))
		}
		if len(parts) == 0 {
			parts = append(parts, "999999999")
		}
		args = append(args, "-segment_times", strings.Join(parts, ","))
	}
	if to > 0 {
		args = append(args, "-to", strconv.FormatFloat(to, 'f', 3, 64))
	}
	return append(args, "-y", filepath.Join(dir, pattern))
}

// follow reads the run's list as it grows.
func (h *HLS) follow(ctx context.Context, s *hlsSession, r *hlsRun, stop chan struct{}) {
	t := time.NewTicker(hlsListPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-t.C:
			h.readList(s, r)
		}
	}
}

// readList takes in the lines the muxer has added since last time: each is
// a segment it has closed, judged against the table and, where it is one
// the run was asked for, recorded as whole. A stub past the run's end is
// ignored; the file the demuxer's landing put before the first whole
// segment is left for the clearing up.
func (h *HLS) readList(s *hlsSession, r *hlsRun) {
	body, err := os.ReadFile(filepath.Join(s.dir, r.list))
	if err != nil {
		return
	}
	cuts := parseSegmentList(body)
	if len(cuts) <= r.seen {
		return
	}
	tb := s.table
	for _, c := range cuts[r.seen:] {
		first := r.seen == 0
		r.seen++
		if c.index >= r.until {
			continue
		}
		if first && r.partial {
			continue
		}
		if err := tb.verify(c, first); err != nil {
			h.log.Warn("hls: a segment was cut where the table did not say; giving the session up",
				"path", s.item.Rel, "err", err)
			s.sm.Lock()
			if s.broken == nil {
				s.broken = err
			}
			s.broadcastLocked()
			s.sm.Unlock()
			r.err = err
			r.cancel()
			return
		}
		name := fmt.Sprintf("run%d-%s", r.seq, hlsSegmentFile(c.index))
		s.sm.Lock()
		if _, ok := s.have[c.index]; !ok {
			s.have[c.index] = name
			s.appendManifest(c.index, name)
		}
		r.state.produced++
		r.state.next = c.index + 1
		if c.index+1 >= r.until {
			r.reached = true
		}
		s.broadcastLocked()
		s.sm.Unlock()
	}
}

// appendManifest records a whole segment for a later run to adopt. Called
// with sm held; the file's own lock keeps two runs' lines apart.
func (s *hlsSession) appendManifest(k int, name string) {
	s.manifestMu.Lock()
	defer s.manifestMu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.dir, hlsManifest), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%d %s\n", k, name)
}

// clearRun removes what a run wrote that is not a whole segment: the stub
// past its end, the file it was in the middle of, the landing's lead-in,
// and its list.
func (h *HLS) clearRun(s *hlsSession, r *hlsRun) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	prefix := fmt.Sprintf("run%d-", r.seq)
	s.sm.Lock()
	kept := map[string]bool{}
	for _, name := range s.have {
		kept[name] = true
	}
	s.sm.Unlock()
	for _, e := range entries {
		name := e.Name()
		if (strings.HasPrefix(name, prefix) && !kept[name]) || name == r.list {
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

// endRun is the clearing up after a run, however it ended, and the word to
// everyone waiting on it.
func (h *HLS) endRun(s *hlsSession, r *hlsRun) {
	h.clearRun(s, r)
	h.mu.Lock()
	s.converting = false
	h.mu.Unlock()
	s.sm.Lock()
	r.state.ended = true
	// Left in place, ended: the next request reads what stopped it, and
	// the next run replaces it.
	s.broadcastLocked()
	s.sm.Unlock()
	close(r.exited)
	h.account()
}

// landing finds where a copy started at a boundary will actually begin: the
// seek to ask for, and the keyframe the demuxer lands on for it.
//
// Asked for a keyframe's own time, ffmpeg lands on the keyframe before it —
// it takes hlsSeekLead off the time first, for the stream's reordering —
// so the boundary plus that lead is tried first, and lands on the boundary
// itself on the ffmpeg measured. Where it does not, a hair past the
// boundary is asked for instead, which lands at or before it on any
// demuxer; the run then begins a keyframe early and the lead-in is thrown
// away. Either way the answer is read back from the one packet ffmpeg is
// asked to copy out, never assumed.
func (h *HLS) landing(ctx context.Context, it library.Item, boundary float64) (seek, landed float64, err error) {
	round := func(v float64) float64 { return math.Round(v*1000) / 1000 }
	seek = round(boundary + hlsSeekLead + 0.001)
	landed, err = h.probe(ctx, it, seek)
	if err == nil && math.Abs(landed-boundary) <= hlsLandTol {
		return seek, boundary, nil
	}
	seek = round(boundary + 0.0005)
	landed, err = h.probe(ctx, it, seek)
	if err != nil {
		return 0, 0, err
	}
	if landed > boundary+hlsLandTol {
		return 0, 0, fmt.Errorf("a seek to %.3f landed past it, at %.3f", seek, landed)
	}
	return seek, landed, nil
}

// gridSeekStep is how far back the first earlier seek is tried, and each
// after it twice as far: a keyframe every ten seconds is the long end of
// ordinary, and a file with them further apart takes a step or two more.
const gridSeekStep = 10.0

// gridSeek finds an input seek that lands at or before a grid point.
//
// A re-encoded run seeks accurately to the point, which trusts the demuxer to
// land on the keyframe at or before it and to decode forward from there.
// Not every one does: a Windows Media file's index sent a seek to 328 s to
// the keyframe at 332.56 — after it — so the run's first frame was 332.56,
// the keyframes it forced four seconds apart from there, and the first
// segment ended at 336.56 where the table said 332. The session was given up
// as the table promised, and the film could not be watched past a seek.
//
// So where a seek to the point lands past it, the seek is tried further back
// — gridSeekStep, then twice that and on, to the film's start — until one
// lands at or before it; the run seeks there and trims to the point
// (conversion.trimTo). A file whose seeks land where they should costs one
// packet read and changes nothing. land says where a seek lands.
func gridSeek(point float64, land func(float64) (float64, error)) (float64, error) {
	landed, err := land(point)
	if err != nil {
		return point, err
	}
	if landed <= point+hlsLandTol {
		return point, nil
	}
	for back := gridSeekStep; ; back *= 2 {
		seek := math.Max(point-back, 0)
		landed, err = land(seek)
		if err != nil {
			return point, err
		}
		if landed <= point+hlsLandTol || seek == 0 {
			return seek, nil
		}
	}
}

// seekLanding asks ffmpeg where an input seek lands: the time of the first
// packet of the picture it copies out after the same seek a conversion
// makes. Read from a framecrc listing, which prints every packet's time
// whatever the codec — the transport stream probe cannot see a WMV picture
// at all, which becomes a stream of private data there, and those are the
// files this is for.
func (h *HLS) seekLanding(ctx context.Context, it library.Item, seek float64) (float64, error) {
	input, _, err := convertInput(it, 0)
	if err != nil {
		return 0, err
	}
	if input.pipe != nil {
		_ = input.pipe.Close()
		return 0, errors.New("content read through a pipe cannot be seeked")
	}
	ctx, cancel := context.WithTimeout(ctx, hlsProbeBudget)
	defer cancel()
	args := append(ffmpegBase(), "-ss", strconv.FormatFloat(seek, 'f', 3, 64), "-copyts")
	args = append(args, input.args...)
	args = append(args, "-map", "0:v:0", "-c", "copy", "-frames:v", "1", "-f", "framecrc", "pipe:1")
	cmd := exec.CommandContext(ctx, h.ffmpeg, args...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if pts, ok := framecrcFirstPTS(out); ok {
		return pts, nil
	}
	if err != nil {
		return 0, err
	}
	return 0, errors.New("the probe read no packet")
}

// framecrcFirstPTS reads the first packet's presentation time, in seconds,
// out of a framecrc listing: "#tb <stream>: <num>/<den>" lines give each
// stream's time base, and then a line a packet, "stream, dts, pts, ...".
func framecrcFirstPTS(out []byte) (float64, bool) {
	tbs := map[string]float64{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "#tb "); ok {
			stream, tb, ok := strings.Cut(rest, ":")
			num, den, ok2 := strings.Cut(strings.TrimSpace(tb), "/")
			n, err1 := strconv.ParseFloat(num, 64)
			d, err2 := strconv.ParseFloat(den, 64)
			if ok && ok2 && err1 == nil && err2 == nil && d != 0 {
				tbs[strings.TrimSpace(stream)] = n / d
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) < 3 {
			continue
		}
		tb, ok := tbs[strings.TrimSpace(f[0])]
		pts, err := strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
		if !ok || err != nil {
			return 0, false
		}
		return pts * tb, true
	}
	return 0, false
}

// probe copies one packet out from a seek and reads its time.
func (h *HLS) probe(ctx context.Context, it library.Item, seek float64) (float64, error) {
	input, _, err := convertInput(it, 0)
	if err != nil {
		return 0, err
	}
	if input.pipe != nil {
		_ = input.pipe.Close()
		return 0, errors.New("content read through a pipe cannot be seeked")
	}
	ctx, cancel := context.WithTimeout(ctx, hlsProbeBudget)
	defer cancel()
	args := append(ffmpegBase(), "-ss", strconv.FormatFloat(seek, 'f', 3, 64), "-copyts")
	args = append(args, input.args...)
	args = append(args, "-map", "0:v:0", "-c:v", "copy", "-frames:v", "1",
		"-f", "mpegts", "-mpegts_copyts", "1", "pipe:1")
	cmd := exec.CommandContext(ctx, h.ffmpeg, args...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return 0, err
	}
	pts, ok := tsFirstVideoPTS(out)
	if !ok {
		return 0, errors.New("the probe produced no video packet")
	}
	return pts, nil
}

// ---- sessions without a table ---------------------------------------------

// run is the conversion itself: the plan both converters share (convert.go)
// delivered as segments — made again where an attempt taught something
// (the aspect repair, the hardware written off), and judged only once the
// last attempt has had its turn.
func (h *HLS) run(ctx context.Context, s *hlsSession, it library.Item, t float64, copyVideo bool, audio string, q quality) {
	for attempt := 1; ; attempt++ {
		switch h.attempt(ctx, s, it, t, copyVideo, audio, q) {
		case attemptAbandoned:
			return
		case attemptAgain:
			// A retry begins by wiping what the last attempt wrote, so it
			// is only ever worth having where that was nothing. An attempt
			// that left a playable prefix behind is served as it stands:
			// throwing it away to try again is how a session ends up in
			// the map with an empty directory — the gate already opened on
			// the segment that has just been deleted, so nothing records a
			// failure and forget never runs, and that key answers for the
			// film at that resume point for the life of the process. This
			// is the one place it has to be true: every retry branch comes
			// through here, whatever taught it something.
			if attempt < hlsMaxAttempts && !hasSegment(s.dir) {
				// The failed attempt's output goes first: ffmpeg will not
				// write over a playlist it finds, and its stale segments
				// would count towards the progress and the budget.
				clearSession(s.dir)
				continue
			}
		}
		break
	}
	h.mu.Lock()
	s.converting = false
	h.mu.Unlock()
	// A run that was *stopped* rather than finished records nothing of its
	// own — attempt's whole error block stands down while the context is
	// done — so the gate used to open with no error on a conversion that had
	// written nothing at all. Every waiter was told the session was ready
	// and handed a playlist nothing had written; worse, with no failure
	// recorded the entry stayed in the map and answered every later ask for
	// that film at that resume point the same way, for the life of the
	// process. Nothing playable is a failure to the caller whatever stopped
	// it, and a session that did produce something is left alone by
	// failIfEmpty as it always was.
	stopped := ctx.Err()
	if stopped == nil {
		stopped = errors.New("the conversion produced nothing")
	}
	s.failIfEmpty(stopped)
	s.finish()
	if s.failure() != nil {
		// The waiters have their error; a fresh ask deserves a fresh
		// attempt rather than this one, cached.
		h.forget(s)
		return
	}
	// What it wrote is now all it will ever write, so this is the moment the
	// budget's figure for it becomes true. Left to the reaper alone it never
	// became true at all for a conversion that finished inside one 30 s
	// tick — reap returns as soon as it sees the session stop converting —
	// and with nothing converting anywhere the figure stood still while the
	// disk went on filling.
	h.account()
}

// hlsMaxAttempts bounds what one session may try. Each retry is bought by a
// verdict that is remembered — the aspect noted, the hardware written off —
// so the loop ends by itself; this is the belt.
const hlsMaxAttempts = 3

type attemptOutcome int

const (
	attemptDone      attemptOutcome = iota // ended, for good or ill: judge the session
	attemptAgain                           // something was learnt that makes another worth it
	attemptAbandoned                       // failed before it ran, and already forgotten
)

// attempt is one ffmpeg over the session: planned, run, and read for what
// stopped it.
func (h *HLS) attempt(ctx context.Context, s *hlsSession, it library.Item, t float64, copyVideo bool, audio string, q quality) attemptOutcome {
	// Read once, and read again nowhere: this run either went through the
	// repair or it did not, and the retry below is about that. Asked of the
	// verdict a second time, the answer could be another goroutine's — a
	// thumbnail of the same film, or a second conversion of it, noting the
	// aspect between the plan and the failure — and the retry that would
	// have worked was refused on the strength of a repair this run never
	// made.
	repaired := aspects.has(it)
	plan, err := planConversion(ctx, h.ffmpeg, it, t, copyVideo, audio, q, repaired, false, h.log)
	if err != nil {
		s.fail(err)
		h.forget(s)
		return attemptAbandoned
	}
	// Let go as this attempt ends rather than when the session does: the
	// next attempt must not run beside this one's repair copy, which would
	// otherwise sit blocked on an unread pipe for the whole of it.
	defer plan.close()
	args := append(plan.args, hlsOutputArgs(s.dir)...)

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
		return attemptAbandoned
	}
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		h.log.Warn("hls conversion ended", "path", it.Rel, "err", err,
			"ffmpeg", strings.TrimSpace(errBuf.String()))
		// What stopped it may be the file's own declaration of what shape
		// its pixels are, which is not about the bytes: the film is
		// converted again through the copy that puts that right (aspect.go),
		// into the same session, whose waiters are still waiting on a first
		// segment that has not been written. Only where the copy can be
		// made — a file it cannot help would be attempted twice identically.
		if startOverWithAspect(errBuf.String(), repaired, repairable(h.ffmpeg, it)) {
			aspects.note(it)
			h.log.Info("converting again with the declared aspect put right", "path", it.Rel)
			return attemptAgain
		}
		// A run the graphics engine was carrying is written off for this
		// file: half a conversion is a viewer watching a spinner, and the
		// processor always works. Where nothing playable was written the
		// session runs again on it, and the waiters go on waiting.
		if plan.hardware {
			hwRefused.note(it)
			h.log.Info("converting on the processor from now on", "path", it.Rel)
			if !hasSegment(s.dir) {
				return attemptAgain
			}
		}
		// Only a failure that produced nothing is a failure to the caller;
		// one that stopped part way leaves a playable prefix behind.
		s.failIfEmpty(err)
	}
	return attemptDone
}

// hlsOutputArgs is the delivery for a session without a table: segments of
// hlsSegmentSec, every one of them kept and listed, into the session's
// directory.
func hlsOutputArgs(dir string) []string {
	return []string{
		"-f", "hls",
		"-hls_time", strconv.Itoa(hlsSegmentSec),
		// Every segment stays listed and on disk: this is a file being
		// converted, not a broadcast, so a player is entitled to go back to
		// what it has already been given.
		"-hls_list_size", "0",
		"-hls_flags", "independent_segments",
		"-hls_playlist_type", "event",
		"-hls_segment_filename", filepath.Join(dir, "seg%05d.ts"),
		// Over whatever is there. With no terminal to ask at, ffmpeg answers
		// an existing playlist by refusing — which is exactly what a second
		// attempt into the same directory finds.
		"-y", filepath.Join(dir, "index.m3u8"),
	}
}

// clearSession empties a session's directory of everything but the key
// file that says what it is a conversion of, for an attempt that is about
// to be made again.
func clearSession(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == hlsKeyFile {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
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
			if hasSegment(s.dir) {
				s.finish()
				return
			}
		}
	}
}

// hasSegment says the conversion has written something a player can start
// on: a segment the playlist names that is actually on disk — the name alone
// is not enough, or the player asks for a file that is still being written.
//
// It is a question about the artefact, and that is the point. Whether the
// gate has been opened is a question about a watcher that ticks every 150 ms,
// which is not the same thing: a run that died just after its first segment
// was listed was judged empty by a gate that had not caught up yet, its error
// recorded and its directory removed, where the same run 150 ms later served
// the prefix it had written.
func hasSegment(dir string) bool {
	if dir == "" {
		// A session with no directory has written nothing. Joining nothing
		// onto the playlist's name asks about ./index.m3u8, which is
		// whatever happens to sit in the process's working directory — an
		// answer about a different file entirely.
		return false
	}
	body, err := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, ".ts") {
			continue
		}
		_, err := os.Stat(filepath.Join(dir, line))
		return err == nil
	}
	return false
}

// fail ends the session with an error, for waiters who have not been
// answered yet. One that already has something playable keeps it: the
// error then describes what stopped the rest, which they can still watch.
func (s *hlsSession) fail(err error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.done {
		return
	}
	s.err = err
	s.done = true
	close(s.ready)
}

// playable says the gate has been opened — that the waiters have been let
// through. What is on disk is hasSegment's question, not this one.
func (s *hlsSession) playable() bool {
	select {
	case <-s.ready:
		return true
	default:
		return false
	}
}

// failIfEmpty records an error only when nothing playable was produced.
//
// What settles that is the disk, not the gate. The gate is opened by a
// watcher on a 150 ms tick, so a conversion that wrote a segment and then
// died a few milliseconds later was judged by which of the two got there
// first: the error was recorded, the waiters were sent away with a 503 and
// forget removed a directory holding a playable prefix — where the same run,
// one tick later, was served. The read is done before the lock is taken, and
// it is safe there because ffmpeg has already exited by the time this is
// asked: what is on disk cannot grow under the answer.
//
// The gate is still consulted, for the case it was always for: a waiter woken
// by the first segment must not be sent away from a conversion that is
// playing, so an error arriving after the gate has opened is not recorded.
func (s *hlsSession) failIfEmpty(err error) {
	if hasSegment(s.dir) {
		return
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	// Decided once, as fail and finish are. The attempt that died records
	// what ffmpeg actually said; run then asks again at the end of the loop
	// with a sentence of its own, for the runs that recorded nothing at all,
	// and the second must not paint over the first. "exit status 1" with the
	// converter's own complaint behind it is worth reading; "the conversion
	// produced nothing" is only worth having where there is nothing better.
	if s.done || s.err != nil {
		return
	}
	s.err = err
}

// finish opens the gate, once, whoever gets there first: the watcher on the
// first segment, or the conversion ending.
func (s *hlsSession) finish() {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if !s.done {
		s.done = true
		close(s.ready)
	}
}

// failure is what the session ended in, if anything, read the way it was
// written.
func (s *hlsSession) failure() error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.err
}
