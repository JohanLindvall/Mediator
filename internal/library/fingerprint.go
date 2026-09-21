package library

// Audio fingerprints, for finding the sound that recurs.
//
// A show's opening and closing are the same audio in every episode of a
// season, and that is the one fact about them a library can act on: nothing
// in the file says where they are, but two episodes laid side by side share
// a stretch of sound where their intros are, and nowhere else. So each
// episode's first minutes and last minutes are reduced to a fingerprint —
// one small number per sixteenth of a second, describing the shape of the
// spectrum and nothing about its level — and the intro is the longest
// stretch of one episode's fingerprint that another episode's contains
// (skipdetect.go).
//
// The fingerprint is Haitsma and Kalker's, from "A Highly Robust Audio
// Fingerprinting System" (2002), chosen because it is robust to exactly the
// ways two copies of one intro differ — the level, the codec, a few
// milliseconds of alignment — and to nothing else. Each frame is 32 bands of
// energy between 300 Hz and 2 kHz, spaced by ratio, and each bit says
// whether the energy difference between two neighbouring bands rose or fell
// since the last frame. Signs only, so the loudness of a copy is invisible;
// differences only, so a broadcast's equalisation is nearly so. Two frames
// of the same sound differ in a few bits; two frames of different sounds
// differ in about half of them, which is what makes a match unmistakable.
//
// One bit is kept for silence. The bands of a silent frame are noise, and
// noise matches noise: without the marker, the black seconds that open half
// of all episodes matched each other and read as an intro.

import (
	"math"
	"math/bits"
	"sync"
)

const (
	// fpRate is the rate the sound is decoded at. The bands end at 2 kHz,
	// so there is nothing above 5.5 kHz worth carrying.
	fpRate = 11025
	// fpFrame is one frame's samples, 371 ms; fpHop is how far the next
	// begins after it, 62.5 ms, which is sixteen frames a second. A mark is
	// then placed to a sixteenth of a second, and two copies of one intro
	// that are out of step by less than half a hop still overlap by more
	// than nine tenths of a frame.
	fpFrame = 4096
	fpHop   = 689
	// fpBands are spaced by ratio between these, which is where the voice
	// and the melody are and what survives every codec.
	fpBands  = 32
	fpLowHz  = 300.0
	fpHighHz = 2000.0
	// fpSilentRMS is the analysis's own floor, -60 dBFS: a frame quieter
	// than this is silence, carries no bits, and matches nothing.
	fpSilentRMS = 0.001
	// fpSound is the marker bit; the other 31 are the differences.
	fpSound = uint32(1) << 31
	// fpMaxBits is how many of the 31 may differ between two frames of the
	// same sound. Measured by Haitsma and Kalker at a few for a compressed
	// copy and about fifteen for unrelated sound; nine is well inside.
	fpMaxBits = 9
	// fpGapFrames is how many frames in a row may fail to match before a
	// run is over: a second, for a sound effect over the theme or a frame
	// on the edge of silence. fpDensity is how much of a run has to match
	// for it to count at all.
	fpGapFrames = 16
	fpDensity   = 0.6
)

var fpSetup struct {
	once   sync.Once
	window []float64
	edges  []int
}

func fpPrepare() {
	fpSetup.once.Do(func() {
		w := make([]float64, fpFrame)
		for i := range w {
			w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(fpFrame))
		}
		edges := make([]int, fpBands+1)
		for b := 0; b <= fpBands; b++ {
			hz := fpLowHz * math.Pow(fpHighHz/fpLowHz, float64(b)/fpBands)
			edges[b] = int(math.Round(hz * fpFrame / fpRate))
		}
		fpSetup.window, fpSetup.edges = w, edges
	})
}

