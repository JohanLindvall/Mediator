// SPDX-License-Identifier: MIT

package library

import (
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"
)

// The text segments and the ranks they give, without a library: the tier,
// then how many words were found only inside longer ones.
func TestHitRanks(t *testing.T) {
	text, nameEnd, cardEnd := segmentedText([]string{"Lee Shore"}, []string{"Pale Harrow", "2019"}, []string{"/library/Saltings"})
	if want := searchText("Lee Shore", "Pale Harrow", "2019", "/library/Saltings"); text != want {
		t.Fatalf("segmented text %q, want searchText's %q", text, want)
	}
	for _, c := range []struct {
		search  string
		tier    int
		partial int
	}{
		{"lee shore", hitName, 0},
		{"LEE  SHORE!", hitName, 0},
		{"shore lee", hitCard, 0}, // every word, but not the name as written
		{"lee", hitCard, 0},
		{"pale harrow", hitCard, 0},
		{"shore 2019", hitCard, 0},
		{"sho", hitCard, 1},
		{"pale harr", hitCard, 1},
		{"sho 201", hitCard, 2},
		{"saltings", hitBeyond, 0},
		{"lee saltings", hitBeyond, 0},
		{"salt", hitBeyond, 1},
		{"lee salt", hitBeyond, 1},
		{"le salt", hitBeyond, 2},
	} {
		h := newHits(searchWords(c.search))
		if got, want := h.rank(text, nameEnd, cardEnd), h.at(c.tier, c.partial); got != want {
			t.Errorf("%q ranks %d, want %d (tier %d, %d partial)", c.search, got, want, c.tier, c.partial)
		}
	}
	// On a card that shows the word, the card decides: part of a word there
	// stays part of one, however whole the folder holds it.
	text, nameEnd, cardEnd = segmentedText([]string{"Gorseland"}, []string{"Pale Harrow"}, []string{"/library/Gorse"})
	if h := newHits(searchWords("gorse")); h.rank(text, nameEnd, cardEnd) != h.at(hitCard, 1) {
		t.Errorf("a part of a word on the card ranks %d, want %d", h.rank(text, nameEnd, cardEnd), h.at(hitCard, 1))
	}
	// Within a tier fewer parts of words come first, and every rank of a tier
	// comes before any of the next, whatever the words.
	for n := 1; n <= 4; n++ {
		h := newHits(make([]string, n))
		for _, tier := range []int{hitCard, hitBeyond} {
			for p := range n {
				if h.at(tier, p) >= h.at(tier, p+1) {
					t.Errorf("with %d words, tier %d: %d partial ranks %d, %d partial %d",
						n, tier, p, h.at(tier, p), p+1, h.at(tier, p+1))
				}
			}
		}
		if !(h.at(hitName, 0) < h.at(hitCard, 0) && h.at(hitCard, n) < h.at(hitBeyond, 0)) {
			t.Errorf("with %d words the tiers overlap: name %d, card %d–%d, beyond from %d",
				n, h.at(hitName, 0), h.at(hitCard, 0), h.at(hitCard, n), h.at(hitBeyond, 0))
		}
	}
}

