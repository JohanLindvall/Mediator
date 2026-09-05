package server

// Rewrapping, for files whose container is the only thing the browser cannot
// open.
//
// An FLV — or an MKV — holding H.264 video and AAC audio holds exactly what
// every browser decodes. Nothing in it needs converting; it needs moving into
// a container the browser will open. Re-encoding it, which is what the live
// conversion does, is slow, lossy and beside the point: measured, `-c copy`
// rewrote a 337 MiB FLV in 1.5 s where libx264 would have re-encoded all
// 38 000 frames of it.
//
// The same copy answers a second fault, in files that are already in the
// right container: HEVC labelled "hev1" rather than "hvc1". Apple's decoders
// accept only the latter and ffmpeg writes the former unless told otherwise,
// so an iPhone that would decode the picture in hardware refuses to start,
// and the player — seeing nothing decoded — used to fall back to re-encoding
// every frame of a 1080p film to fix four bytes.
//
// The other half of the reason is delivery, and it is why this writes a file
// instead of streaming one. iOS Safari probes a media URL with
// `Range: bytes=0-1` and will not play a resource that answers 200 with no
// ranges — which is the only thing the live conversion can answer, being a
// pipe of unknown length. A rewrapped file goes out through
// http.ServeContent: ranges, a length, real seeking, and no clock for the
// player to correct, because the timestamps are the file's own.
//
// What is written here is scratch, not state. It lives in a temp directory
// that goes with the process: it is reproducible in seconds, and the database
// is meant to be the one thing worth keeping — or worth deleting.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// ErrNoRemux means this item is not one rewrapping can help: its streams need
// real conversion, or there is no ffmpeg to do the rewrapping with. The
// player treats it as "use the converter instead".
var ErrNoRemux = errors.New("nothing to gain from rewrapping")

const (
	// remuxKeepFor protects a file from pruning after it was last asked for.
	// A player works through a film in range requests minutes apart, so
	// "nothing is reading it right now" is not the same as "nobody is
	// watching it" — and deleting the file under a viewer is worse than
	// being over budget, which is a number an operator chose and can change.
	remuxKeepFor = 5 * time.Minute
	// remuxTimeout bounds one rewrap. `-c copy` runs at disk speed
	// (measured 337 MiB in 1.5 s), so this is only here to stop a wedged
	// read holding a slot forever.
	remuxTimeout = 15 * time.Minute
)

// remuxVideo and remuxAudio are the codecs that play everywhere once they are
// inside an MP4. Deliberately short: HEVC plays in Safari and not in Chrome,
// so moving it into an MP4 to escape a container one browser cannot open
// would only trade that for a codec another cannot decode.
//
// An empty audio codec is accepted because the video gate above it has
// already forced a successful probe — after one, empty means the file has no
// soundtrack, where before one it would only have meant "not looked at yet".
var (
	remuxVideo = map[string]bool{"h264": true}
	remuxAudio = map[string]bool{"aac": true, "mp3": true, "": true}
)

// nativeContainers are the ones a browser opens by itself, where moving the
// streams into an MP4 would hand back the same file.
var nativeContainers = map[string]bool{".mp4": true, ".m4v": true, ".mov": true}

// remuxKind is which of the two copies a rewrap is, and it is part of the key
// and of the file name for the same reason the soundtrack is: a film whose
// sound has been converted is a different file from the same film merely
// rewrapped, and serving one under the other's name would hand a viewer a
// soundtrack they cannot hear with nothing on screen to say why.
//
// The plain copy is named by the empty string, so that names written before
// there was a second kind still parse and are still served. Those films were
// converted once already and there is nothing to be gained by doing it again.
type remuxKind string

