// SPDX-License-Identifier: MIT

package library

import (
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"
)

// A catalogue holding every way a track can be somebody's: their own
// release, with a guest credited in one tag and, in another, a band the
// library has no release by — the shape of a split partner, which is what
// got onto another band's artist radio; a release of collaborations led by
// them, one of them in the shape the tag reader hands over two names in,
// joined with nothing between, beside a band whose name merely runs on from
// theirs; a live record repeating a song; a compilation credited to nobody,
// one of whose tracks has a guest; a split credited to them by majority,
// holding a track by another performer the library knows; that other
// performer's own release; and a file nobody tagged under nobody's
// directory.
func libForStation(t *testing.T) *Library {
	t.Helper()
	l := New([]string{"/library"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	add := func(path, artist, album, title string) {
		l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
		if artist != "" || album != "" || title != "" {
			l.setMeta(PathID(path), tagMeta{artist: artist, album: album, title: title}, 1000)
		}
	}
	add("/library/Gorse Beacon/Signal Fires/01.mp3", "Gorse Beacon", "Signal Fires", "First Breath")
	add("/library/Gorse Beacon/Signal Fires/02.mp3", "Gorse Beacon", "Signal Fires", "Low Tide")
	add("/library/Gorse Beacon/Signal Fires/03.mp3", "Gorse Beacon", "Signal Fires", "Harbour Wall")
	add("/library/Gorse Beacon/Signal Fires/04.mp3", "Gorse Beacon feat. Sixth Quay", "Signal Fires", "Two Lamps")
	add("/library/Gorse Beacon/Signal Fires/05.mp3", "Sixth Quay", "Signal Fires", "Guest Turn")
	add("/library/Gorse Beacon/Joint Works/01.mp3", "Gorse Beacon", "Joint Works", "Salt Road")
	add("/library/Gorse Beacon/Joint Works/02.mp3", "Gorse Beacon", "Joint Works", "Iron Bell")
	add("/library/Gorse Beacon/Joint Works/03.mp3", "Gorse Beacon", "Joint Works", "Low Bridge")
	add("/library/Gorse Beacon/Joint Works/04.mp3", "Gorse Beacon, Tern Signal", "Joint Works", "Joint Venture")
	add("/library/Gorse Beacon/Joint Works/05.mp3", "Gorse BeaconSixth Quay", "Joint Works", "Glued Credit")
	add("/library/Gorse Beacon/Joint Works/06.mp3", "Gorse Beaconry", "Joint Works", "Other Shore")
	add("/library/Gorse Beacon/Live Record/01.mp3", "Gorse Beacon", "Live Record", "First Breath")
	add("/library/Gorse Beacon/Live Record/02.mp3", "Gorse Beacon", "Live Record", "Low Tide (Live)")
	add("/library/comp/Winter Sampler/01.mp3", "Gorse Beacon", "Winter Sampler", "Night Ferry")
	add("/library/comp/Winter Sampler/02.mp3", "Tern Signal", "Winter Sampler", "Cold Light")
	add("/library/comp/Winter Sampler/03.mp3", "Sixth Quay", "Winter Sampler", "Shore Song")
	add("/library/comp/Winter Sampler/04.mp3", "Gorse Beacon feat. Tern Signal", "Winter Sampler", "Shared Night")
	add("/library/splits/Split Seven/01.mp3", "Gorse Beacon", "Split Seven", "Pier End")
	add("/library/splits/Split Seven/02.mp3", "Gorse Beacon", "Split Seven", "Pier Light")
	add("/library/splits/Split Seven/03.mp3", "Tern Signal", "Split Seven", "Their Side")
	add("/library/Tern Signal/Harbour Lights/01.mp3", "Tern Signal", "Harbour Lights", "Low Water")
	add("/library/Tern Signal/Harbour Lights/02.mp3", "Tern Signal", "Harbour Lights", "High Water")
	add("/library/odds/Unmarked/01.mp3", "", "", "")
	return l
}

func titles(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}

func idOf(path string) string { return PathID(path) }

// What a station holds is one performer's tracks, by the one rule that also
// decides whose a seed is — so whichever of them becomes the next seed asks
// for the same station.
func TestAStationIsOnePerformersTracks(t *testing.T) {
	l := libForStation(t)
	name, got := l.Station("gorse beacon", "", 0, PathFilter{})
	if name != "Gorse Beacon" {
		t.Errorf("station named %q, want the performer as the library spells them", name)
	}
	want := []string{"First Breath", "Low Tide", "Harbour Wall", "Two Lamps",
		"Salt Road", "Iron Bell", "Low Bridge", "Joint Venture", "Glued Credit",
		"Low Tide (Live)", "Night Ferry", "Shared Night", "Pier End", "Pier Light"}
	have := titles(got)
	slices.Sort(have)
	slices.Sort(want)
	if !slices.Equal(have, want) {
		t.Errorf("station holds %q\nwant %q", have, want)
	}
	// Every one of them answers for the same station as a seed.
	for _, it := range got {
		if seedName, _ := l.Station("", it.ID, 0, PathFilter{}); seedName != "Gorse Beacon" {
			t.Errorf("%q seeds a station of %q, not of the performer whose station it is in", it.Title, seedName)
		}
	}
	// And the tracks it leaves out are somebody else's by the same rule.
	for path, who := range map[string]string{
		"/library/splits/Split Seven/03.mp3":         "Tern Signal",    // a known name on another's split
		"/library/Gorse Beacon/Signal Fires/05.mp3":  "Sixth Quay",     // a name with no release, on theirs
		"/library/Gorse Beacon/Joint Works/06.mp3":   "Gorse Beaconry", // a name running on from theirs
		"/library/comp/Winter Sampler/03.mp3":        "Sixth Quay",     // nobody's compilation, a name with no release
		"/library/Tern Signal/Harbour Lights/01.mp3": "Tern Signal",
	} {
		if seedName, _ := l.Station("", idOf(path), 0, PathFilter{}); seedName != who {
			t.Errorf("%s seeds a station of %q, want %q", path, seedName, who)
		}
	}
	if seedName, tracks := l.Station("", idOf("/library/odds/Unmarked/01.mp3"), 0, PathFilter{}); seedName != "" || tracks != nil {
		t.Errorf("a file that is nobody's seeds a station of %q with %d tracks", seedName, len(tracks))
	}
}

// Nearest the seed first, then what nothing has read; never the seed, nor
// another copy of its recording; one copy of every other recording.
func TestAStationRunsNearestFirst(t *testing.T) {
	l := libForStation(t)
	paths := []string{
		"/library/Gorse Beacon/Signal Fires/01.mp3",
		"/library/Gorse Beacon/Signal Fires/02.mp3",
		"/library/Gorse Beacon/Signal Fires/03.mp3",
		"/library/Gorse Beacon/Live Record/01.mp3",
		"/library/Gorse Beacon/Live Record/02.mp3",
		"/library/comp/Winter Sampler/01.mp3",
		"/library/splits/Split Seven/01.mp3",
		"/library/Tern Signal/Harbour Lights/01.mp3",
	}
	for i, p := range paths {
		it, _ := l.Get(idOf(p))
		v := shapedVector(1, false)
		v[0] += float32(i) * 0.05 // each a little farther from the first
		l.SetFeatures(it.ID, it.ModTime, it.Size, v)
	}
	seed := idOf("/library/Gorse Beacon/Signal Fires/01.mp3")
	name, got := l.Station("", seed, 0, PathFilter{})
	if name != "Gorse Beacon" {
		t.Fatalf("station of %q", name)
	}
	sv := l.scaledVectors()
	ranked := true
	last := float32(2)
	for _, it := range got {
		if it.ID == seed || it.Title == "First Breath" {
			t.Errorf("the seed's own recording came back: %s", it.Rel)
		}
		v, ok := sv.vecs[it.ID]
		if !ok {
			ranked = false
			continue
		}
		if !ranked {
			t.Errorf("%q, which was read, came after a track nothing has read", it.Title)
		}
		if d := dot(sv.vecs[seed], v); d > last+1e-6 {
			t.Errorf("%q is nearer than the track before it", it.Title)
		} else {
			last = d
		}
	}
	// Fourteen recordings of theirs, less the seed's, whose live copy goes
	// with it.
	if len(got) != 13 {
		t.Errorf("%d tracks, want the thirteen others: %q", len(got), titles(got))
	}
}

// Started from a performer's page there is no seed, and the station opens on
// what the owner has made of them: a liked track leads.
func TestASeedlessStationLeadsWithTheLiked(t *testing.T) {
	l := libForStation(t)
	liked := idOf("/library/splits/Split Seven/02.mp3")
	l.SetLike(liked, 1)
	_, got := l.Station("Gorse Beacon", "", 0, PathFilter{})
	if len(got) == 0 || got[0].ID != liked {
		t.Errorf("the station opens on %q, want the liked track first", titles(got))
	}
}

// A confined caller's station is what it may see of the performer.
func TestAStationKeepsToWhatTheCallerMaySee(t *testing.T) {
	l := libForStation(t)
	_, got := l.Station("Gorse Beacon", "", 0, ParsePaths("/library/Gorse Beacon"))
	for _, it := range got {
		if it.Album == "Winter Sampler" || it.Album == "Split Seven" {
			t.Errorf("a confined station handed out %q from %q", it.Title, it.Album)
		}
	}
	// Eleven files of theirs under it, one of them a second copy of a
	// recording.
	if len(got) != 10 {
		t.Errorf("%d tracks, want the ten recordings under the allowed directory: %q", len(got), titles(got))
	}
}

// A guest's credit is taken off a tag, and only where it is a credit.
func TestWithoutGuests(t *testing.T) {
	for in, want := range map[string]string{
		"Gorse Beacon feat. Sixth Quay":   "Gorse Beacon",
		"Gorse Beacon Ft. Sixth Quay":     "Gorse Beacon",
		"Gorse Beacon featuring Tern":     "Gorse Beacon",
		"Gorse Beacon (feat. Sixth Quay)": "Gorse Beacon",
		"Gorse Beacon [ft Sixth Quay]":    "Gorse Beacon",
		"Clever Feat":                     "Clever Feat",
		"Clever Feat feat. Sixth Quay":    "Clever Feat",
		"Daftly":                          "Daftly",
		"  Gorse Beacon ":                 "Gorse Beacon",
	} {
		if got := withoutGuests(in); got != want {
			t.Errorf("withoutGuests(%q) = %q, want %q", in, got, want)
		}
	}
}

// A placeholder names no song, so it makes no recording of one: a
// performer's untitled pieces are each their own.
func TestAPlaceholderIsNotOneRecording(t *testing.T) {
	for _, title := range []string{"Untitled", "[untitled]", "(Untitled Track)", "Track 3", "track01", "Unknown", "untitled #2"} {
		if k := RecordingKey("Gorse Beacon", title); k != "" {
			t.Errorf("RecordingKey(%q) = %q, want none", title, k)
		}
	}
	for _, title := range []string{"Untitled Harbour Air", "Tracks in Snow", "The Unknown Shore", "1999"} {
		if RecordingKey("Gorse Beacon", title) == "" {
			t.Errorf("RecordingKey(%q) is empty, but it is a title", title)
		}
	}
	got := FoldRecordings([]Item{
		{ID: "a", Artist: "Gorse Beacon", Title: "[untitled]"},
		{ID: "b", Artist: "Gorse Beacon", Title: "[untitled]"},
		{ID: "c", Artist: "Gorse Beacon", Title: "Low Tide"},
		{ID: "d", Artist: "Gorse Beacon", Title: "Low Tide"},
	})
	if len(got) != 3 {
		t.Errorf("folded to %d, want both untitled pieces and one Low Tide", len(got))
	}
}
