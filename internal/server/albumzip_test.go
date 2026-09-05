package server

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// zipNames unpacks the listing of a downloaded archive.
func zipNames(t *testing.T, body []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

func getBody(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, body
}

// A release is a directory, not a track list: the sleeve and the nfo beside
// the audio are part of what was downloaded, and the archive is named after
// the directory because that is the name the release already has.
func TestAlbumZipTakesTheWholeDirectory(t *testing.T) {
	root := t.TempDir()
	rel := filepath.Join(root, "Some Band - The Album (2026)")
	if err := os.MkdirAll(filepath.Join(rel, "Covers"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"01 - one.mp3":    "audio one",
		"02 - two.mp3":    "audio two",
		"release.nfo":     "notes",
		"folder.jpg":      "sleeve",
		"Covers/back.jpg": "back cover",
		".hidden":         "not this",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(rel, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ts, lib := flagServer(t, root)
	albums := lib.Albums()
	if len(albums) != 1 {
		t.Fatalf("found %d albums, want 1", len(albums))
	}

	res, body := getBody(t, ts.URL+"/api/albums/"+albums[0].ID+"/zip")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content type %q", ct)
	}
	// The directory names the download.
	if cd := res.Header.Get("Content-Disposition"); cd != `attachment; filename="Some Band - The Album (2026).zip"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	got := zipNames(t, body)
	want := []string{"01 - one.mp3", "02 - two.mp3", "Covers/back.jpg", "folder.jpg", "release.nfo"}
	if len(got) != len(want) {
		t.Fatalf("archive holds %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("archive holds %v, want %v", got, want)
		}
	}

	// And the bytes are the files', not placeholders.
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(rc)
		rc.Close()
		if string(content) != files[f.Name] {
			t.Fatalf("%s holds %q, want %q", f.Name, content, files[f.Name])
		}
	}
}

// A playlist is a selection, not a directory — it can reach across several —
// so what goes in is what the index resolved, which is also what keeps a
// playlist from naming anything outside the roots.
func TestAlbumZipOfAPlaylistTakesItsEntries(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"one", "two"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, d, "song.mp3"), []byte("in "+d), 0o644); err != nil {
			t.Fatal(err)
		}
		// Something that is not audio, to prove the entries decide here and
		// not the directories they happen to sit in.
		if err := os.WriteFile(filepath.Join(root, d, "notes.nfo"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m3u := "one/song.mp3\ntwo/song.mp3\n"
	if err := os.WriteFile(filepath.Join(root, "Mixtape.m3u"), []byte(m3u), 0o644); err != nil {
		t.Fatal(err)
	}

	ts, lib := flagServer(t, root)
	var playlist *library.Album
	for _, a := range lib.Albums() {
		if a.Source == "m3u" {
			playlist = a
		}
	}
	if playlist == nil {
		t.Fatal("no playlist album")
	}

	res, body := getBody(t, ts.URL+"/api/albums/"+playlist.ID+"/zip")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	got := zipNames(t, body)
	// Both entries, and the colliding names kept apart so they can be
	// unpacked side by side. Nothing from the directories they live in.
	want := []string{"song (2).mp3", "song.mp3"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("archive holds %v, want %v", got, want)
	}
}

func TestAlbumZipUnknownAlbum(t *testing.T) {
	ts, _ := flagServer(t, t.TempDir())
	res, _ := getBody(t, ts.URL+"/api/albums/nope/zip")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", res.StatusCode)
	}
}
