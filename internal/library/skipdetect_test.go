package library

import (
	"math"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// A season built from sound that never existed: every episode opens with
// its own cold open, then the theme, then its own story, and closes with
// its own story and then the credits music. What the comparison has to find
// is the theme and the credits in each — where they are in *that* episode,
// which differs — and nothing in the one episode that has neither.
func TestDetectSeasonFindsTheThemeAndTheCredits(t *testing.T) {
	theme := synthSound(100, 75)
	credits := synthSound(101, 50)
	// Shorter than the real windows, since the comparison does not care
	// and the test would otherwise fingerprint an hour of sound.
	const dur = 1320.0 // 22 minutes
	const headSecs, tailSecs = 180.0, 150.0
	tailFrom := dur - tailSecs
	type spec struct {
		id       string
		coldOpen float64 // seconds before the theme
		credAt   float64 // seconds into the tail window where the credits begin
		plain    bool    // no theme, no credits
	}
	specs := []spec{
		{"ep1", 0, 60, false},
		{"ep2", 30.5, 90, false},
		{"ep3", 0, 40, false},
		{"ep4", 45, 75, false},
		{"ep5", 0, 0, true},
	}
	var eps []episodePrints
	for i, sp := range specs {
		seed := int64(200 + 10*i)
		var head []float32
		if sp.plain {
			head = synthSound(seed, headSecs)
		} else {
			head = concat(synthSound(seed, sp.coldOpen), theme, synthSound(seed+1, headSecs-sp.coldOpen-75))
		}
		var tail []float32
		if sp.plain {
			tail = synthSound(seed+2, tailSecs)
		} else {
			tail = concat(synthSound(seed+2, sp.credAt), credits, synthSound(seed+3, tailSecs-sp.credAt-50))
		}
		eps = append(eps, episodePrints{
			id: sp.id, episode: i + 1, duration: dur,
			head: fingerprintPCM(head), tailFrom: tailFrom, tail: fingerprintPCM(tail),
		})
	}
	got := detectSeason(eps)
	for i, sp := range specs {
		m, ok := got[sp.id]
		if sp.plain {
			if ok {
				t.Errorf("%s has no theme and no credits and was marked %+v", sp.id, m)
			}
			continue
		}
		if !ok {
			t.Errorf("%s: nothing found", sp.id)
			continue
		}
		if math.Abs(m.IntroStart-sp.coldOpen) > 1 || math.Abs(m.IntroEnd-(sp.coldOpen+75)) > 1.5 {
			t.Errorf("%s: intro %.2f–%.2f, want %.2f–%.2f", sp.id, m.IntroStart, m.IntroEnd, sp.coldOpen, sp.coldOpen+75)
		}
		wantOutro := dur - (tailFrom + sp.credAt)
		if math.Abs(m.Outro-wantOutro) > 1.5 {
			t.Errorf("%s: credits %.2f s before the end, want %.2f", sp.id, m.Outro, wantOutro)
		}
		_ = i
	}
}

// Two episodes are enough: with one neighbour there is nobody to agree
// with, so its answer stands. With more, one neighbour's word is not.
func TestDetectSeasonWantsAgreementWhereItCanHaveIt(t *testing.T) {
	theme := synthSound(300, 60)
	const headSecs = 150.0
	ep := func(id string, seed int64, withTheme bool) episodePrints {
		var head []float32
		if withTheme {
			head = concat(synthSound(seed, 10), theme, synthSound(seed+1, headSecs-70))
		} else {
			head = synthSound(seed, headSecs)
		}
		return episodePrints{id: id, duration: 1320, head: fingerprintPCM(head),
			tailFrom: 1200, tail: fingerprintPCM(synthSound(seed+2, 120))}
	}
	two := detectSeason([]episodePrints{ep("a", 400, true), ep("b", 410, true)})
	if m, ok := two["a"]; !ok || math.Abs(m.IntroStart-10) > 1 {
		t.Errorf("a two-episode season was not judged on its one comparison: %+v", two)
	}
	// Three episodes where only two share the theme: the third shares
	// nothing and is not marked; the two have only each other, every other
	// episode having been asked, so the one answer stands.
	three := detectSeason([]episodePrints{ep("a", 400, true), ep("b", 410, true), ep("c", 420, false)})
	if _, ok := three["c"]; ok {
		t.Error("an episode sharing nothing was marked")
	}
	if _, ok := three["a"]; !ok {
		t.Error("two episodes that agree were not marked because a third shared nothing")
	}
	// But one answer among many asked is not enough: with eight episodes
	// and one neighbour agreeing, nobody else was even asked past the
	// sixth, and a two-part story shares more than its intro.
	var eight []episodePrints
	for i := range 8 {
		eight = append(eight, ep(string(rune('a'+i)), int64(500+10*i), i < 2))
	}
	many := detectSeason(eight)
	if _, ok := many["a"]; ok {
		t.Error("a single agreeing neighbour among many was believed")
	}
	// Neighbours are the nearest, on both sides, as many as may be asked.
	if got := partnersOf(6, 0); len(got) != 5 || got[0] != 1 || got[1] != 2 {
		t.Errorf("partners of the first of six: %v", got)
	}
	if got := partnersOf(6, 3); len(got) != 5 || got[0] != 4 || got[1] != 2 || got[2] != 5 {
		t.Errorf("partners of the fourth of six: %v", got)
	}
	if got := partnersOf(2, 1); len(got) != 1 || got[0] != 0 {
		t.Errorf("partners of the second of two: %v", got)
	}
	if got := partnersOf(20, 10); len(got) != skipMaxPartners {
		t.Errorf("a long season asks %d, want %d", len(got), skipMaxPartners)
	}
}

// The marks are kept, asked for, and forgotten with a zero.
func TestSkipMarksAreHeldAndAskedFor(t *testing.T) {
	l := quietLib(t.TempDir())
	if _, ok := l.SkipFor("x"); ok {
		t.Fatal("marks for nothing")
	}
	l.SetSkip("x", blob.Skip{IntroEnd: 30})
	if m, ok := l.SkipFor("x"); !ok || m.IntroEnd != 30 {
		t.Errorf("marks not held: %+v %v", m, ok)
	}
	l.SetSkip("x", blob.Skip{})
	if _, ok := l.SkipFor("x"); ok {
		t.Error("a zero mark was kept")
	}
	// Asking puts the season at the front of the queue; a film that is
	// nobody's episode asks for nothing.
	l.WantSkips(Item{ID: "e", Kind: KindVideo, Series: "Harbour Lights", Season: 2})
	l.WantSkips(Item{ID: "f", Kind: KindVideo})
	l.skips.mu.Lock()
	_, wanted := l.skips.wanted["harbour lights|2"]
	n := len(l.skips.wanted)
	l.skips.mu.Unlock()
	if !wanted || n != 1 {
		t.Errorf("wanted = %v (%d entries), want the one season", wanted, n)
	}
}

// An intro that runs into the end of the fingerprint was cut by the window
// and not by the episode. Where the rest of the season shows how long the
// intro is, the cut one gets that length from its own start.
func TestDetectSeasonRepairsAnIntroCutByTheWindow(t *testing.T) {
	theme := synthSound(700, 60)
	const headSecs = 150.0
	ep := func(id string, seed int64, coldOpen float64) episodePrints {
		var head []float32
		if coldOpen+60 >= headSecs {
			// The theme runs into the end of the window and is cut there.
			head = concat(synthSound(seed, coldOpen), theme)[:int(headSecs*fpRate)]
		} else {
			head = concat(synthSound(seed, coldOpen), theme, synthSound(seed+1, headSecs-coldOpen-60))
		}
		return episodePrints{id: id, duration: 1320, head: fingerprintPCM(head),
			tailFrom: 1200, tail: fingerprintPCM(synthSound(seed+2, 120))}
	}
	got := detectSeason([]episodePrints{ep("a", 800, 10), ep("b", 810, 20), ep("c", 820, 120), ep("d", 830, 15)})
	c, ok := got["c"]
	if !ok {
		t.Fatal("the cut episode was not marked at all")
	}
	if math.Abs(c.IntroStart-120) > 1 {
		t.Errorf("the cut episode's intro starts at %.2f, want 120", c.IntroStart)
	}
	if math.Abs(c.IntroEnd-180) > 1.5 {
		t.Errorf("the cut episode's intro ends at %.2f, want the season's 60 s from its start (180)", c.IntroEnd)
	}
	if a := got["a"]; math.Abs(a.IntroEnd-70) > 1.5 {
		t.Errorf("an episode seen whole was changed: %+v", a)
	}
	// Reading a season from the asked-for episode outwards.
	eps := []Item{{ID: "1"}, {ID: "2"}, {ID: "3"}, {ID: "4"}, {ID: "5"}}
	var order []string
	for _, it := range nearestFirst(eps, "3") {
		order = append(order, it.ID)
	}
	if strings.Join(order, "") != "34251" {
		t.Errorf("nearest first from the third of five: %v", order)
	}
	if got := nearestFirst(eps, "nope"); len(got) != 5 || got[0].ID != "1" {
		t.Errorf("an unknown episode reorders nothing: %v", got)
	}
}
