package server

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/state"
)

func TestBuildM3U(t *testing.T) {
	cases := []struct {
		name string
		item library.Item
		want string
	}{
		{
			"a plain file is named by its file name",
			library.Item{ID: "a1", Name: "clip.mkv", Duration: 90_000},
			"#EXTINF:90,clip.mkv\nhttp://host:8080/api/stream/a1\n",
		},
		{
			"tags win, with the artist in front",
			library.Item{ID: "b2", Name: "02.mp3", Title: "Song", Artist: "Band", Duration: 1500},
			"#EXTINF:1,Band - Song\nhttp://host:8080/api/stream/b2\n",
		},
		{
			"an unmeasured file says so",
			library.Item{ID: "c3", Name: "clip.avi"},
			"#EXTINF:-1,clip.avi\nhttp://host:8080/api/stream/c3\n",
		},
		{
			"a tag cannot open a line of its own",
			library.Item{ID: "d4", Name: "x.mp3", Title: "One\n#EXTINF:5,Two", Duration: 2000},
			"#EXTINF:2,One #EXTINF:5,Two\nhttp://host:8080/api/stream/d4\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildM3U([]library.Item{c.item}, "http://host:8080")
			if got != "#EXTM3U\n"+c.want {
				t.Fatalf("body = %q", got)
			}
		})
	}
}

func TestPlaylistEndpoint(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"one.mkv", "two.mkv", "picture.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("v"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts, lib := flagServer(t, dir)
	hidden := library.PathID(filepath.Join(dir, "two.mkv"))
	yes := true
	lib.SetFlags([]string{hidden}, &yes, nil, nil, nil)

	res, err := http.Get(ts.URL + "/api/playlist.m3u?kind=video")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "audio/x-mpegurl; charset=utf-8" {
		t.Fatalf("content type %q", ct)
	}
	if cd := res.Header.Get("Content-Disposition"); !strings.Contains(cd, "playlist.m3u") {
		t.Fatalf("content disposition %q", cd)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(lines) != 3 || lines[0] != "#EXTM3U" {
		t.Fatalf("body = %q", body)
	}
	// The kind filter applied, the hidden item stayed out, and the entry is
	// reachable from outside the page that asked for it.
	if !strings.HasPrefix(lines[2], ts.URL+"/api/stream/") {
		t.Fatalf("entry %q is not an absolute stream URL under %s", lines[2], ts.URL)
	}
	if strings.Contains(string(body), hidden) {
		t.Fatalf("hidden item exported: %q", body)
	}
	if !strings.Contains(lines[1], "one.mkv") {
		t.Fatalf("entry title = %q", lines[1])
	}
}

// The listing endpoint serves at most 500 items per call, so the export has
// to page — and must neither stop at the first page nor repeat one.
func TestPlaylistPagesPastTheListLimit(t *testing.T) {
	dir := t.TempDir()
	const files = listPage + 3
	for i := range files {
		name := filepath.Join(dir, "clip"+strconv.Itoa(i)+".mkv")
		if err := os.WriteFile(name, []byte("v"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := library.New([]string{dir}, log)
	lib.Scan(nil)
	var dist fs.FS = fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	s := New(lib, state.Load(nil, log),
		NewThumbnailer(nil, nil, log), NewRemuxer("", NewScratch("", 0), log), NewHLS("", lib, NewScratch("", 0), testLog()), nil, dist, log)

	items := s.collect(library.Query{Sort: "name"}, maxPlaylistEntries)
	if len(items) != files {
		t.Fatalf("collected %d items, want %d", len(items), files)
	}
	seen := make(map[string]struct{}, len(items))
	for _, it := range items {
		if _, dup := seen[it.ID]; dup {
			t.Fatalf("item %s exported twice", it.Rel)
		}
		seen[it.ID] = struct{}{}
	}
	if got := s.collect(library.Query{Sort: "name"}, 3); len(got) != 3 {
		t.Fatalf("cap of 3 yielded %d items", len(got))
	}
}
