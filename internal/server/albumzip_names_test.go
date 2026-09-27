package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

func TestZipRefusesIncompletePlans(t *testing.T) {
	if _, err := dirEntries(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory error = %v", err)
	}
	s := &Server{}
	if _, _, err := s.zipContents(&library.Album{Source: "m3u"}, make([]library.Item, zipMaxEntries+1), true); !errors.Is(err, errZipLimit) {
		t.Fatalf("oversized playlist error = %v", err)
	}
	root := t.TempDir()
	deep := filepath.Join(root, "one", "two", "three")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := dirEntries(root); !errors.Is(err, errZipLimit) {
		t.Fatalf("deep release error = %v", err)
	}
}

func TestZipOmitsHiddenDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".private"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".private", "notes.txt"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := dirEntries(root)
	if err != nil || len(files) != 0 {
		t.Fatalf("hidden directory included: %v, %v", files, err)
	}
}

func TestMusicFaceZipDoesNotExposeVideo(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one.mp3", "two.mp3", "private.mp4", "cover.jpg"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts, _, lib := serverUnderTest(t, root)
	albums := lib.Albums()
	if len(albums) != 1 {
		t.Fatalf("albums = %d", len(albums))
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/albums/"+albums[0].ID+"/zip", nil)
	req.Header.Set(ContentHeader, "music")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("ZIP: status %d, error %v", res.StatusCode, err)
	}
	if got := zipNames(t, body); !slices.Equal(got, []string{"one.mp3", "two.mp3"}) {
		t.Fatalf("restricted ZIP contains %v", got)
	}
}

func TestPlaylistZipNamesStayUnique(t *testing.T) {
	s := &Server{}
	a := &library.Album{Name: "Playlist", Source: "m3u"}
	tracks := []library.Item{{Name: "song.mp3"}, {Name: "song.mp3"}, {Name: "song (2).mp3"}, {Name: "song.mp3"}}
	_, files, err := s.zipContents(a, tracks, true)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.name] {
			t.Fatalf("duplicate archive entry %q", f.name)
		}
		seen[f.name] = true
	}
}

func TestMultiDiscZipIncludesReleaseArtwork(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "Release")
	for _, rel := range []string{"CD1/one.mp3", "CD1/two.mp3", "CD2/one.mp3", "CD2/two.mp3", "cover.jpg"} {
		path := filepath.Join(release, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, s, lib := serverUnderTest(t, root)
	albums := lib.Albums()
	if len(albums) != 1 {
		t.Fatalf("expected one folded release, got %d", len(albums))
	}
	a, tracks, _ := lib.AlbumByID(albums[0].ID)
	_, files, err := s.zipContents(a, tracks, false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.name)
	}
	if !slices.Contains(names, "cover.jpg") || !slices.Contains(names, "CD1/one.mp3") || !slices.Contains(names, "CD2/one.mp3") {
		t.Fatalf("incomplete release ZIP: %v", names)
	}
}