const (
	remuxCopy  remuxKind = ""    // both streams copied through, into an MP4
	remuxSound remuxKind = "aac" // picture copied, soundtrack converted
	// remuxTrack is both streams copied through with the container left as it
	// was, for the one job the MP4 copy cannot do: picking a soundtrack out of
	// a file whose streams belong to no MP4. A video that ships automatic dubs
	// arrives as Matroska carrying VP9 and one Opus track per language, and
	// the only way to say which language is wanted — to a television, and to
	// any browser but Safari — is to hand over a file holding just that one.
	// Copying it into an MP4 would be a container neither the set nor the
	// codecs asked for; copying it into its own is lossless and costs a read.
	remuxTrack remuxKind = "trk"
)

// trackContainer is the container a soundtrack copy of this file keeps, as an
// extension and as the muxer that writes it. Matroska only, deliberately: an
// MP4 that needed one soundtrack taken out of it is already `remuxable`, and
// everything else is a container a browser will not open and a set is unlikely
// to list, where a copy would not be the thing that was wrong.
func trackContainer(it library.Item) (ext, format string, ok bool) {
	switch strings.ToLower(filepath.Ext(it.Name)) {
	case ".webm":
		return ".webm", "webm", true
	case ".mkv":
		return ".mkv", "matroska", true
	}
	return "", "", false
}

// trackCopyable reports whether one soundtrack can be copied out of this file
// into a container of its own kind.
func trackCopyable(it library.Item) bool {
	if it.Kind != library.KindVideo || len(it.Tracks) < 2 {
		return false
	}
	_, _, ok := trackContainer(it)
	return ok
}

// mp4Video is what an MP4 can carry, which is the only question the sound fix
// has to ask about the picture. Whether the browser decodes it was settled
// before the request was made — a soundtrack conversion is asked for
// precisely because the picture was fine — so this list is about the
// container, and deliberately not about any browser.
var mp4Video = map[string]bool{
	"h264": true, "hevc": true, "av1": true, "vp9": true, "mpeg4": true,
}

// soundFixable reports whether the picture can be copied into an MP4 while
// the soundtrack is converted beside it.
//
// This is the fault the live conversion serves worst, and it is the common
// one: a picture every browser decodes with a soundtrack none of them does —
// AC3 or E-AC3 in an MKV, which is most of a television release. The pipe
// cannot answer a range request, so a browser managing its own buffer
// reconnects with `Range: bytes=N-` and is answered from byte zero every
// time. Measured on a 4K release over one viewing: 963 MB crossed the link
// to move the stream 167 MB, and the waste is not a constant — every
// reconnect re-reads from the beginning, so it grows with the playback
// position and is worst at the end of a film. A file has none of it: real
// ranges, a length, seeking the browser does itself, and it is still there
// the next time the film is opened.
func soundFixable(it library.Item) bool {
	if it.Kind != library.KindVideo || !mp4Video[it.VCodec] {
		return false
	}
	// A soundtrack the browser already decodes needs no conversion. Where the
	// container is nonetheless wrong, the plain copy above is the answer, and
	// re-encoding sound that was fine would be slower and worse.
	return it.ACodec != "" && !remuxAudio[it.ACodec]
}

// remuxable reports whether a copy can make this item playable.
//
// Two different faults answer to one: a container the browser will not open
// around streams it decodes perfectly well, and — inside a container it opens
// quite happily — a picture labelled in a way it refuses. Both are fixed by
// copying the streams into a new MP4, which is why they are one endpoint;
// what differs is only what ffmpeg is told on the way.
func remuxable(it library.Item) bool {
	if it.Kind != library.KindVideo {
		return false
	}
	if nativeContainers[strings.ToLower(filepath.Ext(it.Name))] {
		// Already the container every browser opens, so the container is not
		// what is wrong. The one thing left that a copy can fix is the label.
		return misTaggedHEVC(it)
	}
	return remuxVideo[it.VCodec] && remuxAudio[it.ACodec]
}

