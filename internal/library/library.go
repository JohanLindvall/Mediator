// Package library maintains an in-memory index of media files found under a
// set of root directories. It supports fast filtered/sorted listing, live
// updates via fsnotify, and derives music albums from mp3 directories and
// m3u playlists.
package library

import (
	"cmp"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"hash/fnv"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// Kind classifies an indexed file.
type Kind string

const (
	KindVideo    Kind = "video"
	KindImage    Kind = "image"
	KindAudio    Kind = "audio"
	KindPlaylist Kind = "playlist"
)

// AllKinds enumerates every Kind value; cmd/gen-ts derives the TS union from it.
var AllKinds = []Kind{KindVideo, KindImage, KindAudio, KindPlaylist}

// KindSet is a set of kinds, small enough to be compared as a value — which
// is what lets it sit in a query's cache key beside the search and the sort.
// The zero value is every kind, so a query that says nothing about kinds
// asks for all of them and nothing has to remember to fill it in.
type KindSet uint8

// KindsOf builds a set. No arguments is the same as the zero value: all.
func KindsOf(kinds ...Kind) KindSet {
	var s KindSet
	for _, k := range kinds {
		s |= kindBit(k)
	}
	return s
}

// Has reports whether the set admits this kind.
func (s KindSet) Has(k Kind) bool { return s == 0 || s&kindBit(k) != 0 }

func kindBit(k Kind) KindSet {
	for i, v := range AllKinds {
		if v == k {
			return 1 << i
		}
	}
	return 0
}

// AudioTrack is one of the soundtracks a video carries: which stream it is
// (as ffmpeg counts them, so `0:a:<Index>` selects it), and enough about it
// to be named in a menu.
type AudioTrack struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec,omitempty"`
	Lang     string `json:"lang,omitempty"`
	Title    string `json:"title,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Default  bool   `json:"default,omitempty"`
	// Comment marks a commentary track, which is never what anyone means by
	// "the sound" and is picked only when it is asked for by name.
	Comment bool `json:"comment,omitempty"`
}

// Item is one indexed media file.
type Item struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Rel     string `json:"path"` // display path: <root base>/<relative path>
	Kind    Kind   `json:"kind"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"` // unix milliseconds
	// FirstSeen is when the index first saw the path — the library's own
	// notion of "added", which mtime is not: a file copied in keeps the
	// timestamp it had wherever it came from.
	FirstSeen int64 `json:"added,omitempty"` // unix milliseconds

	// Filled in by background enrichment.
	Duration int64  `json:"duration,omitempty"` // playing time in milliseconds (audio/video)
	Title    string `json:"title,omitempty"`
	Artist   string `json:"artist,omitempty"`
	Album    string `json:"album,omitempty"`
	Genre    string `json:"genre,omitempty"`
	Track    int    `json:"track,omitempty"`
	Year     int    `json:"year,omitempty"`
	// Tracks is every soundtrack the file carries, once something has
	// probed it — which is when a video is opened, since that is when the
	// player needs to offer the choice. Empty for anything with one.
	Tracks []AudioTrack `json:"tracks,omitempty"`
	// EmbSubs is the text subtitle streams inside the file, from the same
	// probe. Not serialized: the client sees them through the subtitle
	// listing, merged after the sidecars into one numbering.
	EmbSubs []SubTrack `json:"-"`
	// Codecs of the first video/audio stream, when known (ffprobe). The
	// player uses them to decide whether the browser can play the file.
	VCodec string `json:"vcodec,omitempty"`
	ACodec string `json:"acodec,omitempty"`
	// Reencode says the picture cannot be played by copying it, and so not
	// as it is either: the stream reorders its frames further than it
	// declares, which a browser that trusts the declaration shows as a
	// stutter (server/reorder.go). The player then has it converted rather
	// than handing the file to the element. Stamped by the server on the
	// copy /api/item hands out, once the file has been looked at; never on
	// the indexed item and never written down.
	Reencode bool `json:"reencode,omitempty"`
	// The picture's shape and rate, from the same probe. Kept because how
	// much work a conversion is depends on them and on nothing else: pixels
	// per second is what decides whether this machine's processor can keep
	// ahead of playback or whether the graphics hardware has to (hwaccel.go).
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	FPS    float64 `json:"fps,omitempty"`

	// The owner's judgement, stamped on by List and Get. Held beside the
	// index (see annotate.go), not on the indexed item, because a flag
	// outlives the file it is about.
	// Rotation is the correction this file needs, in quarter turns, as the
	// owner last left it, and NoCrop says to leave its black borders alone.
	// Stamped on from the flags like the two below.
	// Series, Season and EpisodeNo are read out of the path when a video is
	// indexed (series.go). Nothing tags an episode, so the names are all
	// there is, and they are parsed once here rather than on every listing.
	Series    string `json:"series,omitempty"`
	Season    int    `json:"season,omitempty"`
	EpisodeNo int    `json:"episode,omitempty"`
	// Plays is how many times this has been started, stamped into the copy
	// a listing hands out rather than held on the indexed item: the count is
	// the owner's, and an item is rebuilt by every walk that sees it.
	Plays int `json:"plays,omitempty"`
	// Like is the owner's verdict, stamped the same way: 1 liked, -1 disliked.
	Like int `json:"like,omitempty"`
	// Affinity is how much this sounds like what the owner liked (positive)
	// or disliked (negative), graded -2..2, and Akin the title of the track
	// it was measured against — see similar.go. Spoken says it reads as a
	// voice rather than music (spoken.go).
	Affinity  int    `json:"affinity,omitempty"`
	Akin      string `json:"akin,omitempty"`
	Spoken    bool   `json:"spoken,omitempty"`
	Rotation  int    `json:"rotation,omitempty"`
	NoCrop    bool   `json:"nocrop,omitempty"`
	Hidden    bool   `json:"hidden,omitempty"`
	Favourite bool   `json:"favourite,omitempty"`

	Path     string       `json:"-"` // absolute path on disk (virtual for archived items)
	stored   *storedEntry // non-nil: content lives inside some other file
	lower    string       // tokenized name+path+metadata for search
	ino      fileKey      // identity of the underlying file, for deduplication
	symlink  bool         // reached through a symbolic link
	enriched bool         // tags and duration have been looked for
	shape    int          // which reading of the picture's shape this is (shape.go)
	probed   bool         // ffprobe has run over it (see Probe.Probed)
}

