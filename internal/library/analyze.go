package library

// Reading how the music sounds, in the background.
//
// The fourth and lowest tier of background work: below playback, below
// thumbnails, below tag reading. Each audio item is decoded once — three
// twenty-second windows from the middle of it, over ffmpeg, by path or over
// the loopback URL for an archived member — and described by extractFeatures.
// The vector is written to the blob database stamped with the file's mtime
// and size, exactly as thumbnails and metadata are, so a restart never reads
// a file twice and a changed file is read again. What the vectors are for is
// in similar.go and spoken.go.
//
// Measured cost is about a second of one core per track, so a large library
// takes hours the first time: the loop runs one track at a time, only while
// nothing streams, no thumbnail is being made and no tag pass is running,
// and it sleeps between passes. Interrupted work is not written down.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// featureRec is one track's vector, with the stamp of the file it was read
// from. A decode that failed is a record with no vector and failed set:
// remembered for the run so the pass does not run ffmpeg on the same broken
// file every hour, never written down so a restart tries once more — the
// thumbnailer's rule for a run that produced nothing.
type featureRec struct {
	mtime, size int64
	vec         []float32
	failed      bool
	// retry is when a read that was interrupted — our own deadline, or the
	// caller's — may be tried again. An interruption is not a verdict, so
	// it must not be written down as one; but it must not be tried again a
	// minute later either, since what the deadline is usually saying is
	// that the disk is busy, and a track that always overruns would then be
	// ninety seconds of ffmpeg on every pass for the life of the process.
	retry time.Time
}

const (
	// analysisWindow is how much of the track each window decodes.
	analysisWindow = 20 * time.Second
	// analysisRest is how long the loop sleeps once nothing is left to do,
	// before looking again for files that have since arrived.
	analysisRest = time.Minute
	// analysisReport is how often progress is logged, in tracks.
	analysisReport = 250
	// analysisRetryAfter is how long a track whose read ran out of time is
	// left alone before it is offered again. Long enough that a busy disk
	// has stopped being busy, short enough that it is tried several times
	// in the working day a whole pass takes.
	analysisRetryAfter = time.Hour
)

// analysisTimeout bounds one track: three decodes and the arithmetic. It is
// a var rather than a const for one reason — a test spends it up front to
// prove that a budget that runs out is not a verdict, which is the whole of
// what the switch in analyzeAll is for. Nothing changes it at runtime.
var analysisTimeout = 90 * time.Second

var (
	ffmpegOnce sync.Once
	ffmpegPath string
)

// FFmpegPath is where ffmpeg was found, or "" when it is not installed.
func FFmpegPath() string {
	ffmpegOnce.Do(func() { ffmpegPath, _ = exec.LookPath("ffmpeg") })
	return ffmpegPath
}

// SetFeatures records a track's vector, replacing what was there. Used by
// the analysis, by the loader, and by tests that seed a library.
func (l *Library) SetFeatures(id string, mtime, size int64, vec []float32) {
	l.putFeatures(id, mtime, size, vec)
	// Publish, not merely bump: the collections are cached on the library
	// version and group speech apart by the vectors, so a vector arriving
	// through this door — the loader and the tests — must rebuild them, or
	// Albums() serves a shelf that does not know about it.
	l.publishAnalysis()
}

// putFeatures writes a vector without publishing it.
//
// The generation is what the scaled vectors and the affinity ranking are
// cached against, and both are rebuilt on the request path — a listing
// stamps every item it hands out with them. Moving it per track meant that
// during a pass, which writes one a second for hours, **every request
// rebuilt both**: the whole library z-scored again and every analysed track
// measured against every verdict, for one new song. Measured on the live
// library while a pass ran: a listing that answers in 270 ms was taking 10
// to 50 seconds.
//
// So the pass writes with this and publishes on the same beat it moves the
// version (see analyzeAll) — the same reasoning one layer down, which is
// where it had been left out.
func (l *Library) putFeatures(id string, mtime, size int64, vec []float32) {
	l.featMu.Lock()
	if l.features == nil {
		l.features = map[string]featureRec{}
	}
	l.features[id] = featureRec{mtime: mtime, size: size, vec: vec}
	l.featMu.Unlock()
}

