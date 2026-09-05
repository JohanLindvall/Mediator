package server

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	"image/jpeg"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/image/draw"

	_ "image/gif"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/library"
)

// FFmpegPath returns the resolved ffmpeg binary, "" if unavailable.
func (t *Thumbnailer) FFmpegPath() string { return t.ffmpeg }

// Store is the blob database this thumbnailer was given, or nil when there
// is none. Shared with the border detection, which keeps its answers in the
// same file for the same reason everything else does.
func (t *Thumbnailer) Store() *blob.DB { return t.store }

// ErrNoThumb means no thumbnail can be produced for this item (no error worth
// logging: unsupported format, no embedded art, ffmpeg unavailable, ...).
var ErrNoThumb = errors.New("no thumbnail available")

const (
	defaultThumbWidth = 360
	maxThumbWidth     = 1600      // the preview overlay's width; nothing asks for more
	maxDecodePixels   = 120 << 20 // refuse to decode images over 120MP
)

// Scrub sprites: one JPEG holding ten frames sampled across a video, laid
// out in a fixed grid. The convention is fixed on purpose — the client
// derives every frame's position and timestamp from the item's duration
// alone, so there is no sprite metadata endpoint to keep in step.
const (
	spriteFrames = 10
	spriteCols   = 5
	spriteRows   = 2
	// Wide enough to stand in for a grid tile: the sheet is what a hover
	// preview animates, not only what the seek bar shows in a corner.
	spriteFrameWidth = 320
	// spriteMinDurationMs is where a scrub strip starts being worth a
	// second of ffmpeg: below it the ten frames are near-duplicates and the
	// timeline is too short to scrub anyway.
	spriteMinDurationMs = 30_000
	// spriteCacheWidth keys sprites in the thumbnail bucket. Thumbnail
	// widths are clamped into [64, maxThumbWidth], so numbers outside that
	// are free and no schema change is needed to share the store. It is not
	// a width but a slot, and it changes whenever the sheet's own shape or
	// recipe does — a stored sheet from the old one would otherwise be
	// served under the new convention, at half the resolution the client
	// now expects.
	spriteCacheWidth = -1
	// spriteFrameTimeout bounds one frame. Ten seeks, each landing in a
	// different part of the file, and any one of them can be the one that
	// waits on a disk doing something else.
	spriteFrameTimeout = 20 * time.Second
	// spriteTimeout bounds the whole sheet.
	spriteTimeout = 90 * time.Second
	// archiveSpriteTimeout is the same for a member of an archive set, where
	// every frame is a ranged read over loopback: measured at up to 1.9 s
	// each, so ten of them plus the reader's own work wants room.
	archiveSpriteTimeout = 3 * time.Minute
)

// Thumbnailer produces JPEG thumbnails, cached in the blob database when one
// is configured (nil store: every request regenerates).
type Thumbnailer struct {
	store     *blob.DB
	ffmpeg    string      // path to ffmpeg binary, "" if unavailable
	streaming func() bool // media is playing: yield disk/CPU to it
	log       *slog.Logger

	genN  atomic.Int64  // generations in flight (thumbs outrank enrichment)
	sem   chan struct{} // limits concurrent image decodes
	ffSem chan struct{} // limits concurrent ffmpeg processes
	bgSem chan struct{} // collapses generation to one job while streaming

	negMu sync.Mutex
	neg   map[string]struct{} // keys that recently failed; avoids retry storms

	genMu sync.Mutex
	gen   map[string]*genEntry // in-flight generations by cache key
}

// genEntry shares one in-flight generation and its result between requests.
type genEntry struct {
	done chan struct{}
	data []byte
	err  error
}

// NewThumbnailer creates the thumbnailer. store may be nil, disabling the
// persistent cache. streaming (may be nil) reports whether media is being
// served right now; while it returns true, generation collapses to a single
// job at a time so playback keeps priority on disk and CPU.
func NewThumbnailer(store *blob.DB, streaming func() bool, log *slog.Logger) *Thumbnailer {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		ffmpeg = ""
		log.Info("ffmpeg not found; video thumbnails disabled")
	}
	if streaming == nil {
		streaming = func() bool { return false }
	}
	n := max(runtime.NumCPU(), 2)
	return &Thumbnailer{
		store:     store,
		ffmpeg:    ffmpeg,
		streaming: streaming,
		log:       log,
		sem:       make(chan struct{}, n),
		ffSem:     make(chan struct{}, 2),
		bgSem:     make(chan struct{}, 1),
		neg:       make(map[string]struct{}),
		gen:       make(map[string]*genEntry),
	}
}

// processEpoch identifies a run with no database. Nothing is stored in that
// mode, so every run really is a new set of images and browsers should say so.
var processEpoch = func() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "0"
	}
	return hex.EncodeToString(raw[:])
}()

// StoreEpoch identifies the store these thumbnails come from, for the client
// to hang on their URLs. See blob.DB.initEpoch for why they need it.
func (t *Thumbnailer) StoreEpoch() string {
	if t.store == nil {
		return processEpoch
	}
	if e := t.store.Epoch(); e != "" {
		return e
	}
	return processEpoch
}

// Generating reports whether any thumbnail is being generated right now.
func (t *Thumbnailer) Generating() bool { return t.genN.Load() > 0 }

// Get returns the JPEG thumbnail for the item, generating and caching it if
// needed. Concurrent requests for the same thumbnail are deduplicated and
// share the leader's result.
func (t *Thumbnailer) Get(ctx context.Context, it library.Item, width int) ([]byte, error) {
	if width <= 0 {
		width = defaultThumbWidth
	}
	width = min(max(width, 64), maxThumbWidth)
	// Keyed by width alone, deliberately. A change to how the frame is
	// chosen reaches files that change on disk and leaves the rest as they
	// are: remaking every video still in a library costs an ffmpeg seek
	// apiece, and the tiles already made are not wrong, only older.
	return t.cached(ctx, it, width, func(ctx context.Context) ([]byte, error) {
		return t.generate(ctx, it, width)
	})
}

