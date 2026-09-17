package library

import (
	"testing"
)

// speechVector and musicVector are vectors that score either side of
// spokenThreshold, built from the four columns spokenScore reads: the pause
// share, the tempo strength, the beat and the voiced/unvoiced alternation.
// The numbers are the shape real files produce — a long atmospheric song
// measured 0.316 pauses with almost no tempo, a driven one 0.000 pauses with
// a strong beat — rather than the extremes, so the test exercises the rule
// at the distance the library actually sees.
func speechVector(sound float32) []float32 {
	v := make([]float32, featureDims)
	v[51], v[52], v[53], v[54], v[55] = 0.042, 0.316, 0.104, 0.046, sound
	return v
}

func musicVector(sound float32) []float32 {
	v := make([]float32, featureDims)
	v[51], v[52], v[53], v[54], v[55] = 0.572, 0.000, 0.429, 0.046, sound
	return v
}

func TestTheTwoVectorsSitEitherSideOfTheLine(t *testing.T) {
	if sp, judged := spokenVerdict(speechVector(59.8)); !sp || !judged {
		t.Fatalf("the speech vector reads spoken=%v judged=%v, score %.3f", sp, judged, spokenScore(speechVector(59.8)))
	}
	if sp, _ := spokenVerdict(musicVector(59.8)); sp {
		t.Fatalf("the music vector reads as speech, score %.3f", spokenScore(musicVector(59.8)))
	}
}

// part is one track of a made-up release: how long it runs, and which side
// of the line the analysis put it on.
type part struct {
	mins   float64
	speech bool
}

// shelvedAsReading puts one release of those parts together and returns the
// verdict markSpoken reaches.
func shelvedAsReading(t *testing.T, genre string, parts ...part) bool {
	t.Helper()
	a := &Album{Name: "A Release", Genre: genre}
	if genre != "" {
		a.Genres = []string{genre}
	}
	sv := &scaled{vecs: map[string][]float32{}, spoke: map[string]bool{}, judged: map[string]bool{}}
	var items []*Item
	for i, p := range parts {
		id := string(rune('a' + i))
		items = append(items, &Item{ID: id, Name: id + ".mp3", Kind: KindAudio,
			Duration: int64(p.mins * 60 * 1000)})
		a.TrackIDs = append(a.TrackIDs, id)
		sv.spoke[id], sv.judged[id] = p.speech, true
	}
	markSpoken(a, items, sv)
	return a.Spoken
}

// A release is shelved as a reading only where speech beats music by a
// margin. It used to need a bare majority, and on a two-track record of long
// songs that came down to which track ran longer: one scored as speech and
// one as music, the speech one was forty seconds longer out of forty-seven
// minutes, and a black metal record left the music.
func TestOneTrackEachWayIsNotAReading(t *testing.T) {
	if shelvedAsReading(t, "Black Metal", part{23.3, false}, part{24.0, true}) {
		t.Error("a release with one track each way was shelved as a reading; 40 seconds is not a verdict")
	}
	// The other way round is the same non-answer.
	if shelvedAsReading(t, "Black Metal", part{24.0, false}, part{23.3, true}) {
		t.Error("one track each way was shelved with the music track longer")
	}
	// Both tracks reading as speech is what a reading in two parts looks
	// like, and it is still shelved.
	if !shelvedAsReading(t, "", part{23.3, true}, part{24.0, true}) {
		t.Error("a release whose every track reads as speech was left in the music")
	}
	// A single-track reading has nothing to beat and is shelved.
	if !shelvedAsReading(t, "", part{810, true}) {
		t.Error("a thirteen-hour reading in one file was left in the music")
	}
	// Two to one is the bar, so a release that clears it is shelved.
	if !shelvedAsReading(t, "", part{10, false}, part{10, true}, part{10, true}) {
		t.Error("two parts speech to one music did not clear the margin")
	}
	// And three to two does not.
	if shelvedAsReading(t, "", part{10, false}, part{10, false}, part{10, true}, part{10, true}, part{10, true}) {
		t.Error("three to two was taken for a verdict; it is a majority, not a margin")
	}
}

// The tag outranks all of it: a release whose genre names a reading is one
// whatever the sound says, which is how a library that files its readings
// under a genre of their own is believed.
func TestTheTagStillSettlesIt(t *testing.T) {
	if !shelvedAsReading(t, "Ljudbok", part{23.3, false}, part{24.0, false}) {
		t.Error("a release tagged as a reading was left in the music because it sounded like music")
	}
}
