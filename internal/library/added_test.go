package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// firstSeen reads an item's recorded addition time.
func firstSeen(t *testing.T, l *Library, path string) int64 {
	t.Helper()
	it, ok := l.Get(PathID(path))
	if !ok {
		t.Fatalf("%s is not indexed", path)
	}
	return it.FirstSeen
}

func TestFirstSeenSetOnceOnCreation(t *testing.T) {
	dir := t.TempDir()
	movie := filepath.Join(dir, "Movie.mkv")
	write(t, movie, "video")

	before := time.Now().UnixMilli()
	l := quietLib(dir)
	l.Scan(nil)
	added := firstSeen(t, l, movie)
	if added < before || added > time.Now().UnixMilli() {
		t.Fatalf("FirstSeen = %d, want a timestamp from the scan", added)
	}

	// Age it so a reset would be unmistakable, then change the file: an
	// item that grows is still the same addition to the library.
	const old = 1_000_000
	l.mu.Lock()
	l.items[PathID(movie)].FirstSeen = old
	l.mu.Unlock()
	write(t, movie, "video, but longer")
	l.Scan(nil)

	it, _ := l.Get(PathID(movie))
	if it.FirstSeen != old {
		t.Errorf("FirstSeen = %d after a rewrite, want it kept at %d", it.FirstSeen, old)
	}
	if it.Size != int64(len("video, but longer")) {
		t.Errorf("the rewrite was not picked up: %+v", it)
	}
}

func TestFirstSeenSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	movie := filepath.Join(dir, "Movie.mkv")
	write(t, movie, "video")
	dbPath := filepath.Join(t.TempDir(), "media.db")

	db, err := blob.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	const old = 1_000_000
	l.mu.Lock()
	l.items[PathID(movie)].FirstSeen = old
	l.mu.Unlock()
	flushNow(l, db) // the scan already marked the item dirty
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := blob.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	l2 := quietLib(dir)
	l2.SetMetaDB(db2)
	if n := l2.LoadFromDB(db2); n != 1 {
		t.Fatalf("restored %d items, want 1", n)
	}
	if got := firstSeen(t, l2, movie); got != old {
		t.Errorf("restored FirstSeen = %d, want %d", got, old)
	}
	// The scan that follows the restore reconciles the same file; that must
	// not re-date it as a new arrival either.
	l2.Scan(nil)
	if got := firstSeen(t, l2, movie); got != old {
		t.Errorf("FirstSeen = %d after the reconciling scan, want %d", got, old)
	}
}

func TestFirstSeenSeededFromMTimeForOlderRecords(t *testing.T) {
	// Records written before the index tracked additions have none; falling
	// back to the file's own timestamp keeps an existing library from
	// sorting as one big batch at the epoch.
	dir := t.TempDir()
	movie := filepath.Join(dir, "Movie.mkv")
	write(t, movie, "video")
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const mtime = 1_700_000_000_000
	rec := blob.Item{ID: PathID(movie), Path: movie, Kind: string(KindVideo), Size: 5, MTime: mtime}
	if err := db.SaveItems([]blob.Item{rec}, nil); err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	if n := l.LoadFromDB(db); n != 1 {
		t.Fatalf("restored %d items, want 1", n)
	}
	if got := firstSeen(t, l, movie); got != mtime {
		t.Errorf("FirstSeen = %d, want the file's mtime %d", got, mtime)
	}
}

func TestSortByAdded(t *testing.T) {
	l := quietLib("/media")
	for i, name := range []string{"third.mp4", "first.mp4", "second.mp4"} {
		l.upsert("/media/"+name, KindVideo, 10, time.Unix(int64(i), 0), fileKey{}, false)
	}
	// Order the additions explicitly: a scan can index a whole tree inside
	// one millisecond, so real timestamps would not separate them.
	l.mu.Lock()
	for name, added := range map[string]int64{"first.mp4": 100, "second.mp4": 200, "third.mp4": 300} {
		l.items[PathID("/media/"+name)].FirstSeen = added
	}
	l.mu.Unlock()

	res := l.List(Query{Sort: "added"})
	got := []string{res.Items[0].Name, res.Items[1].Name, res.Items[2].Name}
	want := []string{"first.mp4", "second.mp4", "third.mp4"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ascending order %v, want %v", got, want)
		}
	}
	res = l.List(Query{Sort: "added", Desc: true})
	if res.Items[0].Name != "third.mp4" {
		t.Fatalf("descending order starts with %s, want the newest", res.Items[0].Name)
	}
}

func TestScanKeepsItemsOfAFailedRoot(t *testing.T) {
	// A root that briefly fails to stat lists nothing. Reconciling against
	// that would drop and re-add its whole subtree, resetting FirstSeen —
	// and every cache keyed by the item — for the entire root.
	base := t.TempDir()
	root := filepath.Join(base, "root")
	movie := filepath.Join(root, "Movie.mkv")
	write(t, movie, "video")
	write(t, filepath.Join(root, "Song.mp3"), "audio")

	l := quietLib(root)
	l.Scan(nil)
	added := firstSeen(t, l, movie)

	away := filepath.Join(base, "away")
	if err := os.Rename(root, away); err != nil {
		t.Fatal(err)
	}
	l.Scan(nil)
	if got := l.Size(); got != 2 {
		t.Fatalf("after a failed walk %d items remain, want both kept", got)
	}
	if err := os.Rename(away, root); err != nil {
		t.Fatal(err)
	}
	l.Scan(nil)
	if got := firstSeen(t, l, movie); got != added {
		t.Errorf("FirstSeen = %d after the root came back, want %d", got, added)
	}

	// A readable root still reconciles deletions.
	if err := os.Remove(movie); err != nil {
		t.Fatal(err)
	}
	l.Scan(nil)
	if got := l.Size(); got != 1 {
		t.Fatalf("%d items after a deletion, want the sweep to have run", got)
	}
}