// Sprite returns the scrub sheet for a video: spriteFrames frames sampled at
// duration*(i+0.5)/spriteFrames, tiled spriteCols by spriteRows. It is
// generated on first request and cached beside the thumbnails.
//
// ErrNoThumb covers everything the client should treat as "this video has no
// scrub strip": no ffmpeg, an unknown duration, a clip too short to be worth
// sampling, or content inside an archive. Unlike the thumbnail, which needs
// only the first seconds and so can be piped a prefix, a strip samples the
// whole file — through a volume set that is every volume of it, read for one
// small image.
func (t *Thumbnailer) Sprite(ctx context.Context, it library.Item) ([]byte, error) {
	if !spriteEligible(it) || t.ffmpeg == "" {
		return nil, ErrNoThumb
	}
	return t.cached(ctx, it, spriteCacheWidth, func(ctx context.Context) ([]byte, error) {
		return t.makeSprite(ctx, it)
	})
}

// HasSprite reports whether a sheet for this item is already stored, which
// is the question a hover asks before it asks for anything expensive.
func (t *Thumbnailer) HasSprite(it library.Item) bool {
	if t.store == nil {
		return false
	}
	_, ok := t.store.GetThumb(it.ID, it.ModTime, it.Size, spriteCacheWidth)
	return ok
}

// spriteEligible reports whether a scrub sheet can be built for the item at
// all — everything but the presence of ffmpeg, which the Thumbnailer knows.
//
// Archived video is included now, which it was not while the sheet was one
// invocation walking the file: over the loopback URL that read the whole
// member. It is ten seeks, and ten seeks are exactly what the archived
// thumbnail already does — so the recipe is no longer in the way.
//
// The arithmetic still is. One archived seek measured 9.3-28.7 MiB and
// 0.09-1.91 s, so a sheet is of the order of 100-300 MiB and up to twenty
// seconds of a volume set. That is a fair price when somebody opens a film
// and the seek bar wants it; it is not a price to pay because a pointer
// crossed a tile, which is why the hover asks for one without permission to
// make it (see handleSprite).
func spriteEligible(it library.Item) bool {
	return it.Kind == library.KindVideo && it.Duration >= spriteMinDurationMs
}