// bumpFeatures publishes what has been written: the next reading rebuilds
// the scaled vectors, the resemblances and the affinity ranking.
func (l *Library) bumpFeatures() {
	l.featMu.Lock()
	l.featuresGen++
	l.featMu.Unlock()
}

// featuresOf answers a track's vector as it stands, nil for one not read.
func (l *Library) featuresOf(id string) []float32 {
	l.featMu.RLock()
	defer l.featMu.RUnlock()
	return l.features[id].vec
}

// markFailed remembers, for this run only, that a track could not be read.
func (l *Library) markFailed(it Item) {
	l.featMu.Lock()
	if l.features == nil {
		l.features = map[string]featureRec{}
	}
	l.features[it.ID] = featureRec{mtime: it.ModTime, size: it.Size, failed: true}
	l.featMu.Unlock()
}

// markInterrupted remembers that a track's read ran out of time, so the
// pass stops offering it for a while. Nothing else in the record is
// touched: an interrupted read produced no answer, and neither the stamp
// nor whatever vector was there describes anything new. This is the
// thumbnailer's rule — a timeout is never a verdict — with the one thing a
// loop needs that a request does not, which is somewhere to wait.
func (l *Library) markInterrupted(it Item) {
	l.featMu.Lock()
	if l.features == nil {
		l.features = map[string]featureRec{}
	}
	rec := l.features[it.ID]
	rec.retry = time.Now().Add(analysisRetryAfter)
	l.features[it.ID] = rec
	l.featMu.Unlock()
}

// LoadFeatures restores the vectors the database holds. Ones written under
// an older recipe are left out, so they are read again under the new one.
// An empty vector is restored too: it is a track that decoded to silence,
// analysed and with nothing to say, and reading it again on every restart
// was every silent file decoded for nothing.
func (l *Library) LoadFeatures(db *blob.DB) int {
	n := 0
	db.EachFeatures(func(id string, mtime, size int64, version int, vec []float32) {
		if version != featuresVersion || (len(vec) != featureDims && len(vec) != 0) {
			return
		}
		l.putFeatures(id, mtime, size, vec)
		n++
	})
	if n > 0 {
		// One publish for the whole restore, not one per track: the
		// collections rebuild once, against the batch.
		l.publishAnalysis()
		l.log.Info("audio features restored", "tracks", n)
	}
	return n
}

// needsAnalysis says whether a track is still to be read: audio, and either
// never described or described for a file that has since changed.
func (l *Library) needsAnalysis(it *Item) bool {
	if it.Kind != KindAudio {
		return false
	}
	l.featMu.RLock()
	rec, ok := l.features[it.ID]
	l.featMu.RUnlock()
	if !rec.retry.IsZero() && time.Now().Before(rec.retry) {
		return false // asked for and interrupted: not now, rather than never
	}
	return !ok || rec.mtime != it.ModTime || rec.size != it.Size
}

// readyForAnalysis says whether enough is known about a track to describe
// it properly. The windows are a quarter, a half and three quarters of the
// way through, so they are chosen from the duration — and a file nothing
// has examined yet has none, which sends the read to the fixed marks
// meant for a file whose length *cannot* be read. That answer is then
// permanent: the vector is stamped with the file's mtime and size, and
// nothing re-reads it until the file itself changes. A length that is
// merely late is worth waiting for; one that never comes is what the fixed
// marks are for, and an examined file has settled the difference.
func readyForAnalysis(it *Item) bool { return it.Duration > 0 || it.enriched }

// AnalyzeLoop reads every audio track's features, forever: a pass over what
// is missing, a rest, and another look for what arrived meanwhile. It waits
// while busy says higher-priority work is running, and while a tag pass is.
// Without ffmpeg there is nothing to decode with, and it returns at once.
func (l *Library) AnalyzeLoop(ctx context.Context, db *blob.DB, busy func() bool) {
	if FFmpegPath() == "" {
		l.log.Info("audio analysis off: ffmpeg not found")
		return
	}
	if busy == nil {
		busy = func() bool { return false }
	}
	for {
		todo := l.analysisTodo()
		if len(todo) > 0 {
			l.analyzeAll(ctx, db, todo, busy)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(analysisRest):
		}
	}
}

