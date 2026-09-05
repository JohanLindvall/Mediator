package library

// How a track sounds, as numbers.
//
// A fixed-length description of a stretch of audio, built the classic way:
// short frames, a power spectrum each, and statistics over the frames of
// what the spectrum says — timbre (mel cepstral coefficients), brightness
// and breadth (centroid, bandwidth, rolloff, flatness), harmony (chroma),
// loudness and dynamics, and tempo read off the onset envelope. Fifty-six
// numbers. Two tracks that sound alike land near each other, and "near" is
// measured in similar.go after every column has been scaled to the library.
//
// It is deliberately the unglamorous version: pure Go, no model, no
// dependency, about a tenth of a second per minute of audio. It groups by
// production, tempo and timbre — which is what it is — and the doc in
// CLAUDE.md is honest about what that does and does not find.

import (
	"math"
	"math/cmplx"
	"slices"
	"sync"
)

const (
	// featRate is the rate the audio is decoded to. Nothing above 11 kHz
	// tells the features apart, and halving the rate halves the work.
	featRate  = 22050
	featFrame = 2048
	featHop   = 512
	featMels  = 40
	featMFCC  = 13
	// featureDims is the length of the vector; the layout is in extract.
	featureDims = 56
	// featuresVersion is stamped on every stored vector. Raise it when the
	// recipe changes and every track is analysed again — the number is what
	// lets a new column reach the tracks already read (cf. shapeVersion).
	featuresVersion = 2
	// featSilence is the frame loudness, in dB of full scale, below which a
	// frame is silence and says nothing about the track.
	featSilence = -60
)

// fftPlan is a radix-2 plan for one size: the bit-reversal permutation and
// the twiddle factors, built once.
type fftPlan struct {
	n    int
	rev  []int
	twid []complex128
}

var fftPlans sync.Map

func planFFT(n int) *fftPlan {
	if p, ok := fftPlans.Load(n); ok {
		return p.(*fftPlan)
	}
	p := &fftPlan{n: n, rev: make([]int, n), twid: make([]complex128, n/2)}
	bits := 0
	for 1<<bits < n {
		bits++
	}
	for i := range n {
		r := 0
		for b := range bits {
			if i&(1<<b) != 0 {
				r |= 1 << (bits - 1 - b)
			}
		}
		p.rev[i] = r
	}
	for k := range n / 2 {
		p.twid[k] = cmplx.Exp(complex(0, -2*math.Pi*float64(k)/float64(n)))
	}
	fftPlans.Store(n, p)
	return p
}

