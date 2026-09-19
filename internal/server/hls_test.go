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

// A tabled session is adopted as far as its manifest goes — every segment
// it names is whole, a line being written only once the muxer closed the
// file — and the rest of the directory is what a conversion was in the
// middle of, which goes. A manifest line naming a file that is not there is
// a segment that is not there.
func TestHLSAdoptsATabledSessionByItsManifest(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "hls", "s-tabled")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	key := hlsKey(library.Item{ID: "abc", ModTime: 1, Size: 2}, -1, true, "0", quality{})
	if !strings.Contains(key, "|vod|") {
		t.Fatalf("a tabled session's key does not say so: %q", key)
	}
	files := map[string]string{
		hlsKeyFile:         key,
		hlsManifest:        "0 run3-seg00000.ts\n1 run3-seg00001.ts\n7 run9-seg00007.ts\n8 run9-seg00008.ts\n",
		"run3-seg00000.ts": "whole",
		"run3-seg00001.ts": "whole",
		"run3-seg00002.ts": "half written when the run was killed",
		"run9-seg00007.ts": "whole",
		"run9-seg00008.ts": "", // named, but empty: not a segment
		"run9-seg00009.ts": "the stub past the run's end",
		"list-3.csv":       "run3-seg00000.ts,0,4\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHLS("ffmpeg", nil, NewScratch(base, 0), testLog())
	h.Adopt()

	h.mu.Lock()
	s := h.sessions[key]
	h.mu.Unlock()
	if s == nil {
		t.Fatal("the tabled session was not adopted")
	}
	if len(s.have) != 3 || s.have[0] != "run3-seg00000.ts" || s.have[7] != "run9-seg00007.ts" {
		t.Errorf("adopted %v, want segments 0, 1 and 7", s.have)
	}
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := []string{hlsManifest, "run3-seg00000.ts", "run3-seg00001.ts", "run9-seg00007.ts", hlsKeyFile}
	if strings.Join(left, " ") != strings.Join(want, " ") {
		t.Errorf("left in the directory: %v, want %v", left, want)
	}
}

// The muxer is told exactly where to cut, relative to where the run began,
// where to stop, and to keep the film's clock and list what it closes —
// into files named for the run, so a later run never writes over what an
// earlier one made.
func TestHLSTableArgsSayWhereToCut(t *testing.T) {
	args := strings.Join(hlsTableArgs("/d", "list-4.csv", "run4-seg%05d.ts", 150, []float64{4.8745, 15.2495}, false, 1249.325), " ")
	for _, want := range []string{
		"-f segment", "mpegts_copyts=1", "-segment_start_number 150",
		"-segment_times 4.8745,15.2495", "-to 1249.325",
		"-segment_list /d/list-4.csv", "-segment_list_type csv", "+live",
		"-y /d/run4-seg%05d.ts",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("copy-mode args lack %q: %s", want, args)
		}
	}
	if strings.Contains(args, "-segment_time ") {
		t.Errorf("a copy was cut on the grid: %s", args)
	}
	// A run over the film's last segment has no cut to make, and still has
	// to say so, or the muxer cuts every two seconds on its own account.
	last := strings.Join(hlsTableArgs("/d", "list-6.csv", "run6-seg%05d.ts", 258, nil, false, 0), " ")
	if !strings.Contains(last, "-segment_times 999999999") || strings.Contains(last, "-to ") {
		t.Errorf("a run with no cut to make: %s", last)
	}
	grid := strings.Join(hlsTableArgs("/d", "list-5.csv", "run5-seg%05d.ts", 0, nil, true, 0), " ")
	if !strings.Contains(grid, "-segment_time 4 -segment_time_delta 0.06") || strings.Contains(grid, "-to ") || strings.Contains(grid, "-segment_times") {
		t.Errorf("grid args: %s", grid)
	}
}

