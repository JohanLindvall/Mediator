package server

import (
	"os"
	"path/filepath"
	"testing"
)

// coverDir writes the named files (each larger than the last, so size
// ordering is predictable) and returns the directory.
func coverDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for i, n := range names {
		path := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 100*(i+1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// base reports the file names of ranked candidates, relative to dir.
func base(dir string, paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			rel = p
		}
		out[i] = filepath.ToSlash(rel)
	}
	return out
}

func TestCoverCandidatePreferences(t *testing.T) {
	t.Run("conventional name wins over a bigger image", func(t *testing.T) {
		dir := coverDir(t, "cover.jpg", "00-artist-album-group.jpg")
		got := base(dir, coverCandidates(dir))
		if len(got) == 0 || got[0] != "cover.jpg" {
			t.Fatalf("candidates = %v, want cover.jpg first", got)
		}
	})

	t.Run("release artwork is used when nothing is conventionally named", func(t *testing.T) {
		// The common case for a scene release: the cover carries the
		// release name, and previously no artwork was found at all.
		dir := coverDir(t, "00-artist-album-2026-group.jpg")
		got := base(dir, coverCandidates(dir))
		if len(got) != 1 || got[0] != "00-artist-album-2026-group.jpg" {
			t.Fatalf("candidates = %v, want the release image", got)
		}
	})

	t.Run("largest image first among unconventional names", func(t *testing.T) {
		dir := coverDir(t, "small.jpg", "big.png")
		got := base(dir, coverCandidates(dir))
		if len(got) == 0 || got[0] != "big.png" {
			t.Fatalf("candidates = %v, want the larger image first", got)
		}
	})

	t.Run("non-images are ignored", func(t *testing.T) {
		dir := coverDir(t, "notes.txt", "track.flac", "list.m3u")
		got := base(dir, coverCandidates(dir))
		if len(got) != 0 {
			t.Fatalf("candidates = %v, want none", got)
		}
	})

	t.Run("a folder full of pictures is not guessed at", func(t *testing.T) {
		var names []string
		for i := range maxLooseCovers + 1 {
			names = append(names, string(rune('a'+i))+".jpg")
		}
		dir := coverDir(t, names...)
		if got := coverCandidates(dir); len(got) != 0 {
			t.Fatalf("candidates = %v, want none for an ambiguous directory", got)
		}
	})

	t.Run("but a conventional name still wins in a busy folder", func(t *testing.T) {
		names := []string{"cover.jpg"}
		for i := range maxLooseCovers + 1 {
			names = append(names, string(rune('a'+i))+".jpg")
		}
		dir := coverDir(t, names...)
		got := base(dir, coverCandidates(dir))
		if len(got) == 0 || got[0] != "cover.jpg" {
			t.Fatalf("candidates = %v, want cover.jpg first", got)
		}
	})

	t.Run("artwork in a Covers subdirectory is found", func(t *testing.T) {
		// A single-file album rip keeps no image beside the track, but
		// ships scans in a folder of their own.
		dir := coverDir(t, "Covers/CD.jpg", "Covers/Cover Back.jpg", "Covers/Cover Front.jpg")
		got := base(dir, coverCandidates(dir))
		if len(got) == 0 || got[0] != "Covers/Cover Front.jpg" {
			t.Fatalf("candidates = %v, want the front cover first", got)
		}
	})

	t.Run("a cover beside the tracks beats one in a subdirectory", func(t *testing.T) {
		dir := coverDir(t, "Covers/Cover Front.jpg", "cover.jpg")
		got := base(dir, coverCandidates(dir))
		if len(got) == 0 || got[0] != "cover.jpg" {
			t.Fatalf("candidates = %v, want the top-level cover first", got)
		}
	})
}