// Archived reports whether the item's content is bytes inside another file
// rather than a file of its own — a member of a rar set, or a title inside a
// disc image. Everything that follows from that is the same for both: there
// is no path for ffmpeg to open, the offsets are re-derived by each scan
// rather than persisted, and reads go through OpenItem.
func (it Item) Archived() bool { return it.stored != nil }

// Counts holds per-kind totals for the whole library.
type Counts struct {
	Video    int `json:"video"`
	Image    int `json:"image"`
	Audio    int `json:"audio"`
	Playlist int `json:"playlist"`
	Albums   int `json:"albums"`
	// Audiobooks is the releases that are somebody reading (spoken.go); they
	// are kept out of Albums, so the two chips add up to every release.
	Audiobooks int `json:"audiobooks"`
	Artists    int `json:"artists"`
	Genres     int `json:"genres"`
	// Series counts the shows the episode names add up to — television, not
	// films, and only what a path actually says is one.
	Series int `json:"series"`
	// Started and Watched count what has been played: begun and not
	// finished, and finished. Videos, in practice, since nothing else keeps
	// a position.
	Started int `json:"started"`
	Watched int `json:"watched"`
	// Played counts what has been started at all, ever, or judged — the one
	// number the popularity view is a listing of.
	Played int `json:"played"`
	Total  int `json:"total"`
}

// Query selects, orders and pages items.
type Query struct {
	Kind Kind // empty = all kinds
	// Kinds is what the listing may draw from at all, as opposed to Kind,
	// which is the one the viewer asked for. It is how a server that serves
	// only music to some of its callers keeps videos out of every answer
	// without the library having to index any differently.
	Kinds KindSet
	// Watch narrows to how far things have been got through: "started" for
	// begun and unfinished, "done" for watched to the end, empty for all.
	Watch string
	// Played keeps only what has been played at all, or judged, which is
	// what the popularity listing is: sorting by plays alone would bury the
	// handful that matter under the whole library's untouched majority.
	Played bool
	// Series and Season narrow to one show, and to one season of it. Season
	// is read only when a series is named: a bare "season 2" across a whole
	// library is not a question anybody asks.
	Series string
	Season int
	// Paths restricts the caller to what lives under certain directories
	// (paths.go). A value type, so it sits in the listing cache's key —
	// without that, one caller's restricted answer would be served to the
	// next caller who asked the same question unrestricted, which is the
	// same trap `Kinds` is a value type for.
	Paths  PathFilter
	Search string // case-insensitive substring on name/path
	Sort   string // "name" | "mtime" | "size" | "added" | "duration" | "popular" ("plays" is its old name) | "random"
	Seed   int64  // "random" only: picks which shuffle
	Desc   bool
	// ShowHidden is "" or "exclude" (the default), "include" or "only".
	ShowHidden     string
	FavouritesOnly bool
	Offset         int
	Limit          int
}

// Result is a page of items plus totals.
type Result struct {
	Items   []Item `json:"items"`
	Total   int    `json:"total"`
	Version int64  `json:"version"`
	Counts  Counts `json:"counts"`
	// Matching is what the chips show while a search is on: the same totals
	// counted over what the search matches. Absent when nothing is being
	// searched for, where it would only repeat Counts.
	Matching *Counts `json:"matching,omitempty"`
}

// Event is broadcast to subscribers when the library changes.
type Event struct {
	Version int64  `json:"version"`
	Counts  Counts `json:"counts"`
}

