package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDeletionSnapshotIncludesSidecars(t *testing.T) {
	for _, change := range []string{"new sidecar", "changed sidecar", "empty directory", "same stamp replacement"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "release")
			film, sidecar := filepath.Join(dir, "film.mkv"), filepath.Join(dir, "film.nfo")
			writeFile(t, film, "video")
			writeFile(t, sidecar, "notes")
			l := quietLib(root)
			l.Scan(nil)
			p, err := l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: PathID(film)})
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Files) != 2 || p.Bytes != 10 || len(p.Folders) != 1 {
				t.Fatalf("incomplete snapshot: %+v", p)
			}
			switch change {
			case "new sidecar":
				writeFile(t, filepath.Join(dir, "new.nfo"), "new notes")
			case "changed sidecar":
				writeFile(t, sidecar, "changed notes")
			case "empty directory":
				if err := os.Mkdir(filepath.Join(dir, "new"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "same stamp replacement":
				info, err := os.Stat(film)
				if err != nil {
					t.Fatal(err)
				}
				// Rename keeps the old inode allocated, making the replacement
				// a different file even on filesystems that reuse inode numbers.
				if err := os.Rename(film, filepath.Join(root, "old.mkv")); err != nil {
					t.Fatal(err)
				}
				writeFile(t, film, "other")
				if err := os.Chtimes(film, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			out := l.DeleteNow(p)
			if out.Files != 0 || out.Folders != 0 || len(out.Kept) == 0 || !exists(film) || !exists(sidecar) {
				t.Fatalf("a changed folder was removed: %+v", out)
			}
		})
	}
}

func TestDeletionChecksFullTimestampAndCurrentRoots(t *testing.T) {
	for _, change := range []string{"nanoseconds", "roots"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			film := filepath.Join(root, "film.mkv")
			writeFile(t, film, "video")
			stamp := time.Unix(1_700_000_000, 123_000_000)
			if err := os.Chtimes(film, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			l := quietLib(root)
			l.Scan(nil)
			p, err := l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: PathID(film)})
			if err != nil {
				t.Fatal(err)
			}
			if change == "roots" {
				l.SetRoots([]string{t.TempDir()})
			} else {
				later := stamp.Add(100 * time.Microsecond)
				if err := os.Chtimes(film, later, later); err != nil {
					t.Fatal(err)
				}
				info, _ := os.Stat(film)
				if info.ModTime().Equal(stamp) {
					t.Skip("filesystem has no sub-millisecond timestamps")
				}
			}
			out := l.DeleteNow(p)
			if out.Files != 0 || len(out.Kept) != 1 || !exists(film) {
				t.Fatalf("unconfirmed file removed: %+v", out)
			}
		})
	}
}
