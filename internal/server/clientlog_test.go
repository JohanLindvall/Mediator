package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// The page's own faults reach the log, and what it sends cannot forge a line
// of its own: control characters go, and a field longer than a log line can
// hold is cut.
func TestClientFaultReachesTheLog(t *testing.T) {
	var out bytes.Buffer
	dir := t.TempDir()
	ts, srv, _ := serverUnderTest(t, dir)
	srv.log = slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}))

	post := func(body string) int {
		t.Helper()
		res, err := http.Post(ts.URL+"/api/log", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.StatusCode
	}

	if got := post(`{"what":"feed","detail":"the connection went away","at":615.4,"route":"pipe/full","status":0}`); got != http.StatusNoContent {
		t.Fatalf("a fault answered %d, want 204", got)
	}
	line := out.String()
	for _, want := range []string{"the page reports a fault", `what=feed`, "the connection went away", "at=615.4", "route=pipe/full", "level=WARN"} {
		if !strings.Contains(line, want) {
			t.Errorf("the log lacks %q:\n%s", want, line)
		}
	}
	// A conversion picked up again is not a fault: the viewer saw nothing.
	out.Reset()
	post(`{"what":"feed-recovered","at":615}`)
	if !strings.Contains(out.String(), "level=INFO") {
		t.Errorf("a recovery was logged as a fault:\n%s", out.String())
	}

	// A newline in a value would be a forged log line; a very long one
	// would be a page filling the log with one report.
	out.Reset()
	post(`{"what":"script","detail":"first\ntime=2026-01-01T00:00:00Z level=ERROR msg=\"forged\"","where":"` + strings.Repeat("x", 900) + `"}`)
	logged := out.String()
	// One line, whatever the value tried to be: the newline in it became a
	// space, so what looked like a second record is text inside a quoted
	// field of the first.
	if n := strings.Count(strings.TrimSuffix(logged, "\n"), "\n"); n != 0 {
		t.Errorf("a value forged %d extra lines:\n%s", n, logged)
	}
	if !strings.Contains(logged, `detail="first time=`) {
		t.Errorf("the newline did not become a space:\n%s", logged)
	}
	if strings.Contains(logged, strings.Repeat("x", maxClientLogField+1)) {
		t.Error("an unbounded field went into the log")
	}

	// Nothing to say is not a report.
	if got := post(`{"detail":"only this"}`); got != http.StatusBadRequest {
		t.Errorf("an empty report answered %d, want 400", got)
	}
	if got := post(`not json`); got != http.StatusBadRequest {
		t.Errorf("rubbish answered %d, want 400", got)
	}
	// And a body far larger than a fault is refused before it is read.
	if got := post(`{"what":"x","detail":"` + strings.Repeat("y", maxClientLogBody) + `"}`); got != http.StatusBadRequest {
		t.Errorf("an oversized body answered %d, want 400", got)
	}
}

// The one route where a client writes into the server's log is bounded, and
// what it drops is counted rather than lost quietly.
func TestClientLogIsRateLimited(t *testing.T) {
	var l clientLogLimit
	now := time.Now()
	for i := range clientLogPerMinute {
		if ok, _ := l.allow(now); !ok {
			t.Fatalf("refused at %d, before the bound", i)
		}
	}
	if ok, _ := l.allow(now); ok {
		t.Fatal("allowed past the bound")
	}
	for range 5 {
		l.allow(now)
	}
	// The next minute starts fresh, and says what was missed.
	ok, dropped := l.allow(now.Add(time.Minute))
	if !ok || dropped != 6 {
		t.Errorf("after a minute: ok=%v dropped=%d, want true and the six that were refused", ok, dropped)
	}
	if _, dropped := l.allow(now.Add(time.Minute)); dropped != 0 {
		t.Errorf("the count was reported twice: %d", dropped)
	}
}

// A report naming a film the caller may not see must not confirm that it
// exists: the fault is still logged, without the film.
func TestClientFaultDoesNotConfirmAFilm(t *testing.T) {
	var out bytes.Buffer
	dir := t.TempDir()
	writeMKV(t, dir+"/clip.mkv", 2)
	ts, srv, lib := serverUnderTest(t, dir)
	srv.log = slog.New(slog.NewTextHandler(&out, nil))
	var id string
	for _, it := range lib.List(library.Query{Limit: 5}).Items {
		id = it.ID
	}
	if id == "" {
		t.Skip("nothing indexed to report about")
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/log",
		strings.NewReader(`{"what":"playback","item":"`+id+`"}`))
	req.Header.Set(ContentHeader, "music") // a face that cannot see a film
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", res.StatusCode)
	}
	if strings.Contains(out.String(), "clip.mkv") {
		t.Errorf("a face that cannot see the film was told it exists:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "what=playback") {
		t.Errorf("the fault itself was not logged:\n%s", out.String())
	}
}