// cached wraps one generation in the layers every derived image shares: the
// persistent store, the negative cache, in-flight deduplication and the
// background gate. width doubles as the store key, so a sprite and a
// thumbnail of the same item never collide.
func (t *Thumbnailer) cached(ctx context.Context, it library.Item, width int, gen func(context.Context) ([]byte, error)) ([]byte, error) {
	if t.store != nil {
		if data, ok := t.store.GetThumb(it.ID, it.ModTime, it.Size, width); ok {
			return data, nil
		}
	}

	key := fmt.Sprintf("%s|%d|%d|%d", it.ID, it.ModTime, it.Size, width)

	t.negMu.Lock()
	_, failed := t.neg[key]
	t.negMu.Unlock()
	if failed {
		return nil, ErrNoThumb
	}

	var e *genEntry
	for {
		t.genMu.Lock()
		leader, inflight := t.gen[key]
		if !inflight {
			e = &genEntry{done: make(chan struct{})}
			t.gen[key] = e
			t.genMu.Unlock()
			break
		}
		t.genMu.Unlock()
		select {
		case <-leader.done:
			// The leader's request went away — a cell scrolled out of view
			// — while this one is still here: that cancellation is the
			// leader's, not the item's and not ours. Lead the next attempt
			// rather than hand the poster its 404.
			if errors.Is(leader.err, context.Canceled) && ctx.Err() == nil {
				continue
			}
			return leader.data, leader.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	e.data, e.err = t.gated(ctx, gen)

	t.genMu.Lock()
	delete(t.gen, key)
	t.genMu.Unlock()
	close(e.done)

	if e.err != nil {
		// A canceled request says nothing about the item — don't negative-cache.
		if !errors.Is(e.err, context.Canceled) && !errors.Is(e.err, context.DeadlineExceeded) {
			t.negMu.Lock()
			if len(t.neg) > 50000 {
				t.neg = make(map[string]struct{})
			}
			t.neg[key] = struct{}{}
			t.negMu.Unlock()
		}
		return nil, e.err
	}
	if t.store != nil {
		if err := t.store.PutThumb(it.ID, it.ModTime, it.Size, width, e.data); err != nil {
			t.log.Warn("thumb store write failed", "id", it.ID, "err", err)
		}
	}
	return e.data, nil
}

// gated runs one generation, but while media is streaming it first takes the
// single background slot so at most one decode or ffmpeg competes with
// playback for disk and CPU.
func (t *Thumbnailer) gated(ctx context.Context, gen func(context.Context) ([]byte, error)) ([]byte, error) {
	t.genN.Add(1)
	defer t.genN.Add(-1)
	if t.streaming() {
		if err := t.acquire(ctx, t.bgSem); err != nil {
			return nil, err
		}
		defer func() { <-t.bgSem }()
	}
	return gen(ctx)
}

func (t *Thumbnailer) generate(ctx context.Context, it library.Item, width int) ([]byte, error) {
	switch it.Kind {
	case library.KindImage:
		return t.fromImageFile(ctx, it, width)
	case library.KindAudio:
		return t.fromAudio(ctx, it, width)
	case library.KindVideo:
		return t.fromVideo(ctx, it, width)
	}
	return nil, ErrNoThumb
}

func (t *Thumbnailer) acquire(ctx context.Context, sem chan struct{}) error {
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *Thumbnailer) fromImageFile(ctx context.Context, it library.Item, width int) ([]byte, error) {
	if strings.EqualFold(filepath.Ext(it.Name), ".svg") {
		return nil, ErrNoThumb // svg is rendered natively by the browser instead
	}
	f, err := library.OpenItem(it)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return t.encodeResized(ctx, f, width)
}

func (t *Thumbnailer) fromAudio(ctx context.Context, it library.Item, width int) ([]byte, error) {
	if data, _ := library.ReadPicture(it); len(data) > 0 {
		if out, err := t.encodeResized(ctx, bytes.NewReader(data), width); err == nil {
			return out, nil
		}
	}
	// Fall back to cover art files sitting next to the track (for archived
	// tracks, Dir of the virtual path is the directory holding the rar).
	dir := filepath.Dir(it.Path)
	for _, name := range coverCandidates(dir) {
		f, err := os.Open(name)
		if err != nil {
			continue
		}
		out, err := t.encodeResized(ctx, f, width)
		f.Close()
		if err == nil {
			return out, nil
		}
	}
	return nil, ErrNoThumb
}

// coverStems are the conventional names for a folder's cover image.
var coverStems = map[string]bool{
	"cover": true, "folder": true, "front": true, "album": true,
	"albumart": true, "artwork": true, "thumb": true,
}

// artDirs are subdirectory names that hold a release's artwork.
var artDirs = map[string]bool{
	"cover": true, "covers": true, "art": true, "artwork": true,
	"scans": true, "images": true,
}

// coverExts are the image formats the thumbnailer can decode itself.
var coverExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".bmp": true, ".gif": true,
}

// maxLooseCovers is how many images a directory may hold before an
// unnamed one stops looking like album art. Beyond that it is probably a
// folder of pictures that happens to contain a track.
const maxLooseCovers = 8

// coverRank scores a file name by how much it looks like a front cover.
func coverRank(name string) int {
	ext := strings.ToLower(filepath.Ext(name))
	stem := strings.ToLower(strings.TrimSuffix(name, ext))
	switch {
	case coverStems[stem]:
		return 3
	case strings.Contains(stem, "front"):
		return 2
	case strings.Contains(stem, "cover"):
		return 1
	}
	return 0
}

// coverCandidates returns image paths in a music directory, most likely
// cover first: a conventional name, then a name hinting at the front
// cover, then the largest file. Releases routinely ship artwork named
// after the release, or tucked into a "Covers" subdirectory, so neither
// is dismissed.
func coverCandidates(dir string) []string {
	type cand struct {
		path string
		rank int
		size int64
	}
	var cands []cand
	var nested []string

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	loose := 0
	for _, e := range entries {
		if e.IsDir() {
			if artDirs[strings.ToLower(e.Name())] {
				nested = append(nested, filepath.Join(dir, e.Name()))
			}
			continue
		}
		if !coverExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		size := int64(0)
		if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		rank := coverRank(e.Name())
		if rank == 0 {
			loose++
		}
		cands = append(cands, cand{filepath.Join(dir, e.Name()), rank, size})
	}
	// A directory of pictures is not an album cover waiting to be picked.
	if loose > maxLooseCovers {
		cands = slices.DeleteFunc(cands, func(c cand) bool { return c.rank == 0 })
	}
	for _, sub := range nested {
		subEntries, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, e := range subEntries {
			if e.IsDir() || !coverExts[strings.ToLower(filepath.Ext(e.Name()))] {
				continue
			}
			size := int64(0)
			if fi, err := e.Info(); err == nil {
				size = fi.Size()
			}
			// Artwork in a dedicated folder ranks below a cover named as
			// such beside the tracks, but above an unnamed loose image.
			cands = append(cands, cand{filepath.Join(sub, e.Name()), coverRank(e.Name()), size})
		}
	}

	slices.SortStableFunc(cands, func(a, b cand) int {
		if c := cmp.Compare(b.rank, a.rank); c != 0 { // best first
			return c
		}
		if c := cmp.Compare(b.size, a.size); c != 0 { // then largest
			return c
		}
		return strings.Compare(a.path, b.path)
	})
	out := make([]string, 0, min(len(cands), 4))
	for _, c := range cands[:min(len(cands), 4)] {
		out = append(out, c.path)
	}
	return out
}

func (t *Thumbnailer) fromVideo(ctx context.Context, it library.Item, width int) ([]byte, error) {
	if t.ffmpeg == "" {
		return nil, ErrNoThumb
	}
	if it.Archived() {
		return t.fromArchivedVideo(ctx, it, width) // no path for ffmpeg to open
	}
	if err := t.acquire(ctx, t.ffSem); err != nil {
		return nil, err
	}
	defer func() { <-t.ffSem }()

	// A tenth of the way in, for the same reason archived video is (see
	// thumbOffsets): the opening of a film is its front matter. For a
	// television episode that is the distributor's logo card almost without
	// exception, which is what a shelf of shows looked like — Netflix,
	// Paramount, Showtime, one after another, none of them saying which
	// programme it was.
	//
	// Fallbacks in order behind it: the alternative offset, then three
	// seconds, then the first frame. A clip too short for the first seek, a
	// duration nobody has measured, an archived item fed a bounded prefix
	// that the seek lands past the end of — all of them end at frame 0,
	// which needs nothing but the opening bytes.
	for _, seek := range videoSeeks(it.Duration) {
		data, err := t.videoFrame(ctx, it, seek, width)
		if err != nil {
			return nil, err // interrupted, or out of time: no verdict on the item
		}
		if len(data) > 0 {
			return data, nil
		}
	}
	return nil, ErrNoThumb
}

// plainThumbTimeout bounds one seek into a plain file. A variable so a test
// can shorten it.
var plainThumbTimeout = 30 * time.Second

// videoSeeks is where to look for a frame, in the order to try.
//
// Without a duration there is no tenth to take, and the old behaviour is the
// answer: a few seconds in, then the very beginning.
func videoSeeks(durationMs int64) []string {
	if durationMs <= 0 {
		return []string{"3", "0"}
	}
	out := make([]string, 0, 4)
	for _, at := range thumbOffsets(durationMs) {
		out = append(out, strconv.FormatFloat(at, 'f', 2, 64))
	}
	return append(out, "3", "0")
}

// videoFrame runs one ffmpeg invocation for a single scaled frame at seek,
// returning the JPEG, or nil when that seek yielded none. An error means the
// item is not to be tried again this call — the request went away, or the
// seek ran out of time — and neither is a verdict on the item.
//
// The same run the archived path uses (runFrame), and it has to be: this
// used to be a near-copy that checked only the caller's context, so when its
// own thirty-second deadline killed ffmpeg the half-flushed JPEG was read
// back and stored immutable, and a seek that timed out read as "no frame
// here", fell through every offset, and negative-cached the film for the
// life of the process — a disk busy with playback became a permanent grey
// tile. runFrame refuses a torn frame and reports a deadline as a deadline.
func (t *Thumbnailer) videoFrame(ctx context.Context, it library.Item, seek string, width int) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, plainThumbTimeout)
	defer cancel()
	return t.runFrame(ctx, cctx, nil, func(out string) []string {
		return frameArgs(frameSpec{input: it.Path, out: out, seek: seek, width: width, quality: 4})
	})
}

