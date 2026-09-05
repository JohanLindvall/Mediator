package library

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

func names(l *Library) []string {
	var out []string
	for _, it := range l.List(Query{}).Items {
		out = append(out, it.Name)
	}
	sort.Strings(out)
	return out
}

func TestHardLinksIndexedOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no inode identity on this platform")
	}
	dir := t.TempDir()
	original := filepath.Join(dir, "Movie.mkv")
	write(t, original, "the same bytes")
	link := filepath.Join(dir, "Movie (copy).mkv")
	if err := os.Link(original, link); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	// A link in a subdirectory is deduplicated as well.
	nested := filepath.Join(dir, "sub", "Also Movie.mkv")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, nested); err != nil {
		t.Fatal(err)
	}

	l := quietLib(dir)
	l.Scan(nil)
	// Hard links are equally real, so the winner is simply the first path
	// the walk reaches — lexical order, here "Movie (copy).mkv".
	if got := names(l); len(got) != 1 {
		t.Fatalf("indexed %v, want one entry for three links to one file", got)
	}
	indexed := l.List(Query{}).Items[0].Path

	// Removing the indexed path lets a surviving link take over.
	if err := os.Remove(indexed); err != nil {
		t.Fatal(err)
	}
	l.Scan(nil)
	got := names(l)
	if len(got) != 1 {
		t.Fatalf("indexed %v, want exactly one surviving link", got)
	}
	if l.List(Query{}).Items[0].Path == indexed {
		t.Fatal("the deleted path is still indexed")
	}
}

func TestSymlinkDeduplicatedAndSized(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no inode identity on this platform")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "Real.mkv")
	write(t, target, "0123456789")
	link := filepath.Join(dir, "Zlink.mkv") // sorts after the real file
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	l := quietLib(dir)
	l.Scan(nil)
	items := l.List(Query{}).Items
	if len(items) != 1 || items[0].Name != "Real.mkv" {
		t.Fatalf("indexed %v, want only the real file", names(l))
	}
	if items[0].Size != 10 {
		t.Errorf("size = %d, want the target's 10 bytes", items[0].Size)
	}
}

func TestRealFileBeatsSymlinkWhateverTheOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no inode identity on this platform")
	}
	dir := t.TempDir()
	// The link sorts first, so the walk reaches it before its target.
	target := filepath.Join(dir, "Zebra.mkv")
	write(t, target, "0123456789")
	link := filepath.Join(dir, "Alpha.mkv")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	l := quietLib(dir)
	l.Scan(nil)
	items := l.List(Query{}).Items
	if len(items) != 1 {
		t.Fatalf("indexed %v, want one", names(l))
	}
	if items[0].Name != "Zebra.mkv" {
		t.Errorf("indexed %q, want the real file to win", items[0].Name)
	}
	if items[0].Size != 10 {
		t.Errorf("size = %d, want the target's 10 bytes", items[0].Size)
	}
}

func TestSymlinkToOutsideFileIsIndexed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no inode identity on this platform")
	}
	outside := filepath.Join(t.TempDir(), "Elsewhere.mkv")
	write(t, outside, "0123456789")
	dir := t.TempDir()
	link := filepath.Join(dir, "Linked.mkv")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	l := quietLib(dir)
	l.Scan(nil)
	items := l.List(Query{}).Items
	if len(items) != 1 || items[0].Name != "Linked.mkv" {
		t.Fatalf("indexed %v, want the link (its target is outside the roots)", names(l))
	}
	if items[0].Size != 10 {
		t.Errorf("size = %d, want the target's 10 bytes", items[0].Size)
	}
}

func TestDanglingSymlinkIgnored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no inode identity on this platform")
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "missing.mkv"), filepath.Join(dir, "Broken.mkv")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	l := quietLib(dir)
	l.Scan(nil)
	if got := names(l); len(got) != 0 {
		t.Fatalf("indexed %v, want nothing for a dangling link", got)
	}
}

