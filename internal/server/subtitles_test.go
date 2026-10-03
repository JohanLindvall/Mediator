package server

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

func TestSubtitleValidatorsFollowTheCaptions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mkv")
	caption := filepath.Join(dir, "clip.srt")
	for name, body := range map[string]string{path: "video", caption: "1\n00:00:01,000 --> 00:00:02,000\nfirst\n"} {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, srv, _ := serverUnderTest(t, dir)
	r := httptest.NewRequest(http.MethodGet, "/api/subs/"+library.PathID(path)+"/0", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "first") {
		t.Fatalf("first subtitles: %d %s", w.Code, w.Body.String())
	}
	tag := w.Header().Get("ETag")
	if tag == "" {
		t.Fatal("missing caption validator")
	}
	r.Header.Set("If-None-Match", tag)
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotModified {
		t.Fatalf("unchanged subtitles: %d", w.Code)
	}
	if err := os.WriteFile(caption, []byte("1\n00:00:01,000 --> 00:00:02,000\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "second") || w.Header().Get("ETag") == tag {
		t.Fatalf("changed subtitles reused a stale validator: %d %s", w.Code, w.Body.String())
	}
	// An old browser may still send the video's Last-Modified from a
	// previous server version. It must not suppress the corrected captions.
	r.Header.Del("If-None-Match")
	r.Header.Set("If-Modified-Since", "Sat, 01 Jan 2050 00:00:00 GMT")
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("obsolete date validator returned %d", w.Code)
	}
}

func TestSubtitleSidecarSizeIsBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mkv")
	if err := os.WriteFile(path, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "clip.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(subtitleMaxBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	_, srv, lib := serverUnderTest(t, dir)
	it, _ := lib.Get(library.PathID(path))
	if data, _, err := srv.subtitleData(t.Context(), it, 0); err == nil || len(data) != 0 {
		t.Fatalf("oversized sidecar returned %d bytes, %v", len(data), err)
	}
}

func TestSubtitleOutputCannotBypassBufferLimit(t *testing.T) {
	b := boundedBuffer{max: 8}
	// Hide bytes.Reader's WriterTo so io.Copy also exercises the receiving
	// writer's interface selection.
	src := struct{ io.Reader }{bytes.NewReader([]byte("too much output"))}
	n, err := io.Copy(&b, src)
	if !errors.Is(err, errBufferLimit) || n != 8 || b.Len() != 8 {
		t.Fatalf("copied %d, retained %d: %v", n, b.Len(), err)
	}
}