// fingerprintPCM reduces mono sound at fpRate to one hash per hop.
func fingerprintPCM(pcm []float32) []uint32 {
	if len(pcm) < fpFrame {
		return nil
	}
	fpPrepare()
	plan := planFFT(fpFrame)
	frames := (len(pcm)-fpFrame)/fpHop + 1
	out := make([]uint32, frames)
	buf := make([]complex128, fpFrame)
	var prev [fpBands]float64
	havePrev := false
	for f := range frames {
		start := f * fpHop
		var sq float64
		for i := range fpFrame {
			v := float64(pcm[start+i])
			sq += v * v
			buf[i] = complex(v*fpSetup.window[i], 0)
		}
		if math.Sqrt(sq/fpFrame) < fpSilentRMS {
			out[f] = 0
			havePrev = false
			continue
		}
		plan.transform(buf)
		var e [fpBands]float64
		for b := range fpBands {
			for k := fpSetup.edges[b]; k < fpSetup.edges[b+1]; k++ {
				e[b] += real(buf[k])*real(buf[k]) + imag(buf[k])*imag(buf[k])
			}
		}
		h := fpSound
		for b := range fpBands - 1 {
			d := e[b] - e[b+1]
			if havePrev {
				d -= prev[b] - prev[b+1]
			}
			if d > 0 {
				h |= 1 << b
			}
		}
		prev, havePrev = e, true
		out[f] = h
	}
	return out
}

// fpMatch says two frames are the same sound: both with sound in them, and
// few bits apart.
func fpMatch(a, b uint32) bool {
	return a&b&fpSound != 0 && bits.OnesCount32(a^b) <= fpMaxBits
}

// fpSeconds and fpFrames convert between frames and seconds of sound.
func fpSeconds(frames int) float64 { return float64(frames) * fpHop / fpRate }
func fpFrames(secs float64) int    { return int(secs * fpRate / fpHop) }

// commonRun finds the longest stretch of a that b also holds, at any
// alignment: where it starts in each, and how many frames it runs. A run
// rides over up to fpGapFrames of misses in a row and has to be at least
// fpDensity matches overall; below minRun nothing is reported.
//
// Every alignment is tried, and at each the two are walked together. That
// is the product of the two lengths — six minutes against six minutes at
// sixteen frames a second is thirty-three million comparisons, each a
// popcount — which is a few tens of milliseconds, and simpler than an index
// that would have to be right about near misses.
func commonRun(a, b []uint32, minRun int) (aStart, bStart, n int) {
	best, bestD := 0, 0
	for d := -(len(b) - 1); d < len(a); d++ {
		lo, hi := max(0, d), min(len(a), len(b)+d)
		runStart, lastHit, hits, misses := -1, -1, 0, 0
		end := func() {
			if runStart < 0 {
				return
			}
			if l := lastHit - runStart + 1; l > best && float64(hits) >= fpDensity*float64(l) {
				best, aStart, bestD = l, runStart, d
			}
			runStart, hits, misses = -1, 0, 0
		}
		for i := lo; i < hi; i++ {
			if fpMatch(a[i], b[i-d]) {
				if runStart < 0 {
					runStart = i
				}
				lastHit, misses = i, 0
				hits++
				continue
			}
			if runStart >= 0 {
				if misses++; misses > fpGapFrames {
					end()
				}
			}
		}
		end()
	}
	if best < minRun {
		return 0, 0, 0
	}
	// The run's ends are where the matches are dense, not where the first
	// and last chance match fell. Unrelated frames match one time in sixty
	// or so, and the gap a run rides over lets a few of those chain onto
	// its ends — measured, two and three quarter seconds of somebody
	// else's scene in front of the credits, and the longest run wins the
	// vote, so the padding did too. A real match runs at nine in ten.
	aStart, n = trimRun(a, b, aStart, bestD, best)
	if n < minRun {
		return 0, 0, 0
	}
	return aStart, aStart - bestD, n
}

// fpEdgeFrames is how much of a run's end has to be dense for the end to
// stand: a second.
const fpEdgeFrames = 16

// trimRun cuts a run back to where at least half of any second matches.
func trimRun(a, b []uint32, start, d, n int) (int, int) {
	dense := func(from int) bool {
		hits := 0
		for i := from; i < from+fpEdgeFrames && i < start+n; i++ {
			if fpMatch(a[i], b[i-d]) {
				hits++
			}
		}
		return hits*2 >= fpEdgeFrames
	}
	for n > 0 && !dense(start) {
		start, n = start+1, n-1
	}
	denseBack := func(to int) bool {
		hits := 0
		for i := to; i > to-fpEdgeFrames && i >= start; i-- {
			if fpMatch(a[i], b[i-d]) {
				hits++
			}
		}
		return hits*2 >= fpEdgeFrames
	}
	for n > 0 && !denseBack(start+n-1) {
		n--
	}
	return start, n
}