// analysisTodo is what the next pass has to read: the tracks that still
// want reading and are ready to be read.
//
// Readiness belongs here and not only in the pass. Queued and then passed
// over, a track nothing has examined keeps the list non-empty for ever: the
// loop would enter a pass every minute, log a start and a completion with
// nothing done, and — with something streaming — wait two seconds at the
// gate for each track it was never going to read. Left out of the list, an
// unready track simply waits for the tag pass to reach it, which is what
// the rest is for.
func (l *Library) analysisTodo() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var todo []string
	for id, it := range l.items {
		if l.needsAnalysis(it) && readyForAnalysis(it) {
			todo = append(todo, id)
		}
	}
	return todo
}

// analyzeAll reads the given tracks one at a time, yielding to everything
// else, and reports as it goes.
func (l *Library) analyzeAll(ctx context.Context, db *blob.DB, todo []string, busy func() bool) {
	start := time.Now()
	done, failed, published := 0, 0, 0
	l.log.Info("audio analysis starting", "tracks", len(todo))
	for _, id := range todo {
		for busy() || l.enriching.Load() > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
		if ctx.Err() != nil {
			return
		}
		it, ok := l.Get(id)
		if !ok {
			continue
		}
		if !readyForAnalysis(&it) {
			// Asked again because the file may have changed since the list
			// was built, and a file that changed forgets what was read from
			// it (forgetContent) — its length with the rest. Describing it
			// from the fixed marks now would describe the wrong seconds of
			// it for good, the vector being stamped with the file.
			continue
		}
		switch err := l.analyzeOne(ctx, db, it); {
		case err == nil:
			done++
		case ctx.Err() != nil:
			return
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
			// Our own budget, not the pass's — analyzeOne bounds each track
			// — so the parent is still live and the switch above did not
			// catch it. An interruption is not an answer: recording it as
			// one wrote the track off for the whole run, and that is a
			// track absent from every resemblance, from radio, from its
			// release's sound and from the vote that shelves an audiobook.
			l.markInterrupted(it)
			l.log.Debug("audio analysis ran out of time", "path", it.Rel)
			continue
		default:
			failed++
			l.markFailed(it)
			l.log.Debug("audio analysis failed", "path", it.Rel, "err", err)
		}
		// Only a track that was read or refused reaches this; the ones
		// passed over count towards nothing, and the average below divides
		// by how many were actually read.
		if (done+failed)%analysisReport == 0 {
			l.log.Info("audio analysis", "done", done, "failed", failed, "left", len(todo)-done-failed,
				"per_track", (time.Since(start) / time.Duration(done+failed)).Round(time.Millisecond))
			// What the collections say changes as the vectors land — an
			// audiobook shelved, a resemblance found — and they are cached
			// per version, so the version moves now and then rather than
			// only at the end of a pass that takes the better part of a day.
			// Only where a vector was actually read, though: a run of
			// unreadable files publishes nothing and must not discard every
			// cache for a batch that added nothing.
			if done > published {
				l.publishAnalysis()
				published = done
			}
		}
	}
	l.log.Info("audio analysis pass complete", "done", done, "failed", failed,
		"took", time.Since(start).Round(time.Second))
	if done > published {
		l.publishAnalysis()
	}
}

// Touch announces a change nothing else announced: the analysis alters
// what the collections say without altering the index, and a test that
// seeds vectors is in the same position. The version moves and everything
// cached per version is rebuilt on its next reading.
func (l *Library) Touch() { l.notify() }

// publishAnalysis makes a batch of freshly read vectors visible: the
// resemblances, the affinity ranking and the shelves are all cached against
// the features generation or the version, and this moves both. It is the
// pass's own beat — every few hundred tracks and at the end — because each
// of those caches is rebuilt by the next request that needs it, and a
// generation that moved once a second made every request pay for it.
func (l *Library) publishAnalysis() {
	l.bumpFeatures()
	l.notify()
}

// analysisOffsets is where the windows start, in seconds: a quarter, a half
// and three quarters of the way through, the front matter and the fade left
// out — the same reasoning as the thumbnail's offset. A short track is read
// whole; one whose length nobody measured is sampled at fixed marks and
// gives back what it has.
func analysisOffsets(durationMs int64) []float64 {
	d := float64(durationMs) / 1000
	w := analysisWindow.Seconds()
	switch {
	case d <= 0:
		return []float64{30, 90, 150}
	case d <= w*1.5:
		return []float64{0}
	case d <= 4*w:
		return []float64{(d - w) / 2}
	default:
		return []float64{d * 0.25, d * 0.5, d * 0.75}
	}
}