// Library is the concurrent-safe media index.
type Library struct {
	// Guarded by rootsMu, which is never taken while mu is held the other
	// way round: mu is taken first where both are needed.
	rootsMu  sync.RWMutex
	roots    []string
	excludes []string // glob patterns whose matches stay out of the index

	// scanMu serializes Scan: the initial walk, the periodic rescan and a
	// change of directories can otherwise overlap, and each walk clears
	// byInode and reconciles against its own idea of what it saw — two at
	// once mis-detect duplicates and drop each other's files.
	scanMu sync.Mutex

	mu        sync.RWMutex
	items     map[string]*Item    // by ID
	byPath    map[string]*Item    // by absolute path
	byInode   map[fileKey]string  // indexed path per underlying file
	subsByDir map[string][]string // external subtitle files, by directory

	// flags is the owner's judgement per item ID (annotate.go). Guarded by
	// mu like the index, and a superset of it: a flag survives its file.
	flags       map[string]Flags
	flagsLoaded bool

	// kindCounts is maintained incrementally by upsert/dropItem so that
	// Counts — attached to every listing response — never has to walk the
	// index. The album, artist, genre and show totals ride on their caches
	// below, fed by the builds.
	kindCounts Counts

	// Totals of the hidden items that Counts subtracts, memoized per
	// version (hiddenCounts).
	hiddenMu      sync.Mutex
	hiddenTotals  Counts
	hiddenVersion int64
	hiddenValid   bool

	// lastQuery caches the filtered, sorted result of the most recent
	// listing query. The grid pages through one query at a time, so with
	// this a page request costs a copy of 200 items instead of a fresh
	// sort of the whole library.
	queryMu   sync.Mutex
	lastQuery *queryResult

	// counts caches what the chips show for one search (see counts.go).
	counts countsCache

	// members is what each container — a rar set, a disc — held when it was
	// last indexed, by virtual path: what its next indexing reconciles
	// against, rather than the whole of byPath (indexStored).
	members map[string]map[string]struct{}

	// How far things have been watched (see watched.go).
	watchFields

	// How often things have been played, and what the owner thought of
	// them (plays.go, likes.go, ownerCounts). The Popular chip's total is
	// cached per version beside them.
	plays, likes  ownerCounts
	playedMu      sync.Mutex
	playedValid   bool
	playedVersion int64
	playedCount   int

	// How the music sounds (analyze.go, similar.go).
	featMu        sync.RWMutex
	features      map[string]featureRec
	featuresGen   int64
	scaledCache   *scaled
	affinityCache *affinity
	soundsCache   *sounds
	// enriching counts the tag passes running, which the analysis yields to.
	enriching atomic.Int32
	// spokenAlbums is how many of the built releases are audiobooks, set by
	// the build the album total comes from; byRelease (under featMu) is that
	// build's verdict for every track it grouped, which outranks the track's
	// own — see spokenOf.
	spokenAlbums atomic.Int32
	byRelease    map[string]bool

	version int64
	changed chan struct{}

	subMu sync.Mutex
	subs  map[chan Event]struct{}

	// The grouped views, each rebuilt when the version moves and served as
	// it is until then (cache.go). A list nobody asks to build is a chip
	// that stays at nought, which is why RefreshCounts names all four.
	albums  perVersion[Album]
	artists perVersion[Artist]
	genres  perVersion[Genre]
	series  perVersion[Series]

	// streams counts media responses currently being written. Background
	// work (thumbnail generation, tag enrichment) throttles itself while
	// playback is active so it never competes with it for disk and CPU.
	streams atomic.Int64

	metaDB   *blob.DB      // optional persistent cache for enrichment results
	prioGate chan struct{} // one background priority pass at a time
	// probeSem bounds the ffprobe runs started from a request (EnsureCodecs).
	// They serve playback, so they are not paused for it — but a page full of
	// opens must not put one process per item on the disk either.
	probeSem chan struct{}

	// Pending index writes, non-nil only while PersistLoop runs.
	dirty   map[string]struct{}
	removed map[string]struct{}

	// Enrichment results waiting for the next bulk write. Buffered because
	// a synchronous db write per track (commit + fsync each) throttles a
	// cold enrichment pass to a few hundred items a second.
	metaPendMu  sync.Mutex
	metaPending map[string]blob.Meta

	// One pending enrichment per watcher-touched file (enrichAfterQuiet).
	// A file being written emits a stream of Write events, and spawning a
	// tag read for each put an unbounded number of goroutines on a file
	// that was about to change again anyway.
	enrichDebMu sync.Mutex
	enrichDeb   map[string]*time.Timer

	// said is what has already been reported once, for the things a rescan
	// would otherwise repeat every ten minutes (see once).
	saidMu sync.Mutex
	said   map[string]struct{}

	log *slog.Logger
}

// maxSaid bounds the memory of what has been said. Past it everything is
// forgotten and may be said again, which is the cheaper way to be wrong.
const maxSaid = 100_000

// once reports whether this is the first time key has been seen, and
// remembers it. It is for the things a rescan finds afresh every ten
// minutes and would otherwise report every ten minutes: a member of a set
// that cannot be served, a set that cannot be parsed. Measured before it
// existed, one set of three hundred compressed pictures put three hundred
// lines in the log per rescan, for the life of the process. The key is the
// caller's, so a change in what there is to say — a member that was
// incomplete and is now compressed — is a new thing to say.
func (l *Library) once(key string) bool {
	l.saidMu.Lock()
	defer l.saidMu.Unlock()
	if l.said == nil || len(l.said) >= maxSaid {
		l.said = make(map[string]struct{})
	}
	if _, done := l.said[key]; done {
		return false
	}
	l.said[key] = struct{}{}
	return true
}

// StartStream records one active media stream; call the returned func when
// the response is done.
func (l *Library) StartStream() func() {
	l.streams.Add(1)
	return func() { l.streams.Add(-1) }
}

// Streaming reports whether any media stream is being served right now.
func (l *Library) Streaming() bool { return l.streams.Load() > 0 }

// New creates a library for the given root directories (absolute paths).
func New(roots []string, log *slog.Logger) *Library {
	return &Library{
		roots:     roots,
		items:     make(map[string]*Item),
		byPath:    make(map[string]*Item),
		byInode:   make(map[fileKey]string),
		subsByDir: make(map[string][]string),
		flags:     make(map[string]Flags),
		changed:   make(chan struct{}, 1),
		prioGate:  make(chan struct{}, 1),
		probeSem:  make(chan struct{}, 2),
		subs:      make(map[chan Event]struct{}),
		log:       log,
	}
}

// Roots returns the configured root directories.
func (l *Library) Roots() []string {
	l.rootsMu.RLock()
	defer l.rootsMu.RUnlock()
	return append([]string(nil), l.roots...)
}

// rootsNow returns the live roots slice for read-only use on hot paths —
// the walk asks about every file, and a copy per question adds up. Safe to
// iterate without the lock because SetRoots replaces the slice wholesale
// rather than mutating it.
func (l *Library) rootsNow() []string {
	l.rootsMu.RLock()
	defer l.rootsMu.RUnlock()
	return l.roots
}

// SetRoots replaces the directories the library indexes.
//
// It only records them: what makes the change take effect is the scan that
// follows, which walks the new set and — because a path outside every root
// is no longer indexable — drops what belonged to a directory that has gone.
// The caller runs that scan, because it also has to move the filesystem
// watches, and doing half of it here would leave the two disagreeing.
func (l *Library) SetRoots(roots []string) {
	clean := make([]string, 0, len(roots))
	seen := map[string]struct{}{}
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if _, dup := seen[abs]; dup {
			continue
		}
		seen[abs] = struct{}{}
		clean = append(clean, abs)
	}
	l.rootsMu.Lock()
	l.roots = clean
	l.rootsMu.Unlock()
}

