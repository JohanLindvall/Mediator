package server

import (
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/state"
)

// flagServer serves dir and hands back the library too, so a test can check
// what the endpoints did to it.
func flagServer(t *testing.T, dir string) (*httptest.Server, *library.Library) {
	t.Helper()
	ts, _, lib := serverUnderTest(t, dir)
	return ts, lib
}

// serverUnderTest is flagServer with the Server itself as well, for a test that
// has to reach into what the handlers remember.
func serverUnderTest(t *testing.T, dir string) (*httptest.Server, *Server, *library.Library) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := library.New([]string{dir}, log)
	lib.Scan(nil)
	st := state.Load(nil, log)
	var dist fs.FS = fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	thumbs := NewThumbnailer(nil, nil, log)
	// The rewrapper shares ffmpeg with the thumbnailer, as it does in main:
	// handing it an empty path here would make every /api/remux answer 404
	// and the tests that exercise it would pass for the wrong reason.
	remux := NewRemuxer(thumbs.FFmpegPath(), NewScratch("", 0), log)
	t.Cleanup(func() { _ = remux.Close() })
	hls := NewHLS(thumbs.FFmpegPath(), lib, NewScratch("", 0), log)
	t.Cleanup(hls.Close)
	srv := New(lib, st, thumbs, remux, hls, nil, dist, log)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv, lib
}

func putJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func decodeFlags(t *testing.T, res *http.Response) map[string]library.Flags {
	t.Helper()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	var out FlagsResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Flags
}

func TestFlagEndpoints(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"one.mkv", "two.mkv", "three.mkv"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("v"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts, lib := flagServer(t, dir)
	id := func(n string) string { return library.PathID(filepath.Join(dir, n)) }

	// One item, one field: the other is left alone.
	flags := decodeFlags(t, putJSON(t, ts.URL+"/api/flags/"+id("one.mkv"), `{"favourite":true}`))
	if f := flags[id("one.mkv")]; !f.Favourite || f.Hidden {
		t.Fatalf("response flags = %+v", f)
	}
	flags = decodeFlags(t, putJSON(t, ts.URL+"/api/flags/"+id("one.mkv"), `{"hidden":true}`))
	if f := flags[id("one.mkv")]; !f.Favourite || !f.Hidden {
		t.Fatalf("hiding cleared the favourite: %+v", f)
	}

	// A whole selection in one request.
	body := `{"ids":["` + id("two.mkv") + `","` + id("three.mkv") + `"],"hidden":true}`
	flags = decodeFlags(t, putJSON(t, ts.URL+"/api/flags", body))
	if len(flags) != 2 {
		t.Fatalf("batch judged %d items, want 2: %+v", len(flags), flags)
	}
	if lib.List(library.Query{}).Total != 0 {
		t.Fatal("hidden items still listed")
	}
	if lib.List(library.Query{ShowHidden: "only"}).Total != 3 {
		t.Fatal("hidden listing incomplete")
	}

	// The listing endpoint reads the same filters.
	res, err := http.Get(ts.URL + "/api/library?hidden=only&fav=1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var listed library.Result
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if listed.Total != 1 || listed.Items[0].ID != id("one.mkv") {
		t.Fatalf("hidden+favourite listing = %+v", listed.Items)
	}
	if listed.Counts.Video != 0 {
		t.Fatalf("counts still include hidden videos: %+v", listed.Counts)
	}
}

func TestFlagEndpointsRejectBadRequests(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.mkv"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := flagServer(t, dir)

	cases := []struct {
		name, url, body string
		want            int
	}{
		{"unknown item", ts.URL + "/api/flags/deadbeefdeadbeef", `{"hidden":true}`, http.StatusNotFound},
		{"bad body", ts.URL + "/api/flags/" + library.PathID(filepath.Join(dir, "one.mkv")), `nonsense`, http.StatusBadRequest},
		{"batch without ids", ts.URL + "/api/flags", `{"hidden":true}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := putJSON(t, c.url, c.body)
			res.Body.Close()
			if res.StatusCode != c.want {
				t.Fatalf("status %d, want %d", res.StatusCode, c.want)
			}
		})
	}
}
