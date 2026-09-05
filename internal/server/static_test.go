package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// distFS stands in for the embedded bundle: a shell referencing hashed
// assets, one of each kind of file the build emits.
func distFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte(`<script src="/assets/index-abc123.js"></script>`)},
		"assets/index-abc123.js":  {Data: []byte("console.log(1)")},
		"assets/index-def456.css": {Data: []byte("body{}")},
		"robots.txt":              {Data: []byte("User-agent: *")},
	}
}

func get(t *testing.T, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	spaHandler(distFS()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Result()
}

// The whole cache-busting contract in one place: hashed assets are pinned
// forever, and everything that can name them is never cached, so a new build
// is picked up on the next load rather than whenever a cache expires.
func TestStaticCachePolicy(t *testing.T) {
	cases := []struct {
		name, path, cache string
		status            int
	}{
		{"app shell", "/", "no-store", http.StatusOK},
		{"hashed script", "/assets/index-abc123.js", "public, max-age=31536000, immutable", http.StatusOK},
		{"hashed stylesheet", "/assets/index-def456.css", "public, max-age=31536000, immutable", http.StatusOK},
		{"unhashed root file", "/robots.txt", "no-store", http.StatusOK},
		{"client-side route", "/some/deep/route", "no-store", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := get(t, c.path)
			defer res.Body.Close()
			if res.StatusCode != c.status {
				t.Fatalf("status %d, want %d", res.StatusCode, c.status)
			}
			if got := res.Header.Get("Cache-Control"); got != c.cache {
				t.Fatalf("Cache-Control = %q, want %q", got, c.cache)
			}
		})
	}
}

// A hash that no longer exists is a request from a replaced build, not a
// route. Answering it with the shell would hand HTML to a <script> tag and
// the failure would surface as a parse error with no hint of its cause.
func TestStaticStaleAssetHashIsNotFound(t *testing.T) {
	res := get(t, "/assets/index-oldhash.js")
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); strings.Contains(ct, "text/html") {
		t.Fatalf("stale asset answered with %q — that is the app shell", ct)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store: the next build must be able to fill this in", cc)
	}
}

// The shell must reach the browser able to name the current build's assets.
func TestStaticShellServesIndex(t *testing.T) {
	res := get(t, "/")
	defer res.Body.Close()
	buf := make([]byte, 256)
	n, _ := res.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "/assets/index-abc123.js") {
		t.Fatalf("shell does not reference the hashed asset: %q", buf[:n])
	}
}

// A request under /api that matched no handler is not a client-side route:
// answering it with the app shell tells a caller expecting JSON that all is
// well, and the failure then turns up somewhere with no bearing on the cause.
// The mux cleans a path ending in ".." into exactly such a request.
func TestStaticApiMissIsNotTheAppShell(t *testing.T) {
	for _, path := range []string{"/api/nope", "/api/hls/x/.."} {
		res := get(t, path)
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s answered %d, want 404", path, res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); strings.Contains(ct, "text/html") {
			t.Fatalf("%s answered with %q — that is the app shell", path, ct)
		}
	}
}
