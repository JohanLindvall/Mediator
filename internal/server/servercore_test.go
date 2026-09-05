package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/rartest"
)

// The origin an exported link is built on is the one the visitor asked for:
// behind a proxy that rewrites Host, X-Forwarded-Host names it, and the
// scheme comes from X-Forwarded-Proto — anything else in either is ignored.
func TestRequestBaseFollowsTheProxy(t *testing.T) {
	for _, c := range []struct {
		host, fwdHost, proto, want string
	}{
		{"127.0.0.1:8087", "", "", "http://127.0.0.1:8087"},
		{"127.0.0.1:8087", "music.example.com", "https", "https://music.example.com"},
		{"127.0.0.1:8087", "Music.Example.com, inner.example", "https", "https://music.example.com"},
		{"127.0.0.1:8087", "", "ftp", "http://127.0.0.1:8087"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/playlist.m3u", nil)
		r.Host = c.host
		if c.fwdHost != "" {
			r.Header.Set("X-Forwarded-Host", c.fwdHost)
		}
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if got := requestBase(r); got != c.want {
			t.Errorf("requestBase(host %q, forwarded %q, proto %q) = %q, want %q", c.host, c.fwdHost, c.proto, got, c.want)
		}
	}
}

// The names a television decides by: the table in server.go's init is the
// single source of them, and the entries its comment argues for are pinned
// here — a WebM is a film, a DVD title is an MPEG program stream, a
// transport stream is one — beside one of each other kind.
func TestMimeForNamesTheContainer(t *testing.T) {
	for _, c := range []struct {
		name string
		kind library.Kind
		want string
	}{
		{"film.webm", library.KindVideo, "video/webm"},
		{"title.vob", library.KindVideo, "video/mpeg"},
		{"capture.ts", library.KindVideo, "video/mp2t"},
		{"release.mkv", library.KindVideo, "video/x-matroska"},
		{"track.flac", library.KindAudio, "audio/flac"},
		{"sleeve.jpg", library.KindImage, "image/jpeg"},
		// Nothing the table knows: the kind's own default.
		{"nameless", library.KindAudio, "audio/mpeg"},
	} {
		if got := mimeFor(library.Item{Name: c.name, Kind: c.kind}); got != c.want {
			t.Errorf("mimeFor(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// A release whose tracks are members of an archive set has no directory to
// walk — the path of a member is the set's own with the member after a NUL
// — and walking its directory zipped the volumes instead of the music. The
// download holds the member, read through the index.
func TestAlbumZipOfAnArchivedRelease(t *testing.T) {
	dir := t.TempDir()
	payload := rartest.Payload(4096)
	rartest.WriteSet(t, dir, "release", "01 - track.mp3", payload, 2, false)
	ts, lib := flagServer(t, dir)
	albums := lib.Albums()
	if len(albums) != 1 || albums[0].Source != "dir" {
		t.Fatalf("albums = %+v, want the one directory release the member makes", albums)
	}
	res, body := getBody(t, ts.URL+"/api/albums/"+albums[0].ID+"/zip")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("zip answered %d: %s", res.StatusCode, body)
	}
	names := zipNames(t, body)
	if len(names) != 1 || names[0] != "01 - track.mp3" {
		t.Fatalf("archive holds %v, want the member alone", names)
	}
}

// A confined caller is shown a release only where it may see its tracks,
// and then only those — by id, and as a download: the sheet and the zip
// used to hand over the whole release to a caller allowed one directory.
func TestConfinedCallerSeesOnlyItsTracksOfARelease(t *testing.T) {
	dir := t.TempDir()
	for _, band := range []string{"Gorse Beacon", "Tern Signal"} {
		for i := 1; i <= 2; i++ {
			path := filepath.Join(dir, "Music", band, "Record", "0"+string(rune('0'+i))+" - track.mp3")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	ts, lib := flagServer(t, dir)
	var gorse, tern string
	for _, a := range lib.Albums() {
		_, tracks, _ := lib.AlbumByID(a.ID)
		if len(tracks) > 0 && filepath.Base(filepath.Dir(filepath.Dir(tracks[0].Path))) == "Gorse Beacon" {
			gorse = a.ID
		} else {
			tern = a.ID
		}
	}
	if gorse == "" || tern == "" {
		t.Fatal("the two releases were not built")
	}
	get := func(path, allowed string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set(PathsHeader, allowed)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, body
	}
	allowed := filepath.Join(dir, "Music", "Gorse Beacon")
	if st, _ := get("/api/albums/"+tern, allowed); st != http.StatusNotFound {
		t.Errorf("another performer's release answered %d to a confined caller, want 404", st)
	}
	if st, _ := get("/api/albums/"+tern+"/zip", allowed); st != http.StatusNotFound {
		t.Errorf("another performer's download answered %d to a confined caller, want 404", st)
	}
	st, body := get("/api/albums/"+gorse, allowed)
	if st != http.StatusOK {
		t.Fatalf("the caller's own release answered %d", st)
	}
	var detail AlbumDetailResponse
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Tracks) != 2 {
		t.Errorf("the caller's own release lists %d tracks, want both", len(detail.Tracks))
	}
	st, body = get("/api/albums/"+gorse+"/zip", allowed)
	if st != http.StatusOK {
		t.Fatalf("the caller's own download answered %d", st)
	}
	if names := zipNames(t, body); len(names) != 2 {
		t.Errorf("the confined download holds %v, want the two tracks", names)
	}
}

// A restricted face is told nothing about what it cannot see: no position
// of a film on a music face, by id or in the listing of them all, and no
// record at all for a thing that is not there.
func TestPositionsAreFacedToo(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"film.mkv", "track.mp3"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts, _ := flagServer(t, dir)
	film := library.PathID(filepath.Join(dir, "film.mkv"))
	track := library.PathID(filepath.Join(dir, "track.mp3"))
	for _, id := range []string{film, track} {
		res := putJSON(t, ts.URL+"/api/state/"+id, `{"t":30,"d":100}`)
		res.Body.Close()
	}
	get := func(path, face string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if face != "" {
			req.Header.Set(ContentHeader, face)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, body
	}
	if st, _ := get("/api/state/"+film, "music"); st != http.StatusNotFound {
		t.Errorf("a music face read a film's position: %d", st)
	}
	if st, _ := get("/api/state/"+track, "music"); st != http.StatusOK {
		t.Errorf("a music face could not read a track's position: %d", st)
	}
	if st, _ := get("/api/state/feedfacedeadbeef", ""); st != http.StatusNotFound {
		t.Errorf("a position for nothing answered %d, want 404", st)
	}
	_, body := get("/api/state", "music")
	var all PositionsResponse
	if err := json.Unmarshal(body, &all); err != nil {
		t.Fatal(err)
	}
	if _, leaked := all.Positions[film]; leaked {
		t.Error("the listing of positions carried a film's to a music face")
	}
	if _, ok := all.Positions[track]; !ok {
		t.Error("the listing of positions lost the track's")
	}
}
