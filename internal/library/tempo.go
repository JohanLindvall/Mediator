package library

// The tempo a track is shown with.
//
// Column 50 of the vector is a tempo too, and it is the wrong one to show.
// It is read at whole lags of an onset envelope sampled 43 times a second,
// so it can only ever say 117.5, 123.0, 129.2, 136.0, 143.6 — steps of five
// to ten beats a minute exactly where most music is. That serves what the
// vector is for, telling one track's feel from another's, and it is off by
// four as a number on a screen. It stays exactly as it is, the vectors of the
// whole library being comparable only while their recipe holds still; this
// is read beside it, from the same decode and the same onsets, and kept in a
// record of its own (blob.PutTempo) with a version that is free to move.
//
// What changed is how the period is found. The envelope keeps time here —
// a value for every frame, nought where nothing sounded — where the vector's
// onsets run the sounding frames together, and each window is correlated with
// itself on its own, so the join between two windows minutes apart is not
// read as a beat. A candidate tempo is scored by the correlation at its
// period and the next three multiples (a steady beat repeats at all of them)
// under the same prior toward 120 every tempo estimator uses, since without
// one the octave is a coin toss — and the candidates are a quarter of a beat
// a minute apart, **the correlation read between frames** at each
// (correlationAt): a period is rarely a whole number of frames, and reading
// it at the nearest one is exactly what makes column 50 step.
//
// Measured against the 86 tracks in this library whose tags carry a
// plausible tempo (of 587 with the frame at all: 447 said nought and 46 said
// 320, a bitrate in the wrong field): 72 are shown, and of those 49 are
// within 3% of the tag and 47 within one beat a minute. Many readings land
// on a whole number — about a third across a sample of the whole library,
// which is what a track made on a grid is — and where they differ from the
// tag it is most often by exactly one above it, which reads as tags rounded
// down. The rest is the estimator's octave, 13 at half, double or two thirds
// of the tag, and 10 others, most of them one release's alternate versions.
// Of 150 tracks drawn from the whole library, 80 are shown a tempo. Reading
// the period off the peaks at its multiples as well, interpolated between
// frames, was tried and measured: it agreed with this to a fifth of a beat on
// every track, and is not here; nor is smoothing the envelope, which hid
// real tempos long before it stopped a vibrato reading as one.
//
// A tempo is shown only where the onsets repeat clearly (bpmClarity): below
// it, measured on the same tracks, 4 of 14 readings agreed with the tag,
// where above it 49 of 72 did — and only where anything is struck at all
// (bpmOnsetFloor, and the band floor under the envelope, tempoTopDB; bpmOf
// says why each is there). Speech has no tempo, and a reading is never
// shown one (stamp).

