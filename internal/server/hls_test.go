package server

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// The whole reason this exists: Safari opens a media URL with a range request
// and will not play a resource that cannot answer one. Every file here is an
// ordinary file, and the playlist names a segment that is really there — a
// playlist served before its first segment exists is a player that stops at
// once, thinking it has reached the end.
func TestHLSServesAPlayablePlaylist(t *testing.T) {
	dir := t.TempDir()
	// Matroska with H.264 and AAC: what Safari cannot open and what the
	// conversion therefore has to rewrap, copying the picture.
	writeMKV(t, filepath.Join(dir, "clip.mkv"), 6)
	ts, _ := flagServer(t, dir)
	id := library.PathID(filepath.Join(dir, "clip.mkv"))

	// Exactly as a player does it: follow to wherever the playlist really
	// lives, then resolve the names in it against that.
	res, err := http.Get(ts.URL + "/api/hls/" + id + "/index.m3u8?mode=audio")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	playlistURL := res.Request.URL
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Fatalf("content type %q", ct)
	}
	text := string(body)
	if !strings.HasPrefix(text, "#EXTM3U") {
		t.Fatalf("not a playlist: %q", text[:min(len(text), 60)])
	}

	var seg string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), ".ts") {
			seg = strings.TrimSpace(line)
			break
		}
	}
	if seg == "" {
		t.Fatalf("playlist names no segment:\n%s", text)
	}

	// The segment is a real file, answers ranges, and says what it is. The
	// name is resolved against the playlist's own URL, which is what a player
	// does and what drops any query string the session might have hidden in.
	segURL, err := playlistURL.Parse(seg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(segURL.RawQuery, "mode") {
		t.Fatal("the session is in the query, so a relative name loses it")
	}
	sres, err := http.Get(segURL.String())
	if err != nil {
		t.Fatal(err)
	}
	sbody, _ := io.ReadAll(sres.Body)
	sres.Body.Close()
	if sres.StatusCode != http.StatusOK {
		t.Fatalf("segment status %d", sres.StatusCode)
	}
	if ct := sres.Header.Get("Content-Type"); ct != "video/mp2t" {
		t.Fatalf("segment content type %q", ct)
	}
	if ar := sres.Header.Get("Accept-Ranges"); ar != "bytes" {
		t.Fatalf("Accept-Ranges = %q — the browser this exists for requires it", ar)
	}
	// Transport stream packets begin with a sync byte, every 188 of them.
	if len(sbody) < 188 || sbody[0] != 0x47 {
		t.Fatalf("segment is not a transport stream (%d bytes)", len(sbody))
	}
}

// The playlist names plain files beside itself, so a name that tries to leave
// the session's directory did not come from us.
func TestHLSRefusesNamesItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := flagServer(t, dir)
	id := library.PathID(filepath.Join(dir, "clip.mp4"))

	for _, name := range []string{"..%2f..%2fetc%2fpasswd", "..", "sub%2fseg.ts"} {
		res, err := http.Get(ts.URL + "/api/hls/" + id + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s answered %d, want 404", name, res.StatusCode)
		}
	}
}

func TestHLSUnknownItem(t *testing.T) {
	ts, _ := flagServer(t, t.TempDir())
	res, err := http.Get(ts.URL + "/api/hls/nope/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", res.StatusCode)
	}
}

// A token that names no session is a 404, not somebody else's segments.
func TestHLSRefusesAnUnknownSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := flagServer(t, dir)
	id := library.PathID(filepath.Join(dir, "clip.mp4"))

	res, err := http.Get(ts.URL + "/api/hls/" + id + "/deadbeefdeadbeef/seg00000.ts")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session answered %d, want 404", res.StatusCode)
	}
}

