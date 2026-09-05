// Package state keeps playback positions — where the owner got to in each
// item — and how often each one has been played. They live in the blob database with everything else the server
// stores, so there is one file to back up and one file to delete for a clean
// slate. They are the owner's data rather than the file's, so nothing about
// them expires when a file changes.
package state

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// Position is the saved playback state for one media item: where the owner
// got to, and how many times they have played it.
//
// The two live in one record because they are the same kind of fact — what
// this owner has done with this file — with the same lifetime, the same
// absence of any stamp tying them to the file's contents, and the same
// pruning. A second store keyed by the same ids would be the arrangement
// this one exists to avoid.
type Position struct {
	Time     float64 `json:"t"` // seconds into the file
	Duration float64 `json:"d"` // known duration in seconds
	Updated  int64   `json:"u"` // unix millis of last update
	// Plays counts starts that got past the client's own floor — a few
	// seconds of real playback, not an open. Absent from records written
	// before it existed, which read back as nought and are none the worse.
	Plays int `json:"p,omitempty"`
	// Played is when it was last started, unix millis. It is what "recently
	// played" would sort on, and it is the honest thing to show beside a
	// count that says nothing about when.
	Played int64 `json:"lp,omitempty"`
	// Like is the owner's verdict: 1 liked, -1 disliked, 0 neither. In the
	// same record as the count for the same reason the count is here.
	Like int `json:"l,omitempty"`
}

// Store is a concurrent-safe set of positions, held in memory and flushed to
// the database on a debounce.
//
// In memory because both hot paths want it there: Get is consulted as
// playback starts, and All is served whole to the client. Debounced because
// the player saves every few seconds per viewer, and a transaction each time
// would be a commit and an fsync for a number nobody is waiting on.
type Store struct {
	db  *blob.DB // nil with -db off: positions then live only for this run
	log *slog.Logger

	mu        sync.Mutex
	positions map[string]Position
	dirty     map[string]struct{} // ids written since the last flush
	removed   map[string]struct{} // ids deleted since the last flush
}

// Load reads the stored positions. db may be nil, in which case positions are
// kept for the life of the process and not written down — there is nowhere to
// put them, and a JSON file beside the database would be exactly the second
// store this arrangement exists to avoid.
func Load(db *blob.DB, log *slog.Logger) *Store {
	s := &Store{
		db: db, log: log,
		positions: make(map[string]Position),
		dirty:     make(map[string]struct{}),
		removed:   make(map[string]struct{}),
	}
	if db == nil {
		log.Info("no database: playback positions will not be remembered after this run")
		return s
	}
	raw, err := db.GetPositions()
	if err != nil {
		log.Warn("playback positions unreadable, starting fresh", "err", err)
		return s
	}
	for id, v := range raw {
		var p Position
		if err := json.Unmarshal(v, &p); err != nil {
			continue // one unreadable row is not a reason to lose the rest
		}
		s.positions[id] = p
	}
	return s
}

// Get returns the position for an item.
func (s *Store) Get(id string) (Position, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.positions[id]
	return p, ok
}

// All returns a copy of every position.
func (s *Store) All() map[string]Position {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Position, len(s.positions))
	for k, v := range s.positions {
		out[k] = v
	}
	return out
}

// Set records a position.
func (s *Store) Set(id string, t, d float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Read, then write: the record carries the play count as well, and a
	// wholesale replacement here would reset it every few seconds for as
	// long as anything was playing.
	p := s.positions[id]
	p.Time, p.Duration, p.Updated = t, d, time.Now().UnixMilli()
	s.positions[id] = p
	s.dirty[id] = struct{}{}
	delete(s.removed, id)
}

// Play records one play and returns the new count.
//
// The position is deliberately untouched: a track played to the end and
// started again has a position that means nothing, and a film resumed
// half-way has a position that means everything. Counting is not the same
// question as where-was-I, and the caller of one is not the caller of the
// other.
func (s *Store) Play(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.positions[id]
	p.Plays++
	p.Played = time.Now().UnixMilli()
	s.positions[id] = p
	s.dirty[id] = struct{}{}
	delete(s.removed, id)
	return p.Plays
}

// Plays returns the play count of every item that has one.
func (s *Store) Plays() map[string]int { return s.collect(func(p Position) int { return p.Plays }) }

// collect is one number out of every record that has it, for the library
// to be handed at startup: the counts, the verdicts.
func (s *Store) collect(of func(Position) int) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.positions))
	for id, p := range s.positions {
		if n := of(p); n != 0 {
			out[id] = n
		}
	}
	return out
}

// Like records the owner's verdict on an item — 1, -1, or 0 to withdraw it
// — and returns what is now stored. The position and the count are left as
// they are: a verdict is a third fact about the same thing. A record left
// with nothing in it goes, rather than lingering empty for the life of the
// database.
func (s *Store) Like(id string, like int) int {
	like = max(-1, min(1, like))
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.positions[id]
	p.Like = like
	if p.Like == 0 && p.Plays == 0 && p.Updated == 0 {
		s.drop(id)
		return 0
	}
	s.positions[id] = p
	s.dirty[id] = struct{}{}
	delete(s.removed, id)
	return p.Like
}

// Likes returns the verdict on every item that has one.
func (s *Store) Likes() map[string]int { return s.collect(func(p Position) int { return p.Like }) }

// Delete removes a saved position.
func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drop(id)
}

// drop forgets a record and marks it for removal from the database, if
// there was one. Caller holds the lock.
func (s *Store) drop(id string) {
	if _, ok := s.positions[id]; ok {
		delete(s.positions, id)
		delete(s.dirty, id)
		s.removed[id] = struct{}{}
	}
}

// Prune drops the positions of items that are no longer indexed and reports
// how many went. Call it only after a completed scan: while the walk is
// running — or with a root that failed to mount — most of the library is
// missing from live, and every position for it would look abandoned. An
// empty live set is refused for the same reason.
func (s *Store) Prune(live map[string]struct{}) int {
	if len(live) == 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id := range s.positions {
		if _, ok := live[id]; !ok {
			delete(s.positions, id)
			delete(s.dirty, id)
			s.removed[id] = struct{}{}
			n++
		}
	}
	return n
}

// Run flushes periodically until ctx is done, then flushes one final time.
func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.Flush()
			return
		case <-t.C:
			s.Flush()
		}
	}
}

// Flush writes everything that changed since the last call, in one
// transaction.
func (s *Store) Flush() {
	if s.db == nil {
		return
	}
	s.mu.Lock()
	if len(s.dirty) == 0 && len(s.removed) == 0 {
		s.mu.Unlock()
		return
	}
	put := make(map[string][]byte, len(s.dirty))
	for id := range s.dirty {
		p, ok := s.positions[id]
		if !ok {
			continue
		}
		v, err := json.Marshal(p)
		if err != nil {
			continue
		}
		put[id] = v
	}
	remove := make([]string, 0, len(s.removed))
	for id := range s.removed {
		remove = append(remove, id)
	}
	// Cleared before the write, not after: a position saved while the
	// transaction is in flight belongs to the next flush, and holding the
	// lock across the commit would stall every player that reports one.
	s.dirty = make(map[string]struct{})
	s.removed = make(map[string]struct{})
	s.mu.Unlock()

	if err := s.db.PutPositions(put, remove); err != nil {
		s.log.Error("saving playback positions failed", "err", err)
		// Put them back: the next flush tries again rather than losing them.
		s.mu.Lock()
		for id := range put {
			s.dirty[id] = struct{}{}
		}
		for _, id := range remove {
			s.removed[id] = struct{}{}
		}
		s.mu.Unlock()
	}
}