// SetExcludes installs the glob patterns that keep paths out of the index
// (see excluded). Call it before the first scan: the patterns are read
// without synchronisation from every indexing path.
func (l *Library) SetExcludes(patterns []string) { l.excludes = patterns }

// PathID returns the stable ID for an absolute path.
func PathID(path string) string {
	h := sha1.Sum([]byte(path))
	return hex.EncodeToString(h[:])[:16]
}

// Get returns a copy of the item with the given ID.
func (l *Library) Get(id string) (Item, bool) {
	l.ensureFlags()
	l.mu.RLock()
	defer l.mu.RUnlock()
	it, ok := l.items[id]
	if !ok {
		return Item{}, false
	}
	return l.withFlags(*it), true
}

// rel computes the display path for an absolute path: the base name of the
// containing root joined with the path relative to that root.
func (l *Library) rel(path string) string {
	l.rootsMu.RLock()
	defer l.rootsMu.RUnlock()
	for _, root := range l.roots {
		if strings.HasPrefix(path, root+string(filepath.Separator)) || path == root {
			r, err := filepath.Rel(root, path)
			if err != nil {
				break
			}
			return filepath.ToSlash(filepath.Join(filepath.Base(root), r))
		}
	}
	return filepath.ToSlash(path)
}

// upsert adds or updates a file in the index. changed reports whether the
// index was modified; dup reports that another path already represents this
// exact file (a hard link or a symlink), in which case nothing is indexed.
func (l *Library) upsert(path string, kind Kind, size int64, modTime time.Time, key fileKey, symlink bool) (changed, dup bool) {
	id := PathID(path)
	// Decoded for showing and for searching; the path itself, and the id
	// hashed from it, stay the bytes the filesystem gave us (see name.go).
	name := displayText(filepath.Base(path))
	rel := displayText(l.rel(path))
	mt := modTime.UnixMilli()

	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.claim(key, path, symlink) {
		return false, true
	}
	if it, ok := l.items[id]; ok {
		if it.ino != key { // the path now resolves to a different file
			l.releaseInode(it)
			it.ino, it.symlink = key, symlink
		}
		// Claimed on every pass, not only when the identity changed. Scan
		// clears byInode before each walk, so a file that is simply still
		// there would otherwise leave no claim behind — and the hard link
		// beside it, reached later in the same walk, would find the inode
		// unheld and be indexed as a second copy of the same bytes. That is
		// invisible on a cold start, where every path takes the creation
		// path below, and appears on every warm one after it.
		if key.valid() {
			l.byInode[key] = path
		}
		// The id is the hash of the path, so a stored path that differs from
		// the one just walked is a record that came back from somewhere
		// lossy: the mirrored index is JSON, and a JSON string cannot carry
		// a byte that is not valid UTF-8, so a name written in Latin-1
		// returns with U+FFFD where its letters were. Left alone, that
		// record names a file that cannot be opened — and reconciliation
		// drops the item outright, because nothing ever walked the path it
		// claims. Repair it in place: same id, same file, right spelling.
		repaired := false
		if it.Path != path {
			delete(l.byPath, it.Path)
			it.Path, it.Name, it.Rel = path, name, rel
			it.lower = itemSearchText(it)
			l.byPath[path] = it
			l.markDirty(id)
			repaired = true
		}
		// A file whose kind changed — a nameless one that now reads as
		// something else, or a change to what counts as media — moves
		// between the totals, which are what the chips read.
		if it.Kind != kind {
			l.countKind(it.Kind, -1)
			l.countKind(kind, 1)
			it.Kind = kind
			l.markDirty(id)
			repaired = true
		}
		if it.Size == size && it.ModTime == mt {
			return repaired, false
		}
		it.Size = size
		it.ModTime = mt
		it.forgetContent()
		l.markDirty(id)
		return true, false
	}
	// Only here, on creation: an existing item that changed on disk is still
	// the same addition to the library.
	it := &Item{
		ID: id, Name: name, Rel: rel, Kind: kind,
		Size: size, ModTime: mt, FirstSeen: time.Now().UnixMilli(), Path: path,
		lower: searchText(name, displayText(path)), ino: key, symlink: symlink,
	}
	setEpisode(it)
	l.items[id] = it
	l.byPath[path] = it
	if key.valid() {
		l.byInode[key] = path
	}
	l.countKind(kind, 1)
	l.markDirty(id)
	return true, false
}

// forgetContent drops everything that was read from the file's bytes, the
// bytes having changed under the item: the duration, the codecs, the
// picture's shape, the soundtrack and caption lists, and the marks that say
// they were looked for. A replaced file would otherwise go on offering the
// old file's languages and the old file's size. Both upserts call this, so a
// member replaced inside a volume set forgets exactly what a replaced plain
// file does. Caller must hold l.mu.
func (it *Item) forgetContent() {
	it.Duration, it.VCodec, it.ACodec = 0, "", ""
	it.Width, it.Height, it.FPS = 0, 0, 0
	it.Tracks, it.EmbSubs = nil, nil
	it.enriched, it.probed, it.shape = false, false, 0
}

// countKind adjusts the incremental per-kind totals. Caller must hold l.mu.
func (l *Library) countKind(kind Kind, delta int) { addKind(&l.kindCounts, kind, delta) }

// addKind applies a per-kind delta to one set of totals.
func addKind(c *Counts, kind Kind, delta int) {
	switch kind {
	case KindVideo:
		c.Video += delta
	case KindImage:
		c.Image += delta
	case KindAudio:
		c.Audio += delta
	case KindPlaylist:
		c.Playlist += delta
	}
	c.Total += delta
}

