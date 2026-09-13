package blob

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func writeDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// A file deleted and re-created at the same path keeps its id — the id is the
// hash of the path — so one flush can carry the same id as a removal and as a
// record. The record is the newer of the two, and writing it before applying
// the removal discarded it: the item stayed live in memory and unmarked, so
// nothing wrote it again, and the warm start after the next restart was
// missing that file until a walk found it.
func TestSaveItemsKeepsTheRecordWhenBothAreAsked(t *testing.T) {
	db := writeDB(t)
	const id = "deadbeefdeadbeef"
	if err := db.SaveItems([]Item{{ID: id, Path: "/lib/clip.mkv", Kind: "video", Size: 7}}, []string{id}); err != nil {
		t.Fatal(err)
	}
	items, err := db.Items()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("stored %d records, want the one that was written", len(items))
	}
	if items[0].Path != "/lib/clip.mkv" {
		t.Errorf("stored %q", items[0].Path)
	}
}

// The same for the owner's own data, where nothing can regenerate what is
// lost: a position saved while its removal was still in flight.
func TestPutPositionsKeepsTheRecordWhenBothAreAsked(t *testing.T) {
	db := writeDB(t)
	const id = "0011223344556677"
	rec, err := json.Marshal(map[string]any{"t": 12.5, "d": 100.0})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutPositions(map[string][]byte{id: rec}, []string{id}); err != nil {
		t.Fatal(err)
	}
	all, err := db.GetPositions()
	if err != nil {
		t.Fatal(err)
	}
	if string(all[id]) != string(rec) {
		t.Fatalf("stored %q, want the position that was saved", all[id])
	}
}

// And a removal on its own is still a removal: the ordering must not turn
// into "a put always wins".
func TestPutPositionsStillRemoves(t *testing.T) {
	db := writeDB(t)
	const id = "0011223344556677"
	if err := db.PutPositions(map[string][]byte{id: []byte("{}")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.PutPositions(nil, []string{id}); err != nil {
		t.Fatal(err)
	}
	all, err := db.GetPositions()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all[id]; ok {
		t.Error("a position the owner cleared is still stored")
	}
}