// transform runs the FFT in place; n must be a power of two.
func (p *fftPlan) transform(x []complex128) {
	n := p.n
	for i, r := range p.rev {
		if i < r {
			x[i], x[r] = x[r], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half, step := size/2, n/size
		for start := 0; start < n; start += size {
			for k := range half {
				t := p.twid[k*step] * x[start+k+half]
				u := x[start+k]
				x[start+k] = u + t
				x[start+k+half] = u - t
			}
		}
	}
}

// melOf and hzOf are the HTK mel scale.
func melOf(hz float64) float64 { return 2595 * math.Log10(1+hz/700) }
func hzOf(mel float64) float64 { return 700 * (math.Pow(10, mel/2595) - 1) }

// melBank is the triangular filterbank over the power spectrum's bins. Each
// filter is kept as the run of bins it covers and its weights over them: a
// filter is a few dozen bins wide out of a thousand, and a dense row per
// filter had the hot loop test a thousand zeros to find them.
type melBank struct {
	filters []melFilter
}

type melFilter struct {
	start int
	w     []float64
}

func newMelBank(rate, frame, bands int) *melBank {
	bins := frame/2 + 1
	lo, hi := melOf(20), melOf(float64(rate)/2)
	edges := make([]float64, bands+2)
	for i := range edges {
		edges[i] = hzOf(lo + (hi-lo)*float64(i)/float64(bands+1))
	}
	mb := &melBank{filters: make([]melFilter, bands)}
	for b := range bands {
		var f melFilter
		f.start = -1
		for k := range bins {
			hz := float64(k) * float64(rate) / float64(frame)
			var w float64
			switch {
			case hz >= edges[b] && hz <= edges[b+1]:
				w = (hz - edges[b]) / (edges[b+1] - edges[b])
			case hz > edges[b+1] && hz <= edges[b+2]:
				w = (edges[b+2] - hz) / (edges[b+2] - edges[b+1])
			}
			if w == 0 {
				if f.start >= 0 && hz > edges[b+2] {
					break
				}
				continue
			}
			if f.start < 0 {
				f.start = k
			}
			f.w = append(f.w, w)
		}
		if f.start < 0 {
			f.start = 0
		}
		mb.filters[b] = f
	}
	return mb
}

var (
	melOnce sync.Once
	theMel  *melBank
	hann    []float64
	dct     [featMFCC][featMels]float64 // the cepstrum's cosines, once rather than per frame
	pitchOf []int                       // pitch class per bin, -1 outside the range chroma reads
)

func featSetup() {
	melOnce.Do(func() {
		theMel = newMelBank(featRate, featFrame, featMels)
		hann = make([]float64, featFrame)
		for i := range hann {
			hann[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(featFrame))
		}
		for i := range featMFCC {
			for b := range featMels {
				dct[i][b] = math.Cos(math.Pi * float64(i+1) * (float64(b) + 0.5) / featMels)
			}
		}
		pitchOf = make([]int, featFrame/2+1)
		for k := range pitchOf {
			f := float64(k) * featRate / featFrame
			if f < 55 || f > 8000 {
				pitchOf[k] = -1
				continue
			}
			midi := 69 + 12*math.Log2(f/440)
			pc := int(math.Round(midi)) % 12
			if pc < 0 {
				pc += 12
			}
			pitchOf[k] = pc
		}
	})
}

// frameStats accumulates one column's mean and standard deviation.
type frameStats struct{ n, sum, sq float64 }

func (s *frameStats) add(v float64) { s.n++; s.sum += v; s.sq += v * v }
func (s *frameStats) mean() float64 {
	if s.n == 0 {
		return 0
	}
	return s.sum / s.n
}
func (s *frameStats) std() float64 {
	if s.n == 0 {
		return 0
	}
	m := s.mean()
	return math.Sqrt(math.Max(0, s.sq/s.n-m*m))
}

// extractFeatures describes mono audio at featRate. It answers nil for
// audio that is silence throughout — there is nothing to describe — and
// otherwise exactly featureDims finite numbers:
//
//	 0-12  mel cepstral coefficients 1-13, mean over frames (timbre)
//	13-25  the same, standard deviation
//	26-33  spectral centroid, bandwidth, rolloff and flatness: mean, std of each
//	34-45  chroma, mean over frames (harmony; C first)
//	46-48  loudness: mean dB, std dB, and the range between quiet and loud frames
//	49     zero-crossing rate, mean
//	50-51  tempo in beats per minute, and how strongly the onsets repeat at it
//	52     pauses: the share of frames well below the track's loud ones,
//	       counted between the first and the last sound — the silence at a
//	       short track's ends is not a reader drawing breath
//	53     syllables: how much of the loudness envelope moves at 3-8 Hz
//	54     zero-crossing rate, standard deviation
//	55     how many seconds actually sounded, which is what says whether
//	       the rest can be trusted at all
//
// Columns 52-54 are what tells speech from music (spoken.go), and 55 is
// what says whether there was enough of it to tell.
func extractFeatures(pcm []float32) []float32 {
	return extractFeaturesFrom([][]float32{pcm})
}

// extractFeaturesFrom describes several stretches of one track as one: the
// statistics run over all of them, but each stretch starts its own onset
// history and its own loudness envelope, so the join between two windows
// cut from different minutes of a track is not read as a beat or as a swell
// — it is nothing the track did.
func extractFeaturesFrom(windows [][]float32) []float32 {
	featSetup()
	bins := featFrame/2 + 1
	buf := make([]complex128, featFrame)
	power := make([]float64, bins)
	plan := planFFT(featFrame)
	var mfcc [featMFCC]frameStats
	var centroid, bandwidth, rolloff, flatness, loud, zcr frameStats
	var chroma [12]float64
	chromaFrames := 0
	var loudness []float64
	var envelopes [][]float64 // per-frame RMS per window, silent frames included
	silent := 0               // silent frames between the first and last sound
	firstSound, lastSound := -1, -1
	frames := 0
	var onsets []float64
	prevMel := make([]float64, featMels)
	mel := make([]float64, featMels)
	logMel := make([]float64, featMels)

	for _, pcm := range windows {
		if len(pcm) < featFrame {
			continue
		}
		var envelope []float64
		hasPrev := false // an onset is a change since the frame before, within one window
		for start := 0; start+featFrame <= len(pcm); start += featHop {
			frame := pcm[start : start+featFrame]
			// Loudness and zero crossings from the raw frame.
			var sq float64
			crossings := 0
			for i, s := range frame {
				v := float64(s)
				sq += v * v
				if i > 0 && (frame[i-1] < 0) != (s < 0) {
					crossings++
				}
			}
			rms := math.Sqrt(sq / featFrame)
			db := 20 * math.Log10(rms+1e-9)
			envelope = append(envelope, rms)
			if db < featSilence {
				if firstSound >= 0 {
					silent++ // provisionally; the trailing run is taken back below
				}
				hasPrev = false // an onset across silence is not a beat
				frames++
				continue
			}
			if firstSound < 0 {
				firstSound = frames
			}
			lastSound = frames
			frames++
			loud.add(db)
			loudness = append(loudness, db)
			zcr.add(float64(crossings) / featFrame)

			for i := range featFrame {
				buf[i] = complex(float64(frame[i])*hann[i], 0)
			}
			plan.transform(buf)
			var total, weighted float64
			for k := range bins {
				p := real(buf[k])*real(buf[k]) + imag(buf[k])*imag(buf[k])
				power[k] = p
				total += p
				weighted += p * float64(k)
			}
			if total <= 0 {
				continue
			}
			hz := float64(featRate) / featFrame
			c := weighted / total
			centroid.add(c * hz)
			// The geometric mean under the flatness is a log per bin, a
			// thousand a frame and the largest cost after the transform;
			// the bins are multiplied in runs of eight and logged once per
			// run instead. The floor keeps a run of near-silent bins from
			// underflowing: eight of 1e-12 is 1e-96, far inside a float.
			var spread, cum, logSum float64
			roll := 0
			prod, run := 1.0, 0
			for k := range bins {
				d := float64(k) - c
				spread += power[k] * d * d
				cum += power[k]
				if roll == 0 && cum >= 0.85*total {
					roll = k
				}
				prod *= power[k] + 1e-12
				if run++; run == 8 {
					logSum += math.Log(prod)
					prod, run = 1.0, 0
				}
			}
			if run > 0 {
				logSum += math.Log(prod)
			}
			bandwidth.add(math.Sqrt(spread/total) * hz)
			rolloff.add(float64(roll) * hz)
			flatness.add(math.Exp(logSum/float64(bins)) / (total/float64(bins) + 1e-12))

			// Harmony: the spectrum folded onto twelve pitch classes.
			var ch [12]float64
			var chTotal float64
			for k := range bins {
				if pc := pitchOf[k]; pc >= 0 {
					ch[pc] += power[k]
					chTotal += power[k]
				}
			}
			if chTotal > 0 {
				for pc := range ch {
					chroma[pc] += ch[pc] / chTotal
				}
				chromaFrames++
			}

			// Timbre: the mel spectrum's cepstrum, coefficients 1-13 (the
			// zeroth is loudness, which has columns of its own).
			for b, f := range theMel.filters {
				var e float64
				for k, wk := range f.w {
					e += wk * power[f.start+k]
				}
				mel[b] = e
				logMel[b] = math.Log(e + 1e-10)
			}
			for i := range featMFCC {
				var acc float64
				for b := range featMels {
					acc += logMel[b] * dct[i][b]
				}
				mfcc[i].add(acc)
			}
			// Onsets: how much louder each band got since the last frame.
			if hasPrev {
				var flux float64
				for b := range featMels {
					if d := logMel[b] - math.Log(prevMel[b]+1e-10); d > 0 {
						flux += d
					}
				}
				onsets = append(onsets, flux)
			}
			copy(prevMel, mel)
			hasPrev = true
		}
		envelopes = append(envelopes, envelope)
	}
	if loud.n == 0 {
		return nil
	}
	// Silence after the last sound was counted on the way through and is
	// not a pause between sentences: take it back.
	silent -= frames - 1 - lastSound

	out := make([]float32, featureDims)
	for i := range featMFCC {
		out[i] = float32(mfcc[i].mean())
		out[featMFCC+i] = float32(mfcc[i].std())
	}
	for i, s := range []*frameStats{&centroid, &bandwidth, &rolloff, &flatness} {
		out[26+2*i] = float32(s.mean())
		out[27+2*i] = float32(s.std())
	}
	if chromaFrames > 0 {
		for pc := range chroma {
			out[34+pc] = float32(chroma[pc] / float64(chromaFrames))
		}
	}
	out[46] = float32(loud.mean())
	out[47] = float32(loud.std())
	out[48] = float32(loudRange(loudness))
	out[49] = float32(zcr.mean())
	out[50], out[51] = tempoOf(onsets)
	out[52] = float32(pauseShare(loudness, silent, lastSound-firstSound+1))
	out[53] = float32(syllableShare(envelopes))
	out[54] = float32(zcr.std())
	// Seconds of sound: the frames that sounded, each a hop apart, plus the
	// tail of the last one. In frames alone a twenty-second window came to
	// 19.99 and never cleared the twenty the verdict asks for.
	out[55] = float32(loud.n*featHop+featFrame-featHop) / featRate
	for i, v := range out {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			out[i] = 0
		}
	}
	return out
}

