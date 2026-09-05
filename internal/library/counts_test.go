package library

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// A library with a bit of everything, so a search can match some of each.
func libForCounts(t *testing.T) *Library {
	t.Helper()
	l := New([]string{"/library"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	add := func(path string, kind Kind) {
		l.upsert(path, kind, 1000, time.Unix(1, 0), fileKey{}, false)
	}
	// Two tracks of one release, which is also an album and an artist.
	for _, name := range []string{"01 tribute.mp3", "02 elsewhere.mp3"} {
		path := "/library/Tribute Band - Covers/" + name
		add(path, KindAudio)
		l.setMeta(PathID(path), tagMeta{
			artist: "Tribute Band", album: "Covers", genre: "Rock", year: 2001,
		}, 1000)
	}
	// A second release with nothing to do with the search.
	add("/library/Other Band - Songs/01 something.mp3", KindAudio)
	l.setMeta(PathID("/library/Other Band - Songs/01 something.mp3"), tagMeta{
		artist: "Other Band", album: "Songs",
	}, 1000)

	add("/library/clips/tribute concert.mp4", KindVideo)
	add("/library/clips/holiday.mp4", KindVideo)
	add("/library/pictures/tribute poster.jpg", KindImage)
	// Album and artist totals come from their builds, which main seeds after
	// the first scan and the broadcast loop keeps up afterwards.
	l.RefreshCounts()
	return l
}

func TestCountsForASearch(t *testing.T) {
	l := libForCounts(t)

	all := l.Counts()
	if all.Total != 6 || all.Video != 2 || all.Image != 1 || all.Audio != 3 {
		t.Fatalf("library totals = %+v", all)
	}
	if all.Albums != 2 || all.Artists != 2 {
		t.Fatalf("album/artist totals = %+v", all)
	}

	c := l.CountsFor(CountQuery{Search: "tribute"})
	// One video, one image, and the two tracks — whose *path* carries the
	// word, which is what the listing matches on too.
	if c.Video != 1 || c.Image != 1 || c.Audio != 2 || c.Total != 4 {
		t.Errorf("item counts for the search = %+v, want 1 video, 1 image, 2 audio", c)
	}
	// The release is one album and one artist however many of its tracks
	// matched: these are collections, not items.
	if c.Albums != 1 || c.Artists != 1 {
		t.Errorf("album/artist counts for the search = %+v, want 1 and 1", c)
	}
}

func TestCountsForNothingIsTheWholeLibrary(t *testing.T) {
	l := libForCounts(t)
	if got, want := l.CountsFor(CountQuery{}), l.Counts(); got != want {
		t.Errorf("CountsFor an empty search = %+v, want the totals %+v", got, want)
	}
}

func TestCountsForAMissIsZero(t *testing.T) {
	l := libForCounts(t)
	c := l.CountsFor(CountQuery{Search: "nothing here matches this"})
	if c != (Counts{}) {
		t.Errorf("counts for a miss = %+v, want all zero", c)
	}
}

// The answer is cached per (search, version): a change to the library has to
// be seen by the next question, or the chips freeze at what they said before.
func TestCountsForFollowsTheLibrary(t *testing.T) {
	l := libForCounts(t)
	if n := l.CountsFor(CountQuery{Search: "tribute"}).Video; n != 1 {
		t.Fatalf("videos matching = %d, want 1", n)
	}
	l.upsert("/library/clips/another tribute.mp4", KindVideo, 1000, time.Unix(2, 0), fileKey{}, false)
	l.notify()
	if n := l.CountsFor(CountQuery{Search: "tribute"}).Video; n != 2 {
		t.Errorf("videos matching after one arrived = %d, want 2", n)
	}
}

// The listing carries the same answer, so the chips above a search do not
// need a request of their own.
func TestListCarriesTheMatchingCounts(t *testing.T) {
	l := libForCounts(t)

	res := l.List(Query{Search: "tribute", Kind: KindVideo})
	if res.Matching == nil {
		t.Fatal("a search listing came back without counts for the search")
	}
	if res.Matching.Albums != 1 || res.Matching.Video != 1 {
		t.Errorf("matching counts = %+v", *res.Matching)
	}
	// The whole-library totals are still there: an empty result with a full
	// library is "no matches", not "the library is empty".
	if res.Counts.Total != 6 {
		t.Errorf("library totals on the same result = %+v", res.Counts)
	}
	if plain := l.List(Query{}); plain.Matching != nil {
		t.Errorf("a listing with no search carries %+v, want nothing", *plain.Matching)
	}
}

// Drilled into one performer, the chips say what is in front of the viewer:
// that artist's releases and tracks, and the one artist -- not everything in
// the library that happens to match the same word.
func TestCountsForOneArtist(t *testing.T) {
	l := libForCounts(t)

	wide := l.CountsFor(CountQuery{Search: "tribute"})
	if wide.Albums != 1 || wide.Artists != 1 || wide.Video != 1 {
		t.Fatalf("the search on its own = %+v", wide)
	}

	narrow := l.CountsFor(CountQuery{Search: "tribute", Artist: "Tribute Band"})
	if narrow.Audio != 2 {
		t.Errorf("tracks by the artist = %d, want 2", narrow.Audio)
	}
	if narrow.Albums != 1 || narrow.Artists != 1 {
		t.Errorf("their releases = %+v, want one album and one artist", narrow)
	}
	// A film has no artist, so it is not theirs -- which is what makes the
	// video chip nought while their music is on screen.
	if narrow.Video != 0 || narrow.Image != 0 {
		t.Errorf("pictures and films counted as an artist's: %+v", narrow)
	}
	if narrow.Total != 2 {
		t.Errorf("total = %d, want the two tracks", narrow.Total)
	}

	// And with no search at all: everything of theirs.
	all := l.CountsFor(CountQuery{Artist: "Tribute Band"})
	if all.Audio != 2 || all.Albums != 1 || all.Artists != 1 || all.Video != 0 {
		t.Errorf("the artist with no search = %+v", all)
	}
	// Someone else's name counts none of it.
	if other := l.CountsFor(CountQuery{Artist: "Other Band"}); other.Audio != 1 || other.Albums != 1 {
		t.Errorf("the other artist = %+v, want their one track and release", other)
	}
}
