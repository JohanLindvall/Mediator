package library

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// A vector written is a vector restored, stamped with the file it was read
// from; one written under an older recipe is left for reading again. This
// is what makes a restart cheap and a stale vector wrong.
func TestFeaturesSurviveARestart(t *testing.T) {
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vec := make([]float32, featureDims)
	for i := range vec {
		vec[i] = float32(i) * 0.5
	}
	if err := db.PutFeatures("abc", 7, 99, featuresVersion, vec); err != nil {
		t.Fatal(err)
	}
	if err := db.PutFeatures("old", 1, 2, featuresVersion-1, vec); err != nil {
		t.Fatal(err)
	}
	l := quietLib("/m")
	if n := l.LoadFeatures(db); n != 1 {
		t.Fatalf("restored %d vectors, want the one written under this recipe", n)
	}
	if got := l.featuresOf("abc"); !slices.Equal(got, vec) {
		t.Errorf("restored vector = %v, want what was written", got)
	}
	if l.featuresOf("old") != nil {
		t.Error("a vector from an older recipe was restored")
	}
	// The stamp travels with it: the file as it was decides whether it is
	// read again.
	same := &Item{ID: "abc", Kind: KindAudio, ModTime: 7, Size: 99}
	changed := &Item{ID: "abc", Kind: KindAudio, ModTime: 8, Size: 99}
	if l.needsAnalysis(same) {
		t.Error("an unchanged file was to be read again")
	}
	if !l.needsAnalysis(changed) {
		t.Error("a changed file was not to be read again")
	}
	if l.needsAnalysis(&Item{ID: "x", Kind: KindVideo}) {
		t.Error("a film was to be analysed")
	}
}

// The whole pipeline on a tone ffmpeg makes: decoded, described, written
// down, and the twenty seconds of a single window count as twenty.
func TestAnalyzeOneReadsATone(t *testing.T) {
	ffmpeg := FFmpegPath()
	if ffmpeg == "" {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "tone.wav")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=60", "-ac", "1", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not make a tone: %v: %s", err, out)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.upsert(path, KindAudio, info.Size(), info.ModTime(), fileKey{}, false)
	id := PathID(path)
	l.mu.Lock()
	l.items[id].Duration = 60_000
	l.mu.Unlock()
	it, _ := l.Get(id)

	db, err := blob.Open(filepath.Join(dir, "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := l.analyzeOne(context.Background(), db, it); err != nil {
		t.Fatal(err)
	}
	v := l.featuresOf(id)
	if len(v) != featureDims {
		t.Fatalf("vector of %d, want %d", len(v), featureDims)
	}
	if pc := argmax(v[34:46]); pc != 9 {
		t.Errorf("the tone's chroma peaks on class %d, want 9 (A)", pc)
	}
	// One window has to clear the bar for a verdict, and it measures just
	// under its nominal twenty seconds: the partial frame at its tail is
	// dropped, which is what put the bar at nineteen.
	if v[55] < spokenMinSound || v[55] > 20.01 {
		t.Errorf("a twenty-second window counted %.2f seconds of sound, want %d..20", v[55], spokenMinSound)
	}
	l.mu.RLock()
	again := l.needsAnalysis(l.items[id])
	l.mu.RUnlock()
	if again {
		t.Error("a track just read was to be read again")
	}
	stored := 0
	db.EachFeatures(func(string, int64, int64, int, []float32) { stored++ })
	if stored != 1 {
		t.Errorf("the database holds %d vectors, want 1", stored)
	}
}

// The pass yields to higher-priority work: with busy never lifting nothing
// is read, and the loop returns when its context does.
func TestAnalysisYieldsToBusyWork(t *testing.T) {
	l := quietLib("/m")
	l.upsert("/m/a.mp3", KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
	id := PathID("/m/a.mp3")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	asked := 0
	l.analyzeAll(ctx, nil, []string{id}, func() bool { asked++; return true })
	if asked == 0 {
		t.Fatal("the pass never asked whether it may run")
	}
	if l.featuresOf(id) != nil {
		t.Error("a track was read while higher-priority work ran")
	}
}

// A track that could not be read is remembered for the run and not read
// again every pass; a restart, which forgets, tries once more.
func TestAFailedDecodeIsNotRetriedEveryPass(t *testing.T) {
	l := quietLib("/m")
	l.upsert("/m/broken.mp3", KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
	id := PathID("/m/broken.mp3")
	it, _ := l.Get(id)
	l.mu.RLock()
	before := l.needsAnalysis(l.items[id])
	l.mu.RUnlock()
	if !before {
		t.Fatal("an unread track did not need analysis")
	}
	l.markFailed(it)
	l.mu.RLock()
	after := l.needsAnalysis(l.items[id])
	l.mu.RUnlock()
	if after {
		t.Error("a track that failed was to be read again in the same run")
	}
	if l.featuresOf(id) != nil {
		t.Error("a failure was handed out as a vector")
	}
	// A changed file is a different file, read again.
	l.mu.RLock()
	changed := *l.items[id]
	l.mu.RUnlock()
	changed.ModTime++
	if !l.needsAnalysis(&changed) {
		t.Error("a file that changed after failing was not read again")
	}
	// Nothing was written down: a fresh library starts over.
	again := quietLib("/m")
	again.upsert("/m/broken.mp3", KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
	again.mu.RLock()
	fresh := again.needsAnalysis(again.items[id])
	again.mu.RUnlock()
	if !fresh {
		t.Error("a failure outlived the run")
	}
}

// A track that decoded to silence is written down as nothing to say, and
// that survives a restart: it is not read again for being empty.
func TestASilentTrackIsNotReadAgainAfterARestart(t *testing.T) {
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutFeatures("quiet", 3, 4, featuresVersion, []float32{}); err != nil {
		t.Fatal(err)
	}
	l := quietLib("/m")
	if n := l.LoadFeatures(db); n != 1 {
		t.Fatalf("restored %d, want the silent track's empty vector", n)
	}
	if l.needsAnalysis(&Item{ID: "quiet", Kind: KindAudio, ModTime: 3, Size: 4}) {
		t.Error("a silent track was to be decoded again after a restart")
	}
}

// A track nobody measured, shorter than the first fixed mark, is read from
// the start rather than written down as silence for good.
func TestAShortUnmeasuredTrackIsReadFromTheStart(t *testing.T) {
	ffmpeg := FFmpegPath()
	if ffmpeg == "" {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "short.wav")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=10", "-ac", "1", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not make a tone: %v: %s", err, out)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.upsert(path, KindAudio, info.Size(), info.ModTime(), fileKey{}, false)
	it, _ := l.Get(PathID(path)) // Duration stays 0: nobody measured it
	if err := l.analyzeOne(context.Background(), nil, it); err != nil {
		t.Fatal(err)
	}
	if v := l.featuresOf(it.ID); len(v) != featureDims || v[55] < 5 {
		t.Errorf("a ten-second track nobody measured came out as %d columns, %.1f s of sound", len(v), func() float32 {
			if len(v) > 55 {
				return v[55]
			}
			return 0
		}())
	}
}
