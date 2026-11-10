// SPDX-License-Identifier: MIT

package library

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestLiteralRankingPreservesFieldBoundariesAndSpelling(t *testing.T) {
	_, _, _, text := segmentedText([]string{"Tide Song"}, []string{"Élan", "Beacon"}, []string{"/library/disk7/FRESH/film.mkv"})
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"disk7/FRESH/", literalExact},
		{"disk7/fresh/", literalFolded},
		{"disk7 FRESH", literalMiss},
		{"Tide Song", literalExact},
		{"tide song", literalFolded},
		{"Song Tide", literalMiss},
		{"Tide  Song", literalMiss},
		{"élan", literalFolded},
		{"Élan Beacon", literalMiss}, // distinct fields cannot manufacture a phrase
		{"Élan\x00Beacon", literalMiss},
		{"  Tide Song  ", literalExact},
		{"", literalMiss},
		{"   ", literalMiss},
	} {
		if got := newLiteralQuery(tc.query).rank(text); got != tc.want {
			t.Errorf("%q: rank %d, want %d", tc.query, got, tc.want)
		}
	}
}

func TestLiteralPathsBeforeTokenMatchesAndBeforePaging(t *testing.T) {
	l := quietLib("/library")
	paths := []string{
		"/library/disk7/FRESH/z.mkv",
		"/library/disk7/FRESH/y.mkv",
		"/library/disk7/fresh/x.mkv",
		"/library/disk7/elsewhere/FRESH/w.mkv",
		"/library/disk7.FRESH/v.mkv",
	}
	for i, path := range paths {
		l.upsert(path, KindVideo, 10, time.Unix(int64(i+1), 0), fileKey{}, false)
	}
	for _, desc := range []bool{false, true} {
		want := slices.Clone(paths)
		if desc {
			want[0], want[1] = want[1], want[0]
			want[3], want[4] = want[4], want[3]
		}
		q := Query{Search: "disk7/FRESH/", Kind: KindVideo, Sort: "mtime", Desc: desc}
		got := l.List(q)
		if got.Total != len(want) {
			t.Fatalf("total %d, want %d", got.Total, len(want))
		}
		for i, it := range got.Items {
			if it.Path != want[i] {
				t.Errorf("desc=%v, item %d: %q, want %q", desc, i, it.Path, want[i])
			}
		}
		q.Offset, q.Limit = 1, 2
		page := l.List(q)
		if len(page.Items) != 2 || page.Items[0].Path != want[1] || page.Items[1].Path != want[2] {
			t.Errorf("desc=%v: page does not follow relevance order", desc)
		}
	}
	// An empty search retains the requested order, without relevance tiers.
	got := l.List(Query{Sort: "mtime", Desc: true})
	if got.Items[0].Path != paths[4] {
		t.Fatalf("empty search changed the sort")
	}
}

