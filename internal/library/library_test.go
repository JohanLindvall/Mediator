package library

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testLib(t *testing.T) (*Library, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Movies", "clip.mp4"), "vvvv")
	writeFile(t, filepath.Join(root, "Photos", "pic.jpg"), "iii")
	writeFile(t, filepath.Join(root, "Photos", ".hidden.jpg"), "x")
	writeFile(t, filepath.Join(root, "Music", "01 one.mp3"), "aaaaaa")
	writeFile(t, filepath.Join(root, "Music", "02 two.mp3"), "bb")
	writeFile(t, filepath.Join(root, "notes.txt"), "not media")

	// Playlist referencing an existing track, a missing one, and a file
	// outside the root (must not be exposed).
	outside := filepath.Join(t.TempDir(), "evil.mp3")
	writeFile(t, outside, "evil")
	writeFile(t, filepath.Join(root, "Music", "mix.m3u"),
		"#EXTM3U\n01 one.mp3\nmissing.mp3\n"+outside+"\n")

	lib := New([]string{root}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	lib.Scan(nil)
	return lib, root
}

func TestScanAndList(t *testing.T) {
	lib, _ := testLib(t)

	if got := lib.Size(); got != 5 {
		t.Fatalf("indexed %d items, want 5", got)
	}
	c := lib.Counts()
	if c.Video != 1 || c.Image != 1 || c.Audio != 2 || c.Playlist != 1 {
		t.Fatalf("bad counts: %+v", c)
	}

	res := lib.List(Query{Kind: KindAudio, Sort: "name"})
	if res.Total != 2 || res.Items[0].Name != "01 one.mp3" {
		t.Fatalf("audio list wrong: total=%d items=%v", res.Total, res.Items)
	}

	res = lib.List(Query{Search: "CLIP"})
	if res.Total != 1 || res.Items[0].Kind != KindVideo {
		t.Fatalf("search failed: %+v", res)
	}

	res = lib.List(Query{Sort: "size", Desc: true, Limit: 2})
	if len(res.Items) != 2 || res.Items[0].Size < res.Items[1].Size {
		t.Fatalf("size sort wrong: %+v", res.Items)
	}

	res = lib.List(Query{Offset: 4, Limit: 10})
	if res.Total != 5 || len(res.Items) != 1 {
		t.Fatalf("paging wrong: total=%d page=%d", res.Total, len(res.Items))
	}
}

func TestAlbums(t *testing.T) {
	lib, root := testLib(t)

	albums := lib.Albums()
	if len(albums) != 2 { // Music dir album + m3u playlist
		t.Fatalf("got %d albums, want 2: %+v", len(albums), albums)
	}

	_, tracks, ok := lib.AlbumByID("d" + PathID(filepath.Join(root, "Music")))
	if !ok || len(tracks) != 2 {
		t.Fatalf("dir album missing or wrong tracks: %v", tracks)
	}

	var m3u *Album
	for _, a := range albums {
		if a.Source == "m3u" {
			m3u = a
		}
	}
	if m3u == nil {
		t.Fatal("m3u album not found")
	}
	if m3u.Name != "mix" || m3u.Tracks != 1 {
		t.Fatalf("m3u album should only contain the one indexed track: %+v", m3u)
	}
}

func TestRescanReconciles(t *testing.T) {
	lib, root := testLib(t)

	// New file appears.
	writeFile(t, filepath.Join(root, "Photos", "new.png"), "png")
	// Old file disappears.
	if err := os.Remove(filepath.Join(root, "Movies", "clip.mp4")); err != nil {
		t.Fatal(err)
	}
	v := lib.Version()
	lib.Scan(nil)

	if got := lib.Size(); got != 5 {
		t.Fatalf("after rescan got %d items, want 5", got)
	}
	if lib.List(Query{Search: "clip"}).Total != 0 {
		t.Fatal("deleted file still listed")
	}
	if lib.List(Query{Search: "new.png"}).Total != 1 {
		t.Fatal("new file not indexed")
	}
	if lib.Version() == v {
		t.Fatal("version should bump on changes")
	}
}

func TestAddRemove(t *testing.T) {
	lib, root := testLib(t)

	p := filepath.Join(root, "Movies", "added.mkv")
	writeFile(t, p, "mkv")
	lib.AddFile(p)
	if lib.List(Query{Search: "added"}).Total != 1 {
		t.Fatal("AddFile did not index")
	}

	lib.Remove(filepath.Join(root, "Music")) // directory subtree removal
	if lib.Counts().Audio != 0 {
		t.Fatal("Remove(dir) should drop contained audio files")
	}
}

// What counts as media, and what does not. Every entry here was measured
// against real disks rather than guessed at — see the survey that added the
// transport streams, which were the largest thing the library could not see.
func TestClassify(t *testing.T) {
	for _, c := range []struct {
		name string
		want Kind
	}{
		// A stream capture, and what a camcorder writes.
		{"capture.ts", KindVideo},
		{"CLIP0001.MTS", KindVideo},
		{"00001.m2ts", KindVideo},
		// The older wrappers.
		{"film.divx", KindVideo},
		{"clip.f4v", KindVideo},
		{"talk.ogv", KindVideo},
		{"old.rmvb", KindVideo},
		// A JPEG under an older name.
		{"photo.jfif", KindImage},
		// A DVD title, and the ordinary cases.
		{"feature.vob", KindVideo},
		{"song.flac", KindAudio},
		{"release.mkv", KindVideo},
		{"order.m3u", KindPlaylist},
		// Not media, and two that are containers rather than items: the
		// scan reaches inside them instead.
		{"notes.txt", ""},
		{"release.rar", ""},
		{"disc.iso", ""},
		{"subs.sub", ""},
		{"noextension", ""},
	} {
		if got := Classify(c.name); got != c.want {
			t.Errorf("Classify(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}