// A conversion that ran to the end is picked up by a later run. One that was
// interrupted is not: there is no carrying on from where it stopped, because
// the ffmpeg that knew where that was is gone.
func TestHLSAdoptsFinishedConversionsOnly(t *testing.T) {
	base := t.TempDir()
	mk := func(name, playlist string) string {
		dir := filepath.Join(base, "hls", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, hlsKeyFile), []byte("k-"+name), 0o644); err != nil {
			t.Fatal(err)
		}
		if playlist != "" {
			if err := os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte(playlist), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	done := mk("done", "#EXTM3U\nseg00000.ts\n#EXT-X-ENDLIST\n")
	partial := mk("partial", "#EXTM3U\nseg00000.ts\n")
	nokey := filepath.Join(base, "hls", "rubbish")
	if err := os.MkdirAll(nokey, 0o755); err != nil {
		t.Fatal(err)
	}

	h := NewHLS("ffmpeg", nil, NewScratch(base, 0), testLog())
	h.Adopt()

	if _, err := os.Stat(done); err != nil {
		t.Fatalf("a finished conversion was thrown away: %v", err)
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatalf("an interrupted conversion was kept: %v", err)
	}
	if _, err := os.Stat(nokey); !os.IsNotExist(err) {
		t.Fatalf("a directory that is not a session was kept: %v", err)
	}
	// And it is reachable, under a token of this run's making.
	h.mu.Lock()
	n := len(h.sessions)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("adopted %d sessions, want 1", n)
	}
}

// A conversion that has finished is a complete thing, and has to say so.
// ffmpeg writes EVENT throughout and leaves it that way after adding the end
// marker; a player then goes on showing LIVE and a duration of what was
// converted rather than of the film.
func TestHLSFinishedPlaylistIsVOD(t *testing.T) {
	growing := []byte("#EXTM3U\n#EXT-X-PLAYLIST-TYPE:EVENT\n#EXTINF:4,\nseg00000.ts\n")
	if got := settledPlaylist(growing); !bytes.Equal(got, growing) {
		t.Fatalf("a conversion still running was relabelled:\n%s", got)
	}
	done := append(append([]byte{}, growing...), []byte("#EXT-X-ENDLIST\n")...)
	got := settledPlaylist(done)
	if bytes.Contains(got, []byte("PLAYLIST-TYPE:EVENT")) {
		t.Fatalf("a finished conversion still calls itself an event:\n%s", got)
	}
	if !bytes.Contains(got, []byte("PLAYLIST-TYPE:VOD")) {
		t.Fatalf("a finished conversion does not call itself complete:\n%s", got)
	}
	if !bytes.Contains(got, []byte("seg00000.ts")) {
		t.Fatal("the segments did not survive the relabelling")
	}
}

// End to end: the playlist a player is finally served says VOD once the
// conversion behind it has run out.
func TestHLSServesVODWhenTheConversionFinishes(t *testing.T) {
	dir := t.TempDir()
	writeMKV(t, filepath.Join(dir, "short.mkv"), 4)
	ts, _ := flagServer(t, dir)
	id := library.PathID(filepath.Join(dir, "short.mkv"))

	res, err := http.Get(ts.URL + "/api/hls/" + id + "/index.m3u8?mode=audio")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	url := res.Request.URL.String()

	// A four-second clip converts almost at once; give it a moment and read
	// the playlist the player would be reading.
	for range 40 {
		time.Sleep(100 * time.Millisecond)
		again, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(again.Body)
		again.Body.Close()
		if !bytes.Contains(body, []byte("#EXT-X-ENDLIST")) {
			continue
		}
		if !bytes.Contains(body, []byte("PLAYLIST-TYPE:VOD")) {
			t.Fatalf("finished, but still served as an event:\n%s", body)
		}
		return
	}
	t.Skip("the conversion did not finish in time to check")
}