// loudRange is how far the loud frames stand above the quiet ones: the
// 95th percentile less the 5th, in dB. Dynamics, as distinct from level.
func loudRange(db []float64) float64 {
	if len(db) < 2 {
		return 0
	}
	s := append([]float64(nil), db...)
	slices.Sort(s)
	return s[len(s)*95/100] - s[len(s)*5/100]
}

// pauseShare is the share of the frames between the first and last sound
// that are pauses: silent outright, or more than 25 dB under the track's
// loud frames (its 95th percentile). A reader pauses between sentences; a
// band rarely stops. The span, not the whole: a four-second track padded
// with silence at both ends measured as mostly pause, and it was not.
func pauseShare(db []float64, silent, total int) float64 {
	if total == 0 {
		return 0
	}
	if len(db) == 0 {
		return 1
	}
	s := append([]float64(nil), db...)
	slices.Sort(s)
	floor := s[len(s)*95/100] - 25
	quiet := silent
	for _, v := range db {
		if v < floor {
			quiet++
		}
	}
	return float64(quiet) / float64(total)
}

// syllableShare is how much of the loudness envelope's movement happens at
// syllable rate, 3-8 Hz, against everything between a quarter and sixteen
// hertz: speech swells and falls a few times a second, music's envelope
// moves at the beat and below it — 3 Hz is 180 to the minute, and the beat
// of nearly everything is under that. Read from each window's envelope's
// own spectrum, the energies summed over the windows.
func syllableShare(envelopes [][]float64) float64 {
	var band, all float64
	for _, env := range envelopes {
		b, a := syllableEnergy(env)
		band += b
		all += a
	}
	if all <= 0 {
		return 0
	}
	return band / all
}

