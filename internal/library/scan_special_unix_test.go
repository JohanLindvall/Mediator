//go:build unix

package library

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestScanAndWatcherSkipSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"pipe.mp3", "pipe.srt"} {
		if err := unix.Mkfifo(filepath.Join(dir, name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(dir, filepath.Join(dir, "directory.mkv")); err != nil {
		t.Fatal(err)
	}
	for _, watch := range []bool{false, true} {
		l := quietLib(dir)
		if watch {
			for _, name := range []string{"pipe.mp3", "pipe.srt", "directory.mkv"} {
				l.AddFile(filepath.Join(dir, name))
			}
		} else {
			l.Scan(nil)
		}
		if l.Size() != 0 || len(l.subsByDir) != 0 {
			t.Errorf("watch=%v: indexed %d non-files, %d subtitle directories", watch, l.Size(), len(l.subsByDir))
		}
	}
}

func TestRescanRemovesFilesReplacedByPipes(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"track.mp3", "captions.srt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l := quietLib(dir)
	l.Scan(nil)
	if l.Size() != 1 || len(l.subsByDir) != 1 {
		t.Fatal("fixture was not indexed")
	}
	for _, name := range []string{"track.mp3", "captions.srt"} {
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l.Scan(nil)
	if l.Size() != 0 || len(l.subsByDir) != 0 {
		t.Fatalf("kept %d pipe items and %d subtitle directories", l.Size(), len(l.subsByDir))
	}
}

func TestRarVolumesSkipSpecialFiles(t *testing.T) {
	for _, names := range [][2]string{{"set.rar", "set.r00"}, {"set.part1.rar", "set.part2.rar"}} {
		dir := t.TempDir()
		first := filepath.Join(dir, names[0])
		if err := os.WriteFile(first, []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(filepath.Join(dir, names[1]), 0o600); err != nil {
			t.Fatal(err)
		}
		if vols := rarVolumes(first); len(vols) != 1 || vols[0] != first {
			t.Errorf("discovery included a pipe: %v", vols)
		}
	}
}
