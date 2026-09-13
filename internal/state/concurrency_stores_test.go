package state

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"
)

// A write the store asked the database for, remembered as the ids it carried
// rather than as bytes: which ids were put and which were deleted is the
// whole of what these tests are about.
type write struct {
	put    []string
	remove []string
}

// fakeDB is a database that reports what it was asked to write, can be held
// part-way through one write so the test can do what the owner does while a
// commit is in flight, and can be told to fail. A working database cannot
// reach any of that, which is why the store writes through an interface.
type fakeDB struct {
	mu      sync.Mutex
	writes  []write
	fail    error         // returned by every write that is not held
	entered chan struct{} // signalled as a held write begins
	release chan error    // what the held write returns, once the test says so
}

// hold arranges for the *next* write to stop inside PutPositions until the
// test releases it, and hands back the two ends. One write only: what follows
// the held one is what these tests are checking, so it must not block as
// well — which is also why the ends are returned rather than read back off
// the struct, the write itself having cleared them by then.
func (f *fakeDB) hold() (entered <-chan struct{}, release chan<- error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in, out := make(chan struct{}), make(chan error)
	f.entered, f.release = in, out
	return in, out
}

func (f *fakeDB) PutPositions(put map[string][]byte, remove []string) error {
	f.mu.Lock()
	f.writes = append(f.writes, write{
		put:    slices.Sorted(maps.Keys(put)),
		remove: slices.Sorted(slices.Values(remove)),
	})
	entered, release, fail := f.entered, f.release, f.fail
	f.entered, f.release = nil, nil
	f.mu.Unlock()
	if entered == nil {
		return fail
	}
	entered <- struct{}{}
	return <-release
}

func (f *fakeDB) done() []write {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes)
}

func newStore(db writer) *Store {
	return &Store{
		db: db, log: quietLog(),
		positions: make(map[string]Position),
		dirty:     make(map[string]struct{}),
		removed:   make(map[string]struct{}),
	}
}

// A failed write must not put back a removal that something newer has
// already cancelled. Flush lets go of the lock before its transaction, so
// while the removal of an item is in flight the owner can start playing that
// item again: the recovery used to re-add the removal regardless, leaving the
// id in dirty and removed at once — which no other path here can produce —
// and the next flush then asked the database to write the record and delete
// it in the same transaction. What went was the owner's own data, which
// nothing regenerates.
func TestFailedFlushDoesNotResurrectASupersededRemoval(t *testing.T) {
	db := &fakeDB{}
	s := newStore(db)
	s.Set("x", 5, 100)
	s.Flush()
	s.Delete("x")

	entered, release := db.hold()
	flushed := make(chan struct{})
	go func() { defer close(flushed); s.Flush() }()

	<-entered // the removal is in flight and both maps are already clear
	s.Set("x", 7, 100)
	release <- errors.New("the disk went away")
	<-flushed

	s.Flush()
	last := lastWrite(t, db)
	if len(last.remove) != 0 {
		t.Errorf("the last write deletes %v; the owner cancelled that removal by playing it again", last.remove)
	}
	if !slices.Contains(last.put, "x") {
		t.Errorf("the last write puts %v; the newer position was never saved", last.put)
	}
	if p, ok := s.Get("x"); !ok || p.Time != 7 {
		t.Errorf("the store holds %v, %v; want the newer position", p, ok)
	}
}

// The other direction needs no guard, and this is what makes that true: a put
// whose record has been deleted meanwhile is dropped by Flush's own existence
// check, so the id reaches the database as a removal only. Take that check
// away and the same transaction would carry the id as both, which is how a
// position the owner cleared comes back.
func TestFailedFlushLetsADeleteOvertakeAPut(t *testing.T) {
	db := &fakeDB{}
	s := newStore(db)
	s.Set("x", 5, 100)
	s.Flush()
	s.Set("x", 6, 100)

	entered, release := db.hold()
	flushed := make(chan struct{})
	go func() { defer close(flushed); s.Flush() }()

	<-entered
	s.Delete("x") // the owner clears it while the write is in flight
	release <- errors.New("the disk went away")
	<-flushed

	s.Flush()
	last := lastWrite(t, db)
	if !slices.Equal(last.remove, []string{"x"}) {
		t.Errorf("the last write deletes %v, want [x]", last.remove)
	}
	if len(last.put) != 0 {
		t.Errorf("the last write puts %v; the owner cleared that position", last.put)
	}
}

// The last flush looks again, and it is Run that has to ask it to. Flush
// clears its maps before the commit on purpose — a position saved while the
// transaction is in flight belongs to the next flush — and at shutdown there
// is no next flush, so that write was simply dropped. This is the shape of a
// real shutdown: something is playing, the signal lands, and the position of
// the last few seconds is saved while the final commit is under way.
func TestRunFlushesWhatArrivedDuringItsLastCommit(t *testing.T) {
	db := &fakeDB{}
	s := newStore(db)
	s.Set("x", 5, 100)
	entered, release := db.hold()

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); s.Run(ctx) }()
	cancel()

	<-entered
	s.Set("y", 9, 100) // the player reports where it got to
	release <- nil
	waitFor(t, stopped)

	writes := db.done()
	if len(writes) != 2 {
		t.Fatalf("the last flush wrote %d times, want 2 — one for what was marked, one for what arrived during it", len(writes))
	}
	if !slices.Contains(writes[1].put, "y") {
		t.Errorf("the second write puts %v; the position saved during the commit was dropped", writes[1].put)
	}
}

// And it is bounded. A database refusing every write re-marks what it could
// not save, so an unbounded look-again would hold the shutdown open for as
// long as the disk stayed broken — and the process would then be killed
// rather than stopped, which loses more than the write it was waiting on.
func TestRunsLastFlushGivesUpOnABrokenDatabase(t *testing.T) {
	db := &fakeDB{fail: errors.New("the disk went away")}
	s := newStore(db)
	s.Set("x", 5, 100)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := make(chan struct{})
	go func() { defer close(stopped); s.Run(ctx) }()
	waitFor(t, stopped)

	if n := len(db.done()); n != finalFlushRounds {
		t.Errorf("the last flush tried %d times, want %d", n, finalFlushRounds)
	}
}

func lastWrite(t *testing.T, db *fakeDB) write {
	t.Helper()
	writes := db.done()
	if len(writes) == 0 {
		t.Fatal("nothing was written at all")
	}
	return writes[len(writes)-1]
}

// waitFor gives a goroutine a moment to finish rather than hanging the whole
// run: an unbounded final flush would otherwise show up as a test timeout
// with nothing said about which test.
func waitFor(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the store's loop did not stop")
	}
}
