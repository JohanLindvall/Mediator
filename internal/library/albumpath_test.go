package library

import (
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
)

// releasesOnDisk is one performer's release in a folder of its own, another
// split over two disc folders, and a playlist kept somewhere else naming one
// track of the first.
func releasesOnDisk(t *testing.T) (*Library, string) {
	t.Helper()
	root := t.TempDir()
	band := filepath.Join(root, "Music", "Pale Harrow")
	writeFile(t, filepath.Join(band, "Saltings", "01 - first.mp3"), "aaaa")
	writeFile(t, filepath.Join(band, "Saltings", "02 - second.MP3"), "bbbb")
	writeFile(t, filepath.Join(band, "Saltings", "03 - third.flac"), "cccccccc")
	writeFile(t, filepath.Join(band, "Windward", "CD1", "01 - out.mp3"), "dd")
	writeFile(t, filepath.Join(band, "Windward", "CD2", "01 - back.mp3"), "ee")
	writeFile(t, filepath.Join(root, "Lists", "evening.m3u"),
		"#EXTM3U\n../Music/Pale Harrow/Saltings/01 - first.mp3\n")
	l := New([]string{root}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	l.Scan(nil)
	return l, root
}

func albumNamed(t *testing.T, albums []*Album, name string) *Album {
	t.Helper()
	for _, a := range albums {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no release called %q", name)
	return nil
}

// A release says where it is kept, the way the listing names a file: the
// root's own name and the way down from it. A release folded from disc
// folders is the folder above them, and a playlist album is the playlist.
func TestAReleaseSaysWhereItIsKept(t *testing.T) {
	l, root := releasesOnDisk(t)
	albums := l.Albums()
	base := filepath.Base(root)
	for name, want := range map[string]string{
		"Saltings": base + "/Music/Pale Harrow/Saltings",
		"Windward": base + "/Music/Pale Harrow/Windward",
		"evening":  base + "/Lists/evening.m3u",
	} {
		if got := albumNamed(t, albums, name).Path; got != want {
			t.Errorf("%s: path %q, want %q", name, got, want)
		}
	}
}

// What a release is made of, the commonest first: the extension where
// nothing has named a codec, and without its case, so ".MP3" and ".mp3"
// are one format.
func TestAReleaseSaysWhatItsTracksAre(t *testing.T) {
	l, _ := releasesOnDisk(t)
	albums := l.Albums()
	if got, want := albumNamed(t, albums, "Saltings").Formats, []string{"mp3", "flac"}; !slices.Equal(got, want) {
		t.Errorf("formats %q, want %q", got, want)
	}
	if got, want := albumNamed(t, albums, "Windward").Formats, []string{"mp3"}; !slices.Equal(got, want) {
		t.Errorf("formats %q, want %q", got, want)
	}
}

// The codec outranks the container where a probe has named one: an .m4a is
// AAC or ALAC, and only the codec can say which.
func TestAFormatIsTheCodecWhereOneIsKnown(t *testing.T) {
	for _, c := range []struct {
		it   Item
		want string
	}{
		{Item{Name: "01 - first.mp3"}, "mp3"},
		{Item{Name: "01 - first.FLAC"}, "flac"},
		{Item{Name: "01 - first.m4a", ACodec: "ALAC"}, "alac"},
		{Item{Name: "01 - first.m4a", ACodec: "aac"}, "aac"},
		{Item{Name: "no extension"}, ""},
	} {
		if got := formatOf(&c.it); got != c.want {
			t.Errorf("formatOf(%q, %q) = %q, want %q", c.it.Name, c.it.ACodec, got, c.want)
		}
	}
	// Commonest first, and a tie settled by name so two builds agree.
	if got, want := byCount(map[string]int{"wav": 1, "mp3": 5, "flac": 1}), []string{"mp3", "flac", "wav"}; !slices.Equal(got, want) {
		t.Errorf("byCount = %q, want %q", got, want)
	}
	if got := byCount(nil); got != nil {
		t.Errorf("byCount(nil) = %q, want nil", got)
	}
}

// A caller confined to part of the library is shown a playlist whose tracks
// it may see — and is not told where the playlist is kept, that place being
// outside what it may see. The releases inside keep their paths, and the
// shared build is not touched: the next caller, unconfined, is still told.
func TestAConfinedCallerIsNotToldWhereAPlaylistIsKept(t *testing.T) {
	l, root := releasesOnDisk(t)
	music := ParsePaths(filepath.Join(root, "Music"))
	shown := l.AllowedAlbums(l.Albums(), music)
	if len(shown) != 3 {
		t.Fatalf("%d releases under the music, want all 3", len(shown))
	}
	if got := albumNamed(t, shown, "evening").Path; got != "" {
		t.Errorf("the playlist's path was handed over: %q", got)
	}
	if got := albumNamed(t, shown, "Saltings").Path; got == "" {
		t.Error("a release inside what the caller may see lost its path")
	}
	if got := albumNamed(t, l.Albums(), "evening").Path; got == "" {
		t.Error("confining one caller took the path from the shared build")
	}

	// The same for the one release a sheet opens.
	pl := albumNamed(t, l.Albums(), "evening")
	if got := pl.ShownUnder(music).Path; got != "" {
		t.Errorf("ShownUnder handed over %q", got)
	}
	if got := pl.ShownUnder(PathFilter{}); got != pl {
		t.Error("an unconfined caller should be handed the release itself")
	}
	lists := ParsePaths(filepath.Join(root, "Lists") + "," + filepath.Join(root, "Music"))
	if got := pl.ShownUnder(lists).Path; got == "" {
		t.Error("a caller who may see the playlist's folder lost its path")
	}
}