// End to end, the thing this exists for: the playlist is the whole film
// from the first request — every segment, the end marker, VOD — and a
// segment asked for out of order is made on request. A seek is a request
// for a later segment, so that is what is asked for first; the opening
// segment is asked for afterwards, which is a second conversion into the
// same session, stopping where the first began.
func TestHLSMakesSegmentsOnRequest(t *testing.T) {
	dir := t.TempDir()
	// Twelve seconds with a keyframe every second: three segments of four.
	writeMKVKeyed(t, filepath.Join(dir, "clip.mkv"), 12, 10)
	ts, _ := flagServer(t, dir)
	id := library.PathID(filepath.Join(dir, "clip.mkv"))

	res, err := http.Get(ts.URL + "/api/hls/" + id + "/index.m3u8?mode=audio&t=9")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	if got := res.Header.Get(hlsTimelineHeader); got != "film" {
		t.Skipf("no table for the test clip (timeline %q): the keyframe index or the duration was not read", got)
	}
	text := string(body)
	for _, want := range []string{"#EXT-X-PLAYLIST-TYPE:VOD", "#EXT-X-ENDLIST", "seg00002.ts", "#EXT-X-START:TIME-OFFSET=9.000"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the first playlist lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "seg00003.ts") {
		t.Fatalf("a twelve-second clip has a fourth segment:\n%s", text)
	}
	var names []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), ".ts") {
			names = append(names, strings.TrimSpace(line))
		}
	}
	fetch := func(name string) []byte {
		t.Helper()
		sres, err := http.Get(res.Request.URL.ResolveReference(&url.URL{Path: name}).String())
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(sres.Body)
		sres.Body.Close()
		if sres.StatusCode != http.StatusOK {
			t.Fatalf("%s answered %d: %s", name, sres.StatusCode, b)
		}
		return b
	}
	// The segment the seek landed on, made first, and carrying the film's
	// own clock: it begins eight seconds in.
	last := fetch(names[2])
	if pts, ok := tsFirstVideoPTS(last); !ok || pts < 7.9 || pts > 8.1 {
		t.Errorf("the third segment begins at %.3f (%v), want 8", pts, ok)
	}
	// Then the opening, out of order: a second run, from the start.
	first := fetch(names[0])
	if pts, ok := tsFirstVideoPTS(first); !ok || pts > 0.2 {
		t.Errorf("the first segment begins at %.3f (%v), want 0", pts, ok)
	}
	if len(fetch(names[1])) < 188 {
		t.Error("the middle segment is not a transport stream")
	}
}

// The same, re-encoded: the grid is kept by forced keyframes, so a segment
// asked for in the middle begins on its own grid point.
func TestHLSGridSegmentBeginsOnTheGrid(t *testing.T) {
	dir := t.TempDir()
	writeMKVKeyed(t, filepath.Join(dir, "clip.mkv"), 12, 10)
	ts, _ := flagServer(t, dir)
	id := library.PathID(filepath.Join(dir, "clip.mkv"))

	res, err := http.Get(ts.URL + "/api/hls/" + id + "/index.m3u8?t=5")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	if got := res.Header.Get(hlsTimelineHeader); got != "film" {
		t.Skipf("no table for the test clip (timeline %q)", got)
	}
	var middle string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "seg00001.ts") {
			middle = strings.TrimSpace(line)
		}
	}
	if middle == "" {
		t.Fatalf("no middle segment in the playlist:\n%s", body)
	}
	sres, err := http.Get(res.Request.URL.ResolveReference(&url.URL{Path: middle}).String())
	if err != nil {
		t.Fatal(err)
	}
	seg, _ := io.ReadAll(sres.Body)
	sres.Body.Close()
	if sres.StatusCode != http.StatusOK {
		t.Fatalf("the middle segment answered %d: %s", sres.StatusCode, seg)
	}
	if pts, ok := tsFirstVideoPTS(seg); !ok || pts < 3.9 || pts > 4.2 {
		t.Errorf("the middle segment begins at %.3f (%v), want 4", pts, ok)
	}
}
