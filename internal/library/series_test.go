package library

import (
	"fmt"
	"testing"
	"time"
)

// The names here are invented, the *shapes* are not: each case is a real
// release-naming habit, written with a made-up show so the test says nothing
// about anybody's library. See CLAUDE.md on why that rule exists.
func TestParseEpisode(t *testing.T) {
	for _, c := range []struct {
		name   string
		path   string
		series string
		season int
		ep     int
	}{
		{
			// The one that decides the design: the file names the release
			// group and only the directories name the show.
			"file names the group",
			"/m/pub/Harbour.Lights.S01.1080p.BluRay.x264-FIRSTGRP/Harbour.Lights.S01E01.1080p.BluRay.x264-OTHERGRP/othergrp.hl.s01.e01.1080.mkv",
			"Harbour Lights", 1, 1,
		},
		{
			"file names the show too",
			"/m/pub/Norra.Kajen.S01.SWEDiSH.1080p.WEB.H264-GRP/Norra.Kajen.S01E06.SWEDiSH.1080p.WEB.H264-GRP/norra.kajen.s01e06.swedish.1080p.mkv",
			"Norra Kajen", 1, 6,
		},
		{
			// Underscores, and a year that is a disambiguator rather than
			// part of what anybody calls the show.
			"underscores and a year",
			"/m/Quiet_Coast.2023.S02.1080p-GRP/Quiet_Coast_2023_S02E12_1080p_WEB-DL.mkv",
			"Quiet Coast", 2, 12,
		},
		{
			"a name that begins with digits",
			"/m/12.Bridges.The.Series.S01.NORDiC-GRP/12.Bridges.The.Series.S01E65.NORDiC.mkv",
			"12 Bridges The Series", 1, 65,
		},
		{
			// The other half of a library: the file says nothing, the folder
			// says the season, and the show is named one level up.
			"a Season folder names nothing itself",
			"/m/shows/kilnworks/Season 5/kw_05_0808.mp4",
			"kilnworks", 5, 0,
		},
		{
			"the older convention",
			"/m/Grey Harvest/Grey.Harvest.1x02.avi",
			"Grey Harvest", 1, 2,
		},
		{
			"a flat file with everything in its name",
			"/m/Downloads/The.Long.Field.S06E13.1080p.WEB.h264-GRP.mkv",
			"The Long Field", 6, 13,
		},
		{
			"season pack directory, episode only in the file",
			"/m/The.Ninth.Line.S03.DVDRip-GRP/the.ninth.line.s03e07.dvdrip.avi",
			"The Ninth Line", 3, 7,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseEpisode(c.path)
			if !ok {
				t.Fatalf("no episode read from %q", c.path)
			}
			if got.Series != c.series || got.Season != c.season || got.Episode != c.ep {
				t.Errorf("got %q s%02de%02d, want %q s%02de%02d",
					got.Series, got.Season, got.Episode, c.series, c.season, c.ep)
			}
		})
	}
}

// Most of a library is not television, and the cost of a false positive is a
// film filed as episode one of a series named after its release group.
func TestParseEpisodeLeavesFilmsAlone(t *testing.T) {
	for _, path := range []string{
		"/m/Films/The.Long.Field.1977.remastered.1080p.bluray.x264-grp.mkv",
		"/m/Films/Another.Film.1968.DVDRip.XviD-GRP/CD1/grp-af-a.avi",
		"/m/Music/A Band/2001 - An Album/01 - track.mp3",
		"/m/Clips/holiday video 2019.mp4",
		"/m/Photos/DSC_0001.jpg",
		// A resolution is not a season, and neither is a codec.
		"/m/Films/Some.Film.1080p.x264.mkv",
		// The class that forced the whole-token rule: a download whose name
		// ends in a random identifier is not season 74 of anything.
		"/m/Clips/beach-clip-S74tb48v.mp4",
		"/m/Clips/tumblr_s1tuqlsKPg1a8fz7r.mp4",
		// And a pair of timestamps is not a season either.
		"/m/Clips/flash at 03x00 and 04x30.mp4",
	} {
		if got, ok := parseEpisode(path); ok {
			t.Errorf("%q read as %q s%02de%02d, want nothing", path, got.Series, got.Season, got.Episode)
		}
	}
}

func TestSeriesKey(t *testing.T) {
	// Two spellings of one show are one show.
	if SeriesKey("Harbour Lights") != SeriesKey("harbour  lights") {
		t.Error("case and spacing must not split a series")
	}
	if SeriesKey("Harbour Lights") == SeriesKey("Grey Harvest") {
		t.Error("different shows must not collide")
	}
}

