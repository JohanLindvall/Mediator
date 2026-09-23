package library

import (
	"path/filepath"
	"testing"
	"time"
)

// A release's year read out of a name, in the shapes the library spells it.
// The names are invented; the shapes are the measured ones.
func TestAYearIsReadOutOfAName(t *testing.T) {
	for _, c := range []struct {
		name string
		want int
	}{
		// At the front, spaced: the release's own year, whatever reissue
		// follows it.
		{"2007 - Saltings (Demo) (Reissue 2020)", 2007},
		{"1999 - Saltings (Reissue, Remastered 2019)", 1999},
		{"(2007) Saltings", 2007},
		// Alone in brackets.
		{"Pale Harrow - Saltings (2019) [V0]", 2019},
		{"Pale Harrow - Best of the Year 2016 (2016) [CAT-0001]", 2016},
		// A bracketed year outranks one inside a title.
		{"PALE[2011][2CD]1987 Night Tide - Saltings[FLAC-EAC]", 2011},
		// Scene-style: the last year between separators, after a title that
		// may be one and before the group.
		{"Pale_Harrow-Saltings-WEB-2026-GRP", 2026},
		{"00-pale_harrow-saltings-web-2026", 2026},
		{"Pale_Harrow-Saltings-EP-WEB-NO-2025-GRP", 2025},
		{"Pale_Harrow-1956-WEB-2019-GRP", 2019},
		// A performer called a year, run into the rest with dashes, is not
		// the hand-kept "2007 - Title".
		{"1931-Saltings-WEB-2022-GRP", 2022},
		// A year somewhere in the words, and a date.
		{"Pale Harrow - Saltings (Platinum Edition) (2CD) 2018", 2018},
		{"Pale Harrow - Harbour Hall 2018-08-06", 2018},
		// What is not a year.
		{"Collected Sessions 1993-1997", 0},
		{"Collected Sessions 1993 – 1997", 0},
		{"1962", 0},
		{"Saltings 1080p", 0},
		{"Room2019", 0},
		{"Studio 54 Nights", 0},
		{"Saltings [320k]", 0},
		{"Live 2160", 0},
		{"Saltings", 0},
		{"", 0},
	} {
		if got := yearOfName(c.name); got != c.want {
			t.Errorf("yearOfName(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}

// Where to look: a directory's own name, and for a playlist its file's name
// before the directory holding it.
func TestAYearIsReadOutOfWhereAReleaseIsKept(t *testing.T) {
	for _, c := range []struct {
		path, source string
		want         int
	}{
		{"/m/Pale Harrow/2007 - Saltings", "dir", 2007},
		{"/m/Pale_Harrow-Saltings-WEB-2026-GRP/00-pale_harrow-saltings-web-2026.m3u", "m3u", 2026},
		{"/m/Pale Harrow - Saltings (2019)/playlist.m3u", "m3u", 2019},
		// A directory album is dated by its own directory and not the one
		// above it, which holds other releases too.
		{"/m/Pale Harrow (1990-2020)/Saltings", "dir", 0},
		{"/m/2007 - Pale Harrow/Saltings", "dir", 0},
	} {
		if got := yearOfPlace(c.path, c.source); got != c.want {
			t.Errorf("yearOfPlace(%q, %s) = %d, want %d", c.path, c.source, got, c.want)
		}
	}
}

// A release its tags name but do not date takes the year its directory
// carries — the case that left it undated, since only the name was ever
// asked — and a year the tags do give outranks the directory's.
func TestAReleaseIsDatedByItsDirectory(t *testing.T) {
	l := quietLib("/music")
	add := func(dir, file string, year int) {
		path := "/music/" + dir + "/" + file
		l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{title: file, artist: "Pale Harrow", album: "Saltings " + dir[:4], year: year}, 1000)
	}
	add("2007 - Saltings", "01.mp3", 0)
	add("2007 - Saltings", "02.mp3", 0)
	add("2010 - Saltings", "01.mp3", 1999)
	add("2010 - Saltings", "02.mp3", 1999)
	for _, a := range l.Albums() {
		switch a.Name {
		case "Saltings 2007":
			if a.Year != 2007 {
				t.Errorf("%s: year %d, want the directory's 2007", a.Name, a.Year)
			}
		case "Saltings 2010":
			if a.Year != 1999 {
				t.Errorf("%s: year %d, want the tags' 1999", a.Name, a.Year)
			}
		default:
			t.Errorf("unexpected release %q", a.Name)
		}
	}
}

// The shape that was reported: a scene release's playlist, whose tags name
// the release and date nothing, dated by the year its own file name carries.
func TestAPlaylistIsDatedByItsName(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Pale_Harrow-Saltings-WEB-2026-GRP")
	writeFile(t, filepath.Join(dir, "01-pale_harrow-first.mp3"), "a")
	writeFile(t, filepath.Join(dir, "02-pale_harrow-second.mp3"), "b")
	writeFile(t, filepath.Join(dir, "00-pale_harrow-saltings-web-2026.m3u"),
		"01-pale_harrow-first.mp3\n02-pale_harrow-second.mp3\n")
	l := quietLib(root)
	l.Scan(nil)
	albums := l.Albums()
	if len(albums) != 1 || albums[0].Source != "m3u" {
		t.Fatalf("got %d releases, want the playlist alone (the directory is its duplicate)", len(albums))
	}
	if albums[0].Year != 2026 {
		t.Errorf("year %d, want 2026", albums[0].Year)
	}
}
