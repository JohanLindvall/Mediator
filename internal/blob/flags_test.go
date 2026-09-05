package blob

import (
	"path/filepath"
	"testing"
)

func TestFlagsRoundtripAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := db.GetFlags("id1"); ok {
		t.Fatal("hit on empty store")
	}
	set := map[string]Flags{
		"id1": {Hidden: true},
		"id2": {Favourite: true},
		"id3": {Hidden: true, Favourite: true},
		"id4": {}, // nothing to remember: not stored
	}
	if err := db.SaveFlags(set); err != nil {
		t.Fatal(err)
	}
	all, err := db.AllFlags()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all["id1"] != (Flags{Hidden: true}) || all["id3"] != (Flags{Hidden: true, Favourite: true}) {
		t.Fatalf("AllFlags = %+v", all)
	}
	if _, ok := all["id4"]; ok {
		t.Error("an all-false record was stored")
	}

	// Clearing both flags removes the record rather than storing a blank one.
	if err := db.SaveFlags(map[string]Flags{"id1": {}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.GetFlags("id1"); ok {
		t.Error("cleared flags left a record behind")
	}

	// User data survives reopening, with no mtime/size stamp to invalidate it.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, ok := db.GetFlags("id3")
	if !ok || !f.Hidden || !f.Favourite {
		t.Fatalf("after reopen: ok=%v flags=%+v", ok, f)
	}
}

func TestPruneKeepsFlags(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const gone = "deadbeefdeadbeef"
	if err := db.PutMeta(gone, 1, 2, Meta{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveFlags(map[string]Flags{gone: {Hidden: true}}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Prune(map[string]struct{}{"live": {}}); err != nil {
		t.Fatal(err)
	}

	if _, ok := db.GetMeta(gone, 1, 2); ok {
		t.Error("metadata of a removed file survived pruning")
	}
	// The file may come back; the owner's judgement about it must be waiting.
	if f, ok := db.GetFlags(gone); !ok || !f.Hidden {
		t.Errorf("pruning threw away user data: ok=%v flags=%+v", ok, f)
	}
}