// Thumbnails for video inside an archive.
//
// A member of a multi-part set has no path ffmpeg can open, so for a long
// time it was handed a bounded prefix on standard input. That works, and it
// picks the wrong frame: over a pipe nothing can seek, so the only frames
// reachable are the first minute's — which is precisely where a release keeps
// its distributor logo, its title card and its exposition crawl. Measured on
// six archived 2160p releases, the piped recipe stored a logo or a text card
// for four of them, and widening the prefix or the window did not help,
// because the prefix was never the constraint: front matter is where we were
// looking.
//
// So the input is the server's own loopback stream URL instead
// (library.LoopbackURL), which answers Range requests over the same
// OpenItem bytes. With a seekable input the frame can come from a fraction
// of the way into the film, the way media managers pick one, and the
// prefix/window/escalation problem dissolves. Measured over 22 archived
// members, 140 extractions: every seek landed, 9.3-28.7 MiB and 0.09-1.91 s
// each, at most one volume descriptor open at a time in an 89-volume set.
//
// The piped prefix survives as the fallback for the two cases where the
// fraction cannot be taken: no loopback address (see loopback.go), or no
// duration to take a fraction of.
//
// Nothing here may be written down until it has been earned. A thumbnail is
// stored under (id, mtime, size, width) and handleThumb serves what it finds
// as immutable — and for a member of a completed volume set not one of those
// keys will ever change again. So a bad image written once is permanent: in
// the database, and in every browser that ever saw it, out of reach of both
// the frontend's v=<mtime> buster and retryThumbs. Hence the checks below:
// the run was not interrupted, the JPEG reached its end marker, and the frame
// is not one flat colour.

const (
	// archiveThumbFraction is how far into the item the frame is taken.
	// Measured across 15 archived features and episodes, one frame at each
	// of 1/2/3/5/10/20/30/50% of the duration: at 1-3% two to three releases
	// still returned front matter, while 5% and 10% returned a real scene
	// for all 15. Deeper than that is worse, not better, because climactic
	// scenes are dark — the worst tile measured 16.20 at 10%, 10.76 at 20%,
	// 10.46 at 30% and 5.70 at 50%. 5% ties 10% on the worst tile (17.80)
	// but sits closer to the front matter it has to clear, so 10% it is.
	archiveThumbFraction = 0.10

	// archiveThumbMinOffsetSec keeps the first frame out of the opening
	// seconds of a short item, where 10% is only a few seconds in. It is not
	// what clears front matter — measured, that is an absolute-time
	// phenomenon living in the first 0-240 s, and 10% of anything 40 minutes
	// or longer is already past it, while the short items measured (WEB
	// episodes) had no front matter at all.
	archiveThumbMinOffsetSec = 120.0

	// archiveThumbMaxFraction stops the floor above from seeking past the
	// end of a short clip: never beyond the midpoint.
	archiveThumbMaxFraction = 0.5

	// archiveThumbRetryFraction is the one other offset tried when the first
	// frame comes back too flat to trust. It moves away from the front
	// matter rather than toward it, without reaching the dark final third.
	// Measured, it never had to run: across 20 items the first frame was
	// always good enough (worst 16.31).
	archiveThumbRetryFraction = 0.35
)

// thumbOffsets returns the seek positions to try, in order, for an item of
// this length. Two of them: the frame, and one alternative if it is flat.
func thumbOffsets(durationMs int64) []float64 {
	d := float64(durationMs) / 1000
	first := min(max(d*archiveThumbFraction, archiveThumbMinOffsetSec), d*archiveThumbMaxFraction)
	return []float64{first, d * archiveThumbRetryFraction}
}

const (
	// archiveThumbRetryStdDev is where a frame is flat enough to be worth
	// spending the second seek on. The measure is the standard deviation of
	// the frame's luma. Measured: accepted frames at the chosen offset
	// scored 16.31 to 66.64 across 20 items, while the black and near-black
	// front matter that offset exists to avoid scored 0.84 to 1.13. So 12.0
	// is comfortably below every real frame seen and far above every flat
	// one.
	//
	// It cannot catch a logo or a title card, and no threshold on uniformity
	// ever will: a studio logo card measured 41.69, another 36.51, green
	// text on black 25.23, a title card 21.71. High contrast is what those
	// are made of. Seeking past them is the whole point of the offset.
	archiveThumbRetryStdDev = 12.0

	// archiveThumbMinStdDev is the floor below which a frame is not stored
	// at all, because it is not a picture. It is a floor and not a
	// separator: the two populations overlap around it — a measured black
	// title-text frame scored 0.84 and 7.90 on two different releases, while
	// the darkest real frame measured (a very dark face at the midpoint of a
	// feature) scored 5.70. Nothing distinguishes those by uniformity alone,
	// which is why the offset does the work and this only refuses to make
	// something permanent out of a flat colour.
	archiveThumbMinStdDev = 3.0

	// uniformitySamples caps the pixels lumaStdDev looks at. A grid
	// thumbnail is ~50k pixels, so this normally reads all of them.
	uniformitySamples = 20000
)