import (
	"math"
	"slices"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

const (
	// tempoVersion is stamped on every stored tempo. Raise it when the
	// recipe changes and every track is read again for it; the vectors are
	// not, being kept under their own (featuresVersion).
	tempoVersion = 1
	// The tempos a track may be read at, a quarter of a beat a minute
	// apart — finer than anything shown, which is a whole number.
	bpmMin, bpmMax, bpmStep = 50.0, 220.0, 0.25
	// bpmMultiples is how many multiples of a period score it.
	bpmMultiples = 4
	// bpmClarity is how strongly the onsets must repeat at a tempo, on
	// average over its period and the multiples, for it to be shown.
	bpmClarity = 0.3
	// bpmOnsetFloor is how strong the strongest onsets must be — the 99th
	// percentile of the envelope, in the sum of the bands' rises in log
	// power — for anything they repeat at to be a beat. See bpmOf.
	bpmOnsetFloor = 8
)

// tempoRec is one track's tempo, with the stamp of the file it was read from.
type tempoRec struct {
	mtime, size  int64
	bpm, clarity float32
}

// shown is the tempo worth showing for the file as it is now, or nought:
// none read for it, one read from a file that has since changed, or a pulse
// too faint to be a tempo.
func (r tempoRec) shown(it *Item) float64 {
	if r.mtime != it.ModTime || r.size != it.Size || r.clarity < bpmClarity || r.bpm <= 0 {
		return 0
	}
	return math.Round(float64(r.bpm)*10) / 10
}

// bpmOf reads the tempo from each window's onset envelope, and how clearly
// the onsets repeat at it, 0..1. Nought where no window is long enough to
// hold four periods of the slowest tempo twice over, where nothing repeats,
// or where nothing is struck.
//
// **Clarity is not enough by itself**, because it is a ratio: the
// correlation over the envelope's own energy says how regular the onsets
// are, and nothing about whether there are any. A sustained chord's envelope
// is a faint ripple, and a faint ripple can be perfectly regular — measured
// on chords made into MP3s, what was left of the encoder's flicker after the
// floor in describeWindows (tempoTopDB) still read as 141 at a clarity over
// the bar. So the strongest onsets have to reach bpmOnsetFloor as well:
// those chords peaked at 3.4 and a drone at 0.4, where the weakest real
// track shown — a dense, loud mix whose every onset is small — peaked at 15.
// What it cannot tell apart is a sound that really does move regularly with
// nothing struck: a note under a strong vibrato reads as a slow multiple of
// the vibrato, and is shown one.
func bpmOf(beats [][]float64) (bpm, clarity float64) {
	if onsetPeak(beats) < bpmOnsetFloor {
		return 0, 0
	}
	return tempoFrom(beats)
}

// tempoFrom is the tempo the onsets repeat at and how clearly, however faint
// the onsets are: bpmOf without the question of whether there are any.
func tempoFrom(beats [][]float64) (bpm, clarity float64) {
	const fps = float64(featRate) / featHop
	maxLag := int(math.Ceil(fps*60/bpmMin*bpmMultiples)) + 2
	r := onsetCorrelation(beats, maxLag)
	if r == nil {
		return 0, 0
	}
	best, bestT := math.Inf(-1), 0.0
	for t := bpmMin; t <= bpmMax; t += bpmStep {
		p := fps * 60 / t
		var s float64
		for k := 1; k <= bpmMultiples; k++ {
			s += correlationAt(r, float64(k)*p)
		}
		s /= bpmMultiples
		if score := s * tempoPrior(t); score > best {
			best, bestT, clarity = score, t, s
		}
	}
	if bestT == 0 || clarity <= 0 {
		return 0, 0
	}
	return bestT, clarity
}

// onsetPeak is how strong the strongest onsets are: the 99th percentile of
// every frame of every window — a few dozen frames of a minute's reading,
// so one stray click does not count as a beat and a track struck twice a
// second does.
func onsetPeak(beats [][]float64) float64 {
	var all []float64
	for _, env := range beats {
		all = append(all, env...)
	}
	if len(all) == 0 {
		return 0
	}
	slices.Sort(all)
	return all[int(0.99*float64(len(all)-1))]
}

// tempoPrior leans toward 120 by an octave's width, the way column 50 does.
func tempoPrior(bpm float64) float64 {
	return math.Exp(-0.5 * math.Pow(math.Log2(bpm/120), 2))
}

// onsetCorrelation is the autocorrelation of each window's envelope, less
// its mean and over its own energy, averaged over the windows long enough
// to say anything at every lag up to maxLag. Nil where none is.
func onsetCorrelation(beats [][]float64, maxLag int) []float64 {
	sum := make([]float64, maxLag+1)
	used := 0
	x := []float64{}
	for _, env := range beats {
		if len(env) < 2*maxLag {
			continue
		}
		var mean float64
		for _, v := range env {
			mean += v
		}
		mean /= float64(len(env))
		x = x[:0]
		for _, v := range env {
			x = append(x, v-mean)
		}
		var energy float64
		for _, v := range x {
			energy += v * v
		}
		if energy <= 0 {
			continue
		}
		for lag := 0; lag <= maxLag; lag++ {
			var acc float64
			for i := lag; i < len(x); i++ {
				acc += x[i] * x[i-lag]
			}
			sum[lag] += acc / energy
		}
		used++
	}
	if used == 0 {
		return nil
	}
	for lag := range sum {
		sum[lag] /= float64(used)
	}
	return sum
}

// correlationAt reads the correlation between two frames, by the parabola
// through the three nearest: a tempo's period is rarely a whole number of
// frames, and reading it at the nearest one is what makes column 50 step.
func correlationAt(r []float64, lag float64) float64 {
	i := int(math.Round(lag))
	if i < 1 || i >= len(r)-1 {
		if i >= 0 && i < len(r) {
			return r[i]
		}
		return 0
	}
	d := lag - float64(i)
	a, b, c := r[i-1], r[i], r[i+1]
	return b + 0.5*d*(c-a) + 0.5*d*d*(a-2*b+c)
}

// putTempo records a track's tempo without publishing it; the analysis
// publishes on its own beat (analyzeAll), as it does the vectors.
func (l *Library) putTempo(id string, mtime, size int64, bpm, clarity float32) {
	l.featMu.Lock()
	if l.tempos == nil {
		l.tempos = map[string]tempoRec{}
	}
	l.tempos[id] = tempoRec{mtime: mtime, size: size, bpm: bpm, clarity: clarity}
	l.featMu.Unlock()
}

// publishTempos makes the tempos read so far what the listings show and
// sort by: a copy, so a page stamps from one publish's answer while the
// analysis goes on writing the map.
func (l *Library) publishTempos() {
	l.featMu.Lock()
	view := make(map[string]tempoRec, len(l.tempos))
	for id, r := range l.tempos {
		view[id] = r
	}
	l.tempoView = view
	l.featMu.Unlock()
}

// tempoSnapshot is the published tempos, to be read without a lock.
func (l *Library) tempoSnapshot() map[string]tempoRec {
	l.featMu.RLock()
	defer l.featMu.RUnlock()
	return l.tempoView
}

// tempoCurrent says whether a tempo has been read for the file as it is
// now. Caller holds featMu.
func (l *Library) tempoCurrent(it *Item) bool {
	r, ok := l.tempos[it.ID]
	return ok && r.mtime == it.ModTime && r.size == it.Size
}

// LoadTempos restores the tempos the database holds. One written under an
// older recipe is left out, so the track is read again for it.
func (l *Library) LoadTempos(db *blob.DB) int {
	n := 0
	db.EachTempo(func(id string, mtime, size int64, version int, bpm, clarity float32) {
		if version != tempoVersion {
			return
		}
		l.putTempo(id, mtime, size, bpm, clarity)
		n++
	})
	l.publishTempos()
	if n > 0 {
		l.log.Info("tempos restored", "tracks", n)
	}
	return n
}