func TestDistinctFilesWithSameContentBothIndexed(t *testing.T) {
	// Deduplication is about file identity, not equal bytes: two separate
	// copies are two files and both belong in the library.
	dir := t.TempDir()
	write(t, filepath.Join(dir, "A.mkv"), "identical")
	write(t, filepath.Join(dir, "B.mkv"), "identical")
	l := quietLib(dir)
	l.Scan(nil)
	if got := names(l); len(got) != 2 {
		t.Fatalf("indexed %v, want both copies", got)
	}
}

func TestHardLinkAddedByWatcherIsSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no inode identity on this platform")
	}
	dir := t.TempDir()
	original := filepath.Join(dir, "Movie.mkv")
	write(t, original, "bytes")
	l := quietLib(dir)
	l.Scan(nil)

	link := filepath.Join(dir, "Movie2.mkv")
	if err := os.Link(original, link); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	l.AddFile(link) // as the watcher would
	if got := names(l); len(got) != 1 {
		t.Fatalf("indexed %v after linking, want one", got)
	}
}

// A hard link has to stay collapsed across a rescan, not only on the first
// one. Scan clears the inode map before each walk, so an item that is simply
// still there has to re-claim its file — otherwise the link beside it finds
// the inode unheld and is indexed as a second copy, which is what a library
// that had been running for a while actually looked like.
func TestHardLinkStaysCollapsedAcrossRescans(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "film.mp4")
	if err := os.WriteFile(real, []byte("some film"), 0o644); err != nil {
		t.Fatal(err)
	}
	byDate := filepath.Join(dir, "bydate")
	if err := os.MkdirAll(byDate, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(byDate, "film.mp4")
	if err := os.Link(real, link); err != nil {
		t.Skipf("hard links unavailable here: %v", err)
	}

	lib := New([]string{dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for pass := 1; pass <= 3; pass++ {
		lib.Scan(nil)
		if got := lib.List(Query{}).Total; got != 1 {
			var paths []string
			for _, it := range lib.List(Query{}).Items {
				paths = append(paths, it.Path)
			}
			t.Fatalf("pass %d indexed %d items, want 1: %v", pass, got, paths)
		}
	}
}

// The same across a restart: the mirrored index carries no inode, so the
// scan that follows a warm start is exactly the case above.
func TestHardLinkStaysCollapsedAfterAWarmStart(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "film.mp4")
	if err := os.WriteFile(real, []byte("some film"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(real, filepath.Join(dir, "film-again.mp4")); err != nil {
		t.Skipf("hard links unavailable here: %v", err)
	}
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := New([]string{dir}, log)
	first.SetMetaDB(db)
	first.Scan(nil)
	if got := first.List(Query{}).Total; got != 1 {
		t.Fatalf("the first run indexed %d items, want 1", got)
	}
	first.flush(db)

	second := New([]string{dir}, log)
	second.SetMetaDB(db)
	second.LoadFromDB(db)
	second.Scan(nil)
	if got := second.List(Query{}).Total; got != 1 {
		t.Fatalf("after a warm start, %d items, want 1", got)
	}
}

// One unreadable corner of a library must not switch off reconciliation for
// the rest of it. It used to mark the whole root, so a single file the server
// cannot open meant nothing deleted ever left the index and no duplicate was
// ever dropped — on a real library, for as long as that file was there.
func TestOneUnreadableDirectoryDoesNotProtectTheWholeRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable directory is still readable")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "inside.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "gone.mp4")
	if err := os.WriteFile(gone, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(dir, "kept.mp4")
	if err := os.WriteFile(kept, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	lib := New([]string{dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	lib.Scan(nil)
	if got := lib.List(Query{}).Total; got != 3 {
		t.Fatalf("indexed %d items, want 3", got)
	}

	// Now make one directory unreadable and delete a file elsewhere.
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	lib.Scan(nil)

	var paths []string
	for _, it := range lib.List(Query{}).Items {
		paths = append(paths, it.Path)
	}
	sort.Strings(paths)
	// The deleted file is gone; the one behind the unreadable directory is
	// kept, because nothing was learned about it either way.
	want := []string{filepath.Join(locked, "inside.mp4"), kept}
	sort.Strings(want)
	if len(paths) != len(want) {
		t.Fatalf("after the rescan: %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("after the rescan: %v, want %v", paths, want)
		}
	}
}
