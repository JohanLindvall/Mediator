package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// A verdict is recorded by the bar and handed back in every listing from
// then on; withdrawn, it is gone from them; and it is refused for a thing
// the caller is not shown, or a value that is not a verdict.
func TestLikeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "track.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, lib := flagServer(t, dir)
	items := lib.List(library.Query{Limit: 10}).Items
	if len(items) != 1 {
		t.Fatal("the track was not indexed")
	}
	id := items[0].ID

	post := func(path, body string) (int, LikeResponse) {
		t.Helper()
		res, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out LikeResponse
		if res.StatusCode == http.StatusOK {
			if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
		}
		return res.StatusCode, out
	}
	listed := func() int {
		t.Helper()
		res, err := http.Get(ts.URL + "/api/library?limit=10")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct {
			Items []struct {
				Like int `json:"like"`
			} `json:"items"`
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Items[0].Like
	}

	v0 := lib.Version()
	if status, got := post("/api/like/"+id, `{"like":1}`); status != http.StatusOK || got.Like != 1 {
		t.Fatalf("liking answered %d %+v", status, got)
	}
	if lib.Version() == v0 {
		t.Error("a verdict must bump the version, so the collections are rebuilt")
	}
	if got := listed(); got != 1 {
		t.Errorf("the listing carries %d, want the like", got)
	}
	if status, got := post("/api/like/"+id, `{"like":-1}`); status != http.StatusOK || got.Like != -1 {
		t.Fatalf("disliking answered %d %+v", status, got)
	}
	if got := listed(); got != -1 {
		t.Errorf("the listing carries %d, want the dislike", got)
	}
	if status, _ := post("/api/like/"+id, `{"like":0}`); status != http.StatusOK {
		t.Fatalf("withdrawing answered %d", status)
	}
	if got := listed(); got != 0 {
		t.Errorf("the listing carries %d after the verdict was withdrawn", got)
	}
	if status, _ := post("/api/like/"+id, `{"like":5}`); status != http.StatusBadRequest {
		t.Errorf("a value that is not a verdict answered %d, want 400", status)
	}
	if status, _ := post("/api/like/feedfacedeadbeef", `{"like":1}`); status != http.StatusNotFound {
		t.Errorf("a verdict on a ghost answered %d, want 404", status)
	}
}
