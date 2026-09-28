package library

import (
	"context"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// clickTrain is seconds of clicks at bpm over faint noise, each click placed
// to the sample — so a tempo whose period is no whole number of frames is
// played as it is, which is the case the shown tempo exists for.
func clickTrain(bpm, seconds float64, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	pcm := make([]float32, int(seconds*featRate))
	for i := range pcm {
		pcm[i] = float32(r.NormFloat64() * 0.002)
	}
	for beat := 0; ; beat++ {
		at := int(math.Round(float64(beat) * featRate * 60 / bpm))
		if at+200 >= len(pcm) {
			break
		}
		for i := range 200 {
			pcm[at+i] += float32(0.8 * math.Exp(-float64(i)/40) * math.Sin(float64(i)*0.9))
		}
	}
	return pcm
}

// The shown tempo is read between frames. Column 50 can only say the tempo
// of a whole number of frames — 143.6 for a train at 142.5, the lag on either
// side of it being 136.0 and 152.0 — and a number on a screen that far off is
// wrong; this one comes to within a fraction of a beat, at whole tempos and
// at the ones between them. The tempos are ones the correlation read at the
// nearest frame gets wrong by half a beat even on the fine grid (118.8 as
// 119.25, 133.3 as 132.75): the reading between frames is what this pins.
func TestTheShownTempoIsMeasuredBetweenFrames(t *testing.T) {
	for _, bpm := range []float64{96.7, 111.3, 118.8, 128, 133.3, 149.1} {
		windows := [][]float32{clickTrain(bpm, 20, 1), clickTrain(bpm, 20, 2), clickTrain(bpm, 20, 3)}
		vec, beats := describeWindows(windows)
		if vec == nil {
			t.Fatalf("%.1f: clicks were heard as silence", bpm)
		}
		got, clarity := bpmOf(beats)
		if math.Abs(got-bpm) > 0.3 {
			t.Errorf("a train at %.1f measured %.2f", bpm, got)
		}
		if clarity < bpmClarity {
			t.Errorf("a train at %.1f repeats with clarity %.2f, under the bar of %.2f", bpm, clarity, bpmClarity)
		}
	}
	// The vector's own column keeps its steps: it is read as it always was,
	// or every vector written before this would stop being comparable with
	// every one written after.
	vec, _ := describeWindows([][]float32{clickTrain(142.5, 20, 1)})
	if math.Abs(float64(vec[50])-143.6) > 0.1 {
		t.Errorf("column 50 of a train at 142.5 = %.2f, want the lag's 143.6, as it has always been", vec[50])
	}
}

// Noise has onsets and no tempo, and says so by its clarity; silence, and
// a window too short to hold the slowest tempo's multiples, say nothing.
func TestNoTempoIsShownWhereNothingRepeats(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	var windows [][]float32
	for range 3 {
		w := make([]float32, 20*featRate)
		for i := range w {
			// Bursts of noise at random moments: onsets with no period.
			w[i] = float32(r.NormFloat64() * 0.01)
		}
		for at := 0; at < len(w)-400; at += 1000 + r.Intn(20000) {
			for i := range 400 {
				w[at+i] += float32(r.NormFloat64() * 0.5)
			}
		}
		windows = append(windows, w)
	}
	_, beats := describeWindows(windows)
	if bpm, clarity := bpmOf(beats); clarity >= bpmClarity {
		t.Errorf("bursts at random moments read as %.1f at clarity %.2f, over the bar", bpm, clarity)
	}
	if bpm, _ := bpmOf(nil); bpm != 0 {
		t.Errorf("nothing at all read as %.1f", bpm)
	}
	_, beats = describeWindows([][]float32{clickTrain(120, 5, 1)})
	if bpm, _ := bpmOf(beats); bpm != 0 {
		t.Errorf("five seconds read as %.1f; four periods of the slowest tempo twice over do not fit", bpm)
	}
}

// codecFlicker is a sustained note with the rest of the spectrum doing what
// an MP3 encoder's empty bands do: faint noise, on for one of its frames and
// off for the next (576 samples here is 1152 at 44.1 kHz, one MP3 frame).
func codecFlicker(seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	pcm := make([]float32, 20*featRate)
	for i := range pcm {
		v := 0.5 * math.Sin(2*math.Pi*220*float64(i)/featRate)
		if (i/576)%2 == 0 {
			v += r.NormFloat64() * 1e-5
		}
		pcm[i] = float32(v)
	}
	return pcm
}

// An encoder's flicker is not a beat, and two floors are what say so: each
// is shown here to be keeping out the tempo it keeps out, since a test that
// passed with the floor removed would be testing nothing. In logarithms the
// flicker of an empty band is as large as a drum hit and as regular as the
// encoder's frames — measured on sustained chords made into MP3s, 141 to 145
// at a clarity over the bar, which is what this signal reads without the
// band floor. With it, what is left is a faint ripple still regular enough
// to pass the clarity bar alone — a drone measured 112 — and the onset
// floor is what sends that away.
func TestAnEncodersFlickerIsNoTempo(t *testing.T) {
	windows := [][]float32{codecFlicker(1), codecFlicker(2), codecFlicker(3)}
	_, beats := describeWindows(windows)
	if bpm, clarity := bpmOf(beats); bpm != 0 {
		t.Errorf("a sustained note under an encoder's flicker reads as %.1f at %.2f", bpm, clarity)
	}
	if _, clarity := tempoFrom(beats); clarity < bpmClarity {
		t.Errorf("the ripple the band floor leaves repeats at clarity %.2f; the onset floor is keeping out nothing", clarity)
	}
	if peak := onsetPeak(beats); peak >= bpmOnsetFloor {
		t.Errorf("the ripple peaks at %.2f, over the onset floor of %v", peak, bpmOnsetFloor)
	}
	defer func(floor float64) { tempoTopDB = floor }(tempoTopDB)
	tempoTopDB = math.Inf(1)
	_, beats = describeWindows(windows)
	if bpm, clarity := bpmOf(beats); bpm == 0 || clarity < bpmClarity {
		t.Errorf("without the band floor the flicker reads as %.1f at %.2f; want the tempo it is kept out for", bpm, clarity)
	}
}

// What is shown is the tempo of the file as it is, and only a clear one.
func TestATempoIsShownOnlyForTheFileItWasReadFrom(t *testing.T) {
	r := tempoRec{mtime: 7, size: 99, bpm: 128.04, clarity: 0.5}
	if got := r.shown(&Item{ModTime: 7, Size: 99}); got != 128 {
		t.Errorf("shown %.2f, want 128 to a tenth", got)
	}
	if got := r.shown(&Item{ModTime: 8, Size: 99}); got != 0 {
		t.Errorf("a changed file was shown the old one's tempo, %.1f", got)
	}
	r.clarity = bpmClarity - 0.01
	if got := r.shown(&Item{ModTime: 7, Size: 99}); got != 0 {
		t.Errorf("a pulse under the bar was shown as %.1f", got)
	}
}

// A track described before tempos were read is read again for its tempo —
// once, and not a decode that failed.
func TestATrackReadBeforeTemposIsReadForItsTempo(t *testing.T) {
	l := quietLib("/m")
	vec := make([]float32, featureDims)
	it := &Item{ID: "abc", Kind: KindAudio, ModTime: 7, Size: 99}
	l.putFeatures("abc", 7, 99, vec)
	if !l.needsAnalysis(it) {
		t.Error("a track with a vector and no tempo was not to be read for it")
	}
	l.putTempo("abc", 7, 99, 128, 0.5)
	if l.needsAnalysis(it) {
		t.Error("a track with both was to be read again")
	}
	bad := Item{ID: "bad", Kind: KindAudio, ModTime: 1, Size: 2}
	l.markFailed(bad)
	if l.needsAnalysis(&bad) {
		t.Error("a decode that failed was to be tried again for its tempo")
	}
}

// The whole of it on clicks ffmpeg makes: the tempo is read and shown on the
// item. A track read before tempos existed is read
// again for its tempo alone: its vector is left exactly as it was, and the
// features generation — what the resemblances are rebuilt against, on the
// request path — does not move for a batch of tempos.
func TestTheAnalysisReadsTheTempoAndLeavesAVectorAlone(t *testing.T) {
	ffmpeg := FFmpegPath()
	if ffmpeg == "" {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "clicks.wav")
	const bpm = 128.0
	// A tone under the clicks, or the silence between them reads as the
	// pauses of a voice and the track as a reading, which has no tempo.
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
		"aevalsrc=0.2*sin(2*PI*220*t)+0.8*exp(-80*mod(t\\,60/"+strconv.FormatFloat(bpm, 'f', -1, 64)+"))*sin(2*PI*1000*t):s=22050:d=90",
		"-ac", "1", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not make clicks: %v: %s", err, out)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.upsert(path, KindAudio, info.Size(), info.ModTime(), fileKey{}, false)
	id := PathID(path)
	l.mu.Lock()
	l.items[id].Duration = 90_000
	l.mu.Unlock()

	it, _ := l.Get(id)
	if fresh, err := l.analyzeOne(context.Background(), nil, it); err != nil || !fresh {
		t.Fatalf("first read: fresh=%v err=%v", fresh, err)
	}
	l.publishAnalysis()
	if got, _ := l.Get(id); math.Abs(got.BPM-bpm) > 0.3 {
		t.Errorf("the item shows %.2f, want %.1f", got.BPM, bpm)
	}

	// As a track described before tempos were: the vector and no tempo.
	l.featMu.Lock()
	delete(l.tempos, id)
	vec := l.features[id].vec
	gen := l.featuresGen
	l.featMu.Unlock()
	l.publishTempos()
	if got, _ := l.Get(id); got.BPM != 0 {
		t.Fatalf("with its tempo gone the item still shows %.1f", got.BPM)
	}
	it, _ = l.Get(id)
	if !l.needsAnalysis(&it) {
		t.Fatal("the track was not to be read for its tempo")
	}
	version := l.Version()
	l.analyzeAll(context.Background(), nil, []string{id}, func() bool { return false })
	l.featMu.RLock()
	same := &l.features[id].vec[0] == &vec[0]
	moved := l.featuresGen != gen
	l.featMu.RUnlock()
	if !same {
		t.Error("the vector was written again for a read of the tempo alone")
	}
	if moved {
		t.Error("the features generation moved for a batch of tempos")
	}
	if l.Version() == version {
		t.Error("the version did not move: a listing would go on showing no tempo")
	}
	if got, _ := l.Get(id); math.Abs(got.BPM-bpm) > 0.3 {
		t.Errorf("after the read the item shows %.2f, want %.1f", got.BPM, bpm)
	}
}

// Ordered by tempo, what has one comes first whichever way the listing
// runs: a slowest-first listing led by a screenful of tracks with no tempo
// would be an order of nothing. And the tempo is on the tiles it orders.
func TestTheTempoOrderPutsTracksWithoutOneLast(t *testing.T) {
	dir := t.TempDir()
	names := []string{"fast.mp3", "slow.mp3", "mid.mp3", "faint.mp3", "unread.mp3"}
	for _, n := range names {
		write(t, filepath.Join(dir, n), "not really a song")
	}
	l := quietLib(dir)
	l.Scan(nil)
	set := func(name string, bpm, clarity float32) {
		it, _ := l.Get(PathID(filepath.Join(dir, name)))
		l.putTempo(it.ID, it.ModTime, it.Size, bpm, clarity)
	}
	set("fast.mp3", 174, 0.5)
	set("slow.mp3", 72, 0.5)
	set("mid.mp3", 120, 0.5)
	set("faint.mp3", 99, 0.1)
	l.publishTempos()
	l.notify()

	order := func(desc bool) []string {
		res := l.List(Query{Kind: KindAudio, Sort: "tempo", Desc: desc, Limit: 10})
		var out []string
		for _, it := range res.Items {
			out = append(out, it.Name+"@"+strconv.FormatFloat(it.BPM, 'f', -1, 64))
		}
		return out
	}
	want := []string{"slow.mp3@72", "mid.mp3@120", "fast.mp3@174", "faint.mp3@0", "unread.mp3@0"}
	if got := order(false); !equalStrings(got, want) {
		t.Errorf("slowest first: %v, want %v", got, want)
	}
	// Descending, the tracks without one stay at the end — and among
	// themselves the order is the tie-break's, which the direction turns.
	want = []string{"fast.mp3@174", "mid.mp3@120", "slow.mp3@72", "unread.mp3@0", "faint.mp3@0"}
	if got := order(true); !equalStrings(got, want) {
		t.Errorf("fastest first: %v, want %v", got, want)
	}
}

// A reading has no tempo worth the word — the pauses between sentences
// repeat as steadily as a slow beat — so a track its release has judged
// spoken is shown none, on the tile or in the order.
func TestAReadingIsShownNoTempo(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "chapter.mp3"), "not really a chapter")
	l := quietLib(dir)
	l.Scan(nil)
	it, _ := l.Get(PathID(filepath.Join(dir, "chapter.mp3")))
	l.putTempo(it.ID, it.ModTime, it.Size, 96, 0.6)
	l.publishTempos()
	if got, _ := l.Get(it.ID); got.BPM != 96 {
		t.Fatalf("before the verdict the track shows %.1f, want 96", got.BPM)
	}
	l.featMu.Lock()
	l.byRelease = map[string]bool{it.ID: true} // what the album build writes for a reading
	l.featMu.Unlock()
	l.notify()
	if got, _ := l.Get(it.ID); got.BPM != 0 {
		t.Errorf("a reading shows a tempo of %.1f", got.BPM)
	}
	res := l.List(Query{Kind: KindAudio, Sort: "tempo", Limit: 5})
	if len(res.Items) != 1 || res.Items[0].BPM != 0 {
		t.Errorf("ordered by tempo, the reading carries %+v", res.Items)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
