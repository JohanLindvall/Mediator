package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// openDB is a blob database in a directory of the test's own.
func openDB(t *testing.T) *blob.DB {
	t.Helper()
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// A judgement the owner has withdrawn is spelled as a deletion — from the
// map and from the database alike — so a load that finished before the
// withdrawal is holding a record that is no longer true. Merged key by key
// it reads the absent key as "never set" and writes the old value back,
// which is how an item that had just been un-hidden went on being hidden for
// the life of the process while the database said the opposite.
func TestLoadFlagsDropsASnapshotOlderThanAWithdrawal(t *testing.T) {
	db := openDB(t)
	l, ids := flagLib()
	l.SetMetaDB(db)
	id := ids[0]

	l.SetFlags([]string{id}, boolp(true), nil, nil, nil)
	l.SetFlags([]string{id}, boolp(false), nil, nil, nil) // withdrawn
	if l.Flags(id).Hidden {
		t.Fatal("the withdrawal did not take")
	}

	// What a loader that started before the withdrawal is still holding.
	if err := db.SaveFlags(map[string]blob.Flags{id: {Hidden: true}}); err != nil {
		t.Fatal(err)
	}
	l.LoadFlags(db)

	if l.Flags(id).Hidden {
		t.Fatal("a load that arrived after the withdrawal put the judgement back")
	}
}

// The decision and the writing-down are one step: two presses on one item
// must not reach the database in the opposite order to the one they were
// settled in, or memory takes one press and the database the other — and the
// database is what comes back at the next restart.
func TestSetFlagsSettlesAndWritesUnderOneLock(t *testing.T) {
	db := openDB(t)
	l, ids := flagLib()
	l.SetMetaDB(db)
	id := ids[0]
	l.Flags(id) // the lazy load, out of the way

	rot := func(n int) *int { return &n }
	l.SetFlags([]string{id}, nil, nil, nil, rot(1))

	flagStore.Lock()
	done := make(chan struct{})
	go func() {
		l.SetFlags([]string{id}, nil, nil, nil, rot(2))
		close(done)
	}()
	select {
	case <-done:
		flagStore.Unlock()
		t.Fatal("a flag write settled its value with nothing holding the order")
	case <-time.After(50 * time.Millisecond):
	}
	if got := l.Flags(id).Rotation; got != 1 {
		flagStore.Unlock()
		t.Fatalf("memory moved ahead of the write: rotation %d", got)
	}
	flagStore.Unlock()
	<-done

	if got := l.Flags(id).Rotation; got != 2 {
		t.Fatalf("rotation in memory = %d, want 2", got)
	}
	f, ok := db.GetFlags(id)
	if !ok || f.Rotation != 2 {
		t.Fatalf("the database holds %+v (present %v), not what memory settled on", f, ok)
	}
}

// A page of copies is stamped from a stamper, and the flags it stamps from
// are loaded on first use — so the load belongs to the stamper itself. The
// album sheet is the producer that had no load of its own, and reached first
// on a cold process it answered that nothing was hidden, favourite or turned.
func TestStamperEstablishesTheFlags(t *testing.T) {
	db := openDB(t)
	l, ids := flagLib()
	id := ids[0]
	if err := db.SaveFlags(map[string]blob.Flags{id: {Hidden: true, Rotation: 3}}); err != nil {
		t.Fatal(err)
	}
	l.SetMetaDB(db)

	st := l.stamper()
	l.mu.RLock()
	got := st.stamp(*l.items[id])
	l.mu.RUnlock()
	if !got.Hidden || got.Rotation != 3 {
		t.Fatalf("a page stamped before anything else had asked carried %+v", Flags{
			Hidden: got.Hidden, Favourite: got.Favourite, Rotation: got.Rotation, NoCrop: got.NoCrop,
		})
	}
}

// The memo is stamped with the version the walk saw, never with the one the
// caller asked about: those are two acquisitions apart, and an answer
// published under the older stamp is handed to every other request still
// holding it — a hidden set counted over one index subtracted from totals
// taken of another.
func TestHiddenCountsAreStampedWithTheVersionTheyCounted(t *testing.T) {
	l, ids := flagLib()
	l.SetFlags([]string{ids[0]}, boolp(true), nil, nil, nil)
	at := l.GroupVersion()

	if c := l.hiddenCounts(at - 1); c.Video != 1 {
		t.Fatalf("hidden videos = %d, want 1", c.Video)
	}
	if v, valid := hiddenStamp(l); !valid || v != at {
		t.Fatalf("memo published under version %d (valid %v), counted at %d", v, valid, at)
	}

	// And the stamp never goes backwards under a caller running behind.
	l.SetFlags([]string{ids[1]}, boolp(true), nil, nil, nil)
	now := l.GroupVersion()
	if c := l.hiddenCounts(now); c.Video != 2 {
		t.Fatalf("hidden videos = %d, want 2", c.Video)
	}
	if c := l.hiddenCounts(at - 1); c.Video != 2 {
		t.Fatalf("a caller behind was answered %d hidden videos, want 2", c.Video)
	}
	if v, _ := hiddenStamp(l); v != now {
		t.Fatalf("the stamp went backwards to %d, from %d", v, now)
	}
}

func hiddenStamp(l *Library) (int64, bool) {
	l.hiddenMu.Lock()
	defer l.hiddenMu.Unlock()
	return l.hiddenVersion, l.hiddenValid
}

// One id must never reach bolt as a put and a deletion in the same
// transaction: the deletions are applied last, so the record written for a
// live file is deleted a moment after it is written, and both maps are
// cleared by the flush — nothing writes it again.
func TestFlushKeepsTheRecordItIsAlsoWriting(t *testing.T) {
	dir := t.TempDir()
	pic := filepath.Join(dir, "Harbour.jpg")
	write(t, pic, "image")
	db := openDB(t)
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	flushNow(l, db)
	id := PathID(pic)
	if !stored(t, db, id) {
		t.Fatal("the record was never written")
	}

	// Away and back inside one tick: a rename undone, a file replaced in
	// place, a reconciliation that ran while the disk was slow.
	if err := os.Remove(pic); err != nil {
		t.Fatal(err)
	}
	l.Remove(pic)
	write(t, pic, "image")
	l.AddFile(pic)
	flushNow(l, db)

	if !stored(t, db, id) {
		t.Fatal("the flush deleted the record it had just written for a live file")
	}
}

// PruneDB works from a list of the live ids taken before the transaction,
// and nothing orders that against the watcher: a file indexed in between is
// not on the list, so a record written for it in that window is deleted
// while the item is live in memory and no longer dirty.
func TestPruneMarksWhatArrivedWhileItRan(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "Harbour.jpg")
	write(t, old, "image")
	db := openDB(t)
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	flushNow(l, db)

	// The list the prune works from, taken before the arrival.
	l.mu.RLock()
	live := make(map[string]struct{}, len(l.items))
	for id := range l.items {
		live[id] = struct{}{}
	}
	l.mu.RUnlock()

	late := filepath.Join(dir, "Lantern.jpg")
	write(t, late, "image")
	l.AddFile(late)
	lateID := PathID(late)
	flushNow(l, db) // its record lands inside the window

	if n, err := db.Prune(live); err != nil {
		t.Fatal(err)
	} else if n == 0 {
		t.Fatal("the prune deleted nothing, so there is nothing to repair")
	}
	l.remarkAfterPrune(live)
	flushNow(l, db)

	if !stored(t, db, lateID) {
		t.Fatal("a file the index holds has no record: the prune took it and nothing wrote it again")
	}
}

func stored(t *testing.T, db *blob.DB, id string) bool {
	t.Helper()
	recs, err := db.Items()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.ID == id {
			return true
		}
	}
	return false
}
