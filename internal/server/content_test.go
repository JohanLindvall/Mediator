package server

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/state"
)

func TestParseContentHeader(t *testing.T) {
	cases := []struct {
		header string
		want   content
	}{
		{"", everything},
		{"   ", everything},
		{"music", content{music: true}},
		{"videos", content{video: true}},
		{"images", content{image: true}},
		// Spelled either way, spaced any way, in any order.
		{"video", content{video: true}},
		{"image, music", content{image: true, music: true}},
		{" MUSIC ,videos", content{music: true, video: true}},
		{"music,music", content{music: true}},
		// Nothing recognisable is the same as saying nothing: a face that
		// showed an empty library would look broken rather than restricted.
		{"documents", everything},
		{"music,documents", content{music: true}},
	}
	for _, c := range cases {
		if got := parseContent(c.header); got != c.want {
			t.Errorf("parseContent(%q) = %+v, want %+v", c.header, got, c.want)
		}
	}
}

func TestContentMasksTotals(t *testing.T) {
	all := library.Counts{Video: 5, Image: 3, Audio: 7, Playlist: 1, Albums: 2, Artists: 1, Total: 16}

	music := parseContent("music").mask(all)
	if music.Video != 0 || music.Image != 0 {
		t.Errorf("music face still counts pictures: %+v", music)
	}
	if music.Audio != 7 || music.Playlist != 1 || music.Albums != 2 || music.Artists != 1 {
		t.Errorf("music face lost its own counts: %+v", music)
	}
	if music.Total != 8 {
		t.Errorf("total = %d, want the 8 it may see rather than the library's 16", music.Total)
	}

	video := parseContent("videos").mask(all)
	if video.Albums != 0 || video.Artists != 0 || video.Audio != 0 {
		t.Errorf("video face is offered music: %+v", video)
	}
	if video.Video != 5 || video.Total != 5 {
		t.Errorf("video face = %+v, want its 5 videos", video)
	}

	if got := everything.mask(all); got != all {
		t.Errorf("an unrestricted request got %+v, want the library's own %+v", got, all)
	}
}

// The whole point: a caller told it may see music sees only music, whatever
// it asks for and however it asks.
func TestContentHeaderFiltersEveryAnswer(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) string {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return library.PathID(path)
	}
	videoID := write("Movies/clip.mp4", "video")
	write("Pictures/pic.jpg", "image")
	trackID := write("Music/Band - Record/01 song.mp3", "audio")

	ts, lib := flagServer(t, dir)
	lib.Scan(nil)
	lib.RefreshCounts()

	get := func(path, header string) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if header != "" {
			req.Header.Set(ContentHeader, header)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, body
	}

	// Unrestricted: the whole library.
	var full library.Result
	_, body := get("/api/library?limit=100", "")
	if err := json.Unmarshal(body, &full); err != nil {
		t.Fatal(err)
	}
	if full.Total != 3 {
		t.Fatalf("unrestricted listing = %d items, want 3", full.Total)
	}

	// A music face: one track, and counts that say so.
	var onlyMusic library.Result
	_, body = get("/api/library?limit=100", "music")
	if err := json.Unmarshal(body, &onlyMusic); err != nil {
		t.Fatal(err)
	}
	if onlyMusic.Total != 1 || onlyMusic.Items[0].Kind != library.KindAudio {
		t.Errorf("music listing = %d items %v", onlyMusic.Total, onlyMusic.Items)
	}
	if onlyMusic.Counts.Video != 0 || onlyMusic.Counts.Image != 0 || onlyMusic.Counts.Total != 1 {
		t.Errorf("music counts = %+v, want only its own", onlyMusic.Counts)
	}
	if onlyMusic.Counts.Albums != 1 {
		t.Errorf("music face lost its albums: %+v", onlyMusic.Counts)
	}

	// Asking for a kind it may not see is simply empty, not an error.
	_, body = get("/api/library?kind=video&limit=100", "music")
	if err := json.Unmarshal(body, &onlyMusic); err != nil {
		t.Fatal(err)
	}
	if onlyMusic.Total != 0 {
		t.Errorf("music face asked for videos and got %d", onlyMusic.Total)
	}

	// A direct link to a video is nothing here — not its metadata, not its
	// bytes, not its thumbnail. This is the enforcement the client cannot do.
	for _, path := range []string{"/api/item/", "/api/stream/", "/api/thumb/"} {
		if res, _ := get(path+videoID, "music"); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s answered %d for a video on a music face, want 404", path, res.StatusCode)
		}
	}
	// ... while the music it is there for still answers.
	if res, _ := get("/api/item/"+trackID, "music"); res.StatusCode != http.StatusOK {
		t.Errorf("/api/item for a track answered %d on a music face", res.StatusCode)
	}

	// The grouped views are music's own: a video face has none of them.
	var albums AlbumsResponse
	_, body = get("/api/albums", "videos")
	if err := json.Unmarshal(body, &albums); err != nil {
		t.Fatal(err)
	}
	if len(albums.Albums) != 0 {
		t.Errorf("video face was offered %d albums", len(albums.Albums))
	}
	_, body = get("/api/albums", "music")
	if err := json.Unmarshal(body, &albums); err != nil {
		t.Fatal(err)
	}
	if len(albums.Albums) != 1 {
		t.Errorf("music face got %d albums, want its one", len(albums.Albums))
	}

	// And the client is told what it may show.
	var info InfoResponse
	_, body = get("/api/info", "music")
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Content) != 1 || info.Content[0] != "music" {
		t.Errorf("info reported %v, want [music]", info.Content)
	}
	var plain InfoResponse // a fresh one: absent JSON leaves a field as it was
	_, body = get("/api/info", "")
	if err := json.Unmarshal(body, &plain); err != nil {
		t.Fatal(err)
	}
	if plain.Content != nil {
		t.Errorf("an unrestricted request was told %v, want nothing withheld", plain.Content)
	}
}

