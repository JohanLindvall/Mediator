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
}

const (
	// analysisWindow is how much of the track each window decodes.
	analysisWindow = 20 * time.Second
	// analysisTimeout bounds one track: three decodes and the arithmetic.
	analysisTimeout = 90 * time.Second
	// analysisRest is how long the loop sleeps once nothing is left to do,
	// before looking again for files that have since arrived.
	analysisRest = time.Minute
	// analysisReport is how often progress is logged, in tracks.
	analysisReport = 250
)

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
	l.featMu.Lock()
	if l.features == nil {
		l.features = map[string]featureRec{}
	}
	l.features[id] = featureRec{mtime: mtime, size: size, vec: vec}
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
		l.SetFeatures(id, mtime, size, vec)
		n++
	})
	if n > 0 {
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
	return !ok || rec.mtime != it.ModTime || rec.size != it.Size
}

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
		l.mu.RLock()
		var todo []string
		for id, it := range l.items {
			if l.needsAnalysis(it) {
				todo = append(todo, id)
			}
		}
		l.mu.RUnlock()
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

// analyzeAll reads the given tracks one at a time, yielding to everything
// else, and reports as it goes.
func (l *Library) analyzeAll(ctx context.Context, db *blob.DB, todo []string, busy func() bool) {
	start := time.Now()
	done, failed := 0, 0
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
		switch err := l.analyzeOne(ctx, db, it); {
		case err == nil:
			done++
		case ctx.Err() != nil:
			return
		default:
			failed++
			l.markFailed(it)
			l.log.Debug("audio analysis failed", "path", it.Rel, "err", err)
		}
		if (done+failed)%analysisReport == 0 {
			l.log.Info("audio analysis", "done", done, "failed", failed, "left", len(todo)-done-failed,
				"per_track", (time.Since(start) / time.Duration(done+failed)).Round(time.Millisecond))
			// What the collections say changes as the vectors land — an
			// audiobook shelved, a resemblance found — and they are cached
			// per version, so the version moves now and then rather than
			// only at the end of a pass that takes the better part of a
			// day. Every track would be a rebuild a second.
			l.Touch()
		}
	}
	l.log.Info("audio analysis pass complete", "done", done, "failed", failed,
		"took", time.Since(start).Round(time.Second))
	if done > 0 {
		l.Touch()
	}
}

// Touch announces a change nothing else announced: the analysis alters
// what the collections say without altering the index, and a test that
// seeds vectors is in the same position. The version moves and everything
// cached per version is rebuilt on its next reading.
func (l *Library) Touch() { l.notify() }

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
func (l *Library) analyzeOne(ctx context.Context, db *blob.DB, it Item) error {
	ctx, cancel := context.WithTimeout(ctx, analysisTimeout)
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
	if db != nil {
		if err := db.PutFeatures(it.ID, it.ModTime, it.Size, featuresVersion, vec); err != nil {
			return err
		}
	}
	l.SetFeatures(it.ID, it.ModTime, it.Size, vec)
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