// misTaggedHEVC reports an HEVC picture written under the four-character code
// that Apple's decoders refuse.
//
// HEVC in an MP4 is labelled either "hvc1" or "hev1" — the difference is
// only whether the parameter sets travel in the sample description or in the
// stream — and Safari, which is to say every browser on an iPhone, plays
// hvc1 and will not touch hev1. ffmpeg writes hev1 unless it is told
// otherwise, so files an iPhone decodes in hardware arrive looking like
// video that needs re-encoding when all they need is four different bytes.
// Answering this costs a handful of reads (`library.VideoSampleFormat`); the
// re-encode it saves costs every frame of the film.
func misTaggedHEVC(it library.Item) bool {
	return library.VideoSampleFormat(it) == "hev1"
}

// Remuxer produces and caches rewrapped copies.
type Remuxer struct {
	ffmpeg  string
	scratch *Scratch
	log     *slog.Logger

	mu      sync.Mutex
	entries map[string]*remuxEntry
	total   int64
	seq     int64
	closed  bool
}

// remuxEntry is one cached file, or one being written. Followers wait on done
// rather than starting a second ffmpeg for the same key.
type remuxEntry struct {
	path string
	want int64 // the source's length: a copy comes out about the same size
	size int64
	used int64 // for eviction: the sequence number of the last ask
	// held is how many responses are reading it, and last is when one of
	// them arrived. Either makes it in use, and in use is never pruned.
	held int
	last time.Time
	done chan struct{}
	err  error
	// cancel stops the ffmpeg writing this file. Close calls it: a rewrap
	// runs on its own context so a hung-up requester does not kill it, and
	// without this a shutdown left it writing into scratch until its
	// timeout.
	cancel context.CancelFunc
}

// inUse reports whether pruning must leave this file alone.
func (e *remuxEntry) inUse(now time.Time) bool {
	return e.held > 0 || now.Sub(e.last) < remuxKeepFor
}

// NewRemuxer builds the rewrapper. Without ffmpeg on PATH it exists and
// declines everything, which is what the optional-ffmpeg contract asks for.
func NewRemuxer(ffmpeg string, scratch *Scratch, log *slog.Logger) *Remuxer {
	return &Remuxer{ffmpeg: ffmpeg, scratch: scratch, log: log, entries: map[string]*remuxEntry{}}
}

// Close stops accepting work. What has been converted is left where it is:
// it is keyed by the source's identity, so the next run finds it and does not
// convert the same film again. Only the budget removes files.
func (r *Remuxer) Close() error {
	r.mu.Lock()
	r.closed = true
	for _, e := range r.entries {
		if e.cancel != nil {
			e.cancel() // a copy still being written stops; its .part is removed by produce
		}
	}
	r.entries = map[string]*remuxEntry{}
	r.total = 0
	r.mu.Unlock()
	return nil
}

// remuxName is the file a rewrap of this item is written to.
//
// The key is *in* the name rather than beside it, which is what lets a later
// run recognise what it finds: the same three things that key the cache in
// memory — the item, and the size and time that say the file on disk is still
// the file that was converted.
func remuxName(it library.Item, track int, kind remuxKind) string {
	name := fmt.Sprintf("%s-%d-%d-a%d", it.ID, it.ModTime, it.Size, track)
	if kind != remuxCopy {
		name += "-" + string(kind)
	}
	return name + remuxExt(it, kind)
}

// remuxExt is what the file is called, which is now a question: a soundtrack
// copy keeps the container it came from, and a name that lied about it would
// be a file nothing downstream could serve honestly.
func remuxExt(it library.Item, kind remuxKind) string {
	if kind == remuxTrack {
		if ext, _, ok := trackContainer(it); ok {
			return ext
		}
	}
	return ".mp4"
}