// upsertStored indexes one piece of content that lives inside something
// else: a stored member of a rar volume set, or a title on a DVD. The virtual
// path (container path + NUL + member name) keys the entry in byPath so
// reconciliation and subtree removal work like for plain files.
func (l *Library) upsertStored(container string, e *storedEntry, modTime time.Time) bool {
	path := container + "\x00" + e.name
	id := PathID(path)
	name := displayText(filepath.Base(filepath.FromSlash(e.name)))
	rel := displayText(l.rel(container)) + "/" + name
	mt := modTime.UnixMilli()

	l.mu.Lock()
	defer l.mu.Unlock()
	if it, ok := l.items[id]; ok {
		it.stored = e // refresh segment offsets even when metadata is unchanged
		changed := it.Size != e.size || it.ModTime != mt
		if changed {
			it.Size = e.size
			it.ModTime = mt
			it.forgetContent()
		}
		// What the container says the content is worth outranks anything
		// read from it (see declaresDuration), so it is put back after the
		// rest is forgotten, and refreshed even when nothing else changed.
		if e.durationMs > 0 {
			it.Duration = e.durationMs
		}
		return changed
	}
	it := &Item{
		ID: id, Name: name, Rel: rel, Kind: Classify(name),
		Size: e.size, ModTime: mt, FirstSeen: time.Now().UnixMilli(),
		// A DVD says how long its title is and nothing else can (see
		// ifoDuration), so the answer is there from the moment it is indexed
		// rather than waiting for a probe that would get it wrong anyway.
		Duration: e.durationMs,
		Path:     path, stored: e, lower: searchText(name, displayText(path)),
	}
	setEpisode(it)
	l.items[id] = it
	l.byPath[path] = it
	l.countKind(it.Kind, 1)
	return true
}

// removePath removes the file at path, or the whole subtree if path was a
// directory — including items archived inside removed rar volumes (their
// virtual paths are "<rar path>\x00<member>"). Returns the number removed.
func (l *Library) removePath(path string) int {
	dirPrefix := path + string(filepath.Separator)
	rarPrefix := path + "\x00"
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	if it, ok := l.byPath[path]; ok {
		l.dropItem(it)
		n++
	}
	for p, it := range l.byPath {
		if strings.HasPrefix(p, dirPrefix) || strings.HasPrefix(p, rarPrefix) {
			l.dropItem(it)
			n++
		}
	}
	return n
}

// dropItem takes one item out of the index: both maps, the per-kind total,
// its claim on its file, and the record that mirrors it. The one door out,
// as upsert is the one door in — five places used to spell this sequence
// out, and the fifth was the one that forgot a step. Caller must hold l.mu.
func (l *Library) dropItem(it *Item) {
	if i := strings.IndexByte(it.Path, 0); i >= 0 {
		if set := l.members[it.Path[:i]]; set != nil {
			delete(set, it.Path)
		}
	}
	delete(l.byPath, it.Path)
	delete(l.items, it.ID)
	l.countKind(it.Kind, -1)
	l.releaseInode(it)
	l.markRemoved(it.ID)
}

// setMeta stores audio tag metadata (and duration, when known) on an item
// and folds it into the item's search text.
func (l *Library) setMeta(id string, m tagMeta, durationMs int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, ok := l.items[id]
	if !ok {
		return
	}
	// Tags come out of files with stray whitespace surprisingly often, and
	// an artist stored as " Name" both sorts to the top and splits into a
	// second entry of its own. Normalise here, the one place metadata
	// enters the index, so cached values are cleaned up as well.
	m.title, m.artist = cleanTag(m.title), cleanTag(m.artist)
	m.album, m.genre = cleanTag(m.album), cleanGenreTag(m.genre)
	m.year = cleanYear(m.year)

	it.Title, it.Artist, it.Album, it.Genre = m.title, m.artist, m.album, m.genre
	it.Track, it.Year = m.track, m.year
	if durationMs > 0 && !it.declaresDuration() {
		it.Duration = durationMs
	}
	if m != (tagMeta{}) {
		it.lower = itemSearchText(it)
	}
	l.markDirty(id)
}

// cleanTag normalises one tag value.
//
// Whitespace is the common case. A byte-order mark is the other one: a tag
// written as UTF-8-with-BOM keeps the mark inside the string, where it is
// invisible, sorts ahead of every letter, and makes "\ufeffRock" a second
// genre that has nothing to do with "Rock". The third is a value that is not
// UTF-8 at all, which an ID3v1 frame never is: decoded here, at the one door
// tags come through, rather than left to reach the browser as diamonds.
// cleanGenreTag is cleanTag plus what only a genre needs. Both doors
// metadata comes through call this one function: a warm start restores the
// mirrored index (persist.go) while enrichment restores the metadata cache,
// and a rule applied at only one of them shows up as a library that cleans
// itself up when it is re-read and not when it is restarted.
func cleanGenreTag(s string) string {
	return cleanGenre(cleanTag(s))
}

// cleanGenre undoes one thing tag readers do to genres and nothing else.
//
// ID3v1 numbered its genres, and ID3v2 kept the numbers as a reference in
// front of the text: a frame reading "(138)Black Metal" means genre 138,
// which *is* Black Metal, with the name spelt out after it. A reader that
// expands the number and keeps the text hands us both, and the release comes
// out filed under "Black Metal Black Metal" — sixteen of them here.
//
// Only an exact doubling is collapsed, and only for genres. A title may
// legitimately say a thing twice; a genre saying it twice is this.
func cleanGenre(s string) string {
	// Whitespace first, and every kind of it: a tag reading "Black  Metal"
	// is the same genre as "Black Metal" and was showing up as a second card
	// of one album beside a card of nine hundred. Fields splits on any run
	// of Unicode space, so a non-breaking one between the words — which
	// nothing on screen can distinguish — folds in here too.
	s = strings.Join(strings.Fields(s), " ")
	// The same name written twice, in either of the two forms the numeric
	// genre reference produces: "Black Metal Black Metal" and, from taggers
	// that punctuate the expansion, "Black Metal/Black Metal".
	if before, after, ok := strings.Cut(s, "/"); ok && strings.EqualFold(strings.TrimSpace(before), strings.TrimSpace(after)) {
		return strings.TrimSpace(before)
	}
	words := strings.Fields(s)
	if len(words) < 2 || len(words)%2 != 0 {
		return s
	}
	half := len(words) / 2
	first, second := strings.Join(words[:half], " "), strings.Join(words[half:], " ")
	if !strings.EqualFold(first, second) {
		return s
	}
	return first
}

