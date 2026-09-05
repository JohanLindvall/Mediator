package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The tracks behind a view, as the endpoint answers them: every release
// listed in the listing's order, the music alone out of a listing, and —
// the two guards every collection has — nothing for a face without music
// and only what a confined caller may see.
func TestTracksBehindAView(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Two releases of three tracks, untagged — a directory of tracks is a
	// release — and a film beside one of them.
	for _, rel := range []string{"Gorse Beacon/Signal Fires", "Tern Signal/Harbour Lights"} {
		for i := 1; i <= 3; i++ {
			write(filepath.Join(rel, fmt.Sprintf("%02d - track.mp3", i)))
		}
	}
	write("Gorse Beacon/Signal Fires/making of.mkv")
	ts, _ := flagServer(t, dir)

	fetch := func(path, face, allowed string) (TracksResponse, int) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if face != "" {
			req.Header.Set(ContentHeader, face)
		}
		if allowed != "" {
			req.Header.Set(PathsHeader, allowed)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out TracksResponse
		if res.StatusCode == http.StatusOK {
			if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
		}
		return out, res.StatusCode
	}

	got, status := fetch("/api/tracks?of=albums&sort=name&order=asc", "", "")
	if status != http.StatusOK || len(got.Tracks) != 6 {
		t.Fatalf("every release: %d tracks (status %d), want 6", len(got.Tracks), status)
	}
	// The listing's own order, and each release's running order inside it.
	if got.Tracks[0].Name != "01 - track.mp3" || got.Tracks[3].Name != "01 - track.mp3" ||
		filepath.Base(filepath.Dir(got.Tracks[0].Rel)) != "Harbour Lights" ||
		filepath.Base(filepath.Dir(got.Tracks[3].Rel)) != "Signal Fires" {
		t.Errorf("tracks out of order: %s … %s", got.Tracks[0].Rel, got.Tracks[3].Rel)
	}
	if got.Truncated {
		t.Error("six tracks were reported cut")
	}

	// A listing gives up its films: the music alone, however the query read.
	got, _ = fetch("/api/tracks?of=items&sort=name&order=asc", "", "")
	if len(got.Tracks) != 6 {
		t.Errorf("the listing's tracks: %d, want 6 (the film left out)", len(got.Tracks))
	}
	for _, it := range got.Tracks {
		if filepath.Ext(it.Name) != ".mp3" {
			t.Errorf("%s was queued", it.Name)
		}
	}

	// A face without music has nothing to queue.
	got, status = fetch("/api/tracks?of=albums", "videos", "")
	if status != http.StatusOK || len(got.Tracks) != 0 {
		t.Errorf("a videos face was handed %d tracks (status %d)", len(got.Tracks), status)
	}

	// A confined caller gets its own tracks and nobody else's.
	got, _ = fetch("/api/tracks?of=albums", "", filepath.Join(dir, "Tern Signal"))
	if len(got.Tracks) != 3 {
		t.Errorf("a caller confined to one performer was handed %d tracks, want 3", len(got.Tracks))
	}
	for _, it := range got.Tracks {
		if filepath.Base(filepath.Dir(it.Rel)) != "Harbour Lights" {
			t.Errorf("%s reached a caller confined elsewhere", it.Rel)
		}
	}

	// And a view nothing knows is refused rather than answered with nothing.
	if _, status := fetch("/api/tracks?of=everything", "", ""); status != http.StatusBadRequest {
		t.Errorf("of=everything answered %d, want 400", status)
	}
}