// analyzeOne decodes one track's windows, describes them, and writes the
// vector down. A track that decodes to silence, or to nothing at all, is
// written down as an empty vector: analysed, nothing to say, and not to be
// tried again until the file changes. The windows are described as
// separate stretches (extractFeaturesFrom): joined end to end, the frame
// straddling two of them read as an onset and a seam in the envelope that
// the tempo and syllable cues then measured.
func (l *Library) analyzeOne(parent context.Context, db *blob.DB, it Item) error {
	ctx, cancel := context.WithTimeout(parent, analysisTimeout)
	defer cancel()
	input, extra, err := analysisInput(it)
	if err != nil {
		return err
	}
	var windows [][]float32
	for _, off := range analysisOffsets(it.Duration) {
		samples, err := decodeWindow(ctx, input, extra, off)
		if err != nil {
			return err
		}
		if len(samples) > 0 {
			windows = append(windows, samples)
		}
	}
	if len(windows) == 0 && it.Duration <= 0 {
		// A track whose length nobody measured is sampled at fixed marks,
		// and one shorter than the first of them gave back nothing at all
		// and was written down as silence for good. Read it from the start.
		samples, err := decodeWindow(ctx, input, extra, 0)
		if err != nil {
			return err
		}
		if len(samples) > 0 {
			windows = append(windows, samples)
		}
	}
	vec := extractFeaturesFrom(windows)
	if vec == nil {
		vec = []float32{}
	}
	// The vector is kept in memory first: a database write that fails is a
	// storage fault, not a track that could not be read, and returning it
	// as a decode failure would markFailed the track and throw the vector
	// away — read again next run for nothing.
	l.putFeatures(it.ID, it.ModTime, it.Size, vec)
	if parent.Err() != nil {
		// Shutting down, so this write is certainly pointless: the analysis
		// is not one of the loops shutdown waits for, and the database is
		// being closed while the last decode of the run is still finishing.
		// Say plainly what this is and is not. It is a narrowing, not a
		// fix: the cancellation can as easily land a nanosecond later, and
		// then the write goes to a database that is closing anyway — which
		// bolt refuses rather than corrupting anything, since a transaction
		// takes the same locks Close does. The fix proper is for shutdown to
		// wait for this goroutine as it waits for the persist loop and the
		// state store, which is main's to make. The vector stays in memory,
		// which is where a refused write leaves it; the next run reads the
		// file again.
		return nil
	}
	if db != nil {
		if err := db.PutFeatures(it.ID, it.ModTime, it.Size, featuresVersion, vec); err != nil {
			l.log.Debug("audio features not stored", "path", it.Rel, "err", err)
		}
	}
	return nil
}

// analysisInput is what ffmpeg opens: the path, or the loopback URL for
// content that has none, with the options that read needs.
func analysisInput(it Item) (input string, extra []string, err error) {
	if !it.Archived() {
		return it.Path, nil, nil
	}
	u := LoopbackURL(it)
	if u == "" {
		return "", nil, fmt.Errorf("no way to read an archived member without a loopback address")
	}
	return u, []string{
		"-rw_timeout", strconv.Itoa(int((30 * time.Second) / time.Microsecond)),
		"-headers", LoopbackHeaderArg(),
	}, nil
}

// decodeWindow returns analysisWindow of mono audio at featRate from the
// given offset, as ffmpeg decodes it. Past the end of the file it returns
// nothing, which is not an error.
func decodeWindow(ctx context.Context, input string, extra []string, offset float64) ([]float32, error) {
	args := []string{"-nostdin", "-v", "error"}
	args = append(args, extra...)
	args = append(args,
		"-ss", strconv.FormatFloat(offset, 'f', 2, 64),
		"-t", strconv.FormatFloat(analysisWindow.Seconds(), 'f', 0, 64),
		"-i", input,
		"-vn", "-sn", "-dn",
		"-ac", "1", "-ar", strconv.Itoa(featRate),
		"-f", "f32le", "pipe:1")
	cmd := exec.CommandContext(ctx, FFmpegPath(), args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, bytes.TrimSpace(errb.Bytes()))
	}
	b := out.Bytes()
	pcm := make([]float32, len(b)/4)
	for i := range pcm {
		pcm[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return pcm, nil
}