// The class that a bare season marker lets in, and why it is asked only of
// directories and only with two digits: a clip whose name ends in a variant
// tag is not season two of a series named after the sentence in front of it.
func TestParseEpisodeIgnoresVariantTags(t *testing.T) {
	for _, path := range []string{
		"/m/Clips/this-was-a-long-sentence-of-a-file-name-s2_hd_8ac0232f_x265_aac.mp4",
		"/m/Clips/some-clip-s2_hd.mp4",
		"/m/Clips/another-s3-720p.mp4",
	} {
		if got, ok := parseEpisode(path); ok {
			t.Errorf("%q read as %q s%02de%02d, want nothing", path, got.Series, got.Season, got.Episode)
		}
	}
	// A season pack's directory still says what it is, in two digits.
	if got, ok := parseEpisode("/m/Grey.Harvest.S02.1080p-GRP/grp-gh-s02e04.mkv"); !ok ||
		got.Series != "Grey Harvest" || got.Season != 2 || got.Episode != 4 {
		t.Errorf("season pack: got %+v %v", got, ok)
	}
}

// The parenthesised form spells both numbers out, and the episode is worth
// having: it is what orders a season.
func TestParseEpisodeReadsWrittenOutNumbers(t *testing.T) {
	got, ok := parseEpisode("/m/Clips/Grey Harvest (Season 3 Episode 9)-aBcD1234.mp4")
	if !ok || got.Series != "Grey Harvest" || got.Season != 3 || got.Episode != 9 {
		t.Errorf("got %+v %v, want Grey Harvest s03e09", got, ok)
	}
}

// Three shapes that are not television, each of which was on screen as a
// series before the rules below existed: an aspect ratio, a season named in
// the middle of a description, and a file that mentions a season without
// saying which episode it is.
func TestParseEpisodeIgnoresLookalikes(t *testing.T) {
	for _, path := range []string{
		// A vertical clip, not season nine episode sixteen.
		"/m/Clips/some_clip_HD-9x16--aBcD1234.mp4",
		"/m/Clips/other-16x9-clip.mp4",
		// "series 1" inside a sentence of a file name.
		"/m/Clips/site com 2294166 a long description series 1 white with blue.mp4",
		"/m/Clips/A Fan Club series 1.m4v",
		// A file naming a season but no episode identifies nothing: that is
		// a season pack's directory speaking, not a file.
		"/m/Films/Some Open Season 5 Full Footage.webm",
	} {
		if got, ok := parseEpisode(path); ok {
			t.Errorf("%q read as %q s%02de%02d, want nothing", path, got.Series, got.Season, got.Episode)
		}
	}
	// The same words on a directory are exactly how a season is named.
	if _, ok := parseEpisode("/m/Grey Harvest/Series 2/episode one.mkv"); !ok {
		t.Error("a Series 2 directory is a season")
	}
	if _, ok := parseEpisode("/m/Grey Harvest/Season 5/gh_05_0808.mp4"); !ok {
		t.Error("a Season 5 directory is a season")
	}
}

