package server

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// A file the index lists but the disk will not hand over — a mount that has
// dropped, a filesystem that shut itself down — is a different answer from
// an item that does not exist, and the player asks the stream endpoint
// exactly that question when a film will not start. 404 is "no such item";
// this is 503, so the player can say what happened instead of sending the
// file through conversions that all fail at the same open.
func TestUnreadableFileIsNotNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(path, []byte("not really a film"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := flagServer(t, dir)
	id := library.PathID(path)

	get := func(p string) (int, string) {
		t.Helper()
		res, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, strings.TrimSpace(string(body))
	}

	if code, _ := get("/api/stream/" + id); code != http.StatusOK {
		t.Fatalf("a readable file answered %d", code)
	}
	// Gone from the disk while the index still lists it, which is what the
	// index does deliberately for a root that stops answering.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if code, body := get("/api/stream/" + id); code != http.StatusServiceUnavailable ||
		body != "file unavailable: it is no longer where the library found it" {
		t.Errorf("an unreadable file answered %d %q, want 503 and the reason", code, body)
	}
	if code, _ := get("/api/stream/deadbeefdeadbeef"); code != http.StatusNotFound {
		t.Errorf("an unknown item answered %d, want 404", code)
	}
	// The pipe cannot open it either, and says so rather than answering an
	// empty 200 — but only where there is an ffmpeg to have tried.
	if code, _ := get("/api/transcode/" + id); code != http.StatusServiceUnavailable && code != http.StatusNotImplemented {
		t.Errorf("a conversion of an unreadable file answered %d, want 503", code)
	}
}

// The reason travels in the viewer's words, by the class of what the disk
// said: what the player shows is this, not an errno.
func TestOpenFaultSpeaksTheViewersLanguage(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{&fs.PathError{Op: "open", Path: "/x", Err: syscall.ENOENT}, "it is no longer where the library found it"},
		{&fs.PathError{Op: "open", Path: "/x", Err: syscall.EACCES}, "the server is not allowed to read it"},
		{&fs.PathError{Op: "open", Path: "/x", Err: syscall.EIO}, "the disk it is on is not answering"},
		{&fs.PathError{Op: "open", Path: "/x", Err: syscall.EUCLEAN}, "the filesystem it is on is damaged and needs repair"},
		// Anything else is said as it came, without the path.
		{&fs.PathError{Op: "open", Path: "/x", Err: syscall.ENXIO}, syscall.ENXIO.Error()},
	} {
		if got := openFault(c.err); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}
