package library

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// tagged writes a small mp3 whose duration the native parser can read, so
// enrichment has something real to find.
func taggedMP3(t *testing.T, path string) {
	t.Helper()
	// ID3v2 header claiming a 100-byte tag, then a 128 kbps CBR frame; the
	// mp3 parser derives the duration from the remaining bytes.
	data := make([]byte, 110+16000)
	copy(data, "ID3")
	data[3], data[4] = 4, 0
	data[9] = 100
	data[110], data[111], data[112], data[113] = 0xFF, 0xFB, 0x90, 0x40
	write(t, path, string(data))
}

func TestEnrichNowFillsRequestedItemsOnly(t *testing.T) {
	dir := t.TempDir()
	taggedMP3(t, filepath.Join(dir, "wanted.mp3"))
	taggedMP3(t, filepath.Join(dir, "other.mp3"))
	l := quietLib(dir)
	l.Scan(nil)

	wanted := PathID(filepath.Join(dir, "wanted.mp3"))
	other := PathID(filepath.Join(dir, "other.mp3"))

	if !l.EnrichNow(context.Background(), []string{wanted}) {
		t.Fatal("EnrichNow reported nothing to do")
	}
	got, _ := l.Get(wanted)
	if got.Duration == 0 {
		t.Error("the requested item was not enriched")
	}
	rest, _ := l.Get(other)
	if rest.Duration != 0 {
		t.Error("an item that was not asked for got enriched")
	}

	// Nothing left to do for that item: a second call is a no-op, so a
	// panel that reloads repeatedly does not re-read the same files.
	if l.EnrichNow(context.Background(), []string{wanted}) {
		t.Error("EnrichNow repeated work for an already-enriched item")
	}
}

func TestEnrichNowIgnoresTheBusyGate(t *testing.T) {
	// Priority work is for what the user is looking at, so unlike the
	// background sweep it must not wait for playback to go quiet.
	dir := t.TempDir()
	taggedMP3(t, filepath.Join(dir, "song.mp3"))
	l := quietLib(dir)
	l.Scan(nil)
	stop := l.StartStream() // pretend something is playing
	defer stop()

	id := PathID(filepath.Join(dir, "song.mp3"))
	done := make(chan bool, 1)
	go func() { done <- l.EnrichNow(context.Background(), []string{id}) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EnrichNow blocked while a stream was active")
	}
	if it, _ := l.Get(id); it.Duration == 0 {
		t.Error("item was not enriched")
	}
}

func TestUnreadableFilesAreNotRetriedForever(t *testing.T) {
	// A file with no tags and no parseable duration must still be marked
	// examined, or every album open would read it again.
	dir := t.TempDir()
	write(t, filepath.Join(dir, "empty.mp3"), "not really an mp3")
	l := quietLib(dir)
	l.Scan(nil)
	id := PathID(filepath.Join(dir, "empty.mp3"))

	if !l.EnrichNow(context.Background(), []string{id}) {
		t.Fatal("first pass should attempt the file")
	}
	if l.EnrichNow(context.Background(), []string{id}) {
		t.Error("a file with no metadata was queued for a second read")
	}
}

func TestEnrichNowStopsAtDeadline(t *testing.T) {
	dir := t.TempDir()
	var ids []string
	for _, n := range []string{"a", "b", "c"} {
		p := filepath.Join(dir, n+".mp3")
		taggedMP3(t, p)
		ids = append(ids, PathID(p))
	}
	l := quietLib(dir)
	l.Scan(nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already expired
	l.EnrichNow(ctx, ids)
	// Nothing is required to have been read, but the call must return and
	// leave the library usable.
	if l.List(Query{}).Total != 3 {
		t.Fatal("library damaged by a cancelled priority pass")
	}
}

func TestEnrichedFlagSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "empty.mp3"), "not really an mp3")
	dbPath := filepath.Join(t.TempDir(), "media.db")

	db, err := openTestDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	l.EnrichNow(context.Background(), []string{PathID(filepath.Join(dir, "empty.mp3"))})
	flushNow(l, db)
	db.Close()

	db2, err := openTestDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	l2 := quietLib(dir)
	l2.SetMetaDB(db2)
	l2.LoadFromDB(db2)
	if l2.EnrichNow(context.Background(), []string{PathID(filepath.Join(dir, "empty.mp3"))}) {
		t.Error("a file examined in a previous run was queued again")
	}
}
