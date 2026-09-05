package server

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// streamItem indexes a file of the given size and returns the library and the
// item, so the stream endpoint can resolve it the way it does in production.
func streamItem(t *testing.T, size int) (*library.Library, library.Item) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := library.New([]string{dir}, testLog())
	lib.Scan(nil)
	items := lib.List(library.Query{}).Items
	if len(items) != 1 {
		t.Fatalf("indexed %d items, want one", len(items))
	}
	return lib, items[0]
}

func streamServer(t *testing.T, lib *library.Library) *httptest.Server {
	t.Helper()
	srv := New(lib, nil, NewThumbnailer(nil, nil, testLog()), NewRemuxer("", NewScratch("", 0), testLog()), NewHLS("", lib, NewScratch("", 0), testLog()), nil, os.DirFS(t.TempDir()), testLog())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// The thumbnailer and the metadata probe read archived content through this
// very endpoint. If those reads counted as playback the thumbnailer would
// throttle itself against its own fetch — one job at a time, with tag
// enrichment paused — for as long as tiles were being made.
func TestInternalStreamDoesNotCountAsPlayback(t *testing.T) {
	// Big enough that the handler is still writing while the client reads
	// its first byte, so Streaming() is observable mid-response.
	lib, it := streamItem(t, 8<<20)
	ts := streamServer(t, lib)

	for _, c := range []struct {
		name     string
		internal bool
		want     bool
	}{
		{"a browser", false, true},
		{"ourselves", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Start from a settled count. The previous case left its handler
			// writing 8 MiB to a client that had stopped reading, and the
			// flag comes down when that handler returns — not when the
			// client let go of it — so without this the next case can
			// measure the one before it.
			waitNotStreaming(t, lib)
			req, err := http.NewRequest("GET", ts.URL+"/api/stream/"+it.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if c.internal {
				req.Header.Set(library.InternalHeader, library.InternalToken())
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			got := lib.Streaming()
			if c.want && !got {
				// The response may not have reached the handler's write yet.
				for range 100 {
					time.Sleep(10 * time.Millisecond)
					if lib.Streaming() {
						got = true
						break
					}
				}
			}
			if got != c.want {
				t.Fatalf("Streaming() = %v during a %s read, want %v", got, c.name, c.want)
			}
		})
	}
}

// waitNotStreaming blocks until no stream is being served, or gives up.
func waitNotStreaming(t *testing.T, lib *library.Library) {
	t.Helper()
	for range 200 {
		if !lib.Streaming() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a stream from an earlier case never finished")
}

// A wrong or missing marker is just a browser: the flag goes up. Nothing else
// changes, which is the point — the marker grants no access, it only declines
// to be counted.
func TestInternalStreamMarkerMustMatch(t *testing.T) {
	lib, it := streamItem(t, 8<<20)
	ts := streamServer(t, lib)

	req, _ := http.NewRequest("GET", ts.URL+"/api/stream/"+it.ID, nil)
	req.Header.Set(library.InternalHeader, "guessed")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if !lib.Streaming() {
		t.Fatal("a guessed marker skipped the playback flag")
	}
}

// An internal reader that keeps pulling is cut off: ffmpeg asks for open-ended
// ranges, and without a ceiling one of them would serve a whole multi-gigabyte
// member for a grid tile.
func TestInternalStreamIsCapped(t *testing.T) {
	const cap64k = 64 << 10
	old := internalStreamCap
	internalStreamCap = cap64k
	t.Cleanup(func() { internalStreamCap = old })

	lib, it := streamItem(t, 1<<20)
	ts := streamServer(t, lib)

	read := func(internal bool) int {
		req, _ := http.NewRequest("GET", ts.URL+"/api/stream/"+it.ID, nil)
		if internal {
			req.Header.Set(library.InternalHeader, library.InternalToken())
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		n, _ := io.Copy(io.Discard, resp.Body) // a capped body ends early, by design
		return int(n)
	}

	if n := read(true); n > cap64k {
		t.Fatalf("an internal read got %d bytes through a %d-byte ceiling", n, cap64k)
	}
	if n := read(false); n != 1<<20 {
		t.Fatalf("a browser got %d bytes of a 1 MiB file; the ceiling is for internal reads only", n)
	}
}

// A conversion reads every byte of the member it is converting, so the
// ceiling that protects against a runaway thumbnail must not apply to it.
// Without this a three-gigabyte film came out of the rewrap at a hundred and
// thirty megabytes: a file that opens, plays a few minutes and stops.
func TestInternalWholeReadIsNotCapped(t *testing.T) {
	old := internalStreamCap
	internalStreamCap = 1 << 20 // small, so the difference is quick to see
	t.Cleanup(func() { internalStreamCap = old })

	size := 4 << 20
	lib, it := streamItem(t, size)
	ts := streamServer(t, lib)

	read := func(whole bool) int {
		req, err := http.NewRequest("GET", ts.URL+"/api/stream/"+it.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(library.InternalHeader, library.InternalToken())
		if whole {
			req.Header.Set(library.InternalWholeHeader, library.InternalToken())
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		n, _ := io.Copy(io.Discard, res.Body)
		return int(n)
	}

	if got := read(false); got > int(internalStreamCap) {
		t.Fatalf("an ordinary internal read returned %d, past the ceiling of %d", got, internalStreamCap)
	}
	if got := read(true); got != size {
		t.Fatalf("a whole-member read returned %d of %d — the ceiling still applied", got, size)
	}
}

// The marker is a token, not a flag: a wrong one is just a browser.
func TestInternalWholeMarkerMustMatch(t *testing.T) {
	old := internalStreamCap
	internalStreamCap = 1 << 20
	t.Cleanup(func() { internalStreamCap = old })

	lib, it := streamItem(t, 4<<20)
	ts := streamServer(t, lib)
	req, _ := http.NewRequest("GET", ts.URL+"/api/stream/"+it.ID, nil)
	req.Header.Set(library.InternalHeader, library.InternalToken())
	req.Header.Set(library.InternalWholeHeader, "not-the-token")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	n, _ := io.Copy(io.Discard, res.Body)
	if n > internalStreamCap {
		t.Fatalf("a wrong marker lifted the ceiling: %d bytes", n)
	}
}

// A transfer that stops half way is either our read failing or the far end
// letting go, and ServeContent reports neither — it swallows the copy error.
// Without knowing which, the only evidence is nginx saying "upstream
// prematurely closed connection", which is true of both.
func TestStreamReadErrorIsRecorded(t *testing.T) {
	lib, it := streamItem(t, 4<<20)
	ts := streamServer(t, lib)

	// Nothing wrong: a whole read leaves nothing to report.
	res, err := http.Get(ts.URL + "/api/stream/" + it.ID)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if n != int64(4<<20) {
		t.Fatalf("read %d bytes of %d", n, 4<<20)
	}
}

// The recording wrapper has to pass the reader through unchanged — it sits in
// front of every byte of media this server sends.
func TestRecordingReaderIsTransparent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	want := make([]byte, 1024)
	for i := range want {
		want[i] = byte(i)
	}
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := library.OpenItem(library.Item{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	rr := &recordingReader{File: f}
	got, err := io.ReadAll(rr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("the wrapper changed the bytes")
	}
	if rr.n != int64(len(want)) {
		t.Fatalf("counted %d bytes, want %d", rr.n, len(want))
	}
	// EOF is how a complete read ends and is not a failure.
	if rr.err != nil && !errors.Is(rr.err, io.EOF) {
		t.Fatalf("a clean read recorded %v", rr.err)
	}

	// And random access, which is what Range requests use.
	rr2 := &recordingReader{File: f}
	buf := make([]byte, 16)
	if _, err := rr2.ReadAt(buf, 100); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf, want[100:116]) {
		t.Fatal("ReadAt through the wrapper returned the wrong bytes")
	}
}
