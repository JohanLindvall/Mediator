package library

import (
	"slices"
	"testing"
	"time"
)

// holdClock stops the clock the modification-time rule is judged against.
func holdClock(t *testing.T, at time.Time) {
	t.Helper()
	was := nowMillis
	nowMillis = func() int64 { return at.UnixMilli() }
	t.Cleanup(func() { nowMillis = was })
}

var testNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// A time a day ahead of the clock is a clock that disagrees; one decades
// ahead is not a time at all.
func TestAModificationTimeInTheFutureIsNotATime(t *testing.T) {
	now := testNow.UnixMilli()
	for _, c := range []struct {
		at   time.Time
		want bool
	}{
		{testNow.AddDate(-30, 0, 0), true},
		{testNow, true},
		{testNow.Add(23 * time.Hour), true},
		{testNow.Add(25 * time.Hour), false},
		{time.Date(2097, 12, 31, 23, 0, 0, 0, time.UTC), false},
	} {
		if got := knownTime(c.at.UnixMilli(), now); got != c.want {
			t.Errorf("knownTime(%v) = %v, want %v", c.at, got, c.want)
		}
	}
}

// Newest first is led by the newest real time, and a file claiming a year
// that has not happened comes after every real one — whichever way the
// listing runs, as a missing key does everywhere else.
func TestAFileFromTheFutureSortsLastEitherWay(t *testing.T) {
	holdClock(t, testNow)
	l := quietLib("/m")
	for name, at := range map[string]time.Time{
		"older.mkv":   time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC),
		"old.mkv":     time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		"newest.mkv":  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		"stamped.mkv": time.Date(2097, 12, 31, 23, 0, 0, 0, time.UTC),
	} {
		l.upsert("/m/"+name, KindVideo, 10, at, fileKey{}, false)
	}
	names := func(desc bool) []string {
		var out []string
		for _, it := range l.List(Query{Sort: "mtime", Desc: desc, Limit: 10}).Items {
			out = append(out, it.Name)
		}
		return out
	}
	if got, want := names(true), []string{"newest.mkv", "old.mkv", "older.mkv", "stamped.mkv"}; !slices.Equal(got, want) {
		t.Errorf("newest first: %v, want %v", got, want)
	}
	if got, want := names(false), []string{"older.mkv", "old.mkv", "newest.mkv", "stamped.mkv"}; !slices.Equal(got, want) {
		t.Errorf("oldest first: %v, want %v", got, want)
	}
}

// A release and a show are as new as their newest real member: one track
// or episode claiming to be from the future does not make its collection
// the newest there is, and a collection of nothing but such has no time and
// sorts after the rest.
func TestACollectionIsAsNewAsItsNewestRealMember(t *testing.T) {
	holdClock(t, testNow)
	real := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	stamped := time.Date(2097, 12, 31, 23, 0, 0, 0, time.UTC)
	l := quietLib("/m")
	l.upsert("/m/Saltings/01.mp3", KindAudio, 10, real, fileKey{}, false)
	l.upsert("/m/Saltings/02.mp3", KindAudio, 10, stamped, fileKey{}, false)
	l.upsert("/m/Windward/01.mp3", KindAudio, 10, stamped, fileKey{}, false)
	l.upsert("/m/Windward/02.mp3", KindAudio, 10, stamped, fileKey{}, false)
	l.upsert("/m/Lee Shore/01.mp3", KindAudio, 10, real.AddDate(-5, 0, 0), fileKey{}, false)
	l.upsert("/m/tv/Harbour.Lights.S01E01.mkv", KindVideo, 10, real, fileKey{}, false)
	l.upsert("/m/tv/Harbour.Lights.S01E02.mkv", KindVideo, 10, stamped, fileKey{}, false)

	for _, a := range l.Albums() {
		want := map[string]int64{"Saltings": real.UnixMilli(), "Windward": 0, "Lee Shore": real.AddDate(-5, 0, 0).UnixMilli()}[a.Name]
		if a.ModTime != want {
			t.Errorf("%s: modified %d, want %d", a.Name, a.ModTime, want)
		}
	}
	for _, desc := range []bool{true, false} {
		got := l.SearchAlbums(AlbumQuery{Sort: "mtime", Desc: desc})
		if len(got) != 3 || got[2].Name != "Windward" {
			names := []string{}
			for _, a := range got {
				names = append(names, a.Name)
			}
			t.Errorf("by modified (desc %v): %v, want the release with no real time last", desc, names)
		}
	}
	shows := l.Series()
	if len(shows) != 1 || shows[0].ModTime != real.UnixMilli() {
		t.Fatalf("the show's modified time is %+v, want its real episode's", shows)
	}
	if shows[0].Seasons[0].CoverID == "" {
		t.Error("a season lost its cover to the rule")
	}
}
