package library

import (
	"context"
	"sync"
	"time"

	"github.com/dhowden/tag"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// tagMeta is what background enrichment extracts from an audio file's tags.
type tagMeta struct {
	title, artist, album, genre string
	track, year                 int
}

// SetMetaDB attaches the blob database that persists the index and the
// enrichment results across restarts. May be nil (everything is re-read).
// Change tracking starts here rather than in PersistLoop, so deletions
// noticed by an early scan are not missed.
func (l *Library) SetMetaDB(db *blob.DB) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.metaDB = db
	if db != nil && l.dirty == nil {
		l.dirty = make(map[string]struct{})
		l.removed = make(map[string]struct{})
	}
}

// EnrichMeta fills in background metadata with bounded concurrency: tags and
// duration for audio items, duration for videos. Results are served from the
// blob database when the file is unchanged, so after the first run this pass
// does almost no file I/O. busy (may be nil) reports whether higher-priority
// work — media playback, thumbnail generation — is running; enrichment
// pauses while it returns true.
func (l *Library) EnrichMeta(ctx context.Context, busy func() bool) {
	if busy == nil {
		busy = func() bool { return false }
	}
	l.mu.RLock()
	var todo []string
	for id, it := range l.items {
		if needsEnrich(it) {
			todo = append(todo, id)
		}
	}
	l.mu.RUnlock()
	if len(todo) == 0 {
		return
	}
	if l.enrichAll(ctx, todo, busy) {
		l.log.Info("media metadata loaded", "items", len(todo))
	}
	l.notify()
}

// enrichWorkers is how many files are read at once, by either pass.
const enrichWorkers = 4

// enrichAll reads the given items with a few workers and reports whether it
// got to the end of them. busy, when not nil, is asked before every item and
// waited on while it says higher-priority work is running; progress is
// published every thousand items so a long pass reaches the clients as it
// goes. Both passes go through here — the background sweep and the one a
// request asks for — and differ only in whether they yield.
func (l *Library) enrichAll(ctx context.Context, todo []string, busy func() bool) bool {
	l.enriching.Add(1)
	defer l.enriching.Add(-1)
	jobs := make(chan string)
	var wg sync.WaitGroup
	for range enrichWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				for busy != nil && busy() {
					select {
					case <-ctx.Done():
						return
					case <-time.After(500 * time.Millisecond):
					}
				}
				l.enrichOne(ctx, id)
			}
		}()
	}
	fed := 0
feed:
	for _, id := range todo {
		select {
		case <-ctx.Done():
			break feed
		case jobs <- id:
		}
		if fed++; fed%1000 == 0 {
			l.notify() // stream progress to clients during long enrichments
		}
	}
	close(jobs)
	wg.Wait()
	return fed == len(todo)
}

// needsEnrich reports whether an item still has to be looked at. The flag is
// set once the file has been read (or found in the cache), even when it
// turned out to have nothing to offer — otherwise untagged files would be
// re-read on every pass and on every album that contains them.
func needsEnrich(it *Item) bool {
	// Images are here for their size alone, which is read from the header
	// rather than by decoding the picture — a few hundred bytes, against the
	// several megabytes a photograph is.
	switch it.Kind {
	case KindAudio:
		return !it.enriched
	case KindVideo, KindImage:
		// Or where the shape has not been looked for. A library examined for
		// tags before there was anywhere to keep a picture's size comes back
		// from a restart looking finished, and would never be read again.
		return !it.enriched || it.shape < shapeVersion
	}
	return false
}

// maxPriorityEnrich bounds the work one request may trigger, so an album
// with a pathological number of tracks cannot monopolise the disk.
const maxPriorityEnrich = 200

// EnrichSoon starts a priority pass in the background and returns at once.
// Use it for what the browser is displaying — the page of the grid it just
// fetched — where waiting would slow the response down for no benefit: the
// results reach the UI over the change stream anyway.
//
// Only one background pass runs at a time. Dropping the request when one is
// already in flight is safe: items are enriched at most once, so the next
// fetch (or the background sweep) covers whatever this one skipped.
func (l *Library) EnrichSoon(ids []string) {
	select {
	case l.prioGate <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-l.prioGate }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		l.EnrichNow(ctx, ids)
	}()
}

