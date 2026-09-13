package library

import (
	"testing"
	"time"
)

// A read that ran out of time is not a track that could not be read.
//
// analyzeOne bounds each track with a budget of its own, so a decode that
// overruns comes back with that budget's deadline while the pass's own
// context is still live — which the pass used to file under "failed",
// writing the track off for the rest of the run. The rule is the
// thumbnailer's: interrupted is not answered.
func TestInterruptedAnalysisIsNotAVerdict(t *testing.T) {
	l := quietLib("/m")
	it := Item{ID: "track-1", Kind: KindAudio, ModTime: 200, Size: 4096}
	// A vector read from an earlier state of the same file.
	old := make([]float32, featureDims)
	old[0] = 0.5
	l.putFeatures(it.ID, 100, 1024, old)

	l.markInterrupted(it)
	if l.featuresOf(it.ID) == nil {
		t.Error("an interruption must leave the vector that was there alone")
	}
	if l.needsAnalysis(&it) {
		t.Error("a track just interrupted must be left alone for a while, not tried again at once")
	}

	// The wait is a wait and not a verdict: once it is up the track is
	// offered again, where markFailed would have written it off for the run.
	l.featMu.Lock()
	rec := l.features[it.ID]
	rec.retry = time.Now().Add(-time.Second)
	l.features[it.ID] = rec
	l.featMu.Unlock()
	if !l.needsAnalysis(&it) {
		t.Error("once the wait is up the track must be offered again")
	}

	// And the contrast, which is what the pass used to do to it.
	l.markFailed(it)
	if l.needsAnalysis(&it) {
		t.Error("a decode that really failed is remembered for the run")
	}
}

// A track is described from a quarter, a half and three quarters of the way
// through, so the windows are chosen from its duration — and the vector is
// then stamped with the file's mtime and size, which makes it permanent. A
// file the tag pass has never looked at has no duration *yet*, and reading
// it from the fixed marks meant for a file whose length cannot be read at
// all would fix the wrong seconds of it for good.
func TestAnalysisWaitsForALengthNobodyHasReadYet(t *testing.T) {
	for _, c := range []struct {
		name     string
		it       Item
		enriched bool
		want     bool
	}{
		{"length known", Item{Kind: KindAudio, Duration: 180000}, false, true},
		{"length looked for and not found", Item{Kind: KindAudio}, true, true},
		{"never looked at", Item{Kind: KindAudio}, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			it := c.it
			it.enriched = c.enriched
			if got := readyForAnalysis(it); got != c.want {
				t.Errorf("readyForAnalysis = %v, want %v", got, c.want)
			}
		})
	}
}

// The memo is validated in one critical section and committed in another,
// with the walk in between, so two counts can overlap and the older one can
// commit last. Its answer is right; its stamp is behind, and every later
// caller then mismatches it and walks again for a number that was already
// there.
func TestWatchTotalsKeepsTheNewerMemo(t *testing.T) {
	l := quietLib("/m")
	l.commitWatchTotals(5, 3, 7, 2)

	// The overlapping count that sampled the older watch version.
	l.commitWatchTotals(5, 2, 6, 2)
	if l.totalsWatchVer != 3 || l.totalsStarted != 7 {
		t.Errorf("a stale commit replaced a fresher memo: watchVer %d started %d", l.totalsWatchVer, l.totalsStarted)
	}
	// And one from before the library itself moved.
	l.commitWatchTotals(4, 9, 1, 1)
	if l.totalsVersion != 5 || l.totalsWatchVer != 3 {
		t.Errorf("a commit from an older version replaced a newer memo: %d.%d", l.totalsVersion, l.totalsWatchVer)
	}
	// A genuinely newer count is what the memo is for.
	l.commitWatchTotals(5, 4, 8, 2)
	if l.totalsWatchVer != 4 || l.totalsStarted != 8 {
		t.Errorf("the newer count was not committed: watchVer %d started %d", l.totalsWatchVer, l.totalsStarted)
	}
}

// The three feature caches are built with no lock held — half a second over
// the whole library — so two builders overlap whenever a vector or a verdict
// lands between them. Installing unconditionally let the slower one put its
// older answer back, and every request arriving after that rebuilt the lot
// again because the stamp no longer matched.
func TestFeatureCachesKeepTheNewerBuild(t *testing.T) {
	l := quietLib("/m")

	l.putScaled(&scaled{gen: 6})
	l.putScaled(&scaled{gen: 5})
	if l.scaledCache.gen != 6 {
		t.Errorf("an older scaling replaced a newer one: gen %d", l.scaledCache.gen)
	}
	l.putScaled(&scaled{gen: 7})
	if l.scaledCache.gen != 7 {
		t.Errorf("a newer scaling was refused: gen %d", l.scaledCache.gen)
	}

	l.putAffinity(&affinity{likesGen: 2, featGen: 6})
	l.putAffinity(&affinity{likesGen: 2, featGen: 5})
	if l.affinityCache.featGen != 6 {
		t.Errorf("an affinity built against older vectors replaced a newer one: featGen %d", l.affinityCache.featGen)
	}
	l.putAffinity(&affinity{likesGen: 1, featGen: 6})
	if l.affinityCache.likesGen != 2 {
		t.Errorf("an affinity built against older verdicts replaced a newer one: likesGen %d", l.affinityCache.likesGen)
	}
	l.putAffinity(&affinity{likesGen: 3, featGen: 7})
	if l.affinityCache.likesGen != 3 || l.affinityCache.featGen != 7 {
		t.Errorf("a newer affinity was refused: %d.%d", l.affinityCache.likesGen, l.affinityCache.featGen)
	}

	l.putSounds(&sounds{version: 4, featGen: 6})
	l.putSounds(&sounds{version: 3, featGen: 6})
	if l.soundsCache.version != 4 {
		t.Errorf("sounds from an older library replaced newer ones: version %d", l.soundsCache.version)
	}
	l.putSounds(&sounds{version: 5, featGen: 6})
	if l.soundsCache.version != 5 {
		t.Errorf("newer sounds were refused: version %d", l.soundsCache.version)
	}
}

// Similar filters its candidates by which releases are speech and stamps
// what it hands back by the same question, and the two used to be separate
// readings of the release verdicts: an album build landing between them
// handed back a track the answer had admitted as music wearing the mark of
// speech. One set now serves both.
func TestSimilarMarksWhatItFiltered(t *testing.T) {
	l := quietLib("/m")
	l.upsert("/m/one.mp3", KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
	l.upsert("/m/two.mp3", KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
	seed, other := PathID("/m/one.mp3"), PathID("/m/two.mp3")
	a := make([]float32, featureDims)
	b := make([]float32, featureDims)
	for i := range a {
		a[i] = float32(i%7) + 1
		b[i] = a[i] + 0.01
	}
	l.SetFeatures(seed, 1000, 10, a)
	l.SetFeatures(other, 1000, 10, b)

	got := l.Similar(seed, 5, 0, PathFilter{})
	if len(got) != 1 || got[0].ID != other {
		t.Fatalf("Similar answered %d items, want the one other track", len(got))
	}
	if got[0].Spoken {
		t.Error("a track admitted to a music answer must not be handed back marked as speech")
	}
}
