package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/state"
)

func request(t *testing.T, method, url, body string) *http.Response {
	t.Helper()
	if method == http.MethodPut {
		return putJSON(t, url, body)
	}
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The client's "mark unwatched" is a DELETE and nothing more, so the position
// endpoints have to survive the whole round trip on their own.
func TestPlaybackPositionLifecycle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.mkv"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := flagServer(t, dir)
	id := library.PathID(filepath.Join(dir, "one.mkv"))

	get := func() state.Position {
		t.Helper()
		res := request(t, http.MethodGet, ts.URL+"/api/state/"+id, "")
		defer res.Body.Close()
		var p state.Position
		if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	res := request(t, http.MethodPut, ts.URL+"/api/state/"+id, `{"t":12.5,"d":100}`)
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("put status %d", res.StatusCode)
	}
	if p := get(); p.Time != 12.5 || p.Duration != 100 {
		t.Fatalf("stored position = %+v", p)
	}

	res = request(t, http.MethodDelete, ts.URL+"/api/state/"+id, "")
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d", res.StatusCode)
	}
	// Nothing saved reads as a zero position, which is what "unwatched" is.
	if p := get(); p != (state.Position{}) {
		t.Fatalf("position survived the delete: %+v", p)
	}

	cases := []struct {
		name, method, id, body string
		want                   int
	}{
		// A position can outlive its file, so clearing an unknown one is not
		// an error; recording one for a file that is not indexed is.
		{"delete unknown", http.MethodDelete, "deadbeefdeadbeef", "", http.StatusNoContent},
		{"put unknown", http.MethodPut, "deadbeefdeadbeef", `{"t":1,"d":2}`, http.StatusNotFound},
		{"put negative", http.MethodPut, id, `{"t":-1,"d":2}`, http.StatusBadRequest},
		{"put nonsense", http.MethodPut, id, `nonsense`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := request(t, c.method, ts.URL+"/api/state/"+c.id, c.body)
			res.Body.Close()
			if res.StatusCode != c.want {
				t.Fatalf("status %d, want %d", res.StatusCode, c.want)
			}
		})
	}
}