// EnrichNow reads metadata for the given items immediately, ahead of the
// background pass and without waiting for playback or thumbnailing to go
// quiet: these are items the user is looking at right now. It returns
// whether anything was learned, and gives up when ctx expires — whatever is
// left over is picked up by the background pass as usual.
func (l *Library) EnrichNow(ctx context.Context, ids []string) bool {
	l.mu.RLock()
	var todo []string
	for _, id := range ids {
		if it, ok := l.items[id]; ok && needsEnrich(it) {
			todo = append(todo, id)
			if len(todo) == maxPriorityEnrich {
				break
			}
		}
	}
	l.mu.RUnlock()
	if len(todo) == 0 {
		return false
	}
	l.enrichAll(ctx, todo, nil) // the user is waiting: no yielding to anything
	l.notify()
	return true
}

// enrichAfterQuiet enriches one item once its file has stopped changing.
//
// The watcher calls this for every Create and Write it sees, and a file
// being written — a torrent landing, a copy in progress — emits a stream of
// Write events: one goroutine reading tags per event put an unbounded number
// of readers on a file that was about to change again anyway. Each event
// resets the timer instead, so the read happens once, shortly after the
// writer goes quiet — and it still publishes, which is the watcher paths'
// contract (the trailing notify below).
func (l *Library) enrichAfterQuiet(id string) {
	const quiet = 2 * time.Second
	l.enrichDebMu.Lock()
	defer l.enrichDebMu.Unlock()
	if t, ok := l.enrichDeb[id]; ok {
		t.Reset(quiet)
		return
	}
	if l.enrichDeb == nil {
		l.enrichDeb = make(map[string]*time.Timer)
	}
	l.enrichDeb[id] = time.AfterFunc(quiet, func() {
		l.enrichDebMu.Lock()
		delete(l.enrichDeb, id)
		l.enrichDebMu.Unlock()
		l.enrichOne(context.Background(), id)
		l.notify()
	})
}

// enrichOne fills in tags (audio) and duration (audio + video) for one item,
// consulting the metadata cache before touching the file. ctx is the caller's
// and bounds the probe: EnrichNow runs inside a request that is waiting.
func (l *Library) enrichOne(ctx context.Context, id string) {
	it, ok := l.Get(id)
	if !ok || (it.Kind != KindAudio && it.Kind != KindVideo && it.Kind != KindImage) {
		return
	}
	// Mark the item examined whatever the outcome, so a file with nothing to
	// read is not re-read by every later pass — but only if we actually got
	// to look. The probe now takes the caller's context, and a request that
	// gave up (the priority wait is 1.5 s) or a shutdown mid-sweep must not
	// leave "examined, nothing there" behind: that verdict is persisted under
	// a key that never changes for a stable file, so it would be permanent.
	interrupted := false
	defer func() {
		if !interrupted {
			l.markEnriched(id)
		}
	}()

	if db := l.metaDB; db != nil {
		if m, ok := db.GetMeta(it.ID, it.ModTime, it.Size); ok && !archiveProbeMissed(it, m) {
			l.setMeta(id, tagMeta{
				title: m.Title, artist: m.Artist, album: m.Album,
				genre: m.Genre, track: m.Track, year: m.Year,
			}, m.Duration)
			// A record written before there was anywhere to keep the shape
			// knows nothing about it, and to begin with the whole library is
			// such records. Reading it is a header apiece — cheap enough to
			// do once, and written down so that it is only ever once.
			if m.Shape < shapeVersion {
				// Only where it answered. A record may already carry a shape
				// that came from a probe, and the native reader says nothing
				// about the containers it does not know — overwriting with
				// that silence would throw away what was already known.
				if w, h, codec, fps := shapeOf(it, m.VCodec); w > 0 {
					m.Width, m.Height, m.FPS, m.VCodec = w, h, fps, codec
				} else if m.VCodec == "" {
					m.VCodec = codec
				}
				if m.ACodec == "" {
					m.ACodec = soundtrackOf(it)
				}
				// A container with no native reader here — Matroska, AVI, the
				// transport streams — keeps its shape where only ffprobe can
				// reach it. That is a process per file, which is why nothing
				// here does it by the page; but it is a process per file
				// *once*, against a record that will be read for the life of
				// the library, and the alternative is a listing that knows
				// how big some of its films are.
				if it.Kind == KindVideo && m.Width == 0 {
					if p := ProbeMedia(ctx, it); p.Width > 0 {
						m.Width, m.Height, m.FPS = p.Width, p.Height, p.FPS
						if m.VCodec == "" {
							m.VCodec = p.VCodec
						}
						if m.ACodec == "" {
							m.ACodec = p.ACodec
						}
					}
				}
				// A probe the context killed says nothing about the file, and
				// writing the reading down would make that silence permanent:
				// the key never changes for a stable file.
				if interrupted = ctx.Err() != nil; !interrupted {
					m.Shape = shapeVersion
					l.queueMeta(it.ID, m)
				}
			}
			l.setProbe(id, Probe{
				VCodec: m.VCodec, ACodec: m.ACodec,
				Width: m.Width, Height: m.Height, FPS: m.FPS,
			})
			return
		}
	}

	var tm tagMeta
	if it.Kind == KindAudio {
		tm = readTags(it)
	}
	if it.Kind == KindImage {
		// A still has no probe, no tags and no playing time. Its size is the
		// whole of what there is to learn, and it is in the header.
		w, h, _, _ := shapeOf(it, "")
		l.setProbe(id, Probe{Width: w, Height: h, Probed: true})
		l.queueMeta(it.ID, blob.Meta{
			MTime: it.ModTime, Size: it.Size, Width: w, Height: h, Shape: shapeVersion,
		})
		return
	}
	p := ProbeMedia(ctx, it)
	// A film whose duration a native parser read gets no ffprobe, and so has
	// no codec or shape from one either — which is most of a library. For the
	// ISO base media containers both are a handful of reads into the box tree
	// (mp4box.go), so they cost nothing worth avoiding and are read here.
	if it.Kind == KindVideo && p.Width == 0 {
		w, h, codec, rate := shapeOf(it, p.VCodec)
		p.Width, p.Height, p.VCodec = w, h, codec
		if p.FPS == 0 {
			p.FPS = rate
		}
		if p.ACodec == "" {
			p.ACodec = soundtrackOf(it)
		}
	}
	// Whatever the probe managed to read is still worth having in memory, but
	// a probe the context killed says nothing about the file. Note this is
	// narrower than `!p.Probed`, which is also false when no ffprobe was
	// needed at all — a native parser answered — and those results must still
	// be written down.
	interrupted = ctx.Err() != nil
	l.setMeta(id, tm, p.DurationMs)
	l.setProbe(id, p)
	if interrupted {
		return
	}
	if archiveProbeMissed(it, blob.Meta{Duration: p.DurationMs, VCodec: p.VCodec, ACodec: p.ACodec}) {
		// Nothing learned about archived video: do not write that down. The
		// item is still marked enriched, so this process does not read it
		// again, but the next start gets to try. Archived items are
		// deliberately not persisted as index records either, so nothing is
		// left half-remembered.
		return
	}
	// Queued even when empty: that is the record of having looked. The
	// pending buffer is flushed in bulk by the persist loop.
	l.queueMeta(it.ID, blob.Meta{
		MTime: it.ModTime, Size: it.Size,
		Duration: p.DurationMs, VCodec: p.VCodec, ACodec: p.ACodec,
		Width: p.Width, Height: p.Height, FPS: p.FPS, Shape: shapeVersion,
		Title: tm.title, Artist: tm.artist, Album: tm.album,
		Genre: tm.genre, Track: tm.track, Year: tm.year,
	})
}