// remuxKeyFromName reads the key back out of a file name, or reports that the
// name is not one of ours.
func remuxKeyFromName(name string) (string, bool) {
	// The extension is no longer always .mp4 — a soundtrack copy keeps the
	// container it came from — so what is cut is whatever the name ends with.
	// A part-written file survives this and is rejected below, its remaining
	// ".mp4" or ".webm" leaving a track number that is not a number.
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return "", false
	}
	base := name[:dot]
	// Which of the copies this is, where the name says so at all. The plain
	// one carries no marker, so a name from before there was a second kind
	// reads as what it is rather than being deleted and made again.
	kind := remuxCopy
	for _, k := range []remuxKind{remuxSound, remuxTrack} {
		if rest, marked := strings.CutSuffix(base, "-"+string(k)); marked {
			base, kind = rest, k
			break
		}
	}
	// <id>-<mtime>-<size>-a<track>: taken from the right, since only these
	// are known to be numbers and an id could hold anything. A name from
	// before the soundtrack was part of it does not parse here and is
	// deleted rather than adopted, which is right: it holds whichever
	// soundtrack came first, and nothing records which that was.
	base, track, ok := cutLast(base, "-a")
	if !ok {
		return "", false
	}
	base, size, ok := cutLast(base, "-")
	if !ok {
		return "", false
	}
	id, mtime, ok := cutLast(base, "-")
	if !ok || id == "" {
		return "", false
	}
	for _, n := range []string{mtime, size, track} {
		if _, err := strconv.ParseInt(n, 10, 64); err != nil {
			return "", false
		}
	}
	return fmt.Sprintf("%s|%s|%s|%s|%s", id, mtime, size, track, kind), true
}