// archiveThumbTimeout bounds one item: every attempt together, so a retry
// cannot double how long a tile holds an ffmpeg slot. Measured, the seeking
// path needs a small fraction of it — 0.09-1.91 s per frame, at most 1.50 s
// end to end for a whole item — and the piped fallback pulls up to its
// prefix, which over the measured 11 MiB/s mount is about six seconds and was
// 9.19 s at worst. The rest is headroom for a disk busy with playback. A var
// so tests can shorten it.
var archiveThumbTimeout = 60 * time.Second

// frameSpec is one still to take: from where, at what moment, how large.
type frameSpec struct {
	input string // a path, or this server's own loopback stream URL
	out   string
	seek  string // seconds, as ffmpeg is to read them
	width int
	// quality is JPEG's -q:v: 4 for a tile, 5 for a scrub-sheet frame.
	quality int
	// loopback says the input is our own stream URL, which wants the
	// internal marker, a socket timeout and an inaccurate seek — see below.
	loopback bool
	// accurate decodes forward to the frame asked for rather than taking the
	// keyframe at or before it. A scrub sheet on a plain file wants it; a
	// tile does not, and over loopback nothing does.
	accurate bool
}

// frameArgs is the ffmpeg command line for one scaled frame at a seek — the
// one recipe every still here is taken by, whichever input and whatever it
// is for. Four builders used to spell it out, one per purpose.
//
// The seek goes before the input, which makes it an input seek: ffmpeg jumps
// to the keyframe at or before the moment and reads a few megabytes there
// rather than the file. -noaccurate_seek takes that keyframe as the frame
// instead of decoding forward to the exact one; measured, that lands within
// 3 s of the request, which for choosing a tile is the same picture, and
// over loopback it is what makes a ranged read cheap. -rw_timeout is in
// microseconds and stops a wedged socket read from sitting there until the
// item's whole budget is gone. -headers marks the request as ours so it is
// not counted as playback (see library.InternalHeader).
func frameArgs(f frameSpec) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if !f.accurate {
		args = append(args, "-noaccurate_seek")
	}
	if f.loopback {
		args = append(args,
			"-rw_timeout", strconv.Itoa(int(archiveSeekReadTimeout/time.Microsecond)),
			"-headers", library.LoopbackHeaderArg())
	}
	return append(args,
		"-ss", f.seek,
		"-i", f.input,
		"-map", "0:v:0", "-an", "-sn", "-dn",
		"-frames:v", "1", "-vf", videoFilter(fmt.Sprintf("scale=%d:-2", f.width)),
		"-q:v", strconv.Itoa(f.quality), "-y", f.out)
}

// archiveSeekArgs is the frame recipe for one tile taken at an offset into
// an archived item, read over the loopback stream URL.
func archiveSeekArgs(out, url string, width int, offset float64) []string {
	return frameArgs(frameSpec{
		input: url, out: out, seek: strconv.FormatFloat(offset, 'f', 3, 64),
		width: width, quality: 4, loopback: true,
	})
}

// archiveSeekReadTimeout is how long one socket read may stall before ffmpeg
// gives up on it. Well above the measured whole-frame times (max 1.91 s) and
// well below archiveThumbTimeout, so a stall is reported rather than eating
// the item's entire budget.
const archiveSeekReadTimeout = 20 * time.Second

// The piped fallback. Nothing seeks, so a candidate is taken every
// archivePipeSampleSec seconds over the first archivePipeWindowSec, and
// ffmpeg's thumbnail filter keeps the least ordinary of them. That filter
// steps past black; it does not step past a distributor card, which it is in
// fact drawn to — measured, four of six releases came back with a logo or a
// text card. This path is a fallback and nothing more.
const (
	// archivePipePrefix is a ceiling on the bytes drawn out of the volume
	// set, not a read: ffmpeg stops as soon as it has its frame.
	archivePipePrefix = 64 << 20
	// archivePipeWindowSec bounds the playing time decoded.
	archivePipeWindowSec = 60
	// archivePipeSampleSec is one candidate every this many seconds.
	// Keyframes only at this spacing was measured at 1.44 s against 6.13 s
	// for decoding every frame at 2 s spacing — and on that release it also
	// produced the better frame.
	archivePipeSampleSec = 10
	// archiveThumbFrames is how many candidates the thumbnail filter will
	// score before emitting one. It is a ceiling: with the window and
	// spacing above there are six, and the filter emits the best of what it
	// buffered when the input ends, which is what makes a short prefix
	// degrade instead of fail.
	archiveThumbFrames = 30
)

// archivePipeArgs builds the ffmpeg command line for the piped fallback.
// -skip_frame is a decoder option, so it goes before the input it applies to.
func archivePipeArgs(out string, width int) []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-skip_frame", "nokey",
		"-t", strconv.Itoa(archivePipeWindowSec), "-i", "pipe:0",
		"-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", deinterlacer + "," + fmt.Sprintf("fps=1/%d,scale=%d:-2,thumbnail=%d",
			archivePipeSampleSec, width, archiveThumbFrames),
		"-frames:v", "1", "-q:v", "4", "-y", out,
	}
}

// archiveFirstFrameArgs takes whatever decodes first, with no sampling and no
// choosing. It is for items too short for the sampler to see a single
// candidate, where there is nothing to choose between.
func archiveFirstFrameArgs(out string, width int) []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-i", "pipe:0",
		"-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", videoFilter(fmt.Sprintf("scale=%d:-2", width)),
		"-frames:v", "1", "-q:v", "4", "-y", out,
	}
}