// archiveProbeMissed reports a "nothing found" verdict on archived video.
// Nothing found can as easily mean nobody could look: the loopback address
// may not have been up yet, or the piped fallback's prefix may have stopped
// short of the end of the container header. Once such a verdict is cached it
// outlives every later improvement to the probe, since the cache key
// (id, mtime, size) never changes for a member of a completed volume set.
// For a plain file, which ffprobe reads whole by path, an empty result is an
// answer, so this is deliberately narrow.
func archiveProbeMissed(it Item, m blob.Meta) bool {
	return it.Archived() && it.Kind == KindVideo &&
		m.Duration == 0 && m.VCodec == "" && m.ACodec == ""
}

// EnsureCodecs fills in a video's codec names if they are still unknown —
// called when the item is opened for playback, which is the first moment
// they matter. Eager enrichment deliberately skips this probe for files
// whose duration the native parsers handled (one process spawn per video
// across a whole library, for a value almost never consulted).
//
// It is also the second chance for a video the eager pass learned nothing
// about: a missing duration is reason enough to run, so this is where an
// archived member nobody could read earlier gets its duration as well as its
// codecs.
//
// It runs at most once per item per process, and the flag that says so is
// "ffprobe answered", not "a probe produced a duration" — plenty of
// containers hand over codecs and no duration ever, and asking for one that
// cannot exist made this re-probe on every single request. Nor is it "we
// tried": a run that never happened is not a verdict, and marking it as one
// would spend this item's one second chance on nothing.
//
// ctx is the caller's, and it is honoured: this runs inside the request that
// is waiting for it. Concurrency is bounded by probeSem, so a burst of opens
// cannot put an unbounded number of ffprobes on the disk playback is using.
// What it deliberately does not do is wait for playback to go quiet the way
// the background sweep does — this probe is what tells the player whether the
// browser can decode what it is already playing.
func (l *Library) EnsureCodecs(ctx context.Context, id string) {
	it, ok := l.Get(id)
	// Probed is the whole test now. The codecs alone used to be enough to
	// skip this, but the soundtracks come from the same call and are not
	// written down anywhere — a film restored from the cache with its codecs
	// known would otherwise never offer the choice it carries. One ffprobe
	// per film per run, at the moment it is opened, which is what this has
	// always cost for a film whose codecs were unknown.
	if !ok || it.Kind != KindVideo || it.probed {
		return
	}
	if ctx.Err() != nil {
		return // the caller is already gone; spawn nothing
	}
	select {
	case l.probeSem <- struct{}{}:
		defer func() { <-l.probeSem }()
	case <-ctx.Done():
		return
	}
	// Re-check: the item may have been probed while this waited for a slot.
	if it, ok = l.Get(id); !ok || it.probed {
		return
	}
	out := probeItem(ctx, it)
	if ctx.Err() != nil {
		return // interrupted, not answered: leave it to be tried again
	}
	// An empty answer is still an answer, and recording it is the point: the
	// alternative is spawning ffprobe again on the next request forever. But
	// only an answer counts — no ffprobe on PATH, an unreadable member or a
	// killed run leave Probed false, so the second chance above survives.
	// The duration comes out of the same call, and dropping it would leave
	// the item unsortable and the player without a timeline for no saving.
	l.setProbe(id, Probe{
		VCodec: out.vcodec, ACodec: out.acodec, DurationMs: sane(out.durationMs),
		// The soundtracks come out of this same call, which is what makes
		// the player's menu free: this probe runs because a video is being
		// opened, which is the moment the choice is wanted.
		Tracks: out.tracks,
		// And the embedded subtitles: the captions a television release
		// carries are muxed into the file, not laid beside it, and this is
		// the only probe that ever sees them.
		Subs: out.subs,
		// So does the picture's shape and rate, and this is the probe that
		// matters for them: it is the one that runs when a film is opened,
		// which is the moment something has to decide how to convert it.
		// Dropping them here left that decision with nothing to go on.
		Width: out.width, Height: out.height, FPS: out.fps,
		Probed: out.answered,
	})
	if out.vcodec == "" && out.acodec == "" && out.durationMs == 0 {
		return // nothing to write down, and nothing changed on the item
	}
	if fresh, ok := l.Get(id); ok {
		// The whole record, the shape included. This write replaces whatever
		// the eager pass queued under the same id, and one that left the
		// picture's size and its reading marker out put every film that had
		// ever been opened back to "shape unread" — a header to re-read, or
		// for a Matroska file a probe to re-run, on every restart.
		l.queueMeta(id, blob.Meta{
			MTime: fresh.ModTime, Size: fresh.Size, Duration: fresh.Duration,
			VCodec: fresh.VCodec, ACodec: fresh.ACodec,
			Width: fresh.Width, Height: fresh.Height, FPS: fresh.FPS, Shape: fresh.shape,
			Title: fresh.Title, Artist: fresh.Artist, Album: fresh.Album,
			Genre: fresh.Genre, Track: fresh.Track, Year: fresh.Year,
		})
	}
}

