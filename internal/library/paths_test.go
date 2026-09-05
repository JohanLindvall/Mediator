package library

import (
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"
)

// libAcrossTwoRoots holds one file under each of two directories, which is
// the shape every question here is about.
func libAcrossTwoRoots(t *testing.T) *Library {
	t.Helper()
	l := New([]string{"/srv"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, p := range []string{"/srv/media/rock/a.mp3", "/srv/other/b.mp3"} {
		l.upsert(p, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
	}
	return l
}

func TestParsePaths(t *testing.T) {
	// Absent is everything: a header nobody set must not hide the library.
	if ParsePaths("").Restricted() {
		t.Error("an empty header restricts nothing")
	}
	if !ParsePaths("/srv/media").Allows("/srv/media/rock/a.mp3") {
		t.Error("a file under the named directory is allowed")
	}
	// Relative paths are dropped rather than resolved: they would resolve
	// against this process's working directory, which is not something the
	// person writing the proxy configuration can see.
	if ParsePaths("media, ../etc").Restricted() {
		t.Error("relative paths must not become a restriction")
	}
	// Canonical, so two spellings of one restriction are one cache key.
	a := ParsePaths("/srv/media/, /srv/other")
	b := ParsePaths("/srv/other,/srv/media")
	if a != b {
		t.Errorf("%q and %q should be the same filter", a.Key(), b.Key())
	}
	// Comparable at all, which is what lets it sit in a cache key.
	if ParsePaths("/a") == ParsePaths("/b") {
		t.Error("different restrictions must not compare equal")
	}
	// A path with a comma in it can be given on its own line.
	f := ParsePaths("/srv/a,b\n/srv/c")
	if !f.Allows("/srv/c/x") {
		t.Error("newline-separated paths are read")
	}
}

// The trap: a plain string prefix hands over a directory nobody named.
func TestPathsAreComponentAware(t *testing.T) {
	f := ParsePaths("/srv/media")
	for _, c := range []struct {
		path string
		want bool
	}{
		{"/srv/media", true},       // the directory itself
		{"/srv/media/a.mp3", true}, // and what is in it
		{"/srv/media/deep/a.mp3", true},
		{"/srv/mediax/a.mp3", false}, // a different directory that starts the same
		{"/srv/media2", false},
		{"/srv/other/a.mp3", false},
		{"/srv", false}, // the parent is not under the child
		{"", false},
	} {
		if got := f.Allows(c.path); got != c.want {
			t.Errorf("Allows(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// An archived member's path is the archive's own with the member after a
// NUL, so it needs no special case — but that is worth pinning down, since
// the day it stops being true this is where it would go wrong silently.
func TestPathsAllowArchivedMembers(t *testing.T) {
	f := ParsePaths("/srv/media")
	member := "/srv/media/set.rar\x00inside/film.mkv"
	if !f.Allows(member) {
		t.Error("a member of an allowed archive is allowed")
	}
	if f.Allows("/srv/elsewhere/set.rar\x00inside/film.mkv") {
		t.Error("a member of an archive outside the allowed paths is not")
	}
}

func TestPathsFilterListing(t *testing.T) {
	l := libAcrossTwoRoots(t)

	all := l.List(Query{Limit: 10})
	if all.Total != 2 {
		t.Fatalf("unrestricted listing has %d items, want 2", all.Total)
	}
	res := l.List(Query{Limit: 10, Paths: ParsePaths("/srv/media")})
	if res.Total != 1 || res.Items[0].Path != "/srv/media/rock/a.mp3" {
		t.Fatalf("restricted listing = %+v, want only a", res.Items)
	}
	// The chips have to agree with the grid: a restricted caller cannot be
	// handed the library's running totals, which count what it may not see.
	if res.Counts.Audio != 1 || res.Counts.Total != 1 {
		t.Errorf("restricted counts = %+v, want one audio item", res.Counts)
	}
	if all.Counts.Audio != 2 {
		t.Errorf("unrestricted counts = %+v, want two", all.Counts)
	}
}

// The cache is keyed by the whole query, and the restriction is part of it —
// or the first caller's answer is served to the next one, which is the trap
// the kinds filter is a value type for.
func TestPathsAreInTheCacheKey(t *testing.T) {
	l := libAcrossTwoRoots(t)

	if n := l.List(Query{Limit: 10, Paths: ParsePaths("/srv/media")}).Total; n != 1 {
		t.Fatalf("restricted = %d, want 1", n)
	}
	// Same query, no restriction, straight afterwards.
	if n := l.List(Query{Limit: 10}).Total; n != 2 {
		t.Fatalf("unrestricted after restricted = %d, want 2 — the cache is not keyed by the filter", n)
	}
	if n := l.List(Query{Limit: 10, Paths: ParsePaths("/srv/other")}).Total; n != 1 {
		t.Fatalf("other restriction = %d, want 1", n)
	}
}

// A restricted caller sees the shows it can reach, grouped from what it can
// reach — not the whole library's grouping with some shows filtered out.
// The parameter used to be accepted and ignored, and the grid then drew
// forty-two shows under a chip that said thirty-two.
func TestAllowedSeries(t *testing.T) {
	l := quietLib("/m")
	// Two shows in one place, one in another.
	for _, p := range []string{
		"/m/public/Harbour.Lights.S01E01.1080p-GRP.mkv",
		"/m/public/Harbour.Lights.S01E02.1080p-GRP.mkv",
		"/m/public/Grey.Harvest.S02E01.1080p-GRP.mkv",
		"/m/public/Grey.Harvest.S02E02.1080p-GRP.mkv",
		"/m/private/Quiet.Coast.S01E01.1080p-GRP.mkv",
		"/m/private/Quiet.Coast.S01E02.1080p-GRP.mkv",
	} {
		l.upsert(p, KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	}
	if n := len(l.Series()); n != 3 {
		t.Fatalf("the whole library has %d shows, want 3", n)
	}

	only := ParsePaths("/m/public")
	got := l.AllowedSeries(only)
	names := make([]string, 0, len(got))
	for _, s := range got {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "Grey Harvest" || names[1] != "Harbour Lights" {
		t.Fatalf("got %v, want the two under the allowed directory", names)
	}
	// And the listing agrees with the chip, which counts the same way.
	if n := l.CountsFor(CountQuery{Kinds: KindsOf(KindVideo), Paths: only}).Series; n != len(got) {
		t.Errorf("the chip says %d shows and the grid draws %d", n, len(got))
	}
}

// A show whose episodes straddle the boundary is counted by what is in front
// of the viewer — and one episode is not a series to them either.
func TestAllowedSeriesCountsWhatIsVisible(t *testing.T) {
	l := quietLib("/m")
	for _, p := range []string{
		"/m/public/Harbour.Lights.S01E01.1080p-GRP.mkv",
		"/m/public/Harbour.Lights.S01E02.1080p-GRP.mkv",
		"/m/private/Harbour.Lights.S02E01.1080p-GRP.mkv",
		// Only one of this show's episodes is visible, so to this caller it
		// is an episode rather than a series.
		"/m/public/Grey.Harvest.S01E01.1080p-GRP.mkv",
		"/m/private/Grey.Harvest.S01E02.1080p-GRP.mkv",
	} {
		l.upsert(p, KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	}
	got := l.AllowedSeries(ParsePaths("/m/public"))
	if len(got) != 1 || got[0].Name != "Harbour Lights" {
		t.Fatalf("got %+v, want only the show with two episodes in view", got)
	}
	if got[0].Episodes != 2 {
		t.Errorf("episodes %d, want the two that can be reached", got[0].Episodes)
	}
	if len(got[0].Seasons) != 1 {
		t.Errorf("%d seasons, want only the one in view", len(got[0].Seasons))
	}
}
