package library

import (
	"math"
	"strings"
)

// Whether a track is somebody talking.
//
// An audiobook is filed among the music — it is a directory of tracks with
// tags, and nothing on the disk says otherwise — but it is not music, and a
// library that offers a chapter of it as "more like this" after a song, or
// counts a narrator among the performers a genre holds, has misread it.
//
// The rule was set against real files rather than reasoned out, and the
// measurement overturned one assumption. Over twelve chapters of three
// narrated books and twenty-three tracks across as many genres as the
// library offered:
//
//   - pauses (column 52) are the whole story: a reader stops between
//     sentences and a band does not. Spoken 0.31-0.46, music 0.00-0.02.
//   - the zero-crossing spread (54) says voiced and unvoiced sounds
//     alternate: spoken 0.10-0.12, music mostly 0.01-0.04 — but two harsh
//     tracks reached 0.11, so it cannot carry the verdict alone.
//   - a voice's onsets repeat at no tempo (51): spoken 0.04-0.10, music
//     0.09-0.56, overlapping at the low end.
//   - syllable-rate movement of the envelope (53), expected to mark speech,
//     marks music instead: drums move the envelope at 3-8 Hz far more than
//     a voice does (spoken 0.10-0.16, music 0.13-0.91). It is read the way
//     the data says, as a cue for music.
//   - how evenly the pitch classes are occupied says nothing: both sit at
//     0.85-0.98. It is left out.
//
// Under the rule below the two populations sit at music 0.04-0.28 and
// spoken 0.96-1.00, and the threshold is in the middle of the gap. The
// score is read off the stored vector rather than stored itself, so the
// rule can be tuned without analysing anything again. A track nothing has
// read is music: an audiobook taken for music is filed where it always
// was, where a song taken for a book vanishes from the music.

// spokenThreshold is the score at which a track is taken to be speech.
const spokenThreshold = 0.6

// spokenMinSound is how many seconds have to have actually sounded before
// a verdict is given at all. Measured on a grindcore release of ninety-nine
// tracks with a median length of four seconds: its long songs scored as
// music (0.06-0.45) and its four-second ones as speech (0.75-1.0) — three
// seconds of shouting with silence at both ends has "pauses", no tempo a
// window that short can measure, and the voiced/unvoiced alternation of a
// harsh voice. A chapter of a book is minutes; nothing under one full window
// of sound is judged, and the unjudged are music. A window is twenty
// seconds, and read in whole frames it measures 19.99 — the partial frame at
// its tail is dropped — which is why the bar is nineteen: one window clears
// it, and a four-second track never will.
const spokenMinSound = 19

// spokenScore is 0 for something that sounds entirely like music and 1 for
// something that sounds entirely like a voice reading; nil is 0.
func spokenScore(v []float32) float64 {
	if len(v) < featureDims {
		return 0
	}
	pauses := clamp01(float64(v[52]) / 0.3)
	unvoiced := clamp01(float64(v[54]) / 0.1)
	arrhythmic := clamp01((0.25 - float64(v[51])) / 0.2)
	beat := clamp01((float64(v[53]) - 0.15) / 0.3)
	return 0.55*pauses + 0.2*unvoiced + 0.15*arrhythmic + 0.1*(1-beat)
}

// spokenVerdict says whether a track reads as speech, and whether there was
// enough sound to say anything: nil, or too little, is not judged.
func spokenVerdict(v []float32) (spoken, judged bool) {
	if len(v) < featureDims || v[55] < spokenMinSound {
		return false, false
	}
	return spokenScore(v) >= spokenThreshold, true
}

// spokenGenre says whether a genre tag names an audiobook outright, in the
// spellings these libraries carry. A tag that says so needs no analysis,
// and no analysis can be argued with by a tag that does not.
func spokenGenre(tag string) bool {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "audiobook", "audiobooks", "audio book", "spoken word", "spoken", "speech",
		"ljudbok", "ljudböcker", "lydbok", "lydbog", "hörbuch", "hoerbuch", "hörbücher",
		"äänikirja", "audiolibro", "livre audio", "podcast":
		return true
	}
	return false
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }
