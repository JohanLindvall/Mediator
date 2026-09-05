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