// syllableEnergy is one envelope's energy at syllable rate and its energy
// over the whole band the share is measured against.
func syllableEnergy(env []float64) (band, all float64) {
	const fps = float64(featRate) / featHop
	if len(env) < 64 {
		return 0, 0
	}
	n := 1
	for n < len(env) {
		n <<= 1
	}
	var mean float64
	for _, v := range env {
		mean += v
	}
	mean /= float64(len(env))
	x := make([]complex128, n)
	for i, v := range env {
		w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(len(env)))
		x[i] = complex((v-mean)*w, 0)
	}
	planFFT(n).transform(x)
	for k := 1; k < n/2; k++ {
		f := float64(k) * fps / float64(n)
		if f < 0.25 || f > 16 {
			continue
		}
		p := real(x[k])*real(x[k]) + imag(x[k])*imag(x[k])
		all += p
		if f >= 3 && f <= 8 {
			band += p
		}
	}
	return band, all
}

// tempoOf reads the tempo off the onset envelope by autocorrelation over the
// lags of 60 to 200 beats per minute, nudged toward 120 the way every tempo
// estimator is: a train of beats repeats at every multiple of its period,
// so without a preference the octave would be a coin toss. Returns the
// tempo and the strength of the repetition, 0..1.
func tempoOf(onsets []float64) (float32, float32) {
	const fps = float64(featRate) / featHop
	n := len(onsets)
	minLag, maxLag := int(math.Round(fps*60/200)), int(math.Round(fps*60/60))
	if n < 2*maxLag {
		return 0, 0
	}
	var mean float64
	for _, v := range onsets {
		mean += v
	}
	mean /= float64(n)
	x := make([]float64, n)
	var energy float64
	for i, v := range onsets {
		x[i] = v - mean
		energy += x[i] * x[i]
	}
	if energy <= 0 {
		return 0, 0
	}
	bestLag, best := 0, -1.0
	for lag := minLag; lag <= maxLag; lag++ {
		var acc float64
		for i := lag; i < n; i++ {
			acc += x[i] * x[i-lag]
		}
		r := acc / energy
		bpm := 60 * fps / float64(lag)
		prior := math.Exp(-0.5 * math.Pow(math.Log2(bpm/120), 2))
		if score := r * prior; score > best {
			best, bestLag = score, lag
		}
	}
	if bestLag == 0 {
		return 0, 0
	}
	var acc float64
	for i := bestLag; i < n; i++ {
		acc += x[i] * x[i-bestLag]
	}
	return float32(60 * fps / float64(bestLag)), float32(math.Max(0, math.Min(1, acc/energy)))
}