// genreSeparators are the characters that mean "and also": a tag reading
// "Death Metal | Viking Metal" is two genres in one field.
//
// A slash is **not** one of them, and that is the whole judgement here.
// Measured over this library: 8 tags use a pipe or semicolon and 11 a comma,
// all of them plainly lists — while 48 use a slash, and there most of them
// are one compound name. "Black/Death Metal" is blackened death metal, not
// black metal and death metal; splitting it would invent a genre called
// "Black", which happens to be a truncation this library already suffers
// from. Guessing wrong on 48 tags to tidy 19 is the wrong trade, and a
// compound name left whole is at least the name somebody typed.
const genreSeparators = "|;,"

// splitGenres turns one genre tag into the genres it names.
func splitGenres(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return strings.ContainsRune(genreSeparators, r)
	})
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, p := range parts {
		p = cleanGenre(p)
		if p == "" {
			continue
		}
		// A tag listing the same genre twice is one genre; the first
		// spelling of it wins, as it does everywhere else here.
		key := strings.ToLower(p)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out
}

func cleanTag(s string) string {
	return reinterpretCyrillic(reinterpretThai(displayText(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "\ufeff")))))
}

// cleanYear reduces a year tag to a year.
//
// The frame is supposed to hold one, but files in the wild hold whole dates
// (20220519), a date with the year repeated (20072007), or a range. Anything
// that is not already a plausible year is cut back to its leading four
// digits, and what is left over is dropped: sorting a library by year is
// worthless when one release claims to be from the year twenty million.
func cleanYear(y int) int {
	const minYear, maxYear = 1000, 9999
	for y > maxYear {
		y /= 10
	}
	if y < minYear {
		return 0
	}
	return y
}

// markEnriched records that an item's metadata has been looked for.
func (l *Library) markEnriched(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if it, ok := l.items[id]; ok && (!it.enriched || it.shape < shapeVersion) {
		it.enriched, it.shape = true, shapeVersion
		l.markDirty(id)
	}
}

// setProbe stores what probing learned about a video (duration, codecs).
func (l *Library) setProbe(id string, p Probe) {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, ok := l.items[id]
	if !ok {
		return
	}
	if p.DurationMs > 0 && !it.declaresDuration() {
		it.Duration = p.DurationMs
	}
	if p.VCodec != "" {
		it.VCodec = p.VCodec
	}
	if p.Width > 0 && p.Height > 0 {
		it.Width, it.Height = p.Width, p.Height
	}
	if p.FPS > 0 {
		it.FPS = p.FPS
	}
	if p.ACodec != "" {
		it.ACodec = p.ACodec
	}
	// Offered only where there is a choice: one soundtrack is not a menu.
	// An answered probe is authoritative either way — a file replaced on
	// disk may have fewer tracks than the menu it used to earn, and only
	// clearing on a real answer keeps the cache-restore path (which carries
	// no tracks at all) from wiping a menu this process already probed.
	if len(p.Tracks) > 1 {
		it.Tracks = p.Tracks
	} else if p.Probed {
		it.Tracks = nil
	}
	// The embedded subtitles ride the same rule, except that one is a menu:
	// a lone caption track is still worth offering, where a lone soundtrack
	// is not a choice.
	if len(p.Subs) > 0 {
		it.EmbSubs = p.Subs
	} else if p.Probed {
		it.EmbSubs = nil
	}
	// Sticky, and never inferred from the fields: a Probe rebuilt from the
	// metadata cache carries codecs nobody probed for in this process, while
	// a probe that ran and found nothing carries no fields at all.
	it.probed = it.probed || p.Probed
	l.markDirty(id)
}

// declaresDuration reports that the container the content lives in stated
// its playing time, which outranks anything measured or remembered.
//
// A DVD does, in its own information file, and it is the only one that knows
// (see ifoDuration). Both doors metadata comes through have to honour it, or
// the estimate wins: the metadata cache restores one at enrichment, and the
// ffprobe that runs when a film is opened produces another. Either is out by
// a factor of two on a measured disc. Caller holds l.mu.
func (it *Item) declaresDuration() bool {
	return it.stored != nil && it.stored.durationMs > 0
}

// notify bumps the version and signals the broadcast loop.
func (l *Library) notify() {
	l.mu.Lock()
	l.version++
	l.mu.Unlock()
	select {
	case l.changed <- struct{}{}:
	default:
	}
}

// Version returns the current library version.
func (l *Library) Version() int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.version
}

// Subscribe registers a change listener. Call the returned func to unsubscribe.
func (l *Library) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 4)
	l.subMu.Lock()
	l.subs[ch] = struct{}{}
	l.subMu.Unlock()
	return ch, func() {
		l.subMu.Lock()
		delete(l.subs, ch)
		l.subMu.Unlock()
	}
}

// BroadcastLoop coalesces change signals and publishes events to subscribers.
// Run it in a goroutine; it exits when ctx is done.
func (l *Library) BroadcastLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.changed:
		}
		// Coalesce bursts of changes (e.g. during scans) into one event.
		timer := time.NewTimer(400 * time.Millisecond)
	drain:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-l.changed:
			case <-timer.C:
				break drain
			}
		}
		// The one place album/artist lists are refreshed after a change:
		// coalesced to at most once per event, off every request path.
		ev := Event{Version: l.Version(), Counts: l.RefreshCounts()}
		l.subMu.Lock()
		for ch := range l.subs {
			select {
			case ch <- ev:
			default:
			}
		}
		l.subMu.Unlock()
	}
}