// Which soundtrack a conversion is asked for, straight from the URL.
func TestAudioMap(t *testing.T) {
	cases := map[string]string{
		"":     "0:a:0?", // the ordinary case: one soundtrack, the first
		"0":    "0:a:0?",
		"3":    "0:a:3?",
		"63":   "0:a:63?",
		"64":   "0:a:0?", // past what any film carries
		"-1":   "0:a:0?",
		"two":  "0:a:0?",
		"1;rm": "0:a:0?", // nothing but a number ever reaches ffmpeg
	}
	for in, want := range cases {
		if got := audioMap(in); got != want {
			t.Errorf("audioMap(%q) = %q, want %q", in, got, want)
		}
	}
}

// A signed link is the permission itself, so it has to be exactly as narrow
// as it claims: issued here, and only until it expires.
func TestSignedLinks(t *testing.T) {
	s := newSigner(nil, testLog())
	now := time.Now()

	token, expires, ok := s.mint(now)
	if !ok || expires <= now.Unix() {
		t.Fatalf("mint = %q %d %v", token, expires, ok)
	}
	if !s.verify(token, now) {
		t.Fatal("a token this server just minted did not verify")
	}
	// Not after it runs out.
	if s.verify(token, now.Add(signTTL+time.Minute)) {
		t.Error("an expired token still worked")
	}
	// Not with the expiry rewritten: that is what the signature covers.
	later := strconv.FormatInt(now.Add(100*time.Hour).Unix(), 10)
	if s.verify(later+token[strings.Index(token, "."):], now) {
		t.Error("moving the expiry forward was accepted")
	}
	// Not from another server.
	if newSigner(nil, testLog()).verify(token, now) {
		t.Error("another server's key verified this token")
	}
	for _, junk := range []string{"", ".", "abc", "9999999999.", "9999999999.zzzz"} {
		if s.verify(junk, now) {
			t.Errorf("%q was accepted", junk)
		}
	}
}