// queueMeta buffers one enrichment result for the next bulk write. A no-op
// without a database.
func (l *Library) queueMeta(id string, m blob.Meta) {
	if l.metaDB == nil {
		return
	}
	l.metaPendMu.Lock()
	if l.metaPending == nil {
		l.metaPending = make(map[string]blob.Meta)
	}
	l.metaPending[id] = m
	l.metaPendMu.Unlock()
}

// withTags opens an item and hands its tags to fn. tag.ReadFrom can panic on
// corrupt files, so every read of it goes through this one recover; a file
// that cannot be opened or read simply never reaches fn.
func withTags(it Item, fn func(tag.Metadata)) {
	defer func() { _ = recover() }()
	f, err := OpenItem(it)
	if err != nil {
		return
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return
	}
	fn(m)
}

// readTags reads an audio file's tags.
func readTags(it Item) (tm tagMeta) {
	withTags(it, func(m tag.Metadata) {
		tm.title, tm.artist, tm.album, tm.genre = m.Title(), m.Artist(), m.Album(), m.Genre()
		tm.track, _ = m.Track()
		tm.year = m.Year()
	})
	return tm
}

// ReadPicture extracts embedded cover art (data, mime) from an audio item.
func ReadPicture(it Item) (data []byte, mime string) {
	withTags(it, func(m tag.Metadata) {
		if p := m.Picture(); p != nil && len(p.Data) > 0 {
			data, mime = p.Data, p.MIMEType
		}
	})
	return data, mime
}