// Counts computes per-kind totals of the items a default listing shows.
// Counts is O(1): per-kind totals are maintained by upsert/dropItem, the
// hidden ones subtracted from them are memoized per version, and
// album/artist totals are whatever their last build produced (the broadcast
// loop refreshes them after every change, so they are at most one coalesced
// event behind). It is attached to every listing response, so it must never
// walk the index.
func (l *Library) Counts() Counts {
	l.ensureFlags()
	l.mu.RLock()
	c, version := l.kindCounts, l.version
	l.mu.RUnlock()
	// Hidden items are indexed but out of the way, so a chip that counted
	// them would disagree with the grid beneath it.
	c = visibleCounts(c, l.hiddenCounts(version))
	c.Audiobooks = int(l.spokenAlbums.Load())
	c.Albums = l.albums.total() - c.Audiobooks
	c.Artists = l.artists.total()
	c.Genres = l.genres.total()
	c.Series = l.series.total()
	c.Started, c.Watched = l.watchTotals()
	c.Played = l.playedTotal()
	return c
}

// RefreshCounts rebuilds the album, artist and genre lists if the library
// changed and returns fresh totals. Called from the broadcast loop — off
// every request path — so the once-per-event rebuild is the only one there
// is.
//
// Each of the three has to be named here: the totals are atomics fed by the
// builds, so a list nobody asks to build is a chip that stays at nought. A
// warm start whose index is unchanged emits no event at all, which is why
// main seeds these once after the initial scan.
func (l *Library) RefreshCounts() Counts {
	l.Albums()
	l.Artists()
	l.Genres()
	l.Series()
	return l.Counts()
}

// queryResult is one filtered+sorted listing, cached until the library
// version moves or a different query arrives.
type queryResult struct {
	version    int64
	watchVer   int64
	kind       Kind
	kinds      KindSet
	watch      string
	played     bool
	series     string
	season     int
	paths      PathFilter
	search     string
	sort       string
	seed       int64
	desc       bool
	showHidden string
	favourites bool
	items      []*Item
}

// List returns a filtered, sorted page of items. The full result is cached
// per (query, version): the grid pages through a single query, so only the
// first page of a new query — or the first after a change — pays for the
// filter and sort; every following page is a 200-item copy.
func (l *Library) List(q Query) Result {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 500
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.Sort != "random" {
		// Nothing else depends on the seed; keeping it out of the cache key
		// stops a stray parameter from rebuilding the query per page.
		q.Seed = 0
	}
	if q.ShowHidden != "include" && q.ShowHidden != "only" {
		// One spelling of the default ("", "exclude", anything unknown), so
		// two clients wording it differently share the cached result.
		q.ShowHidden = ""
	}
	l.ensureFlags()

	l.queryMu.Lock()
	defer l.queryMu.Unlock()

	l.mu.RLock()
	version := l.version
	l.mu.RUnlock()

	res := l.lastQuery
	// The seed belongs in the comparison as much as the sort key does:
	// rebuilding a shuffle for page two would deal a different hand.
	if res == nil || res.version != version ||
		// A position is saved every few seconds while something plays, so it
		// gets a counter of its own: only a query that filters on watching
		// has to be rebuilt when one moves.
		(q.Watch != "" && res.watchVer != l.watchVersion()) ||
		res.kind != q.Kind || res.kinds != q.Kinds || res.watch != q.Watch ||
		res.played != q.Played || res.series != q.Series || res.season != q.Season ||
		res.paths != q.Paths || res.search != q.Search ||
		res.sort != q.Sort || res.seed != q.Seed || res.desc != q.Desc ||
		res.showHidden != q.ShowHidden || res.favourites != q.FavouritesOnly {
		res = l.buildQuery(q, version)
		l.lastQuery = res
	}

	total := len(res.items)
	end := min(q.Offset+q.Limit, total)
	start := min(q.Offset, total)
	page := make([]Item, end-start)
	// One set of snapshots for the page — the counts, the verdicts, the
	// resemblances, the release verdicts — rather than seven locks per item.
	st := l.stamper()
	l.mu.RLock()
	for i, it := range res.items[start:end] {
		// Items mutate under the write lock; copy under read.
		page[i] = st.stamp(*it)
	}
	l.mu.RUnlock()

	// A restricted caller cannot be given the running totals: they count
	// what it may not see, and the three totals that run across kinds —
	// started, finished, played — cannot be masked afterwards, there being
	// one number each. The pass that answers properly is the one the chips
	// already use for a search, cached the same way and only paid by a
	// caller who is actually restricted.
	counts := l.Counts()
	if q.Paths.Restricted() || q.Kinds != 0 {
		counts = l.CountsFor(CountQuery{
			Paths: q.Paths, Kinds: q.Kinds,
			ShowHidden: q.ShowHidden, Favourites: q.FavouritesOnly,
		})
	}
	out := Result{Items: page, Total: total, Version: version, Counts: counts}
	if q.Search != "" {
		// The same restrictions as the listing itself, or a restricted
		// caller's chips would count matches it may not see — mask() can
		// zero the per-kind numbers afterwards, but started, finished and
		// played are one number each and can only be counted right.
		m := l.CountsFor(CountQuery{
			Search: q.Search, Paths: q.Paths, Kinds: q.Kinds,
			ShowHidden: q.ShowHidden, Favourites: q.FavouritesOnly,
		})
		out.Matching = &m
	}
	return out
}