// End to end: the token /api/info hands out opens the signed path, and
// nothing else does.
func TestSignedStreamPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Movies", "clip.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, lib := flagServer(t, dir)
	lib.Scan(nil)
	id := library.PathID(path)

	var info InfoResponse
	res, err := http.Get(ts.URL + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	if info.StreamToken == "" || info.StreamExpires == 0 {
		t.Fatalf("info carried no token: %+v", info)
	}

	get := func(url string) (int, string) {
		res, err := http.Get(ts.URL + url)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, string(b)
	}
	if code, body := get("/api/signed/" + info.StreamToken + "/stream/" + id); code != http.StatusOK || body != "video" {
		t.Errorf("signed stream = %d %q, want the file", code, body)
	}
	// A token this server never issued opens nothing.
	if code, _ := get("/api/signed/9999999999.aaaaaaaaaaaaaaaaaaaaaa/stream/" + id); code != http.StatusNotFound {
		t.Errorf("a forged token answered %d, want 404", code)
	}
	// A signed link reaches media and nothing else: the library is not
	// listable with it, whoever holds it.
	if code, _ := get("/api/signed/" + info.StreamToken + "/library?limit=1"); code != http.StatusNotFound {
		t.Errorf("a signed link listed the library (%d)", code)
	}
	// And an item that does not exist is still not there.
	if code, _ := get("/api/signed/" + info.StreamToken + "/stream/deadbeefdeadbeef"); code != http.StatusNotFound {
		t.Errorf("a signed link to nothing answered %d, want 404", code)
	}
}

// The signed handler rewrites the path on a clone, and it has to stay the
// clone's: for a while the rewrite reached through to the original request
// (&(*r.URL) is r.URL, not a copy), so the access log — which reads the URL
// after the handler returns — recorded every signed request under its inner
// path, and what actually arrived was nowhere in the log.
func TestSignedRewriteLeavesTheRequestAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := library.New([]string{dir}, log)
	lib.Scan(nil)
	h := handlerOf(t, lib, log)
	id := library.PathID(filepath.Join(dir, "clip.mp4"))

	// The token has to come from this same server: the key is minted per
	// run when there is no database, so another instance's token is nobody's.
	infoRec := httptest.NewRecorder()
	h.ServeHTTP(infoRec, httptest.NewRequest(http.MethodGet, "/api/info", nil))
	var info InfoResponse
	if err := json.Unmarshal(infoRec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}

	signed := "/api/signed/" + info.StreamToken + "/stream/" + id
	req := httptest.NewRequest(http.MethodGet, signed, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("signed fetch answered %d", rec.Code)
	}
	if req.URL.Path != signed {
		t.Fatalf("the request's own path became %q — the rewrite reached the original", req.URL.Path)
	}
}

// handlerOf is the same server flagServer starts, handed back as a handler so
// a test can hold the request it serves.
func handlerOf(t *testing.T, lib *library.Library, log *slog.Logger) http.Handler {
	t.Helper()
	st := state.Load(nil, log)
	var dist fs.FS = fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	thumbs := NewThumbnailer(nil, nil, log)
	remux := NewRemuxer("", NewScratch("", 0), log)
	t.Cleanup(func() { _ = remux.Close() })
	hls := NewHLS("", lib, NewScratch("", 0), log)
	t.Cleanup(hls.Close)
	return New(lib, st, thumbs, remux, hls, nil, dist, log).Handler()
}