// The bug this replaced a redirect to avoid.
//
// ffmpeg writes plain segment names and a player resolves them against the
// playlist's URL — but *which* URL is not agreed. Safari uses the one it was
// finally served from, Chrome the one it asked for. With a redirect in the
// way, one of them resolved every segment without the session and was
// answered 404, which reaches the viewer as "this format cannot be played".
//
// Serving the playlist where it was asked for and naming the segments
// relative to that leaves nothing to disagree about, so this checks the
// names work under *both* rules.
func TestHLSSegmentNamesResolveUnderEitherRule(t *testing.T) {
	dir := t.TempDir()
	writeMKV(t, filepath.Join(dir, "clip.mkv"), 6)
	ts, _ := flagServer(t, dir)
	id := library.PathID(filepath.Join(dir, "clip.mkv"))

	asked := ts.URL + "/api/hls/" + id + "/index.m3u8?mode=audio"
	res, err := http.Get(asked)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	// No redirect: what was asked for is what answered.
	if got := res.Request.URL.String(); got != asked {
		t.Fatalf("redirected to %q — the base a player resolves against is now in doubt", got)
	}

	var seg string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), ".ts") {
			seg = strings.TrimSpace(line)
			break
		}
	}
	if seg == "" {
		t.Fatalf("playlist names no segment:\n%s", body)
	}
	// The name carries the session, which is what makes it unambiguous.
	if !strings.Contains(seg, "/") {
		t.Fatalf("segment %q does not name its session", seg)
	}

	askedURL, err := url.Parse(asked)
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []struct {
		what string
		u    *url.URL
	}{
		{"the URL that was asked for (Chrome)", askedURL},
		{"the URL that answered (Safari)", res.Request.URL},
	} {
		ref, err := base.u.Parse(seg)
		if err != nil {
			t.Fatal(err)
		}
		got, err := http.Get(ref.String())
		if err != nil {
			t.Fatal(err)
		}
		n, _ := io.Copy(io.Discard, got.Body)
		got.Body.Close()
		if got.StatusCode != http.StatusOK {
			t.Fatalf("resolved against %s: %s answered %d", base.what, ref, got.StatusCode)
		}
		if n == 0 {
			t.Fatalf("resolved against %s: %s was empty", base.what, ref)
		}
	}
}

// What iOS requires of anything it is asked to play, in one place: every
// piece of it is an ordinary file with a length, answering byte ranges. A
// conversion piped down one response can do none of that, which is why that
// route never played on a phone at all.
func TestHLSMeetsWhatIOSRequires(t *testing.T) {
	dir := t.TempDir()
	writeMKV(t, filepath.Join(dir, "clip.mkv"), 6)
	ts, _ := flagServer(t, dir)
	id := library.PathID(filepath.Join(dir, "clip.mkv"))

	res, err := http.Get(ts.URL + "/api/hls/" + id + "/index.m3u8?mode=audio")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if ct := res.Header.Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Fatalf("playlist content type %q", ct)
	}
	if ar := res.Header.Get("Accept-Ranges"); ar != "bytes" {
		t.Fatalf("playlist Accept-Ranges %q — the browser this exists for opens with a range request", ar)
	}

	var seg string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), ".ts") {
			seg = strings.TrimSpace(line)
			break
		}
	}
	segURL := ts.URL + "/api/hls/" + id + "/" + seg

	// The opening probe: two bytes, and a 206 that says how long the whole
	// thing is. A 200 here is what a phone refuses.
	req, _ := http.NewRequest(http.MethodGet, segURL, nil)
	req.Header.Set("Range", "bytes=0-1")
	probe, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := io.ReadAll(probe.Body)
	probe.Body.Close()
	if probe.StatusCode != http.StatusPartialContent {
		t.Fatalf("the opening range probe answered %d, want 206", probe.StatusCode)
	}
	if cr := probe.Header.Get("Content-Range"); !strings.Contains(cr, "/") {
		t.Fatalf("206 without a usable Content-Range: %q", cr)
	}
	if len(two) != 2 {
		t.Fatalf("range probe returned %d bytes, want 2", len(two))
	}
	if ct := probe.Header.Get("Content-Type"); ct != "video/mp2t" {
		t.Fatalf("segment content type %q", ct)
	}
}