// sortEntry is one item on its way through a sort, with the keys that need
// computing — the lowercased name, the shuffle hash — computed once here
// rather than once per comparison.
type sortEntry struct {
	it   *Item
	key  string // precomputed primary sort key for name ordering
	rand uint64 // precomputed shuffle key, likewise never per comparison
}

// byField orders entries by one stored number, ties broken by the search
// text so that paging stays stable: the shape every numeric sort shares.
func byField(field func(*Item) int64) func(a, b sortEntry) int {
	return func(a, b sortEntry) int {
		if c := cmp.Compare(field(a.it), field(b.it)); c != 0 {
			return c
		}
		return strings.Compare(a.it.lower, b.it.lower)
	}
}

// buildQuery filters and sorts the whole index once for a query.
func (l *Library) buildQuery(q Query, version int64) *queryResult {
	words := searchWords(q.Search)
	// Split once for the whole pass rather than per item — and the same for
	// what has been watched, played and judged, which used to be three lock
	// acquisitions for every file in the library.
	allowed := q.Paths.allower()
	var watch map[string]WatchState
	if q.Watch != "" {
		watch = l.watchSnapshot()
	}
	var plays, likes map[string]int
	if q.Played {
		plays, likes = l.playsSnapshot(), l.likesSnapshot()
	}

	l.mu.RLock()
	entries := make([]sortEntry, 0, len(l.items))
	for _, it := range l.items {
		if q.Kind != "" && it.Kind != q.Kind {
			continue
		}
		if !q.Kinds.Has(it.Kind) {
			continue
		}
		if q.Watch != "" && !keepWatched(watch[it.ID], q.Watch) {
			continue
		}
		if q.Played && plays[it.ID] == 0 && likes[it.ID] == 0 {
			continue
		}
		if q.Series != "" {
			if !strings.EqualFold(it.Series, q.Series) {
				continue
			}
			if q.Season > 0 && it.Season != q.Season {
				continue
			}
		}
		if !allowed(it.Path) {
			continue
		}
		if !l.keepFlagged(it.ID, q.ShowHidden, q.FavouritesOnly) {
			continue
		}
		if !matchWords(it.lower, words) {
			continue
		}
		e := sortEntry{it: it}
		switch q.Sort {
		case "mtime", "size", "added", "duration", "pixels", "bitrate": // ordered by stored fields
		case "random":
			e.rand = shuffleKey(it.ID, q.Seed)
		default:
			e.key = strings.ToLower(it.Name)
		}
		entries = append(entries, e)
	}

	var order func(a, b sortEntry) int
	switch q.Sort {
	case "mtime":
		order = byField(func(it *Item) int64 { return it.ModTime })
	case "size":
		order = byField(func(it *Item) int64 { return it.Size })
	case "added":
		order = byField(func(it *Item) int64 { return it.FirstSeen })
	case "duration":
		order = byField(func(it *Item) int64 { return it.Duration })
	case "pixels":
		// How big the picture is, which is one number rather than two: a
		// listing sorted by width alone puts a tall clip from a phone above a
		// film, and by height alone the other way round.
		order = byField(pixelCount)
	case "bitrate":
		// What the file spends per second: its size over its playing time,
		// which is the whole file rather than the picture alone. That is the
		// only rate obtainable without reading the stream, and for a
		// variable-rate file it is the truer one anyway.
		order = byField(bitsPerSecond)
	case "episode":
		order = func(a, b sortEntry) int {
			if c := cmp.Compare(a.it.Season, b.it.Season); c != 0 {
				return c
			}
			if c := cmp.Compare(a.it.EpisodeNo, b.it.EpisodeNo); c != 0 {
				return c
			}
			// Two files of one episode, or a season whose episodes are not
			// numbered at all: the name is the only order left, and it is
			// the one a viewer expects.
			return strings.Compare(a.it.lower, b.it.lower)
		}
	case "plays", "popular":
		// Taken from a snapshot rather than the map itself: this comparison
		// runs O(n log n) times over the whole library, and taking a lock in
		// each of them would cost more than the sort. The verdict outranks
		// the count (popularity); "plays" is the key's old name, kept so an
		// address minted before the rename still sorts.
		plays := l.playsSnapshot()
		likes := l.likesSnapshot()
		aff := l.affinities()
		order = byField(func(it *Item) int64 {
			return trackPopularity(likes[it.ID], aff.bucket[it.ID], plays[it.ID])
		})
	case "random":
		order = func(a, b sortEntry) int {
			if c := cmp.Compare(a.rand, b.rand); c != 0 {
				return c
			}
			return strings.Compare(a.it.lower, b.it.lower)
		}
	default: // name
		order = func(a, b sortEntry) int {
			if c := strings.Compare(a.key, b.key); c != 0 {
				return c
			}
			return strings.Compare(a.it.Rel, b.it.Rel)
		}
	}
	if q.Desc {
		inner := order
		order = func(a, b sortEntry) int { return -inner(a, b) }
	}
	slices.SortFunc(entries, order)
	l.mu.RUnlock()

	items := make([]*Item, len(entries))
	for i, e := range entries {
		items[i] = e.it
	}
	return &queryResult{
		version: version, watchVer: l.watchVersion(),
		kind: q.Kind, kinds: q.Kinds, watch: q.Watch, played: q.Played,
		series: q.Series, season: q.Season, paths: q.Paths, search: q.Search,
		sort: q.Sort, seed: q.Seed, desc: q.Desc,
		showHidden: q.ShowHidden, favourites: q.FavouritesOnly,
		items: items,
	}
}

// shuffleKey orders the seeded shuffle. Hashing the item ID with the seed
// keeps a given seed's order stable across pages and across restarts while
// correlating with nothing else about the item, which a cheaper trick like
// hashing the name would not manage.
func shuffleKey(id string, seed int64) uint64 {
	h := fnv.New64a()
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(seed))
	_, _ = h.Write(b[:])
	_, _ = h.Write([]byte(id))
	return h.Sum64()
}