// fromArchivedVideo makes a thumbnail for video inside an archive. It is
// called from fromVideo, so it inherits the whole cached/gated stack: the
// stored thumbnail, the negative cache, in-flight deduplication, and the
// single background slot that keeps this behind playback.
//
// A frame that is near-uniform is not a usable tile and above all must not be
// stored, so a second offset is tried; if that is flat too the item gets no
// thumbnail at all. That is the deliberate choice: ErrNoThumb is
// negative-cached in memory only, so the grid shows a blank tile until the
// next start rather than remembering a black one forever.
func (t *Thumbnailer) fromArchivedVideo(ctx context.Context, it library.Item, width int) ([]byte, error) {
	if err := t.acquire(ctx, t.ffSem); err != nil {
		return nil, err
	}
	defer func() { <-t.ffSem }()

	// One budget for the item, not one per attempt: a retry must not double
	// how long a single tile can hold an ffmpeg slot.
	cctx, cancel := context.WithTimeout(ctx, archiveThumbTimeout)
	defer cancel()

	if url := library.LoopbackURL(it); url != "" {
		dur := it.Duration
		if dur <= 0 {
			// The offset is a fraction of the length, so the length has to
			// be known. Reading it is one ffprobe of the very same URL —
			// measured 0.06 s and 5.2 MiB on a 2160p archived member — and
			// it runs inside the ffmpeg slot this already holds, so it adds
			// no process beyond the budget the tile was given.
			dur = library.ProbeDuration(cctx, it)
		}
		if dur > 0 {
			data, err := t.seekThumb(ctx, cctx, it, url, width, dur)
			// Seeking is the better tool but not the only one, and the
			// reasons it can come back empty are not all about the file: an
			// ffmpeg built without the http protocol, or a loopback the
			// process cannot reach, would otherwise leave the item with no
			// thumbnail at all when piping would still have worked. A
			// cancelled or expired budget is different — nobody is waiting
			// for a second attempt, and it must not be spent.
			if !errors.Is(err, errSeekProducedNothing) || cctx.Err() != nil {
				return data, err
			}
			t.log.Debug("archived thumbnail: seeking produced nothing, falling back to a piped prefix",
				"id", it.ID, "err", err)
		} else {
			// No length, so no fraction to take: sampling a prefix is the
			// tool that does not need one.
			t.log.Debug("archived thumbnail: no duration, falling back to a piped prefix", "id", it.ID)
		}
	}
	return t.pipedThumb(ctx, cctx, it, width)
}

// seekThumb takes the frame from a fraction of the way into the item, over
// the seekable loopback URL, and retries once at another offset if what came
// back is too flat to trust.
func (t *Thumbnailer) seekThumb(ctx, cctx context.Context, it library.Item, url string, width int, durationMs int64) ([]byte, error) {
	var best []byte
	var bestSD float64
	decoded := false
	for _, offset := range thumbOffsets(durationMs) {
		data, err := t.runFrame(ctx, cctx, nil, func(out string) []string {
			return archiveSeekArgs(out, url, width, offset)
		})
		if err != nil {
			return nil, err // interrupted: no verdict on the item
		}
		if len(data) == 0 {
			continue // nothing decoded there; the other offset may do better
		}
		sd, err := lumaStdDev(data)
		if err != nil {
			t.log.Debug("archived thumbnail did not decode", "id", it.ID, "err", err)
			continue
		}
		decoded = true
		if sd > bestSD {
			best, bestSD = data, sd
		}
		if sd >= archiveThumbRetryStdDev {
			return best, nil
		}
		t.log.Debug("archived thumbnail frame is flat",
			"id", it.ID, "stddev", sd, "offset", offset)
	}
	if bestSD >= archiveThumbMinStdDev {
		return best, nil
	}
	if !decoded {
		// Not one frame came back from either offset. That is a statement
		// about the input, not about the film — an ffmpeg without the http
		// protocol, or a loopback this process cannot reach — and piping is
		// worth trying. Flat frames are the opposite: they are an answer, and
		// the piped path samples the opening minute, which is the front
		// matter this whole approach exists to seek past.
		return nil, errSeekProducedNothing
	}
	return nil, ErrNoThumb
}

// errSeekProducedNothing separates "the seekable input gave us nothing" from
// "the film is flat there", so only the first falls back to piping.
var errSeekProducedNothing = errors.New("no frame from the seekable input")

// pipedThumb is the fallback: a bounded prefix of the member on standard
// input, sampled and scored. See the comment above archivePipePrefix for
// what it can and cannot reach.
func (t *Thumbnailer) pipedThumb(ctx, cctx context.Context, it library.Item, width int) ([]byte, error) {
	// Never os.Open: the member is byte runs spread across the volumes, and
	// the reader holds only a few volume descriptors at a time. Reading it
	// sequentially keeps that to one, two while crossing a boundary.
	f, err := library.OpenItem(it)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := t.runFrame(ctx, cctx, io.LimitReader(f, archivePipePrefix),
		func(out string) []string { return archivePipeArgs(out, width) })
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		// Sampling takes one candidate every archivePipeSampleSec, so an item
		// shorter than that interval offers none at all — the clip is over
		// before the first is due. Nothing was going to be gained by choosing
		// among frames there anyway: take the first one.
		if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
			return nil, ErrNoThumb
		}
		data, err = t.runFrame(ctx, cctx, io.LimitReader(f, archivePipePrefix),
			func(out string) []string { return archiveFirstFrameArgs(out, width) })
		if err != nil {
			return nil, err
		}
	}
	if len(data) == 0 {
		return nil, ErrNoThumb
	}
	sd, err := lumaStdDev(data)
	if err != nil {
		t.log.Debug("archived thumbnail did not decode", "id", it.ID, "err", err)
		return nil, ErrNoThumb
	}
	if sd < archiveThumbMinStdDev {
		t.log.Debug("archived thumbnail frame is near-uniform", "id", it.ID, "stddev", sd)
		return nil, ErrNoThumb
	}
	return data, nil
}

