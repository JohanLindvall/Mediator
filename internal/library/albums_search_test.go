package library

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// libWithTaggedAlbum builds a library holding one fully tagged album,
// bypassing the filesystem (enrichment is exercised elsewhere).
func libWithTaggedAlbum(t *testing.T) *Library {
	t.Helper()
	l := New([]string{"/music"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	titles := []string{"The First Light", "Autumn Comes", "Low Water"}
	for i, title := range titles {
		path := "/music/Ashgrove - 1993/0" + string(rune('1'+i)) + " - track.mp3"
		l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
		id := PathID(path)
		l.setMeta(id, tagMeta{
			title: title, artist: "Ashgrove",
			album: "Under Ashen Skies",
			genre: "Melodic Death Metal", track: i + 1, year: 1993,
		}, 150_000)
	}
	return l
}

func TestAlbumSearchUsesTagMetadata(t *testing.T) {
	l := libWithTaggedAlbum(t)

	hits := func(q string) int { return len(l.SearchAlbums(AlbumQuery{Search: q, Sort: "name", Desc: false})) }

	// Album metadata is searchable, word order and punctuation irrelevant.
	for _, q := range []string{
		"",
		"ashen skies",
		"skies ashen",            // any order
		"ashgrove",               // artist
		"melodic death",          // genre
		"1993",                   // year
		"ashgrove 1993",          // artist + year together
		"melodic ashen ashgrove", // across all three fields
	} {
		if n := hits(q); n != 1 {
			t.Errorf("SearchAlbums(%q) = %d albums, want 1", q, n)
		}
	}
	for _, q := range []string{"1994", "ashen nowhere", "jazz"} {
		if n := hits(q); n != 0 {
			t.Errorf("SearchAlbums(%q) = %d albums, want 0", q, n)
		}
	}
}

func TestAlbumDerivesTagFields(t *testing.T) {
	l := libWithTaggedAlbum(t)
	albums := l.Albums()
	if len(albums) != 1 {
		t.Fatalf("got %d albums, want 1", len(albums))
	}
	a := albums[0]
	if a.Name != "Under Ashen Skies" {
		t.Errorf("name = %q, want the tag album name", a.Name)
	}
	if a.Artist != "Ashgrove" || a.Genre != "Melodic Death Metal" || a.Year != 1993 {
		t.Errorf("artist/genre/year = %q/%q/%d", a.Artist, a.Genre, a.Year)
	}
	if a.Duration != 450_000 {
		t.Errorf("duration = %d ms, want 450000 (sum of tracks)", a.Duration)
	}

	// Tracks come back in tag track-number order.
	_, tracks, ok := l.AlbumByID(a.ID)
	if !ok || len(tracks) != 3 {
		t.Fatalf("AlbumByID: ok=%v tracks=%d", ok, len(tracks))
	}
	if tracks[0].Title != "The First Light" || tracks[2].Title != "Low Water" {
		t.Errorf("track order: %q, %q, %q", tracks[0].Title, tracks[1].Title, tracks[2].Title)
	}
}

func TestAlbumDurationHiddenUntilComplete(t *testing.T) {
	l := libWithTaggedAlbum(t)
	// A fourth track with no duration yet: the total must not be reported,
	// otherwise the UI shows a plausible but wrong album length mid-scan.
	path := "/music/Ashgrove - 1993/04 - track.mp3"
	l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
	l.setMeta(PathID(path), tagMeta{
		title: "First Breath", artist: "Ashgrove",
		album: "Under Ashen Skies",
	}, 0)

	a := l.Albums()[0]
	if a.Tracks != 4 {
		t.Fatalf("tracks = %d, want 4", a.Tracks)
	}
	if a.Duration != 0 {
		t.Errorf("duration = %d, want 0 while a track is unmeasured", a.Duration)
	}
}

func TestItemSearchUsesTagMetadata(t *testing.T) {
	l := libWithTaggedAlbum(t)
	hits := func(q string) int { return l.List(Query{Search: q}).Total }

	if n := hits("autumn comes"); n != 1 {
		t.Errorf("title search = %d, want 1", n)
	}
	if n := hits("melodic death metal"); n != 3 {
		t.Errorf("genre search = %d, want 3", n)
	}
	if n := hits("1993 ashgrove"); n != 3 {
		t.Errorf("year+artist search = %d, want 3", n)
	}
	if n := hits("nonexistent"); n != 0 {
		t.Errorf("miss = %d, want 0", n)
	}
}

func TestPlaylistNamedByTagsOnlyWhenUnanimous(t *testing.T) {
	l := libWithTaggedAlbum(t)
	tracks := l.List(Query{}).Items

	// A playlist listing exactly one album's tracks takes the album's name,
	// rather than showing the release file name it happens to be stored as.
	a := &Album{ID: "p1", Name: "00-band-release-2026-group", Source: "m3u"}
	ptrs := make([]*Item, 0, len(tracks))
	for i := range tracks {
		it := tracks[i]
		ptrs = append(ptrs, &it)
	}
	fillAlbum(a, "/library/releases/00-band-release-2026-group.m3u", ptrs, nil, nil)
	if a.Name != "Under Ashen Skies" {
		t.Errorf("playlist name = %q, want the album from the tags", a.Name)
	}

	// A mixtape keeps its own name.
	mixed := &Album{ID: "p2", Name: "My Mixtape", Source: "m3u"}
	other := *ptrs[0]
	other.Album = "Another Record"
	fillAlbum(mixed, "/library/playlists/My Mixtape.m3u", []*Item{ptrs[0], ptrs[1], &other}, nil, nil)
	if mixed.Name != "My Mixtape" {
		t.Errorf("mixtape name = %q, want it left alone", mixed.Name)
	}
}

func TestArtistsGroupAlbums(t *testing.T) {
	l := libWithTaggedAlbum(t)
	// A second album by the same artist, and one by another.
	add := func(dir, album, artist string, n int) {
		for i := range n {
			path := "/music/" + dir + "/0" + string(rune('1'+i)) + " - track.mp3"
			l.upsert(path, KindAudio, 2000, time.Unix(1, 0), fileKey{}, false)
			l.setMeta(PathID(path), tagMeta{
				title: "T", artist: artist, album: album, track: i + 1,
			}, 60_000)
		}
	}
	add("Ashgrove - 1995", "Harvest of the Quiet", "Ashgrove", 2)
	add("Other Band - 2001", "Some Record", "Other Band", 1)

	artists := l.SearchArtists("", "name", false, PathFilter{})
	if len(artists) != 2 {
		t.Fatalf("got %d artists %v, want 2", len(artists), artists)
	}
	byName := map[string]*Artist{}
	for _, a := range artists {
		byName[a.Name] = a
	}
	band := byName["Ashgrove"]
	if band == nil {
		t.Fatal("the artist with two albums is missing")
	}
	if band.Albums != 2 || band.Tracks != 5 {
		t.Errorf("albums/tracks = %d/%d, want 2/5", band.Albums, band.Tracks)
	}
	if band.Duration != 3*150_000+2*60_000 {
		t.Errorf("duration = %d, want the sum of both albums", band.Duration)
	}
	if band.CoverID == "" {
		t.Error("artist has no cover")
	}

	// Searching artists works on the same word rules as everything else.
	if n := len(l.SearchArtists("ashgrove", "name", false, PathFilter{})); n != 1 {
		t.Errorf("search 'ashgrove' matched %d artists, want 1", n)
	}

	// Drilling in shows exactly that artist's albums.
	got := l.SearchAlbums(AlbumQuery{Artist: "Ashgrove", Sort: "name", Desc: false})
	if len(got) != 2 {
		t.Fatalf("artist filter returned %d albums, want 2", len(got))
	}
	for _, a := range got {
		if a.Artist != "Ashgrove" {
			t.Errorf("filter leaked an album by %q", a.Artist)
		}
	}
	if n := len(l.SearchAlbums(AlbumQuery{Artist: "Nobody", Sort: "name", Desc: false})); n != 0 {
		t.Errorf("unknown artist matched %d albums", n)
	}
}

func TestArtistDurationUnknownWhenAnAlbumIs(t *testing.T) {
	l := libWithTaggedAlbum(t)
	path := "/music/Ashgrove - 1995/01 - untimed.mp3"
	l.upsert(path, KindAudio, 2000, time.Unix(1, 0), fileKey{}, false)
	l.setMeta(PathID(path), tagMeta{title: "T", artist: "Ashgrove", album: "Slaughter"}, 0)

	artists := l.SearchArtists("", "name", false, PathFilter{})
	if len(artists) != 1 {
		t.Fatalf("got %d artists, want 1", len(artists))
	}
	if artists[0].Duration != 0 {
		t.Errorf("duration = %d, want 0 while an album is unmeasured", artists[0].Duration)
	}
	if artists[0].Albums != 2 {
		t.Errorf("albums = %d, want 2", artists[0].Albums)
	}
}

func TestTagWhitespaceNormalised(t *testing.T) {
	// Padded tags would otherwise sort ahead of everything and split one
	// performer into two artists.
	l := quietLib("/music")
	for i, artist := range []string{" Padded", "Padded ", "Padded"} {
		path := "/music/dir" + string(rune('1'+i)) + "/track.mp3"
		l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{
			title: " A Title ", artist: artist, album: " Record ", genre: " Metal ",
		}, 1000)
	}
	artists := l.SearchArtists("", "name", false, PathFilter{})
	if len(artists) != 1 || artists[0].Name != "Padded" {
		t.Fatalf("got %d artists %v, want one named \"Padded\"", len(artists), artists)
	}
	it, _ := l.Get(PathID("/music/dir1/track.mp3"))
	if it.Title != "A Title" || it.Album != "Record" || it.Genre != "Metal" {
		t.Errorf("untrimmed metadata: %q / %q / %q", it.Title, it.Album, it.Genre)
	}
}

func TestPlaylistSupersedesTheIdenticalDirectoryAlbum(t *testing.T) {
	dir := t.TempDir()
	album := filepath.Join(dir, "Band - Record")
	var names []string
	for _, n := range []string{"01 - one.mp3", "02 - two.mp3", "03 - three.mp3"} {
		write(t, filepath.Join(album, n), "audio")
		names = append(names, n)
	}
	// The release ships its own playlist listing exactly those tracks.
	write(t, filepath.Join(album, "record.m3u"), strings.Join(names, "\n")+"\n")

	l := quietLib(dir)
	l.Scan(nil)
	got := l.SearchAlbums(AlbumQuery{Sort: "name", Desc: false})
	if len(got) != 1 {
		var desc []string
		for _, a := range got {
			desc = append(desc, a.Source+":"+a.Name)
		}
		t.Fatalf("got %d albums %v, want the playlist only", len(got), desc)
	}
	if got[0].Source != "m3u" {
		t.Errorf("kept the %q album, want the playlist to win", got[0].Source)
	}
	if got[0].Tracks != 3 {
		t.Errorf("tracks = %d, want 3", got[0].Tracks)
	}
}

func TestPartialPlaylistKeepsBothAlbums(t *testing.T) {
	// A playlist covering only part of a directory is a selection of its
	// own, so the directory album stays.
	dir := t.TempDir()
	album := filepath.Join(dir, "Band - Record")
	for _, n := range []string{"01 - one.mp3", "02 - two.mp3", "03 - three.mp3"} {
		write(t, filepath.Join(album, n), "audio")
	}
	write(t, filepath.Join(album, "highlights.m3u"), "01 - one.mp3\n02 - two.mp3\n")

	l := quietLib(dir)
	l.Scan(nil)
	if got := l.SearchAlbums(AlbumQuery{Sort: "name", Desc: false}); len(got) != 2 {
		t.Fatalf("got %d albums, want both the directory and the selection", len(got))
	}
}

// Sorting a listing by a tag has to put the releases that carry that tag
// first, whichever way it runs: tags are missing often enough that the
// alternative buries every tagged release behind a screen of blanks.
func TestAlbumSortByMetadata(t *testing.T) {
	dir := t.TempDir()
	type rel struct {
		dir, artist, genre string
		year               int
		tracks             int
	}
	rels := []rel{
		{"beta", "Zed", "Doom", 1994, 3},
		{"alpha", "Adams", "Black", 2011, 1},
		{"gamma", "", "", 0, 2},
	}
	for _, r := range rels {
		d := filepath.Join(dir, r.dir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < r.tracks; i++ {
			p := filepath.Join(d, fmt.Sprintf("%02d.mp3", i+1))
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	lib := New([]string{dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	lib.Scan(nil)
	// Tags do not come from these fixtures, so they are set directly: what is
	// under test is the ordering, not the reading.
	byDir := map[string]*Album{}
	for _, a := range lib.Albums() {
		byDir[a.Name] = a
	}
	for _, r := range rels {
		a := byDir[r.dir]
		if a == nil {
			t.Fatalf("no album for %s", r.dir)
		}
		a.Artist, a.Genre, a.Year = r.artist, r.genre, r.year
	}

	names := func(key string, desc bool) []string {
		var out []string
		for _, a := range lib.SearchAlbums(AlbumQuery{Sort: key, Desc: desc}) {
			out = append(out, a.Name)
		}
		return out
	}

	for _, c := range []struct {
		key  string
		desc bool
		want []string
	}{
		{"artist", false, []string{"alpha", "beta", "gamma"}},
		{"artist", true, []string{"beta", "alpha", "gamma"}},
		{"year", false, []string{"beta", "alpha", "gamma"}},
		{"year", true, []string{"alpha", "beta", "gamma"}},
		{"genre", false, []string{"alpha", "beta", "gamma"}},
		{"tracks", false, []string{"alpha", "gamma", "beta"}},
	} {
		t.Run(c.key+map[bool]string{true: " desc", false: " asc"}[c.desc], func(t *testing.T) {
			got := names(c.key, c.desc)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			}
		})
	}
}

// A performer is worth finding by what they released, not only by their name.
func TestArtistSearchUsesReleaseMetadata(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "Nightfall In Winter")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "01.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := New([]string{dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	lib.Scan(nil)
	for _, a := range lib.Albums() {
		a.Artist, a.Genre = "Some Performer", "Doom"
	}
	lib.RefreshCounts()

	for _, q := range []string{"some performer", "nightfall", "doom"} {
		if got := lib.SearchArtists(q, "name", false, PathFilter{}); len(got) != 1 {
			t.Fatalf("searching artists for %q found %d, want 1", q, len(got))
		}
	}
	if got := lib.SearchArtists("nothinglikethis", "name", false, PathFilter{}); len(got) != 0 {
		t.Fatalf("found %d for a word nothing carries", len(got))
	}
}

// Tags in the wild are not the tags the spec describes.
func TestTagNormalisation(t *testing.T) {
	t.Run("year", func(t *testing.T) {
		for _, c := range []struct{ in, want int }{
			{2007, 2007},
			{20220519, 2022}, // a whole date in the year frame
			{20072007, 2007}, // the year, written twice
			{0, 0},
			{75, 0}, // too short to be a year; better none than a wrong one
			{1969, 1969},
		} {
			if got := cleanYear(c.in); got != c.want {
				t.Fatalf("cleanYear(%d) = %d, want %d", c.in, got, c.want)
			}
		}
	})
	t.Run("byte order mark", func(t *testing.T) {
		// Invisible, sorts ahead of every letter, and splits one genre in two.
		if got := cleanTag("\ufeffМетал"); got != "Метал" {
			t.Fatalf("cleanTag left %q", got)
		}
		if got := cleanTag("  \ufeff Rock  "); got != "Rock" {
			t.Fatalf("cleanTag left %q", got)
		}
		if got := cleanTag("Rock"); got != "Rock" {
			t.Fatalf("cleanTag changed an ordinary value to %q", got)
		}
	})
}

// A release spread over CD1, CD2 and CD3 is one release, and its tracks run
// in the order the discs do.
func TestDiscFoldersMakeOneAlbum(t *testing.T) {
	l := New([]string{"/music"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	add := func(dir, name string, track int, album string) {
		path := "/music/Band - Anthology (2025)/" + dir + "/" + name
		l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{artist: "Band", album: album, track: track}, 1000)
	}
	add("CD1", "01 one.mp3", 1, "Anthology (CD1)")
	add("CD1", "02 two.mp3", 2, "Anthology (CD1)")
	add("CD2", "01 three.mp3", 1, "Anthology (CD2)")
	add("CD2", "02 four.mp3", 2, "Anthology (CD2)")

	albums := l.SearchAlbums(AlbumQuery{Sort: "name", Desc: false})
	if len(albums) != 1 {
		names := []string{}
		for _, a := range albums {
			names = append(names, a.Name)
		}
		t.Fatalf("built %d albums (%v), want the one release", len(albums), names)
	}
	a := albums[0]
	if a.Tracks != 4 {
		t.Errorf("tracks = %d, want all four", a.Tracks)
	}
	// The disc marker comes off, so the three discs agree on a name.
	if a.Name != "Anthology" {
		t.Errorf("name = %q, want the release's own", a.Name)
	}
	// And the running order is disc by disc, not interleaved by track number.
	_, tracks, ok := l.AlbumByID(a.ID)
	if !ok || len(tracks) != 4 {
		t.Fatalf("album detail = %d tracks, ok=%v", len(tracks), ok)
	}
	want := []string{"01 one.mp3", "02 two.mp3", "01 three.mp3", "02 four.mp3"}
	for i, w := range want {
		if tracks[i].Name != w {
			t.Errorf("track %d = %q, want %q", i, tracks[i].Name, w)
		}
	}
}

func TestDiscNumberIsStrict(t *testing.T) {
	for name, want := range map[string]int{
		"CD1": 1, "cd 2": 2, "Disc.3": 3, "disk-4": 4, "CD10": 10,
		"CD singles": 0, "Discography": 0, "cd": 0, "B-Sides": 0, "": 0,
	} {
		if got := discNumber(name); got != want {
			t.Errorf("discNumber(%q) = %d, want %d", name, got, want)
		}
	}
	for tag, want := range map[string]string{
		"Anthology (CD1)":    "Anthology",
		"Anthology - Disc 2": "Anthology",
		"Anthology [disk 3]": "Anthology",
		"Anthology":          "Anthology",
		"Live in Discoteque": "Live in Discoteque",
		// The marker often carries the disc's own title after it.
		"Collected Sessions 1993-1997 CD 1-Firstlight": "Collected Sessions 1993-1997",
		"Best of 1980-1990":                            "Best of 1980-1990",
	} {
		if got := albumTitle(tag); got != want {
			t.Errorf("albumTitle(%q) = %q, want %q", tag, got, want)
		}
	}
}

// A lone directory named like a disc is just a directory: folding it would
// pour every unrelated album beside it into one heap.
func TestLoneDiscFolderIsLeftAlone(t *testing.T) {
	l := New([]string{"/music"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	add := func(dir, name string) {
		path := "/music/" + dir + "/" + name
		l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
	}
	add("CD 1 - Some Record", "01 one.mp3")
	add("Another Record", "01 two.mp3")
	add("A Third Record", "01 three.mp3")

	albums := l.SearchAlbums(AlbumQuery{Sort: "name", Desc: false})
	if len(albums) != 3 {
		names := []string{}
		for _, a := range albums {
			names = append(names, a.Name)
		}
		t.Fatalf("built %d albums (%v), want the three that are there", len(albums), names)
	}
}

// A performer is drawn with the sleeve of their most recent release that
// has one, not of their most recent release. Measured on one performer
// here: sixteen releases, twelve with a picture beside the tracks, and the
// four without were the newest — so the card showed the fallback icon while
// a dozen of their records had artwork.
func TestCoverPrefersAReleaseThatHasOne(t *testing.T) {
	l := New([]string{"/library"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	add := func(dir string, year int, picture bool) {
		path := fmt.Sprintf("/library/Gorse Beacon/%s/01 track.mp3", dir)
		// Later releases are more recently modified, as they are on a disk
		// filled over time.
		l.upsert(path, KindAudio, 1000, time.Unix(int64(year), 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{artist: "Gorse Beacon", album: dir, year: year}, 1000)
		if picture {
			l.upsert(fmt.Sprintf("/library/Gorse Beacon/%s/cover.jpg", dir),
				KindImage, 500, time.Unix(int64(year), 0), fileKey{}, false)
		}
	}
	add("Signal Fires", 1993, true)
	add("Low Water", 1995, true)
	add("Live In The Hall", 2003, false) // the newest, and no sleeve

	withArt := PathID("/library/Gorse Beacon/Low Water/01 track.mp3")
	artists := l.Artists()
	if len(artists) != 1 {
		t.Fatalf("performers = %d, want 1", len(artists))
	}
	if got := artists[0].CoverID; got != withArt {
		t.Errorf("the performer's cover is %q, want the newest release that has one", got)
	}
	genres := l.Genres()
	for _, g := range genres {
		if g.CoverID != withArt {
			t.Errorf("genre %q took its cover from a release with no sleeve", g.Name)
		}
	}

	// Where none of them has a picture, the rule is the old one: the most
	// recent release, rather than nothing at all.
	l2 := New([]string{"/library"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, y := range []int{1993, 2003} {
		path := fmt.Sprintf("/library/Tern Signal/%d/01 track.mp3", y)
		l2.upsert(path, KindAudio, 1000, time.Unix(int64(y), 0), fileKey{}, false)
		l2.setMeta(PathID(path), tagMeta{artist: "Tern Signal", album: fmt.Sprint(y)}, 1000)
	}
	got := l2.Artists()
	if len(got) != 1 || got[0].CoverID != PathID("/library/Tern Signal/2003/01 track.mp3") {
		t.Errorf("with no sleeves anywhere the cover is %v, want the most recent release", got)
	}
}

// A collection can be ordered by what arrived in the library lately, which
// is not the same question as what was written lately: a download carries
// the timestamp it was made with, so a record from 1994 fetched this
// morning is old by one and new by the other.
func TestCollectionsSortByWhatArrived(t *testing.T) {
	l := New([]string{"/library"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Two performers. The old record arrived today; the new one has been
	// here for months. By modification time they sort one way and by
	// arrival the other, which is the whole point of the key.
	add := func(artist, album string, modified, arrived int64) {
		path := fmt.Sprintf("/library/%s/%s/01 track.mp3", artist, album)
		l.upsert(path, KindAudio, 1000, time.Unix(modified, 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{artist: artist, album: album, genre: "Rock"}, 1000)
		l.mu.Lock()
		l.items[PathID(path)].FirstSeen = arrived
		l.mu.Unlock()
	}
	add("Gorse Beacon", "Old Record", 100, 9000) // written long ago, here today
	add("Tern Signal", "New Record", 900, 1000)  // written lately, here for ages

	byKey := func(sortKey string) []string {
		out := []string{}
		for _, a := range l.SearchArtists("", sortKey, true, PathFilter{}) {
			out = append(out, a.Name)
		}
		return out
	}
	if got := byKey("mtime"); !slices.Equal(got, []string{"Tern Signal", "Gorse Beacon"}) {
		t.Errorf("by modified = %v", got)
	}
	if got := byKey("added"); !slices.Equal(got, []string{"Gorse Beacon", "Tern Signal"}) {
		t.Errorf("by added = %v, want the performer whose file arrived last first", got)
	}

	// The releases answer the same way, and each carries the arrival of its
	// newest track rather than of its first.
	albums := l.SearchAlbums(AlbumQuery{Sort: "added", Desc: true})
	if len(albums) != 2 || albums[0].Name != "Old Record" {
		t.Errorf("releases by added = %v", albumNames(albums))
	}
	if albums[0].Added != 9000 {
		t.Errorf("the release's arrival = %d, want its newest track's", albums[0].Added)
	}
	// And a genre gathers the newest of everything filed under it.
	genres := l.SearchGenres("", "added", true, PathFilter{})
	if len(genres) != 1 || genres[0].Added != 9000 {
		t.Errorf("the genre's arrival = %v", genres)
	}
}
