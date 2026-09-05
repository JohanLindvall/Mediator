package state

import (
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

func testDB(t *testing.T) *blob.DB {
	t.Helper()
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testStore(t *testing.T) *Store {
	t.Helper()
	s := Load(testDB(t), quietLog())
	s.Set("a", 1, 10)
	s.Set("b", 2, 20)
	s.Set("c", 3, 30)
	return s
}

func TestPrune(t *testing.T) {
	cases := []struct {
		name    string
		live    []string
		dropped int
		left    []string
	}{
		{"forgets what is no longer indexed", []string{"a"}, 2, []string{"a"}},
		{"keeps every live item", []string{"a", "b", "c"}, 0, []string{"a", "b", "c"}},
		{"ignores ids it has no position for", []string{"a", "b", "c", "d"}, 0, []string{"a", "b", "c"}},
		// An index that reports nothing is a scan in progress or a root that
		// failed to mount, never a library that emptied itself.
		{"refuses an empty index", nil, 0, []string{"a", "b", "c"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := testStore(t)
			// A verdict and a count live in the same record and go with it.
			s.Like("b", 1)
			s.Play("c")
			live := make(map[string]struct{}, len(c.live))
			for _, id := range c.live {
				live[id] = struct{}{}
			}
			if n := s.Prune(live); n != c.dropped {
				t.Fatalf("pruned %d, want %d", n, c.dropped)
			}
			var left []string
			for id := range s.All() {
				left = append(left, id)
			}
			slices.Sort(left)
			if !slices.Equal(left, c.left) {
				t.Fatalf("left %v, want %v", left, c.left)
			}
			if _, kept := s.Likes()["b"]; kept != slices.Contains(c.left, "b") {
				t.Errorf("the verdict on b: kept %v, want %v", kept, slices.Contains(c.left, "b"))
			}
			if _, kept := s.Plays()["c"]; kept != slices.Contains(c.left, "c") {
				t.Errorf("the count on c: kept %v, want %v", kept, slices.Contains(c.left, "c"))
			}
		})
	}
}

// Pruning has to reach the database: the whole point is that the positions do
// not come back on the next start.
func TestPruneIsFlushed(t *testing.T) {
	db := testDB(t)
	s := Load(db, quietLog())
	s.Set("a", 1, 10)
	s.Set("b", 2, 20)
	s.Flush()

	s.Prune(map[string]struct{}{"a": {}})
	s.Flush()

	reloaded := Load(db, quietLog())
	if _, ok := reloaded.Get("a"); !ok {
		t.Fatal("live position lost")
	}
	if _, ok := reloaded.Get("b"); ok {
		t.Fatal("pruned position came back")
	}
}

// Positions belong in the database with everything else, so a restart finds
// them there — and only there.
func TestPositionsSurviveInTheDatabase(t *testing.T) {
	db := testDB(t)
	s := Load(db, quietLog())
	s.Set("film", 123.5, 4000)
	s.Flush()

	reloaded := Load(db, quietLog())
	p, ok := reloaded.Get("film")
	if !ok {
		t.Fatal("position did not survive")
	}
	if p.Time != 123.5 || p.Duration != 4000 {
		t.Fatalf("position came back as %+v", p)
	}
	if p.Updated == 0 {
		t.Error("no timestamp recorded")
	}
}

// A deletion has to reach the database too, or the position returns from the
// dead on the next start.
func TestDeleteIsFlushed(t *testing.T) {
	db := testDB(t)
	s := Load(db, quietLog())
	s.Set("gone", 5, 50)
	s.Flush()
	s.Delete("gone")
	s.Flush()

	if _, ok := Load(db, quietLog()).Get("gone"); ok {
		t.Fatal("a deleted position came back")
	}
}

// With no database there is nowhere to put them, and inventing a second store
// beside it is the thing this arrangement exists to avoid. They last the run.
func TestWithoutADatabasePositionsAreMemoryOnly(t *testing.T) {
	s := Load(nil, quietLog())
	s.Set("a", 1, 10)
	if _, ok := s.Get("a"); !ok {
		t.Fatal("position not held in memory")
	}
	s.Flush() // must not panic, must write nothing
	if _, ok := Load(nil, quietLog()).Get("a"); ok {
		t.Fatal("a position outlived a run that had nowhere to store it")
	}
}

// Only what changed since the last flush is written, so a debounced save does
// not rewrite the whole store.
func TestFlushWritesOnlyWhatChanged(t *testing.T) {
	db := testDB(t)
	s := Load(db, quietLog())
	s.Set("a", 1, 10)
	s.Flush()

	s.mu.Lock()
	dirty, removed := len(s.dirty), len(s.removed)
	s.mu.Unlock()
	if dirty != 0 || removed != 0 {
		t.Fatalf("after a flush: %d dirty, %d removed; want none", dirty, removed)
	}
	s.Set("b", 2, 20)
	s.mu.Lock()
	dirty = len(s.dirty)
	s.mu.Unlock()
	if dirty != 1 {
		t.Fatalf("one new position marked %d entries dirty", dirty)
	}
}

// A verdict is a third fact in the record beside the position and the
// count: recording one leaves the others alone, the count moving leaves it
// alone, it survives a flush, and withdrawn with nothing beside it the
// record goes rather than lingering empty.
func TestLikeIsRememberedBesideTheCount(t *testing.T) {
	db := testDB(t)
	s := Load(db, quietLog())
	s.Set("a", 5, 10)
	s.Play("a")
	if got := s.Like("a", 1); got != 1 {
		t.Fatalf("Like = %d, want 1", got)
	}
	if got := s.Like("b", 7); got != 1 {
		t.Errorf("a verdict is clamped to one: got %d", got)
	}
	if p, _ := s.Get("a"); p.Plays != 1 || p.Time != 5 || p.Like != 1 {
		t.Errorf("record = %+v, want the position, one play and the like", p)
	}
	s.Play("a")
	if p, _ := s.Get("a"); p.Like != 1 || p.Plays != 2 {
		t.Errorf("after another play the record = %+v", p)
	}
	s.Flush()
	again := Load(db, quietLog())
	if got := again.Likes(); len(got) != 2 || got["a"] != 1 || got["b"] != 1 {
		t.Errorf("verdicts after a restart = %v", got)
	}
	again.Like("b", 0)
	if _, ok := again.Get("b"); ok {
		t.Error("an empty record was kept after its verdict was withdrawn")
	}
	if _, ok := again.Get("a"); !ok {
		t.Error("withdrawing one verdict lost another record")
	}
}
