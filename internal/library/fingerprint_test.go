package library

import (
	"math"
	"math/rand"
	"testing"
)

// synthSound is sound that never existed: a few tones whose pitches change
// every quarter of a second, drawn from a seed, so two seeds are two
// different pieces of music and one seed is the same piece twice. Within
// the fingerprint's bands, so there is something for the bands to say.
func synthSound(seed int64, seconds float64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	n := int(seconds * fpRate)
	out := make([]float32, n)
	seg := fpRate / 4
	var freqs [3]float64
	var phase [3]float64
	for i := range n {
		if i%seg == 0 {
			for k := range freqs {
				freqs[k] = fpLowHz + rng.Float64()*(fpHighHz-fpLowHz)
			}
		}
		var v float64
		for k := range freqs {
			phase[k] += 2 * math.Pi * freqs[k] / fpRate
			v += 0.2 * math.Sin(phase[k])
		}
		out[i] = float32(v)
	}
	return out
}

func concat(parts ...[]float32) []float32 {
	var out []float32
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// The whole of it: one piece of sound inside two different surroundings, at
// two different offsets, one copy quieter and dirtier than the other, is
// found where it is and for as long as it lasts; two different pieces share
// nothing; and silence matches nothing, least of all more silence.
func TestFingerprintFindsTheSharedSound(t *testing.T) {
	theme := synthSound(1, 60)
	// The second copy at six tenths of the level, with a little noise over
	// it, and at an offset that is not a whole number of hops.
	rng := rand.New(rand.NewSource(99))
	dirty := make([]float32, len(theme))
	for i, v := range theme {
		dirty[i] = 0.6*v + float32(rng.NormFloat64()*0.01)
	}
	a := concat(synthSound(2, 20.03), theme, synthSound(3, 100))
	b := concat(synthSound(4, 45.17), dirty, synthSound(5, 80))
	pa, pb := fingerprintPCM(a), fingerprintPCM(b)
	ai, bi, n := commonRun(pa, pb, fpFrames(skipMinRun))
	if n == 0 {
		t.Fatal("the shared minute was not found")
	}
	if got := fpSeconds(ai); math.Abs(got-20.03) > 0.5 {
		t.Errorf("found at %.2f s in the first, want 20.03", got)
	}
	if got := fpSeconds(bi); math.Abs(got-45.17) > 0.5 {
		t.Errorf("found at %.2f s in the second, want 45.17", got)
	}
	if got := fpSeconds(n); math.Abs(got-60) > 1.5 {
		t.Errorf("found %.2f s of it, want 60", got)
	}

	// Two different pieces share nothing worth calling an intro.
	x, y := fingerprintPCM(synthSound(6, 120)), fingerprintPCM(synthSound(7, 120))
	if _, _, n := commonRun(x, y, fpFrames(skipMinRun)); n != 0 {
		t.Errorf("unrelated sound shared %.1f s", fpSeconds(n))
	}
	// Silence carries no bits and matches nothing — half of all episodes
	// open with black, and black matched black before this was so.
	quiet := fingerprintPCM(make([]float32, fpRate*30))
	for _, h := range quiet {
		if h != 0 {
			t.Fatalf("a silent frame carries bits: %x", h)
		}
	}
	if _, _, n := commonRun(quiet, quiet, fpFrames(skipMinRun)); n != 0 {
		t.Errorf("silence matched silence for %.1f s", fpSeconds(n))
	}
}

// The fingerprint says nothing about the level: the same sound at half the
// volume is the same fingerprint, bit for bit almost.
func TestFingerprintIgnoresTheLevel(t *testing.T) {
	loud := synthSound(8, 30)
	soft := make([]float32, len(loud))
	for i, v := range loud {
		soft[i] = v * 0.3
	}
	pl, ps := fingerprintPCM(loud), fingerprintPCM(soft)
	if len(pl) != len(ps) || len(pl) == 0 {
		t.Fatalf("%d and %d frames", len(pl), len(ps))
	}
	matched := 0
	for i := range pl {
		if fpMatch(pl[i], ps[i]) {
			matched++
		}
	}
	if matched < len(pl)*95/100 {
		t.Errorf("only %d of %d frames matched across a change of level", matched, len(pl))
	}
}