// runFrame executes one ffmpeg recipe and returns the frame it wrote. The
// temp file ffmpeg writes to is made here and handed to build, so no caller
// has to invent one.
//
// No frame is not an error — the caller may try elsewhere — while an error
// means the run was interrupted, which says nothing about the item and must
// not be written off (see cached, which negative-caches everything else).
//
// cctx is what the run is given: the caller's context under the item's time
// budget. ctx is kept alongside it only to tell the two apart afterwards,
// because "the browser went away" and "we ran out of time" are different
// things to say, and neither is a verdict.
func (t *Thumbnailer) runFrame(ctx, cctx context.Context, stdin io.Reader, build func(out string) []string) ([]byte, error) {
	// ffmpeg wants a seekable output, which a pipe is not; hand it a temp
	// path and read the frame back.
	tmpf, err := os.CreateTemp("", "media-athumb-*.jpg")
	if err != nil {
		return nil, err
	}
	tmp := tmpf.Name()
	tmpf.Close()
	defer os.Remove(tmp)

	cmd := exec.CommandContext(cctx, t.ffmpeg, build(tmp)...)
	if stdin != nil {
		cmd.Stdin = stdin
		// The stdin copy runs on a goroutine that Wait blocks on, so a read
		// stuck on an unresponsive mount would hold an ffmpeg slot after the
		// process is already dead. Same bound, and the same reason, as the
		// transcoder's. The seeking path has no stdin and needs none of this.
		cmd.WaitDelay = 5 * time.Second
	}
	// The exit status is not the question: a prefix ending mid-cluster makes
	// ffmpeg complain about a truncated file while it has already written a
	// perfectly good frame. What the run was stopped *by* is the question,
	// and it has to be asked before the file is read: a killed ffmpeg leaves
	// behind whatever it had flushed, which is a JPEG missing its tail.
	_ = cmd.Run()
	if cctx.Err() != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err() // the grid recycled the cell; not a verdict
		}
		// Our own deadline. The disk was busy — which is the very thing the
		// timeout exists for — so this is not a verdict either, and wrapping
		// DeadlineExceeded is what keeps it out of the negative cache.
		return nil, fmt.Errorf("thumbnail gave up waiting for a frame: %w", context.DeadlineExceeded)
	}
	data, rerr := os.ReadFile(tmp)
	if rerr != nil || !completeJPEG(data) {
		return nil, nil
	}
	return data, nil
}

// completeJPEG reports whether data is a JPEG that reached its end-of-image
// marker — the O(1) proof that the file is the whole frame and not the part
// of it an interrupted ffmpeg had flushed. Measured: a cancelled run left
// 2149 bytes behind, which the code returned with err=nil and stored. The
// frame is decoded a moment later anyway (lumaStdDev), and Go's decoder
// rejects every truncation measured here with "short Huffman data" — but
// that is one decoder's forgiveness setting, not a property of the data, and
// nothing that permanent should rest on it.
func completeJPEG(data []byte) bool {
	return len(data) > 4 &&
		data[0] == 0xFF && data[1] == 0xD8 &&
		data[len(data)-2] == 0xFF && data[len(data)-1] == 0xD9
}

// lumaStdDev returns the standard deviation of a frame's luma — the cheap
// test for "this is one flat colour and not a picture" (see
// archiveThumbMinStdDev). jpeg.Decode hands back a *image.YCbCr for anything
// ffmpeg writes, whose Y plane is the luma already, so this is a pass over at
// most uniformitySamples bytes.
func lumaStdDev(data []byte) (float64, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	b := img.Bounds()
	total := b.Dx() * b.Dy()
	if total <= 0 {
		return 0, fmt.Errorf("empty frame")
	}
	step := max(total/uniformitySamples, 1)

	var sum, sumsq float64
	n := 0
	add := func(v float64) {
		sum += v
		sumsq += v * v
		n++
	}
	i := 0
	if yuv, ok := img.(*image.YCbCr); ok {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if i%step == 0 {
					add(float64(yuv.Y[yuv.YOffset(x, y)]))
				}
				i++
			}
		}
	} else {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if i%step == 0 {
					r, g, bl, _ := img.At(x, y).RGBA()
					add((0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257)
				}
				i++
			}
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("empty frame")
	}
	mean := sum / float64(n)
	return math.Sqrt(max(sumsq/float64(n)-mean*mean, 0)), nil
}

// spriteFrameArgs is the frame recipe for one scrub-sheet frame at t seconds
// of a plain file, decoded forward to the exact moment.
func spriteFrameArgs(path, out string, t float64) []string {
	return frameArgs(frameSpec{
		input: path, out: out, seek: strconv.FormatFloat(t, 'f', 3, 64),
		width: spriteFrameWidth, quality: 5, accurate: true,
	})
}

// archiveSpriteFrameArgs is the same frame over the loopback stream, where
// the keyframe before the moment is taken rather than decoded past.
func archiveSpriteFrameArgs(url, out string, t float64) []string {
	return frameArgs(frameSpec{
		input: url, out: out, seek: strconv.FormatFloat(t, 'f', 3, 64),
		width: spriteFrameWidth, quality: 5, loopback: true,
	})
}

// spriteOffsets is where the ten frames are taken from: evenly across the
// film, each half an interval in, so the first is past the front matter and
// the last is not the credits.
func spriteOffsets(durationMs int64) []float64 {
	interval := float64(durationMs) / 1000 / spriteFrames
	out := make([]float64, 0, spriteFrames)
	for i := range spriteFrames {
		out = append(out, interval/2+float64(i)*interval)
	}
	return out
}

