package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// One library wearing every face: each grouped endpoint answers with its own
// view's things, and empty — not somebody else's — where the face has no
// such view. The enforcement lives in the handlers, so it is pinned there.
func TestCollectionsPerFace(t *testing.T) {
	dir := t.TempDir()
	// Music with tags is not needed: a directory of tracks is an album.
	album := filepath.Join(dir, "Gorse Beacon", "1998 - Signal Fires")
	if err := os.MkdirAll(album, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := os.WriteFile(filepath.Join(album, fmt.Sprintf("%02d - track.mp3", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"Harbour.Lights.S01E01.mkv", "Harbour.Lights.S01E02.mkv"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts, _ := flagServer(t, dir)

	fetch := func(path, face string) map[string]json.RawMessage {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if face != "" {
			req.Header.Set("X-Media-Content", face)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s as %q: %d", path, face, res.StatusCode)
		}
		var out map[string]json.RawMessage
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	count := func(raw json.RawMessage) int {
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Fatalf("not a list: %s", raw)
		}
		return len(list)
	}

	for _, c := range []struct {
		path, key, face string
		want            int
	}{
		// The whole library sees everything it has. The tracks carry no
		// tags, so the directory makes an album but no performer and no
		// genre: both views group from what the tags say, and there is
		// deliberately no "Unknown" bucket in either.
		{"/api/albums", "albums", "", 1},
		{"/api/artists", "artists", "", 0},
		{"/api/genres", "genres", "", 0},
		{"/api/series", "series", "", 1},
		// A music face has no television; a videos face has no music.
		{"/api/series", "series", "music", 0},
		{"/api/albums", "albums", "videos", 0},
		{"/api/artists", "artists", "videos", 0},
		{"/api/genres", "genres", "videos", 0},
		{"/api/series", "series", "videos", 1},
		{"/api/albums", "albums", "music", 1},
	} {
		t.Run(c.path+" as "+c.face, func(t *testing.T) {
			if got := count(fetch(c.path, c.face)[c.key]); got != c.want {
				t.Errorf("%s as %q: %d %s, want %d", c.path, c.face, got, c.key, c.want)
			}
		})
	}
}

// A play is counted by the client and told to the server, which hands it
// back in every listing from then on.
func TestPlayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "track.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, lib := flagServer(t, dir)
	var id string
	if items := lib.List(library.Query{Limit: 10}).Items; len(items) == 1 {
		id = items[0].ID
	} else {
		t.Fatal("the track was not indexed")
	}

	for i := 0; i < 2; i++ {
		res, err := http.Post(ts.URL+"/api/plays/"+id, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("play answered %d", res.StatusCode)
		}
	}
	res, err := http.Get(ts.URL + "/api/library?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Items []struct {
			ID    string `json:"id"`
			Plays int    `json:"plays"`
		} `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].Plays != 2 {
		t.Fatalf("listing carries %+v, want two plays", out.Items)
	}
	// And a play on nothing is a 404, not a quiet success.
	res2, _ := http.Post(ts.URL+"/api/plays/feedfacedeadbeef", "", nil)
	res2.Body.Close()
	if res2.StatusCode != http.StatusNotFound {
		t.Errorf("a play on a ghost answered %d", res2.StatusCode)
	}
}