// A caller confined to part of the library is refused the preferences
// outright, reading as well as writing.
//
// Changing them is the obvious half — it is the one call that could hand
// somebody the whole disk. Reading them matters as much: the list names the
// directories the library is rooted at, and a caller allowed one branch of
// that tree has no business learning what the others are called. Everything
// else such a caller is told is already filtered to what they may see; this
// would have been the one place that was not.
func TestPrefsRefusedWhenConfined(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := flagServer(t, dir)

	ask := func(method, paths string) (int, string) {
		t.Helper()
		var body io.Reader
		if method == http.MethodPut {
			body = strings.NewReader(`{"roots":["/etc"]}`)
		}
		req, err := http.NewRequest(method, ts.URL+"/api/prefs", body)
		if err != nil {
			t.Fatal(err)
		}
		if paths != "" {
			req.Header.Set(PathsHeader, paths)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	// Unconfined, the directories are readable as they always were.
	if code, body := ask(http.MethodGet, ""); code != http.StatusOK || !strings.Contains(body, dir) {
		t.Fatalf("unconfined GET answered %d: %s", code, body)
	}
	// Confined, neither reading nor writing is allowed — and the refusal
	// names no directory.
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		code, body := ask(m, dir)
		if code != http.StatusForbidden {
			t.Errorf("confined %s answered %d, want 403", m, code)
		}
		if strings.Contains(body, dir) {
			t.Errorf("confined %s leaked a directory: %s", m, body)
		}
	}
}

// And the page is told, so it can leave the button out rather than offering
// one that answers 403.
func TestInfoSaysWhenConfined(t *testing.T) {
	dir := t.TempDir()
	ts, _ := flagServer(t, dir)
	get := func(paths string) InfoResponse {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/info", nil)
		if paths != "" {
			req.Header.Set(PathsHeader, paths)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var info InfoResponse
		if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
			t.Fatal(err)
		}
		return info
	}
	if get("").Confined {
		t.Error("an unconfined caller was told it was confined")
	}
	if !get(dir).Confined {
		t.Error("a confined caller was not told")
	}
}

// A caller confined to part of the library (X-Allowed-Paths) is shown the
// collections grouped from what it may see — every one of them. The albums
// endpoint used to leave the restriction out: it answered with the whole
// library's releases under chips that counted only the allowed ones, which
// is the one fault a restricted face must never have.
func TestPathsRestrictEveryCollection(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Two performers in two directories, each a release of three tracks;
	// two shows in two directories, each of two episodes.
	for _, band := range []string{"Gorse Beacon", "Tern Signal"} {
		for i := 1; i <= 3; i++ {
			write(filepath.Join("Music", band, "Record", fmt.Sprintf("%02d - track.mp3", i)))
		}
	}
	for _, show := range []string{"Harbour.Lights", "Marsh.Road"} {
		for i := 1; i <= 2; i++ {
			write(filepath.Join("TV", show, fmt.Sprintf("%s.S01E%02d.mkv", show, i)))
		}
	}
	ts, lib := flagServer(t, dir)
	lib.RefreshCounts()

	get := func(path, allowed string) map[string]json.RawMessage {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if allowed != "" {
			req.Header.Set(PathsHeader, allowed)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, res.StatusCode)
		}
		var out map[string]json.RawMessage
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	count := func(raw json.RawMessage) int {
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Fatalf("not a list: %s", raw)
		}
		return len(list)
	}

	oneBand := filepath.Join(dir, "Music", "Gorse Beacon")
	oneShow := filepath.Join(dir, "TV", "Marsh.Road")
	for _, c := range []struct {
		path, key, allowed string
		want               int
	}{
		{"/api/albums", "albums", "", 2},
		{"/api/albums", "albums", oneBand, 1},
		{"/api/series", "series", "", 2},
		{"/api/series", "series", oneShow, 1},
		// Nothing of either under a directory holding neither.
		{"/api/albums", "albums", oneShow, 0},
		{"/api/series", "series", oneBand, 0},
	} {
		t.Run(c.path+" under "+filepath.Base(c.allowed), func(t *testing.T) {
			if got := count(get(c.path, c.allowed)[c.key]); got != c.want {
				t.Errorf("%s under %q: %d %s, want %d", c.path, c.allowed, got, c.key, c.want)
			}
		})
	}
	// And the chips agree with the grid they stand over: the matching
	// counts a restricted albums answer carries are of the same one release.
	var albums AlbumsResponse
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/albums?q=record", nil)
	req.Header.Set(PathsHeader, oneBand)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(res.Body).Decode(&albums); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if albums.Matching == nil || albums.Matching.Albums != len(albums.Albums) {
		t.Errorf("chips count %+v over %d albums", albums.Matching, len(albums.Albums))
	}
}
