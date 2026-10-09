// SPDX-License-Identifier: MIT

package library

import (
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"
)

// The text segments and the tiers they give, without a library.
func TestHitTiers(t *testing.T) {
	text, nameEnd, cardEnd := segmentedText([]string{"Lee Shore"}, []string{"Pale Harrow", "2019"}, []string{"/library/Saltings"})
	if want := searchText("Lee Shore", "Pale Harrow", "2019", "/library/Saltings"); text != want {
		t.Fatalf("segmented text %q, want searchText's %q", text, want)
	}
	for _, c := range []struct {
		search string
		want   int
	}{
		{"lee shore", hitName},
		{"LEE  SHORE!", hitName},
		{"lee", hitCard},
		{"pale harrow", hitCard},
		{"shore 2019", hitCard},
		{"saltings", hitBeyond},
		{"lee saltings", hitBeyond},
	} {
		words := searchWords(c.search)
		phrase := ""
		for i, w := range words {
			if i > 0 {
				phrase += " "
			}
			phrase += w
		}
		if got := hitTier(text, nameEnd, cardEnd, words, phrase); got != c.want {
			t.Errorf("%q is tier %d, want %d", c.search, got, c.want)
		}
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
