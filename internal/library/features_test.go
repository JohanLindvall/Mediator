package library

import (
	"math"
	"math/cmplx"
	"math/rand"
	"testing"
)

// The transform against the definition, on a size small enough to check.
func TestFFTMatchesTheDefinition(t *testing.T) {
	const n = 16
	x := make([]complex128, n)
	for i := range x {
		x[i] = complex(math.Sin(float64(i)*0.7)+0.3*float64(i%3), 0)
	}
	want := make([]complex128, n)
	for k := range want {
		for j := range x {
			want[k] += x[j] * cmplx.Exp(complex(0, -2*math.Pi*float64(k*j)/n))
		}
	}
	got := append([]complex128(nil), x...)
	planFFT(n).transform(got)
	for k := range got {
		if cmplx.Abs(got[k]-want[k]) > 1e-9 {
			t.Fatalf("bin %d: %v, want %v", k, got[k], want[k])
		}
	}
}

func sine(hz float64, seconds float64, amp float64) []float32 {
	out := make([]float32, int(seconds*featRate))
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*hz*float64(i)/featRate))
	}
	return out
}

func argmax(v []float32) int {
	best := 0
	for i, x := range v {
		if x > v[best] {
			best = i
		}
	}
	return best
}

// A pure A lands on A: the chroma peaks on the ninth class, the centroid
// sits at the tone, the spectrum is anything but flat, and the zero
// crossings count the tone's cycles.
func TestAToneIsHeardAsA(t *testing.T) {
	f := extractFeatures(sine(440, 3, 0.5))
	if len(f) != featureDims {
		t.Fatalf("dims = %d, want %d", len(f), featureDims)
	}
	if pc := argmax(f[34:46]); pc != 9 {
		t.Errorf("chroma peaks on class %d, want 9 (A)", pc)
	}
	if c := f[26]; c < 400 || c > 500 {
		t.Errorf("centroid = %.0f Hz, want about 440", c)
	}
	if fl := f[32]; fl > 0.2 {
		t.Errorf("flatness = %.2f for a pure tone, want near 0", fl)
	}
	if z := f[49]; math.Abs(float64(z)-2.0*440/featRate) > 0.01 {
		t.Errorf("zero-crossing rate = %.4f, want about %.4f", z, 2.0*440/featRate)
	}
	for i, v := range f {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			t.Errorf("column %d is not a number", i)
		}
	}
}

// Noise is flat and belongs to no key.
func TestNoiseIsFlat(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	pcm := make([]float32, 3*featRate)
	for i := range pcm {
		pcm[i] = float32(r.NormFloat64() * 0.2)
	}
	f := extractFeatures(pcm)
	if f == nil {
		t.Fatal("noise was heard as silence")
	}
	if fl := f[32]; fl < 0.4 {
		t.Errorf("flatness = %.2f for white noise, want well above a tone's", fl)
	}
	if peak := f[34+argmax(f[34:46])]; peak > 0.2 {
		t.Errorf("chroma peak = %.2f for noise, want no class standing out", peak)
	}
}

// A train of clicks at 120 beats a minute measures 120, not its half or
// its double — the prior toward 120 breaks the octave tie a perfect train
// leaves.
func TestClicksMeasureTheirTempo(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	pcm := make([]float32, 12*featRate)
	for i := range pcm {
		pcm[i] = float32(r.NormFloat64() * 0.002)
	}
	period := featRate / 2 // 120 bpm
	for at := 0; at+200 < len(pcm); at += period {
		for i := range 200 {
			pcm[at+i] += float32(0.8 * math.Exp(-float64(i)/40) * math.Sin(float64(i)*0.9))
		}
	}
	f := extractFeatures(pcm)
	if f == nil {
		t.Fatal("clicks were heard as silence")
	}
	if bpm := f[50]; bpm < 115 || bpm > 125 {
		t.Errorf("tempo = %.1f, want about 120", bpm)
	}
	if f[51] <= 0.2 {
		t.Errorf("tempo strength = %.2f for a perfect train, want a clear repetition", f[51])
	}
}

