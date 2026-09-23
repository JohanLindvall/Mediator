package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// A release carries where it is kept and what its tracks are, on the listing
// and on its sheet — and a caller confined to part of the library is not
// told where a playlist is kept when that is outside what it may see, by
// either door.
func TestAReleaseSaysWhereItIsKeptToWhoMaySee(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Music/Pale Harrow/Saltings/01 - first.mp3", "x")
	write("Music/Pale Harrow/Saltings/02 - second.mp3", "x")
	write("Lists/evening.m3u", "#EXTM3U\n../Music/Pale Harrow/Saltings/01 - first.mp3\n")
	ts, _ := flagServer(t, dir)

	get := func(path, allowed string, out any) {
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
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	paths := func(allowed string) map[string]string {
		var list AlbumsResponse
		get("/api/albums", allowed, &list)
		out := map[string]string{}
		for _, a := range list.Albums {
			var sheet AlbumDetailResponse
			get("/api/albums/"+a.ID, allowed, &sheet)
			if sheet.Album.Path != a.Path {
				t.Errorf("%s: the sheet says %q where the listing says %q", a.Name, sheet.Album.Path, a.Path)
			}
			out[a.Name] = a.Path
			if len(a.Formats) != 1 || a.Formats[0] != "mp3" {
				t.Errorf("%s: formats %q, want [mp3]", a.Name, a.Formats)
			}
		}
		return out
	}

	base := filepath.Base(dir)
	all := paths("")
	if all["Saltings"] != base+"/Music/Pale Harrow/Saltings" || all["evening"] != base+"/Lists/evening.m3u" {
		t.Errorf("unconfined: %q", all)
	}
	confined := paths(filepath.Join(dir, "Music"))
	if confined["Saltings"] != all["Saltings"] {
		t.Errorf("a release inside what the caller may see lost its path: %q", confined["Saltings"])
	}
	if p, ok := confined["evening"]; !ok || p != "" {
		t.Errorf("the playlist should be listed without its path, got %q (listed: %v)", p, ok)
	}
}