func TestLiteralCaseOutranksWordRelevance(t *testing.T) {
	l := quietLib("/library")
	for i, title := range []string{"Gorse", "Hillgorse", "gorse"} {
		path := fmt.Sprintf("/library/release/%d.mp3", i)
		l.upsert(path, KindAudio, 10, time.Unix(int64(i+1), 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{title: title}, 1000)
	}
	var got []string
	for _, it := range l.List(Query{Search: "gorse", Sort: "mtime"}).Items {
		got = append(got, it.Title)
	}
	// Both literal spellings precede the folded one; among them the whole
	// name still beats a substring, regardless of the requested time order.
	if want := []string{"gorse", "Hillgorse", "Gorse"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLiteralRankingAcrossCollections(t *testing.T) {
	l := quietLib("/library")
	// The collection names all answer by tokens already. Their members must
	// still be checked for a better literal answer. Alphabetical order opposes
	// relevance; the exact path must outrank even a token-exact album name.
	rows := []struct{ dir, artist, album, genre string }{
		{"disk7/elsewhere/FRESH", "Amber Quay", "disk7 FRESH", "Reed.Harbour"},
		{"disk7/fresh", "Birch Signal", "Middle", "reed harbour"},
		{"disk7/FRESH", "Cedar Beacon", "Last", "Reed Harbour"},
	}
	for i, r := range rows {
		path := fmt.Sprintf("/library/%s/track%d.mp3", r.dir, i)
		l.upsert(path, KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{title: "Tide Song", artist: r.artist, album: r.album, genre: r.genre}, 1000)
	}
	for _, desc := range []bool{false, true} {
		var albums, artists, folders, genres []string
		for _, a := range l.SearchAlbums(AlbumQuery{Search: "disk7/FRESH", Sort: "name", Desc: desc}) {
			albums = append(albums, a.Name)
		}
		for _, a := range l.SearchArtists("disk7/FRESH", "name", desc, PathFilter{}) {
			artists = append(artists, a.Name)
		}
		for _, f := range l.SearchFolders(Query{Search: "disk7/FRESH", Sort: "name", Desc: desc}) {
			folders = append(folders, f.Name)
		}
		// Genre identity is case-insensitive, so use punctuation to test literal
		// ordering here; case preference is covered for independent entities.
		for _, g := range l.SearchGenres("reed harbour", "name", desc, PathFilter{}) {
			genres = append(genres, g.Name)
		}
		for _, tc := range []struct {
			view      string
			got, want []string
		}{
			{"albums", albums, []string{"Last", "Middle", "disk7 FRESH"}},
			{"artists", artists, []string{"Cedar Beacon", "Birch Signal", "Amber Quay"}},
			{"folders", folders, []string{"FRESH", "fresh"}},
		} {
			if !slices.Equal(tc.got, tc.want) {
				t.Errorf("%s desc=%v: %v, want %v", tc.view, desc, tc.got, tc.want)
			}
		}
		if len(genres) != 2 || genres[1] != "Reed.Harbour" {
			t.Errorf("genres: %v", genres)
		}
	}
}

func TestLiteralSeriesRankingByEpisodes(t *testing.T) {
	l := quietLib("/library")
	for _, row := range []struct{ dir, show string }{
		{"disk7/elsewhere/FRESH", "Amber.Quay"},
		{"disk7/fresh", "Birch.Signal"},
		{"disk7/FRESH", "Cedar.Beacon"},
	} {
		for ep := 1; ep <= 2; ep++ {
			path := fmt.Sprintf("/library/%s/%s.S01E%02d.mkv", row.dir, row.show, ep)
			l.upsert(path, KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
		}
	}
	for _, desc := range []bool{false, true} {
		var got []string
		for _, s := range l.SearchSeries("disk7/FRESH/", "name", desc, PathFilter{}) {
			got = append(got, s.Name)
		}
		if want := []string{"Cedar Beacon", "Birch Signal", "Amber Quay"}; !slices.Equal(got, want) {
			t.Errorf("desc=%v: %v, want %v", desc, got, want)
		}
	}
}

func TestLiteralAlbumMembersRespectPaths(t *testing.T) {
	l := quietLib("/library")
	for _, row := range []struct{ path, title string }{
		{"/library/allowed/one.mp3", "Tide.Song"},
		{"/library/excluded/two.mp3", "Tide Song"},
	} {
		l.upsert(row.path, KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
		l.setMeta(PathID(row.path), tagMeta{title: row.title}, 1000)
	}
	// A playlist may contain tracks in both scopes. Its own words answer,
	// but only the excluded member has the literal phrase.
	a := &Album{TrackIDs: []string{PathID("/library/allowed/one.mp3"), PathID("/library/excluded/two.mp3")}}
	a.lower, a.nameEnd, a.cardEnd, a.literal = segmentedText([]string{"Tide.Song"}, nil, nil)
	for _, tc := range []struct {
		paths PathFilter
		want  int
	}{
		{PathFilter{}, literalExact},
		{ParsePaths("/library/allowed"), literalMiss},
	} {
		got := l.albumLiteralRanks([]*Album{a}, "Tide Song", tc.paths)[a]
		if got != tc.want {
			t.Errorf("restricted=%v: rank %d, want %d", tc.paths.Restricted(), got, tc.want)
		}
	}
}