// Silence describes nothing and must not produce a vector of NaNs.
func TestSilenceHasNoFeatures(t *testing.T) {
	if f := extractFeatures(make([]float32, 5*featRate)); f != nil {
		t.Errorf("silence produced %v", f)
	}
	if f := extractFeatures(make([]float32, 10)); f != nil {
		t.Error("a handful of samples produced a vector")
	}
}

// The speech cues as defined, on signals built to have them and not to
// have them: a voice-shaped burst of noise that swells four times a second
// with gaps between phrases reads as pauses and as syllable-rate movement,
// and a tone swelling once a second reads as neither. (What the cues are
// worth on real music was measured separately — see spoken.go — and the
// syllable one turned out to mark drums more than voices; this pins only
// that each column measures what it says.)
func TestSpeechCuesTellASwellingVoiceFromASteadyTone(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	voice := make([]float32, 12*featRate)
	for i := range voice {
		sec := float64(i) / featRate
		// Phrases of two seconds, a pause of one, syllables at 4 Hz inside.
		if math.Mod(sec, 3) > 2 {
			continue
		}
		syl := 0.5 + 0.5*math.Sin(2*math.Pi*4*sec)
		voice[i] = float32(r.NormFloat64() * 0.3 * syl)
	}
	// Music's envelope moves at the beat and below it: a tone swelling
	// once a second, never falling silent.
	tone := sine(220, 12, 1)
	for i := range tone {
		tone[i] *= float32(0.6 + 0.4*math.Sin(2*math.Pi*float64(i)/featRate))
	}
	v, m := extractFeatures(voice), extractFeatures(tone)
	if v == nil || m == nil {
		t.Fatal("a signal was heard as silence")
	}
	if v[52] < 0.2 || m[52] > 0.1 {
		t.Errorf("pause share: voice %.2f, tone %.2f; want the voice well above the tone", v[52], m[52])
	}
	if v[53] <= m[53] {
		t.Errorf("syllable share: voice %.2f, tone %.2f; want the voice above the tone", v[53], m[53])
	}
	if spokenScore(v) <= spokenScore(m) {
		t.Errorf("spoken score: voice %.2f, tone %.2f", spokenScore(v), spokenScore(m))
	}
}

// A train at 140 measures 140 and not its half: the prior toward 120 breaks
// the octave tie toward the nearer of the two, which here is the true one.
func TestClicksAtOneFortyAreNotSeventy(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	pcm := make([]float32, 12*featRate)
	for i := range pcm {
		pcm[i] = float32(r.NormFloat64() * 0.002)
	}
	period := featRate * 60 / 140
	for at := 0; at+200 < len(pcm); at += period {
		for i := range 200 {
			pcm[at+i] += float32(0.8 * math.Exp(-float64(i)/40) * math.Sin(float64(i)*0.9))
		}
	}
	f := extractFeatures(pcm)
	if f == nil {
		t.Fatal("clicks were heard as silence")
	}
	if bpm := f[50]; bpm < 135 || bpm > 145 {
		t.Errorf("tempo = %.1f, want about 140", bpm)
	}
}

// Two windows described as one track: the join between them is not a beat.
// A tone in one window and the same tone in the next carry no onset, so a
// train of clicks with a silent seam between the windows still measures its
// own tempo rather than the seam's.
func TestWindowsAreDescribedWithoutASeam(t *testing.T) {
	a := sine(220, 4, 0.5)
	b := sine(220, 4, 0.5)
	joined := extractFeatures(append(append([]float32{}, a...), b...))
	apart := extractFeaturesFrom([][]float32{a, b})
	if joined == nil || apart == nil {
		t.Fatal("a tone was heard as silence")
	}
	if apart[55] < 7.9 || apart[55] > 8.1 {
		t.Errorf("two four-second windows counted %.2f s of sound, want eight", apart[55])
	}
	if apart[26] < 200 || apart[26] > 240 {
		t.Errorf("centroid over two windows = %.0f, want the tone", apart[26])
	}
}