// cutLast splits at the last occurrence of sep.
func cutLast(s, sep string) (before, after string, ok bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// Adopt takes over whatever a previous run left behind.
//
// Anything that is not a finished rewrap of something goes: a name that does
// not carry a key, and the part-written file a run interrupted mid-rewrap —
// which is the one that matters, because adopting a truncated file would
// serve a film that stops in the middle and look exactly like the bug that
// made all this necessary.
func (r *Remuxer) Adopt() {
	dir, err := r.scratch.Sub("remux")
	if err != nil {
		r.log.Warn("scratch: no place to keep rewraps", "err", err)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	adopted, total := 0, int64(0)
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		key, ok := remuxKeyFromName(e.Name())
		if e.IsDir() || !ok {
			// Includes the ".part" of an interrupted rewrap, which has no
			// business being served.
			_ = os.RemoveAll(path)
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.Size() == 0 {
			_ = os.Remove(path)
			continue
		}
		done := make(chan struct{})
		close(done)
		r.mu.Lock()
		r.seq++
		r.entries[key] = &remuxEntry{
			path: path, size: fi.Size(), used: r.seq,
			// Last wanted when it was last written, which is in the past —
			// so it is prunable straight away rather than protected by a
			// window it did not earn.
			last: fi.ModTime(), done: done,
		}
		r.total += fi.Size()
		total = r.total
		r.mu.Unlock()
		adopted++
	}
	if adopted > 0 {
		r.scratch.Report("remux", total)
		r.log.Info("kept rewraps from an earlier run", "files", adopted, "bytes", total)
	}
	r.pruneNow()
}

// pruneNow frees whatever the converters are over between them.
func (r *Remuxer) pruneNow() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(r.scratch.Excess())
}

// File returns the path of a rewrapped copy of the item, producing it if this
// is the first ask.
//
// The rewrap itself deliberately does NOT run on the caller's context. Safari
// opens with a one-byte range probe and is free to hang up on it the moment
// it has what it wants; tying the work to that request would cancel the
// rewrap every time it was started and the next request would begin again
// from nothing. The caller's context is honoured for the *wait* — a client
// that leaves is not made to hold on — while the work carries on for whoever
// asks next.
// File returns the rewrapped file for an item, producing it if necessary.
//
// audio names which soundtrack to keep, in the file's own numbering. It is
// part of the key rather than a detail of the run: a film with four
// languages rewrapped for one of them is a different file from the same film
// rewrapped for another, and serving the second under the first's name would
// hand a viewer the wrong language with no way to tell.
func (r *Remuxer) File(ctx context.Context, it library.Item, audio string, kind remuxKind) (string, error) {
	// A source larger than the whole budget would evict everything else to
	// hold itself and still not fit, so it is left to the segmented
	// converter, which stores only what it has produced so far.
	if limit := r.scratch.Limit(); limit > 0 && it.Size > limit {
		return "", ErrNoRemux
	}
	if r.ffmpeg == "" {
		return "", ErrNoRemux
	}
	// Two different faults, two different tests, one machine to produce and
	// cache the answer to either.
	switch kind {
	case remuxSound:
		if !soundFixable(it) {
			return "", ErrNoRemux
		}
	case remuxTrack:
		if !trackCopyable(it) {
			return "", ErrNoRemux
		}
	default:
		if !remuxable(it) {
			return "", ErrNoRemux
		}
	}
	// Keyed like every other derived artefact here: a file that changed
	// under us is a different file, not a stale copy of the same one.
	track := audioTrack(audio)
	key := fmt.Sprintf("%s|%d|%d|%d|%s", it.ID, it.ModTime, it.Size, track, kind)

	// An idempotent mkdir, so a Remuxer that was never told to adopt an
	// earlier run's files still has somewhere to write this one.
	dirs, err := r.scratch.Sub("remux")
	if err != nil {
		return "", err
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return "", ErrNoRemux
	}
	if e, ok := r.entries[key]; ok {
		r.seq++
		e.used = r.seq
		e.last = time.Now()
		r.mu.Unlock()
		return r.wait(ctx, e)
	}
	// Make room before writing rather than after: the point of a budget is
	// that it is not exceeded, and a rewrap is the size of the film. The
	// lock is already held here, which is why this is the locked form.
	if limit := r.scratch.Limit(); limit > 0 {
		r.pruneLocked(r.total + it.Size - limit)
	}
	r.seq++
	e := &remuxEntry{
		path: filepath.Join(dirs, remuxName(it, track, kind)), want: it.Size,
		used: r.seq, last: time.Now(), done: make(chan struct{}),
	}
	r.entries[key] = e
	r.mu.Unlock()

	go r.produce(it, track, kind, key, e)
	return r.wait(ctx, e)
}

// Progress reports how far along a rewrap of this item is, and whether one is
// running at all.
//
// Measured from the file as it grows rather than from anything ffmpeg says:
// a copy comes out about the length of its source, which is close enough for
// something whose whole job is to stop a wait looking like a hang.
func (r *Remuxer) Progress(id string) (float64, bool) {
	// The item's most recently asked-for rewrap — a soundtrack change makes
	// a second — is the one the readout is about; the first found in a map
	// is whichever.
	r.mu.Lock()
	var e *remuxEntry
	for key, cand := range r.entries {
		if strings.HasPrefix(key, id+"|") && (e == nil || cand.used > e.used) {
			e = cand
		}
	}
	r.mu.Unlock()
	if e == nil {
		return 0, false
	}
	select {
	case <-e.done:
		return 1, false // finished; nothing left to wait for
	default:
	}
	if e.want <= 0 {
		return 0, true
	}
	fi, err := os.Stat(e.path)
	if err != nil {
		return 0, true
	}
	return min(float64(fi.Size())/float64(e.want), 0.99), true
}

// wait blocks for the entry, or for the caller to give up on it.
func (r *Remuxer) wait(ctx context.Context, e *remuxEntry) (string, error) {
	select {
	case <-e.done:
		if e.err != nil {
			return "", e.err
		}
		return e.path, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// produce runs the rewrap and publishes the entry.
func (r *Remuxer) produce(it library.Item, track int, kind remuxKind, key string, e *remuxEntry) {
	// Written beside the real name and moved into place at the end, so a run
	// that is interrupted leaves something obviously unfinished rather than a
	// truncated film under a name the next run would trust.
	part := e.path + ".part"
	ctx, cancel := context.WithTimeout(context.Background(), remuxTimeout)
	defer cancel()
	r.mu.Lock()
	e.cancel = cancel
	r.mu.Unlock()
	e.err = r.run(ctx, it, track, kind, part)
	if e.err == nil {
		e.err = os.Rename(part, e.path)
	}
	if e.err != nil {
		_ = os.Remove(part)
	}
	if e.err == nil {
		if fi, err := os.Stat(e.path); err == nil {
			e.size = fi.Size()
		}
	}
	// The books are settled before anybody is told the file is there. A
	// caller released by done and asking for the next film at once must find
	// this one counted, and whatever this one pushed over the budget must
	// already have gone — announced first, the older file was still on disk
	// a moment after the newer one was ready. This entry cannot prune
	// itself: only finished files are candidates, and it is not finished
	// until done closes below.
	r.mu.Lock()
	failed := e.err != nil || r.closed
	if failed {
		// A failure is not remembered: the player has already moved on to
		// the converter, and a later ask deserves a fresh attempt rather
		// than a cached refusal.
		delete(r.entries, key)
	} else {
		r.total += e.size
		r.scratch.Report("remux", r.total)
		// The written file is rarely exactly the size predicted, so the
		// budget is checked again against what it really came to.
		r.pruneLocked(r.scratch.Excess())
	}
	r.mu.Unlock()
	close(e.done)
	if failed {
		os.Remove(e.path)
	}
}

// Hold marks a file as being read, so pruning leaves it alone until the
// response is done with it. The returned func releases the claim.
func (r *Remuxer) Hold(path string) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.path != path {
			continue
		}
		e.held++
		e.last = time.Now()
		return func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if e.held > 0 {
				e.held--
			}
			e.last = time.Now()
		}
	}
	return func() {}
}

