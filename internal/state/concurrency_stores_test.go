package state

import "testing"

// A failed write must not put back a removal that something newer has
// already cancelled. Flush lets go of the lock before its transaction, so
// while the removal of an item is in flight the owner can start playing that
// item again: the recovery used to re-add the removal regardless, leaving the
// id in dirty and removed at once — which no other path here can produce —
// and the next flush then wrote the record and deleted it in the same
// transaction. What went was the owner's own data, which nothing regenerates.
func TestFailedFlushDoesNotResurrectASupersededRemoval(t *testing.T) {
	s := Load(testDB(t), quietLog())
	s.Set("x", 5, 100)
	s.Flush()
	s.Delete("x")

	// What Flush does next: it takes the removal, clears both maps, and lets
	// go of the lock before the transaction.
	remove := []string{"x"}
	s.mu.Lock()
	s.dirty = map[string]struct{}{}
	s.removed = map[string]struct{}{}
	s.mu.Unlock()

	// And while the write is in flight, the owner plays it again.
	s.Set("x", 7, 100)

	// The write then fails and Flush puts back what it could not save.
	s.restore(nil, remove)

	s.mu.Lock()
	_, gone := s.removed["x"]
	_, kept := s.dirty["x"]
	s.mu.Unlock()
	if gone {
		t.Error("a removal the owner has since cancelled was put back")
	}
	if !kept {
		t.Error("the newer position was not left to be written")
	}

	// And it really is written, rather than written and deleted in one go.
	s.Flush()
	back := Load(s.db, quietLog())
	p, ok := back.Get("x")
	if !ok {
		t.Fatal("the position the owner saved is not in the database")
	}
	if p.Time != 7 {
		t.Errorf("stored position %v, want the newer one", p.Time)
	}
}

// The other direction needs no guard, and this says so: a put whose record
// has been deleted meanwhile is skipped by Flush's own existence check, so
// the id reaches the write as a removal only, which is what the newer intent
// asked for.
func TestFailedFlushLetsADeleteOvertakeAPut(t *testing.T) {
	s := Load(testDB(t), quietLog())
	s.Set("x", 5, 100)
	s.Flush()

	put := map[string][]byte{"x": []byte("{}")}
	s.mu.Lock()
	s.dirty = map[string]struct{}{}
	s.removed = map[string]struct{}{}
	s.mu.Unlock()

	s.Delete("x") // the owner clears it while the write is in flight
	s.restore(put, nil)

	s.Flush()
	back := Load(s.db, quietLog())
	if _, ok := back.Get("x"); ok {
		t.Error("a position the owner cleared came back")
	}
}

// The last flush looks again, because Flush clears its maps before the
// commit: at shutdown there is no next flush to catch what landed during the
// transaction, so that write would simply be dropped.
func TestFinalFlushLooksAgain(t *testing.T) {
	rounds := 0
	left := 2
	flushUntilQuiet(func() {
		rounds++
		if left > 0 {
			left--
		}
	}, func() bool { return left > 0 }, finalFlushRounds)
	if rounds != 2 {
		t.Errorf("flushed %d times, want 2 — one for what was marked, one for what arrived during it", rounds)
	}
}

// And it is bounded: a database that refuses every write re-marks what it
// could not save, and an unbounded loop would hold the shutdown open for as
// long as the disk stayed broken.
func TestFinalFlushGivesUp(t *testing.T) {
	rounds := 0
	flushUntilQuiet(func() { rounds++ }, func() bool { return true }, finalFlushRounds)
	if rounds != finalFlushRounds {
		t.Errorf("flushed %d times, want %d", rounds, finalFlushRounds)
	}
}
