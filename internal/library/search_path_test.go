package library

import (
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// A library rooted deep inside a mount point used to be unsearchable by where
// it is: the display path starts at the root's own base name, so every
// directory above that was missing from the indexed text and a query naming
// the mount point matched nothing at all.
func TestSearchMatchesDirectoriesAboveTheRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "mnt", "disk4", "download")
	write(t, filepath.Join(root, "Some.Release", "film.mkv"), "video")
	write(t, filepath.Join(root, "Some.Release", "cover.jpg"), "image")

	l := quietLib(root)
	l.Scan(nil)

	for _, q := range []string{
		"disk4",                 // a component above the root
		"mnt disk4 download",    // the root spelled out, as one would type it
		"disk4 film",            // path and file name together
		"download some release", // the root's own name still works
	} {
		if n := l.List(Query{Search: q}).Total; n == 0 {
			t.Errorf("search %q found nothing", q)
		}
	}
	if n := l.List(Query{Search: "mnt disk4 film"}).Total; n != 1 {
		t.Errorf("search for one file under the mount = %d hits, want 1", n)
	}
	// Still a conjunction: a directory that is not on this path excludes.
	if n := l.List(Query{Search: "disk9 film"}).Total; n != 0 {
		t.Errorf("search naming a different mount = %d hits, want 0", n)
	}
}

// The warm start is the second door into the index and does not go through
// upsert, so it builds the same text from the stored absolute path.
func TestPathSearchSurvivesRestart(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "mnt", "disk4", "download")
	write(t, filepath.Join(root, "Some.Release", "film.mkv"), "video")

	dbPath := filepath.Join(t.TempDir(), "media.db")
	db, err := blob.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	l := quietLib(root)
	l.SetMetaDB(db)
	l.Scan(nil)
	flushNow(l, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := blob.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	l2 := quietLib(root)
	if n := l2.LoadFromDB(db2); n != 1 {
		t.Fatalf("restored %d items, want 1", n)
	}
	if n := l2.List(Query{Search: "mnt disk4 film"}).Total; n != 1 {
		t.Errorf("restored index is not searchable by path (%d hits)", n)
	}
}

// Enrichment rebuilds the text to fold the tags in; the path must survive it.
func TestPathSearchSurvivesEnrichment(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "mnt", "disk4", "music")
	track := filepath.Join(root, "Release", "01 one.mp3")
	write(t, track, "audio")

	l := quietLib(root)
	l.Scan(nil)
	l.setMeta(PathID(track), tagMeta{title: "A Song", artist: "An Artist"}, 1000)

	if n := l.List(Query{Search: "disk4 song"}).Total; n != 1 {
		t.Errorf("path+tag search after enrichment = %d hits, want 1", n)
	}
}

// A release is found by where it is kept, so a query that answers in the file
// listing does not come back empty in the albums view.
func TestAlbumSearchMatchesItsLocation(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "mnt", "disk4", "music")
	write(t, filepath.Join(root, "Some Release", "01 one.mp3"), "audio")
	write(t, filepath.Join(root, "Some Release", "02 two.mp3"), "audio")

	l := quietLib(root)
	l.Scan(nil)

	if n := len(l.SearchAlbums(AlbumQuery{Search: "mnt disk4", Sort: "name", Desc: false})); n != 1 {
		t.Errorf("album search by mount point = %d, want 1", n)
	}
	if n := len(l.SearchAlbums(AlbumQuery{Search: "disk9", Sort: "name", Desc: false})); n != 0 {
		t.Errorf("album search naming a different mount = %d, want 0", n)
	}
}
