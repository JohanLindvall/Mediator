package library

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"

	"github.com/JohanLindvall/Mediator/internal/rartest"
)

func quietLib(roots ...string) *Library {
	return New(roots, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// flushNow runs one persist cycle without waiting for the ticker.
func flushNow(l *Library, db *blob.DB) {
	if l.dirty == nil {
		l.mu.Lock()
		l.dirty = make(map[string]struct{})
		l.removed = make(map[string]struct{})
		for id, it := range l.items {
			if persistable(it) {
				l.dirty[id] = struct{}{}
			}
		}
		l.mu.Unlock()
	}
	l.flush(db)
}

func TestIndexSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Movie.mkv"), "video")
	write(t, filepath.Join(dir, "Song.mp3"), "audio")
	dbPath := filepath.Join(t.TempDir(), "media.db")

	db, err := blob.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	l.setMeta(PathID(filepath.Join(dir, "Song.mp3")), tagMeta{
		title: "A Song", artist: "An Artist", year: 1999,
	}, 123_456)
	flushNow(l, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart: the index must be usable before anything touches the disk.
	db2, err := blob.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	l2 := quietLib(dir)
	if n := l2.LoadFromDB(db2); n != 2 {
		t.Fatalf("restored %d items, want 2", n)
	}
	res := l2.List(Query{})
	if res.Total != 2 {
		t.Fatalf("listed %d items, want 2", res.Total)
	}
	// Enrichment results come back too, so no re-reading of files.
	hit := l2.List(Query{Search: "song artist 1999"})
	if hit.Total != 1 {
		t.Fatalf("restored metadata is not searchable (%d hits)", hit.Total)
	}
	if hit.Items[0].Title != "A Song" || hit.Items[0].Duration != 123_456 {
		t.Errorf("restored item = %+v", hit.Items[0])
	}
}

func TestRestartDropsDeletedFiles(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "Keep.mkv")
	gone := filepath.Join(dir, "Gone.mkv")
	write(t, keep, "video")
	write(t, gone, "video")
	dbPath := filepath.Join(t.TempDir(), "media.db")

	db, _ := blob.Open(dbPath)
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	flushNow(l, db)
	db.Close()

	// The file disappears while nothing is running.
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	db2, _ := blob.Open(dbPath)
	defer db2.Close()
	l2 := quietLib(dir)
	l2.SetMetaDB(db2) // as main does, before anything scans
	if n := l2.LoadFromDB(db2); n != 2 {
		t.Fatalf("restored %d, want both records", n)
	}
	// The scan reconciles the stale record away...
	l2.Scan(nil)
	if total := l2.List(Query{}).Total; total != 1 {
		t.Fatalf("after scan %d items, want 1", total)
	}
	// ...and the record is removed from the database as well.
	flushNow(l2, db2)
	recs, err := db2.Items()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Path != keep {
		t.Fatalf("database still holds %+v", recs)
	}
}

func TestPruneDropsStaleCaches(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Keep.mkv"), "video")
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	l := quietLib(dir)
	l.Scan(nil)
	liveID := PathID(filepath.Join(dir, "Keep.mkv"))

	// Caches for a live item and for one that no longer exists.
	for _, id := range []string{liveID, "deadbeefdeadbeef"} {
		if err := db.PutThumb(id, 1, 2, 360, []byte("jpeg")); err != nil {
			t.Fatal(err)
		}
		if err := db.PutMeta(id, 1, 2, blob.Meta{Title: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveItems([]blob.Item{{ID: "deadbeefdeadbeef", Path: "/gone.mkv"}}, nil); err != nil {
		t.Fatal(err)
	}

	l.PruneDB(db)

	if _, ok := db.GetThumb("deadbeefdeadbeef", 1, 2, 360); ok {
		t.Error("thumbnail of a removed file survived pruning")
	}
	if _, ok := db.GetMeta("deadbeefdeadbeef", 1, 2); ok {
		t.Error("metadata of a removed file survived pruning")
	}
	recs, _ := db.Items()
	for _, r := range recs {
		if r.ID == "deadbeefdeadbeef" {
			t.Error("index record of a removed file survived pruning")
		}
	}
	// Everything belonging to the live item is untouched.
	if _, ok := db.GetThumb(liveID, 1, 2, 360); !ok {
		t.Error("pruning removed a live thumbnail")
	}
	if _, ok := db.GetMeta(liveID, 1, 2); !ok {
		t.Error("pruning removed live metadata")
	}
}

func TestPruneSkippedWhenIndexEmpty(t *testing.T) {
	// An unreadable root must not be mistaken for "the library is empty",
	// which would wipe every cache entry.
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutThumb("someid", 1, 2, 360, []byte("jpeg")); err != nil {
		t.Fatal(err)
	}
	quietLib(filepath.Join(t.TempDir(), "missing")).PruneDB(db)
	if _, ok := db.GetThumb("someid", 1, 2, 360); !ok {
		t.Error("pruning with an empty index wiped the cache")
	}
}

func TestArchivedItemsAreNotPersisted(t *testing.T) {
	dir := t.TempDir()
	payload := rartest.Payload(60_000)
	rartest.WriteSet(t, dir, "show", "Episode.mkv", payload, 2, true)
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	l := quietLib(dir)
	l.Scan(nil)
	if l.List(Query{}).Total != 1 {
		t.Fatal("expected the archived episode to be indexed")
	}
	flushNow(l, db)

	// Byte offsets into rar volumes must not be replayed from a stale
	// record, so archived items are re-derived by the scan instead.
	recs, err := db.Items()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("archived items were persisted: %+v", recs)
	}
}

func TestPersistLoopWritesInBackground(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Movie.mkv"), "video")
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	l := quietLib(dir)
	l.Scan(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.PersistLoop(ctx, db); close(done) }()

	// Shutdown flushes whatever is pending, without waiting for a tick.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PersistLoop did not stop")
	}
	recs, _ := db.Items()
	if len(recs) != 1 {
		t.Fatalf("persisted %d records, want 1", len(recs))
	}
}

// openTestDB is a thin alias so tests read a little more clearly.
func openTestDB(path string) (*blob.DB, error) { return blob.Open(path) }

// A record written before the normalisation rules existed comes back through
// LoadFromDB, not through setMeta, and is marked already enriched — so it is
// never read from the file again. Normalising only on the way in from disk
// therefore leaves a warm library carrying whatever it was carrying.
func TestLoadFromDBNormalisesStoredTags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := New([]string{dir}, log)
	lib.SetMetaDB(db)
	lib.Scan(nil)

	id := PathID(path)
	// Write the record the way an older build would have.
	it, ok := lib.Get(id)
	if !ok {
		t.Fatal("item not indexed")
	}
	if err := db.SaveItems([]blob.Item{{
		ID: id, Path: path, Kind: string(it.Kind), Size: it.Size, MTime: it.ModTime,
		FirstSeen: it.FirstSeen, Enriched: true,
		Artist: " Some Band ", Genre: "\ufeffDoom", Album: "An Album", Title: "A Song",
		Year: 20220519,
	}}, nil); err != nil {
		t.Fatal(err)
	}

	fresh := New([]string{dir}, log)
	fresh.SetMetaDB(db)
	if n := fresh.LoadFromDB(db); n != 1 {
		t.Fatalf("restored %d items, want 1", n)
	}
	got, ok := fresh.Get(id)
	if !ok {
		t.Fatal("item not restored")
	}
	if got.Year != 2022 {
		t.Fatalf("year restored as %d, want 2022", got.Year)
	}
	if got.Genre != "Doom" {
		t.Fatalf("genre restored as %q, want %q", got.Genre, "Doom")
	}
	if got.Artist != "Some Band" {
		t.Fatalf("artist restored as %q", got.Artist)
	}
}