// Pruning frees the least recently wanted first and never touches one that
// is in use — a file deleted under a viewer is worse than being over a number
// the operator chose and can change.
// pruneLocked frees at least `need` bytes if it can. Called with the lock.
func (r *Remuxer) pruneLocked(need int64) {
	if need <= 0 {
		return
	}
	now := time.Now()
	type aged struct {
		key  string
		used int64
	}
	order := make([]aged, 0, len(r.entries))
	for k, e := range r.entries {
		select {
		case <-e.done: // only finished files are candidates
			if !e.inUse(now) {
				order = append(order, aged{k, e.used})
			}
		default:
		}
	}
	slices.SortFunc(order, func(a, b aged) int { return cmp.Compare(a.used, b.used) })
	for _, a := range order {
		if need <= 0 {
			break
		}
		e := r.entries[a.key]
		delete(r.entries, a.key)
		r.total -= e.size
		need -= e.size
		if err := os.Remove(e.path); err != nil && !os.IsNotExist(err) {
			r.log.Debug("scratch: could not remove", "path", e.path, "err", err)
		}
	}
	r.scratch.Report("remux", r.total)
	if need > 0 {
		// Everything left is being watched. Being over is the lesser of the
		// two wrongs, and it is worth saying so once.
		r.log.Info("scratch budget exceeded; everything held is in use",
			"over", need, "held", len(r.entries))
	}
}

