package blob

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestThumbRoundtrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sub", "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, ok := db.GetThumb("id1", 111, 222, 360); ok {
		t.Fatal("hit on empty store")
	}
	if err := db.PutThumb("id1", 111, 222, 360, []byte("jpegdata")); err != nil {
		t.Fatal(err)
	}
	if data, ok := db.GetThumb("id1", 111, 222, 360); !ok || string(data) != "jpegdata" {
		t.Fatalf("roundtrip: ok=%v data=%q", ok, data)
	}
	if _, ok := db.GetThumb("id1", 111, 222, 720); ok {
		t.Fatal("hit for width never stored")
	}
	if _, ok := db.GetThumb("id1", 999, 222, 360); ok {
		t.Fatal("hit despite changed mtime")
	}
	if _, ok := db.GetThumb("id1", 111, 999, 360); ok {
		t.Fatal("hit despite changed size")
	}
}

func TestMetaRoundtripAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := db.GetMeta("id1", 111, 222); ok {
		t.Fatal("hit on empty store")
	}
	in := Meta{Duration: 12345, Title: "T", Artist: "A", Album: "L", Genre: "G", Track: 7, Year: 1999}
	if err := db.PutMeta("id1", 111, 222, in); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.GetMeta("id1", 999, 222); ok {
		t.Fatal("hit despite changed mtime")
	}

	// Entries survive reopening.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, ok := db.GetMeta("id1", 111, 222)
	if !ok || m.Duration != 12345 || m.Title != "T" || m.Track != 7 || m.Year != 1999 {
		t.Fatalf("after reopen: ok=%v meta=%+v", ok, m)
	}
}

func TestSecondOpenExplainsTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	_, err = Open(path)
	if err == nil {
		t.Fatal("a second open of a locked database should fail")
	}
	// The bare "timeout" bbolt reports gives the user nothing to act on.
	msg := err.Error()
	for _, want := range []string{"locked by another instance", "-db"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

// The epoch identifies the store to browsers, which hang it on thumbnail URLs
// that are otherwise cached immutable for a year. So it must be stable while
// the file lives, and different for a file created after it was deleted —
// that difference is the whole mechanism for "I deleted the database and the
// browser still shows the old images".
func TestEpochIsStablePerStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "media.db")

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := db.Epoch()
	if first == "" {
		t.Fatal("no epoch minted")
	}
	db.Close()

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Epoch(); got != first {
		t.Errorf("reopening changed the epoch: %q then %q", first, got)
	}
	again.Close()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if fresh.Epoch() == first {
		t.Error("a database created after deletion kept the old epoch; browsers would keep their stale thumbnails")
	}
}

// A file name is bytes, and plenty of real ones are not UTF-8. Marshalling
// such a path into a JSON string replaces every offending byte with U+FFFD,
// which would restore an item pointing at a file that does not exist.
func TestItemPathSurvivesNonUTF8(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	raw := "/library/Shopping_In_K\xf6ln.mp4"
	plain := "/library/plain.mp4"
	if err := db.SaveItems([]Item{
		{ID: "a", Path: raw, Kind: "video", Size: 1, MTime: 2},
		{ID: "b", Path: plain, Kind: "video", Size: 3, MTime: 4},
	}, nil); err != nil {
		t.Fatal(err)
	}

	recs, err := db.Items()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Item{}
	for _, r := range recs {
		got[r.ID] = r
	}
	if len(got) != 2 {
		t.Fatalf("stored %d records, want 2", len(got))
	}
	if got["a"].Path != raw {
		t.Errorf("path came back %q, want the bytes that went in %q", got["a"].Path, raw)
	}
	if got["a"].PathBytes != nil {
		t.Errorf("PathBytes = %q, want it folded back into Path", got["a"].PathBytes)
	}
	// A path that needs nothing is stored as it always was.
	if got["b"].Path != plain || got["b"].PathBytes != nil {
		t.Errorf("plain record = %+v, want it untouched", got["b"])
	}
}