// makeSprite builds the scrub sheet: ten seeks, stitched here.
//
// It used to be one ffmpeg invocation with an fps filter, which is elegant
// and reads the entire film — measured on an 87-minute 1080p release, over
// five minutes, against a two-minute timeout it could not meet. The sheet
// therefore never appeared for exactly the long films a scrub bar is for.
// Ten independent seeks produce the same ten positions in 3.5 s, because
// each reads a few megabytes around its own timestamp instead of decoding
// everything in between.
//
// They run one at a time on purpose: ten concurrent ffmpegs on one disk is
// how a preview competes with the playback it is supposed to be beside.
func (t *Thumbnailer) makeSprite(ctx context.Context, it library.Item) ([]byte, error) {
	if err := t.acquire(ctx, t.ffSem); err != nil {
		return nil, err
	}
	defer func() { <-t.ffSem }()

	budget := spriteTimeout
	if it.Archived() {
		budget = archiveSpriteTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	dir, err := os.MkdirTemp("", "media-sprite-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	// An archived member has no path for ffmpeg to open, so it is read the
	// way its thumbnail is: ranged requests against this server's own stream
	// URL, which is the only seekable view of one there is. Without a
	// loopback address there is nothing to point at and no sheet to make.
	source, archived := it.Path, false
	if it.Archived() {
		source = library.LoopbackURL(it)
		if source == "" {
			return nil, ErrNoThumb
		}
		archived = true
	}

	// One slot per offset, filled where the seek answered and left empty
	// where it did not: the sheet is laid out by position, and appending
	// only the frames that came back would shift every later one into an
	// earlier cell — and so into the wrong timestamp, which the client
	// derives from the cell alone.
	frames := make([]image.Image, spriteFrames)
	got := 0
	for i, at := range spriteOffsets(it.Duration) {
		if cctx.Err() != nil {
			break
		}
		out := filepath.Join(dir, fmt.Sprintf("%d.jpg", i))
		fctx, fcancel := context.WithTimeout(cctx, spriteFrameTimeout)
		args := spriteFrameArgs(source, out, at)
		if archived {
			args = archiveSpriteFrameArgs(source, out, at)
		}
		err := exec.CommandContext(fctx, t.ffmpeg, args...).Run()
		fcancel()
		if err != nil {
			// A seek past the end of a file whose duration was overstated,
			// or a frame ffmpeg could not decode: the sheet is still worth
			// having with a gap in it.
			continue
		}
		f, err := os.Open(out)
		if err != nil {
			continue
		}
		img, derr := jpeg.Decode(f)
		f.Close()
		if derr == nil {
			frames[i] = img
			got++
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err() // closing the player mid-run is not a verdict
	}
	if got == 0 {
		return nil, ErrNoThumb
	}
	return tileSprite(frames)
}

// tileSprite lays the frames out in the fixed grid the client derives
// positions from: frame i goes in cell i, and a nil frame — one that could
// not be taken — leaves its cell black rather than shifting every frame
// after it into the wrong timestamp.
func tileSprite(frames []image.Image) ([]byte, error) {
	first := -1
	for i, f := range frames {
		if f != nil {
			first = i
			break
		}
	}
	if first < 0 {
		return nil, ErrNoThumb
	}
	b := frames[first].Bounds()
	w, h := b.Dx(), b.Dy()
	sheet := image.NewRGBA(image.Rect(0, 0, w*spriteCols, h*spriteRows))
	stddraw.Draw(sheet, sheet.Bounds(), image.NewUniform(color.Black), image.Point{}, stddraw.Src)
	for i, f := range frames {
		if i >= spriteFrames {
			break
		}
		if f == nil {
			continue
		}
		at := image.Rect((i%spriteCols)*w, (i/spriteCols)*h, (i%spriteCols)*w+w, (i/spriteCols)*h+h)
		stddraw.Draw(sheet, at, f, f.Bounds().Min, stddraw.Src)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, sheet, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeResized decodes r, fixes JPEG EXIF orientation, downsizes to width
// and returns the encoded JPEG.
func (t *Thumbnailer) encodeResized(ctx context.Context, r io.Reader, width int) ([]byte, error) {
	if err := t.acquire(ctx, t.sem); err != nil {
		return nil, err
	}
	defer func() { <-t.sem }()

	// The dimensions live in the header, so an image too large to decode is
	// rejected from the first megabyte rather than after buffering the whole
	// file — up to half a gigabyte per decode, times a semaphore of decodes,
	// is memory this exists to not spend. A header the probe cannot read in
	// that much (a JPEG behind an outsized EXIF blob) falls through to the
	// full read and the same check below.
	head, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return nil, err
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(head)); err == nil {
		if int64(cfg.Width)*int64(cfg.Height) > maxDecodePixels {
			return nil, fmt.Errorf("image too large: %dx%d", cfg.Width, cfg.Height)
		}
	}
	rest, err := io.ReadAll(io.LimitReader(r, 512<<20-int64(len(head))))
	if err != nil {
		return nil, err
	}
	data := append(head, rest...)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxDecodePixels {
		return nil, fmt.Errorf("image too large: %dx%d", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	orientation := jpegOrientation(bytes.NewReader(data))

	small := resize(img, width)
	small = orient(small, orientation)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, small, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// resize scales img down to at most maxW wide (never upscales), flattening
// transparency onto a dark background to match the UI.
func resize(img image.Image, maxW int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	if w > maxW {
		nw = maxW
		nh = max(h*maxW/w, 1)
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	stddraw.Draw(dst, dst.Bounds(), image.NewUniform(color.RGBA{18, 20, 27, 255}), image.Point{}, stddraw.Src)
	if nw == w && nh == h {
		stddraw.Draw(dst, dst.Bounds(), img, b.Min, stddraw.Over)
	} else {
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	}
	return dst
}

// orient applies an EXIF orientation (1..8) to an image.
func orient(src *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := o >= 5 // orientations 5..8 transpose dimensions
	nw, nh := w, h
	if swap {
		nw, nh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			var sx, sy int
			switch o {
			case 2: // mirror horizontal
				sx, sy = w-1-x, y
			case 3: // rotate 180
				sx, sy = w-1-x, h-1-y
			case 4: // mirror vertical
				sx, sy = x, h-1-y
			case 5: // transpose
				sx, sy = y, x
			case 6: // rotate 90 CW
				sx, sy = y, h-1-x
			case 7: // transverse
				sx, sy = w-1-y, h-1-x
			case 8: // rotate 270 CW
				sx, sy = w-1-y, x
			}
			dst.SetRGBA(x, y, src.RGBAAt(sx, sy))
		}
	}
	return dst
}
