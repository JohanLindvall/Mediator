package library

import "testing"

func TestSearchMatching(t *testing.T) {
	// The path part is the absolute one, exactly as the index builds it: the
	// directories above the root are searchable too.
	text := searchText("Tide.Song_1968-Remaster.mp3",
		"/srv/music/Gorse Beacon/Tide.Song_1968-Remaster.mp3",
		"Tide Song", "Gorse Beacon")
	for _, q := range []string{
		"tide song", "song tide", "TIDE", "beacon tide", "remaster 1968",
		"music beacon", "tid", "eacon", "",
		"srv music", "music beacon tide",
	} {
		if !matchWords(text, searchWords(q)) {
			t.Fatalf("query %q should match %q", q, text)
		}
	}
	for _, q := range []string{"tide quay", "1969", "srv9 tide"} {
		if matchWords(text, searchWords(q)) {
			t.Fatalf("query %q should NOT match %q", q, text)
		}
	}
}
