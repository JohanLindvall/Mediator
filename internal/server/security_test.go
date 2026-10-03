package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

func TestBrowserWritesRequireSameOrigin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "track.mp3")
	if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, srv, _ := serverUnderTest(t, dir)
	id := library.PathID(path)
	for _, tc := range []struct {
		name, origin, site string
		status             int
	}{
		{"other site", "https://unrelated.example", "cross-site", http.StatusForbidden},
		{"sibling site", "https://other.example", "same-site", http.StatusForbidden},
		{"older browser", "https://unrelated.example", "", http.StatusForbidden},
		{"opaque origin", "null", "", http.StatusForbidden},
		{"same origin", "http://example.com", "same-origin", http.StatusOK},
		{"same host without fetch metadata", "http://example.com", "", http.StatusOK},
		{"command line", "", "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := srv.st.Get(id)
			r := httptest.NewRequest(http.MethodPost, "/api/plays/"+id, nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			after, _ := srv.st.Get(id)
			if tc.status == http.StatusForbidden && after != before {
				t.Fatal("a rejected browser request changed playback state")
			}
		})
	}
	// Receivers and links opened from other sites still fetch media normally.
	r := httptest.NewRequest(http.MethodGet, "/api/stream/"+id, nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("cross-origin media read = %d", w.Code)
	}
	if vary := w.Header().Get("Vary"); !strings.Contains(vary, ContentHeader) || !strings.Contains(vary, PathsHeader) {
		t.Errorf("stream cache ignores restrictions: Vary %q", vary)
	}
}

func TestInvalidRestrictionsFailClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "track.mp3"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, srv, _ := serverUnderTest(t, dir)
	for _, tc := range []struct{ header, value string }{
		{ContentHeader, "vidoes"},
		{ContentHeader, ",,"},
		{PathsHeader, "relative/path"},
		{PathsHeader, `"/unclosed,path`},
	} {
		for _, route := range []string{"/api/library", "/api/info", "/api/prefs"} {
			r := httptest.NewRequest(http.MethodGet, route, nil)
			r.Header.Set(tc.header, tc.value)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s: %s %q answered %d: %s", route, tc.header, tc.value, w.Code, w.Body.String())
			}
		}
	}
}

func TestContentFaceCannotManageDirectories(t *testing.T) {
	_, srv, _ := serverUnderTest(t, t.TempDir())
	called := false
	srv.AllowRootChanges(func(roots []string) ([]string, error) {
		called = true
		return roots, nil
	}, false)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		r := httptest.NewRequest(method, "/api/prefs", strings.NewReader(`{"roots":["/"]}`))
		r.Header.Set(ContentHeader, "music")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || called {
			t.Fatalf("%s prefs: status %d, callback called %v", method, w.Code, called)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/info", nil)
	r.Header.Set(ContentHeader, "music")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	var info InfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || !info.Confined {
		t.Fatalf("restricted UI still offers directory preferences: %+v, %v", info, err)
	}
}

func TestStreamCannotRunActiveContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drawing.svg")
	payload := `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	_, srv, _ := serverUnderTest(t, dir)
	token, _, _ := srv.sign.mint(time.Now())
	for _, prefix := range []string{"/api", "/api/signed/" + token} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			r := httptest.NewRequest(method, prefix+"/stream/"+library.PathID(path), nil)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("stream = %d", w.Code)
			}
			if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || strings.Contains(csp, "allow-scripts") || strings.Contains(csp, "allow-same-origin") {
				t.Errorf("unsafe media CSP: %q", csp)
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Error("stream allows MIME sniffing")
			}
			if method == http.MethodGet && w.Body.String() != payload {
				t.Error("sandbox changed the downloaded bytes")
			}
		}
	}
}
