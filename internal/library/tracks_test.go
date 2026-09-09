package library

import (
	"fmt"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"
)

// A small tagged catalogue: one performer with a dated pair of releases and
// an undated one, another with one release, one release nobody is credited
// for, and genres that overlap.
func libForQueue(t *testing.T) *Library {
	t.Helper()
	l := New([]string{"/library"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	add := func(dir, artist, album, genre string, year, tracks int) {
		for i := 1; i <= tracks; i++ {
			path := fmt.Sprintf("/library/%s/%02d track.mp3", dir, i)
			l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
			l.setMeta(PathID(path), tagMeta{
				artist: artist, album: album, genre: genre, year: year, track: i,
			}, 1000)
		}
	}
	add("Gorse Beacon/Later Work", "Gorse Beacon", "Later Work", "Rock|Folk", 2004, 1)
	add("Gorse Beacon/Signal Fires", "Gorse Beacon", "Signal Fires", "Rock", 2001, 2)
	add("Gorse Beacon/Demos", "Gorse Beacon", "Demos", "Rock", 0, 1)
	add("Tern Signal/Harbour Lights", "Tern Signal", "Harbour Lights", "Folk", 1999, 2)
	add("comp/Sampler", "", "Sampler", "Rock", 2010, 1)
	return l
}

func albumNames(albums []*Album) []string {
	out := make([]string, 0, len(albums))
	for _, a := range albums {
		out = append(out, a.Name)
	}
	return out
}

// A performer's releases are played through from the first: dated ones
// oldest first, whatever they are called, and the undated ones after.
func TestReleasesOfArePlayedThroughFromTheFirst(t *testing.T) {
	l := libForQueue(t)
	artists := l.SearchArtists("gorse", "name", false, PathFilter{})
	if len(artists) != 1 {
		t.Fatalf("performers matching the search: %d, want 1", len(artists))
	}
	got := albumNames(l.ReleasesOf(artists, PathFilter{}))
	want := []string{"Signal Fires", "Later Work", "Demos"}
	if !slices.Equal(got, want) {
		t.Errorf("releases = %q, want %q", got, want)
	}
}

// A genre is its performers' discographies one after another, a release
// filed under two of the genres asked for is there once, and the releases
// nobody is credited for come last.
func TestReleasesInAGenreAreEachThereOnce(t *testing.T) {
	l := libForQueue(t)
	genres := l.SearchGenres("", "name", false, PathFilter{})
	if got := albumNames(nil); len(genres) != 2 || genres[0].Name != "Folk" || len(got) != 0 {
		t.Fatalf("genres = %+v, want Folk then Rock", genres)
	}
	got := albumNames(l.ReleasesIn(genres, PathFilter{}))
	want := []string{"Later Work", "Harbour Lights", "Signal Fires", "Demos", "Sampler"}
	if !slices.Equal(got, want) {
		t.Errorf("releases = %q, want %q", got, want)
	}
}

// The tracks keep each release's running order, and the cap is a cap.
func TestTracksOfKeepTheRunningOrderAndTheCap(t *testing.T) {
	l := libForQueue(t)
	albums := l.SearchAlbums(AlbumQuery{Artist: "gorse beacon", Sort: "year", Desc: false})
	tracks := l.TracksOf(albums, PathFilter{}, 3)
	var got []string
	for _, it := range tracks {
		got = append(got, it.Album+"/"+it.Name)
	}
	want := []string{"Signal Fires/01 track.mp3", "Signal Fires/02 track.mp3", "Later Work/01 track.mp3"}
	if !slices.Equal(got, want) {
		t.Errorf("tracks = %q, want %q", got, want)
	}
}

// A confined caller is handed only the tracks it may see, even from a
// release it may see the rest of.
func TestTracksOfHandAConfinedCallerOnlyItsOwn(t *testing.T) {
	l := libForQueue(t)
	f := ParsePaths("/library/Gorse Beacon/Signal Fires")
	tracks := l.TracksOf(l.Albums(), f, 100)
	if len(tracks) != 2 {
		t.Fatalf("tracks under one release = %d, want 2", len(tracks))
	}
	for _, it := range tracks {
		if it.Album != "Signal Fires" {
			t.Errorf("a track from %q was handed to a caller confined to another release", it.Album)
		}
	}
}

// A queue holds one place for one recording, whatever filled it — and folds
// nothing that is a different song, a different performer's, a distinctly
// named performance, or untagged.
func TestFoldRecordings(t *testing.T) {
	track := func(id, artist, title string) Item {
		return Item{ID: id, Artist: artist, Title: title}
	}
	got := FoldRecordings([]Item{
		// The album, then the same song on three live records and a
		// bootleg, tagged alike: one song in five files.
		track("1", "Gorse Beacon", "Signal Fires"),
		track("2", "Gorse Beacon", "First Breath"),
		track("3", "Gorse Beacon", "signal fires"),
		track("4", "GORSE BEACON", "Signal Fires"),
		// A performance that says it is one keeps its place.
		track("5", "Gorse Beacon", "Signal Fires [Live]"),
		// Another performer's song of the same name is another song.
		track("6", "Tern Signal", "Signal Fires"),
		// Nothing tagged is never folded: the title is unknown, and two
		// files called "01" are not one recording.
		track("7", "", ""),
		track("8", "", ""),
	})
	want := []string{"1", "2", "5", "6", "7", "8"}
	ids := make([]string, 0, len(got))
	for _, it := range got {
		ids = append(ids, it.ID)
	}
	if !slices.Equal(ids, want) {
		t.Errorf("folded to %v, want %v", ids, want)
	}
	if len(FoldRecordings(nil)) != 0 {
		t.Error("nothing folded to something")
	}
}

// A release answers to one of its tracks as well as to its own id: that is
// what lets a song's tile link to the album it is on, the client having no
// way to work out an id hashed from a directory it never sees.
func TestAlbumByIDAnswersToATrack(t *testing.T) {
	l := libForQueue(t)
	albums := l.SearchAlbums(AlbumQuery{Sort: "name", Desc: false})
	if len(albums) == 0 {
		t.Fatal("no releases were grouped")
	}
	want := albums[0]
	if len(want.TrackIDs) == 0 {
		t.Fatalf("release %q has no tracks", want.Name)
	}
	got, tracks, ok := l.AlbumByID(want.TrackIDs[len(want.TrackIDs)-1])
	if !ok {
		t.Fatal("a track's id found no release")
	}
	if got.ID != want.ID {
		t.Errorf("track answered with release %q, want %q", got.Name, want.Name)
	}
	if len(tracks) != len(want.TrackIDs) {
		t.Errorf("release came back with %d tracks, want %d", len(tracks), len(want.TrackIDs))
	}
	// Its own id still answers, and nothing else does.
	if _, _, ok := l.AlbumByID(want.ID); !ok {
		t.Error("a release no longer answers to its own id")
	}
	if _, _, ok := l.AlbumByID("feedfacedeadbeef"); ok {
		t.Error("an id belonging to nothing found a release")
	}
}
