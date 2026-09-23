package library

import (
	"slices"
	"testing"
	"time"
)

// Two shows: one whose episodes carry titles of their own, across two
// seasons, and one whose episodes carry nothing but their numbers.
func showsWithTitles(t *testing.T) *Library {
	t.Helper()
	l := quietLib("/m")
	for _, p := range []string{
		"/m/tv/Harbour.Lights.S01E01.The.Signal.Station.1080p-GRP.mkv",
		"/m/tv/Harbour.Lights.S01E02.Low.Tide.1080p-GRP.mkv",
		"/m/tv/Harbour.Lights.S02E01.Customs.Examinations.1080p-GRP.mkv",
		"/m/tv/Harbour.Lights.S02E02.Harbour.Master.1080p-GRP.mkv",
		"/m/tv/Grey.Harvest.S01E01.1080p-GRP.mkv",
		"/m/tv/Grey.Harvest.S01E02.1080p-GRP.mkv",
	} {
		l.upsert(p, KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	}
	return l
}

// The shows a search finds, by name, with the seasons each offers under it.
func shownFor(l *Library, search string, paths PathFilter) map[string][]int {
	out := map[string][]int{}
	for _, s := range l.SearchSeries(search, "name", false, paths) {
		out[s.Name] = s.Matched
	}
	return out
}

// **A show is found by its name or by any episode in it**, and the chip over
// the listing counts by the same rule. It used to count shows with two
// episodes answering the search while the listing asked the name alone, so a
// search matching two episode titles read "Series 1" over an empty grid.
func TestAShowIsFoundByItsEpisodes(t *testing.T) {
	l := showsWithTitles(t)
	for _, c := range []struct {
		search string
		want   map[string][]int
	}{
		// By name: the whole show, every season on offer.
		{"harbour", map[string][]int{"Harbour Lights": nil}},
		{"grey harvest", map[string][]int{"Grey Harvest": nil}},
		// By one episode's title, which is enough: the show, with the season
		// that episode is in.
		{"examin", map[string][]int{"Harbour Lights": {2}}},
		{"signal station", map[string][]int{"Harbour Lights": {1}}},
		{"tide", map[string][]int{"Harbour Lights": {1}}},
		// By episodes in every season, which offers every season: the show
		// whole, as a name match hands it out.
		{"1080p", map[string][]int{"Harbour Lights": nil, "Grey Harvest": nil}},
		// One word in the name and one in an episode's title is that episode:
		// the name answers "harbour" alone, and only one episode answers both.
		{"harbour master", map[string][]int{"Harbour Lights": {2}}},
		// Every word has to answer in one episode, as it does in the file
		// listing: one word in each of two episodes is not a match of either.
		{"tide examinations", map[string][]int{}},
		{"nothing like it", map[string][]int{}},
	} {
		got := shownFor(l, c.search, PathFilter{})
		if len(got) != len(c.want) {
			t.Errorf("%q found %v, want %v", c.search, got, c.want)
			continue
		}
		for name, seasons := range c.want {
			if m, ok := got[name]; !ok || !slices.Equal(m, seasons) {
				t.Errorf("%q: %s offers seasons %v (found: %v), want %v", c.search, name, m, ok, seasons)
			}
		}
		// And the chip counts what the grid draws.
		if n := l.CountsFor(CountQuery{Search: c.search}).Series; n != len(got) {
			t.Errorf("%q: the chip says %d shows and the grid draws %d", c.search, n, len(got))
		}
	}
}

// A show found by its name is handed out whole, and one found by its
// episodes is a copy: the shared grouping is every caller's.
func TestAShowFoundByNameIsWhole(t *testing.T) {
	l := showsWithTitles(t)
	for _, s := range l.SearchSeries("harbour", "name", false, PathFilter{}) {
		if s.Matched != nil || len(s.Seasons) != 2 {
			t.Errorf("%s: matched %v over %d seasons, want the whole show", s.Name, s.Matched, len(s.Seasons))
		}
	}
	// The cached grouping is not the answer's to change: a search found by
	// episodes hands out a copy.
	_ = l.SearchSeries("examin", "name", false, PathFilter{})
	for _, s := range l.Series() {
		if s.Matched != nil {
			t.Errorf("%s: the shared grouping was given %v", s.Name, s.Matched)
		}
	}
}

// A caller confined to part of the library finds a show only by what it may
// see: an episode kept elsewhere answers nothing, for the grid or the chip.
func TestAShowIsNotFoundByAnEpisodeTheCallerCannotSee(t *testing.T) {
	l := showsWithTitles(t)
	for _, p := range []string{
		"/m/private/Harbour.Lights.S03E01.The.Lamp.Room.1080p-GRP.mkv",
		"/m/private/Harbour.Lights.S03E02.Night.Watch.1080p-GRP.mkv",
	} {
		l.upsert(p, KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	}
	tv := ParsePaths("/m/tv")
	if got := shownFor(l, "lamp room", tv); len(got) != 0 {
		t.Errorf("confined to /m/tv, %q found %v", "lamp room", got)
	}
	if n := l.CountsFor(CountQuery{Search: "lamp room", Paths: tv}).Series; n != 0 {
		t.Errorf("confined to /m/tv, the chip counts %d shows for an episode it cannot see", n)
	}
	// Unconfined, the same search finds the show, at the season it is in.
	if got := shownFor(l, "lamp room", PathFilter{}); !slices.Equal(got["Harbour Lights"], []int{3}) {
		t.Errorf("unconfined, %q found %v", "lamp room", got)
	}
}

// Television is video's own, and a performer and a genre are music's: a face
// without video, or a view narrowed to a performer, has no show in front of
// it whatever the search.
func TestNoShowIsCountedWhereNoneCanBeShown(t *testing.T) {
	l := showsWithTitles(t)
	for _, q := range []CountQuery{
		{Search: "harbour", Kinds: KindsOf(KindAudio)},
		{Search: "harbour", Artist: "Somebody"},
		{Search: "harbour", Genre: "Ambient"},
	} {
		if n := l.CountsFor(q).Series; n != 0 {
			t.Errorf("%+v counted %d shows", q, n)
		}
	}
}