// One episode is not a series. It is where the mistakes collect: a clip whose
// name happens to carry a marker makes a show of one, and every false
// positive found in practice was of that shape. The file is not hidden by
// this — it is still in the listings and still searchable.
func TestSeriesNeedsMoreThanOneEpisode(t *testing.T) {
	// The version is what the cached grouping is keyed by, so each of these
	// is asked of its own library rather than of one that grew.
	alone := quietLib("/m")
	alone.upsert("/m/Grey.Harvest.S01E01.1080p-GRP.mkv", KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	if n := len(alone.Series()); n != 0 {
		t.Fatalf("one episode made %d series, want none", n)
	}

	pair := quietLib("/m")
	pair.upsert("/m/Grey.Harvest.S01E01.1080p-GRP.mkv", KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	pair.upsert("/m/Grey.Harvest.S01E02.1080p-GRP.mkv", KindVideo, 10, time.Unix(2, 0), fileKey{}, false)
	got := pair.Series()
	if len(got) != 1 || got[0].Episodes != 2 || got[0].Name != "Grey Harvest" {
		t.Fatalf("got %+v, want one show of two episodes", got)
	}
}

// The series listing's own sort keys, which nothing exercised: the view
// offers them, so a key that quietly broke would be a control that lies.
func TestSearchSeriesSorts(t *testing.T) {
	l := quietLib("/m")
	add := func(path string, mt int64) {
		t.Helper()
		l.upsert(path, KindVideo, 10, time.Unix(mt, 0), fileKey{}, false)
	}
	// Three shows: sizes 2, 3 and 4 episodes, interleaved mtimes.
	for i := 1; i <= 2; i++ {
		add(fmt.Sprintf("/m/Grey.Harvest.S01E%02d.mkv", i), int64(100+i))
	}
	for i := 1; i <= 3; i++ {
		add(fmt.Sprintf("/m/Harbour.Lights.S01E%02d.mkv", i), int64(200+i))
	}
	for i := 1; i <= 4; i++ {
		add(fmt.Sprintf("/m/Norra.Kajen.S01E%02d.mkv", i), int64(i))
	}

	names := func(ss []*Series) []string {
		out := make([]string, len(ss))
		for i, s := range ss {
			out[i] = s.Name
		}
		return out
	}
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	if got := names(l.SearchSeries("", "name", false, PathFilter{})); !eq(got, []string{"Grey Harvest", "Harbour Lights", "Norra Kajen"}) {
		t.Errorf("by name: %v", got)
	}
	if got := names(l.SearchSeries("", "episodes", true, PathFilter{})); !eq(got, []string{"Norra Kajen", "Harbour Lights", "Grey Harvest"}) {
		t.Errorf("by episodes desc: %v", got)
	}
	if got := names(l.SearchSeries("", "mtime", true, PathFilter{})); !eq(got, []string{"Harbour Lights", "Grey Harvest", "Norra Kajen"}) {
		t.Errorf("by newest: %v", got)
	}
	// The search matches the show's name.
	if got := names(l.SearchSeries("harbour", "name", false, PathFilter{})); !eq(got, []string{"Harbour Lights"}) {
		t.Errorf("searched: %v", got)
	}
}

// Two builds of one library must agree about everything, and this is where
// they did not.
//
// The grouping walks the index's own map, which arrives in a different order
// every time. A show whose episodes all parsed as the same season and number
// — a folder of unnumbered episodes, which is common — had nothing to order
// them by, so the first one seen became the show's face. The list is rebuilt
// on every change to the library, which is every few seconds while anything
// is being written, so the tiles visibly cycled through episodes and blanked
// whenever the newest choice had no thumbnail made yet.
func TestSeriesCoverIsTheSameEveryBuild(t *testing.T) {
	l := quietLib("/m")
	// Twelve files that all read as season one, episode nothing.
	for i := 1; i <= 12; i++ {
		p := fmt.Sprintf("/m/Harbour Lights/Season 1/part_%02d.mkv", i)
		l.upsert(p, KindVideo, int64(i), time.Unix(int64(i), 0), fileKey{}, false)
	}
	first := l.Series()
	if len(first) != 1 || first[0].Episodes != 12 {
		t.Fatalf("got %+v, want one show of twelve", first)
	}
	cover := first[0].CoverID
	seasonCover := first[0].Seasons[0].CoverID
	if cover == "" {
		t.Fatal("the show has no cover at all")
	}

	// Rebuilt from scratch, many times: map order differs on every walk, so
	// a tie broken by nothing would show up within a few rounds.
	for i := range 30 {
		l.series.invalidate()
		got := l.Series()
		if got[0].CoverID != cover {
			t.Fatalf("build %d chose cover %q, first build chose %q", i, got[0].CoverID, cover)
		}
		if got[0].Seasons[0].CoverID != seasonCover {
			t.Fatalf("build %d chose season cover %q, first chose %q", i, got[0].Seasons[0].CoverID, seasonCover)
		}
	}
}

// And where the episodes are numbered, the numbers still decide: the first
// episode of the earliest season is the face, not whichever id sorts first.
func TestSeriesCoverPrefersTheFirstEpisode(t *testing.T) {
	l := quietLib("/m")
	for _, p := range []string{
		"/m/Grey.Harvest.S02E05.mkv",
		"/m/Grey.Harvest.S01E02.mkv",
		"/m/Grey.Harvest.S01E01.mkv",
	} {
		l.upsert(p, KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	}
	got := l.Series()
	if len(got) != 1 {
		t.Fatalf("got %d shows", len(got))
	}
	if want := PathID("/m/Grey.Harvest.S01E01.mkv"); got[0].CoverID != want {
		t.Errorf("cover is %q, want the first episode", got[0].CoverID)
	}
}

// A show's running time, and a season's, is shown only when every episode
// was measured: half a show measured showed half its length, where a
// release with a track unmeasured shows no length at all.
func TestAHalfMeasuredShowShowsNoLength(t *testing.T) {
	l := quietLib("/m")
	for i, ms := range []int64{2_400_000, 0} {
		path := "/m/Grey.Harvest.S01E0" + string(rune('1'+i)) + ".1080p-GRP.mkv"
		l.upsert(path, KindVideo, 10, time.Unix(int64(i+1), 0), fileKey{}, false)
		l.mu.Lock()
		l.items[PathID(path)].Duration = ms
		l.mu.Unlock()
	}
	shows := l.Series()
	if len(shows) != 1 {
		t.Fatalf("shows = %d, want 1", len(shows))
	}
	if shows[0].Duration != 0 || shows[0].Seasons[0].Duration != 0 {
		t.Errorf("a show with an unmeasured episode reports %d ms (season %d ms), want none", shows[0].Duration, shows[0].Seasons[0].Duration)
	}
	// Measured, both add up.
	l.mu.Lock()
	l.items[PathID("/m/Grey.Harvest.S01E02.1080p-GRP.mkv")].Duration = 2_600_000
	l.mu.Unlock()
	l.notify()
	shows = l.Series()
	if shows[0].Duration != 5_000_000 || shows[0].Seasons[0].Duration != 5_000_000 {
		t.Errorf("a measured show reports %d ms (season %d ms), want the sum", shows[0].Duration, shows[0].Seasons[0].Duration)
	}
}