// A whole word comes before part of one, in each tier: the tile titled with
// the word before the ones whose title only holds it, and a folder named by
// it before one whose name holds it — each kept in the view's order, and the
// card's tier still before the folder's.
func TestWholeWordsListBeforePartsOfWords(t *testing.T) {
	l := New([]string{"/library"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	add := func(path string, mtime int64, title string) {
		l.upsert(path, KindAudio, 1000, time.Unix(mtime, 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{title: title, artist: "Pale Harrow", album: "Saltings"}, 1000)
	}
	// Oldest first: newest first puts every part of a word ahead of a whole one.
	add("/library/a/01 a.mp3", 100, "Gorse")
	add("/library/b/01 b.mp3", 200, "Gorse Walk")
	add("/library/c/01 c.mp3", 300, "Hillgorse")
	add("/library/d/01 d.mp3", 400, "Gorseland")
	add("/library/Gorse Hollow/01 e.mp3", 500, "Shingle")
	add("/library/Gorsemoor/01 f.mp3", 600, "Brinewater")
	titles := func(desc bool) []string {
		var out []string
		for _, it := range l.List(Query{Search: "gorse", Sort: "mtime", Desc: desc}).Items {
			out = append(out, it.Title)
		}
		return out
	}
	if got, want := titles(true), []string{"Gorse", "Gorse Walk", "Gorseland", "Hillgorse", "Shingle", "Brinewater"}; !slices.Equal(got, want) {
		t.Errorf("newest first: %v, want %v", got, want)
	}
	if got, want := titles(false), []string{"Gorse", "Gorse Walk", "Hillgorse", "Gorseland", "Shingle", "Brinewater"}; !slices.Equal(got, want) {
		t.Errorf("oldest first: %v, want %v", got, want)
	}
}

// libForWholeness holds releases and performers found by a whole word and by
// part of one, by their own text and through a track or a release.
func libForWholeness(t *testing.T) *Library {
	t.Helper()
	l := New([]string{"/library"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	add := func(path, title, artist, album string) {
		l.upsert(path, KindAudio, 1000, time.Unix(100, 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{title: title, artist: artist, album: album}, 1000)
	}
	add("/library/Pale Harrow/Gorse Walk/01 a.mp3", "Shingle", "Pale Harrow", "Gorse Walk")    // its title
	add("/library/Grey Harvest/Gorseland/01 a.mp3", "Brinewater", "Grey Harvest", "Gorseland") // part of its title
	add("/library/Gorsemen/Brackenfold/01 a.mp3", "Marram Path", "Gorsemen", "Brackenfold")    // part of its performer
	add("/library/Tern Signal/Windward/01 a.mp3", "Gorse", "Tern Signal", "Windward")          // a track's title
	add("/library/Lee Shore/Leeward/01 a.mp3", "Hillgorse", "Lee Shore", "Leeward")            // part of a track's
	return l
}

// A release found by a track ranks by how whole that track's answer is: a
// track titled with the word before one whose title only holds it.
func TestReleasesFoundByATrackRankByIt(t *testing.T) {
	l := libForWholeness(t)
	names := func(desc bool) []string {
		var out []string
		for _, a := range l.SearchAlbums(AlbumQuery{Search: "gorse", Sort: "name", Desc: desc}) {
			out = append(out, a.Name)
		}
		return out
	}
	if got, want := names(false), []string{"Gorse Walk", "Brackenfold", "Gorseland", "Windward", "Leeward"}; !slices.Equal(got, want) {
		t.Errorf("by name: %v, want %v", got, want)
	}
	if got, want := names(true), []string{"Gorse Walk", "Gorseland", "Brackenfold", "Windward", "Leeward"}; !slices.Equal(got, want) {
		t.Errorf("by name, turned round: %v, want %v", got, want)
	}
}

// A performer found by a release ranks by how whole the release's answer is,
// its own title or the track that found it, as one found by their own text.
func TestPerformersFoundByAReleaseRankByIt(t *testing.T) {
	l := libForWholeness(t)
	names := func(desc bool) []string {
		var out []string
		for _, a := range l.SearchArtists("gorse", "name", desc, PathFilter{}) {
			out = append(out, a.Name)
		}
		return out
	}
	if got, want := names(false), []string{"Gorsemen", "Pale Harrow", "Tern Signal", "Grey Harvest", "Lee Shore"}; !slices.Equal(got, want) {
		t.Errorf("by name: %v, want %v", got, want)
	}
	if got, want := names(true), []string{"Gorsemen", "Tern Signal", "Pale Harrow", "Lee Shore", "Grey Harvest"}; !slices.Equal(got, want) {
		t.Errorf("by name, turned round: %v, want %v", got, want)
	}
}

// A show found by an episode ranks by how whole that episode's answer is.
func TestShowsFoundByAnEpisodeRankByIt(t *testing.T) {
	l := quietLib("/m")
	for _, p := range []string{
		"/m/tv/Harbour.Lights.S01E01.Gorse.Fire.1080p-GRP.mkv",
		"/m/tv/Harbour.Lights.S01E02.Low.Tide.1080p-GRP.mkv",
		"/m/tv/Grey.Harvest.S01E01.Gorseland.1080p-GRP.mkv",
		"/m/tv/Grey.Harvest.S01E02.Stubble.1080p-GRP.mkv",
		"/m/tv/Gorsemoor.S01E01.1080p-GRP.mkv",
		"/m/tv/Gorsemoor.S01E02.1080p-GRP.mkv",
	} {
		l.upsert(p, KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	}
	var names []string
	for _, s := range l.SearchSeries("gorse", "name", false, PathFilter{}) {
		names = append(names, s.Name)
	}
	// Part of its name on the card first; then by an episode titled with the
	// word, then by one whose title only holds it — by name, the other way.
	if want := []string{"Gorsemoor", "Harbour Lights", "Grey Harvest"}; !slices.Equal(names, want) {
		t.Errorf("shows %v, want %v", names, want)
	}
}

// libForRanking holds what one search finds three ways: a track and a release
// named by it, a track and a release whose card shows it as the performer, and
// tracks found only by the folder they are kept in or the release they are on.
func libForRanking(t *testing.T) *Library {
	t.Helper()
	l := New([]string{"/library"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	add := func(path string, mtime int64, title, artist, album string) {
		l.upsert(path, KindAudio, 1000, time.Unix(mtime, 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{title: title, artist: artist, album: album}, 1000)
	}
	// Oldest first, so newest first puts every tier in the wrong order.
	add("/library/Pale Harrow/Saltings/01 lee.mp3", 100, "Lee Shore", "Pale Harrow", "Saltings")
	add("/library/Lee Shore/Windward/01 tide.mp3", 200, "Tide", "Lee Shore", "Windward")
	add("/library/Gorse Beacon/Lee Shore/01 one.mp3", 250, "One", "Gorse Beacon", "Lee Shore")
	add("/library/bootlegs of lee shore/01 salt.mp3", 300, "Salt", "Tern Signal", "Live")
	add("/library/bootlegs of lee shore/02 spray.mp3", 400, "Spray", "Tern Signal", "Live")
	return l
}

// A search lists what it names first, then what shows it on the card in
// front of the viewer, then what was found further away — each tier in the
// order the view is sorted by, whichever way that runs.
func TestASearchListsWhatItNamesFirst(t *testing.T) {
	l := libForRanking(t)
	titles := func(desc bool) []string {
		var out []string
		for _, it := range l.List(Query{Search: "lee shore", Sort: "mtime", Desc: desc}).Items {
			out = append(out, it.Title)
		}
		return out
	}
	// Named "Lee Shore"; by Lee Shore, and on the release called Lee Shore,
	// both on the tile; and the two kept in a folder of that name.
	if got, want := titles(true), []string{"Lee Shore", "One", "Tide", "Spray", "Salt"}; !slices.Equal(got, want) {
		t.Errorf("newest first: %v, want %v", got, want)
	}
	if got, want := titles(false), []string{"Lee Shore", "Tide", "One", "Salt", "Spray"}; !slices.Equal(got, want) {
		t.Errorf("oldest first: %v, want %v", got, want)
	}
}

// A release named by the search comes first, then one whose card shows it —
// the performer — and last one found by a track on it, a page further in.
func TestAReleaseSearchListsWhatItNamesFirst(t *testing.T) {
	l := libForRanking(t)
	var names []string
	for _, a := range l.SearchAlbums(AlbumQuery{Search: "lee shore", Sort: "name"}) {
		names = append(names, a.Name)
	}
	// By name, Live and Saltings would come before Windward.
	if want := []string{"Lee Shore", "Windward", "Live", "Saltings"}; !slices.Equal(names, want) {
		t.Errorf("releases %v, want %v", names, want)
	}
}

// The case this was asked for: the performer the search names first, and the
// performers found through their releases after, in the view's own order.
func TestAPerformerSearchListsWhoItNamesFirst(t *testing.T) {
	l := libForRanking(t)
	var names []string
	for _, a := range l.SearchArtists("lee shore", "name", true, PathFilter{}) {
		names = append(names, a.Name)
	}
	if want := []string{"Lee Shore", "Tern Signal", "Pale Harrow", "Gorse Beacon"}; !slices.Equal(names, want) {
		t.Errorf("performers %v, want %v", names, want)
	}
}