// run is the rewrap: streams copied verbatim into an MP4 with its index at
// the front, so playback can start on the first bytes rather than after the
// whole file has been fetched to find an index at the end.
func (r *Remuxer) run(ctx context.Context, it library.Item, track int, kind remuxKind, out string) error {
	// A rewrap reads from the first byte to the last and never looks back,
	// so it asks for no seek and the pipe fallback costs it nothing.
	input, _, err := convertInput(it, 0)
	if err != nil {
		return err
	}
	var stdin io.Reader
	if input.pipe != nil {
		defer input.pipe.Close()
		stdin = input.pipe
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}
	args = append(args, input.args...)

	// One soundtrack, and it is the one asked for. A film shipping four
	// languages hands a television all four and lets it choose, which is how
	// a Swedish release comes out in Danish; DLNA has no way to say which,
	// so the choice is made by what is in the file it is given.
	args = append(args, "-map", "0:v:0", "-map", audioMapN(track), "-sn", "-dn", "-c:v", "copy")
	if kind == remuxSound {
		// The picture goes through untouched — it was never the problem —
		// and only the soundtrack is made into something the browser has a
		// decoder for. The settings are the live conversion's, so the same
		// film sounds the same whichever route it took, save for the coder:
		// "fast" is what this is waited on for, and measured over a
		// television episode it took the encode from 61 s to 37 s. That
		// difference is the whole wait, the copy itself being 2 s.
		args = append(args, "-c:a", "aac", "-aac_coder", "fast", "-b:a", "160k", "-ac", "2")
	} else {
		args = append(args, "-c:a", "copy")
	}
	// HEVC in an MP4 is labelled hvc1 or hev1, and Apple's decoders take only
	// the first while ffmpeg writes the second unless told otherwise. For a
	// file already in an MP4 that mislabelling is the whole fault being
	// fixed; for a picture being copied out of some other container it would
	// be a fault newly introduced, which is why the codec is asked as well.
	if misTaggedHEVC(it) || it.VCodec == "hevc" {
		args = append(args, "-tag:v", "hvc1")
	}
	if kind == remuxTrack {
		// Its own container, so there is no faststart to ask for and nothing
		// to relabel: what comes out is what went in, one soundtrack lighter.
		_, format, _ := trackContainer(it)
		args = append(args, "-f", format, out)
	} else {
		args = append(args, "-movflags", "+faststart", "-f", "mp4", out)
	}
	cmd := exec.CommandContext(ctx, r.ffmpeg, args...)
	cmd.Stdin = stdin
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	// The reader may still be working through the archive when ffmpeg is
	// done with it; it must not be able to hold Wait open behind that.
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("remux %s: %w: %s", it.Rel, err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// convertInput works out how ffmpeg should be given an item, and how a seek
// into it should be done — one decision, because only one of the three
// inputs can be seeked by position.
//
// A plain file is opened by path. Content inside another file (a rar member,
// a DVD title) is read over this server's own stream URL, which answers
// Range and is the only seekable view of it there is. The pipe is what is
// left when there is no loopback address, and it cannot seek at all: reaching
// a seek then means reading the whole file up to it.
//
// byPosition says the seek has already been done, by starting the input at a
// byte offset — which is how a DVD is seeked, since its timestamps cannot be
// (see library.SeekByte). The caller must then not add -ss, and the stream's
// clock simply starts at zero where it was asked to.
func convertInput(it library.Item, t float64) (in convertSource, byPosition bool, err error) {
	switch url := library.LoopbackURL(it); {
	case !it.Archived():
		in.args = []string{"-i", it.Path}
	case url != "":
		if off, ok := library.SeekByte(it, t); ok {
			// -seekable 0 is half the seek and not an optimisation: given a
			// seekable input the demuxer rewinds to the start of the stream
			// before reading it, and the offset is silently undone —
			// measured, all three seeks into a film returned its first frame.
			// Told the input cannot be seeked, ffmpeg reads forward from
			// where it was put, which is all a conversion ever does.
			in.args = append(in.args, "-seekable", "0", "-offset", strconv.FormatInt(off, 10))
			byPosition = true
		}
		in.args = append(in.args, "-headers", library.LoopbackWholeHeaderArg(), "-i", url)
	default:
		f, err := library.OpenItem(it)
		if err != nil {
			return in, false, err
		}
		in.pipe = f
		in.args = []string{"-i", "pipe:0"}
	}
	return in, byPosition, nil
}

// convertSource is an ffmpeg input: the arguments that name it, and the
// reader to hand it on stdin where that is the route. The caller closes it.
type convertSource struct {
	args []string
	pipe io.ReadCloser
}
