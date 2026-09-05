package server

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/state"
)

func testServer(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := library.New([]string{dir}, log)
	lib.Scan(nil)
	st := state.Load(nil, log)
	thumbs := NewThumbnailer(nil, nil, log)
	var dist fs.FS = fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	srv := New(lib, st, thumbs, NewRemuxer("", NewScratch("", 0), log), NewHLS("", lib, NewScratch("", 0), testLog()), nil, dist, log)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestAlbumsRevalidateWithETag(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"01 - one.mp3", "02 - two.mp3"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts := testServer(t, dir)

	res, err := http.Get(ts.URL + "/api/albums")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	tag := res.Header.Get("ETag")
	if tag == "" || len(body) == 0 {
		t.Fatalf("first response: etag=%q body=%d bytes", tag, len(body))
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache (no-store would disable revalidation)", cc)
	}

	// Unchanged library: the tag round-trips to an empty 304.
	req, _ := http.NewRequest("GET", ts.URL+"/api/albums", nil)
	req.Header.Set("If-None-Match", tag)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(res2.Body)
	res2.Body.Close()
	if res2.StatusCode != http.StatusNotModified || len(b2) != 0 {
		t.Fatalf("revalidation: status=%d body=%d bytes, want 304 and empty", res2.StatusCode, len(b2))
	}

	// The listing endpoint keeps its no-store policy untouched.
	res3, err := http.Get(ts.URL + "/api/library?limit=1")
	if err != nil {
		t.Fatal(err)
	}
	defer res3.Body.Close()
	if cc := res3.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("library Cache-Control = %q, want no-store", cc)
	}
}
