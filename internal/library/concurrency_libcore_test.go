package library

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// What these pin is lock *scope*: which work is allowed to run while which
// lock is held. A lock scope cannot be observed from outside, so each test
// takes the lock the slow work would be waiting on, sets the slow work
// going, and then asks whether the lock that must have been released still
// is. The asking is a sampling window rather than a rendezvous — there is no
// way to be *told* that a goroutine has reached a blocking acquire — but the
// property is one-sided: with the scopes right the sampled lock is never
// taken at all, and with them wrong it is taken within microseconds and held
// for as long as the test cares to look.
//
// The window is opened only once the watched goroutine is known to be parked
// inside the code under test (waitParkedIn). Without that these pass
// vacuously: the handshake below says the goroutine reached the line before
// the call, and a goroutine descheduled for the length of the window would
// leave the sampler looking at a lock nobody was ever going to take. A test
// that can quietly stop protecting is worse than no test, and the failure
// would be silent — so the one thing that can be read from outside, the
// runtime's own account of where every goroutine is, is read.

// waitParkedIn waits until some goroutine is blocked inside the named
// function. The stack dump is the only rendezvous available: a mutex tells
// nobody it has waiters, and a handshake before the call proves only that
// the goroutine reached the call.
func waitParkedIn(t *testing.T, fn string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n := runtime.Stack(buf, true)
		for n == len(buf) { // filled exactly means it was probably cut short
			buf = make([]byte, 2*len(buf))
			n = runtime.Stack(buf, true)
		}
		for _, g := range strings.Split(string(buf[:n]), "\n\ngoroutine ") {
			head, _, ok := strings.Cut(g, "\n")
			if !ok || !strings.Contains(g, fn) {
				continue
			}
			// "[running]" is this test's own dump; "[runnable]" is a
			// goroutine with work left to do. Anything else is parked, and
			// parked inside fn is what the window needs.
			if !strings.Contains(head, "[running]") && !strings.Contains(head, "[runnable]") {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("nothing ever blocked inside %s: the window would have proved nothing", fn)
}

// heldFree samples a mutex for a while and fails if it is ever held. The
// goroutine it is watching is blocked for the whole of the window by a lock
// the test holds, so anything taking this one is the scope under test.
func heldFree(t *testing.T, name string, try func() bool, unlock func()) {
	t.Helper()
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !try() {
			t.Fatalf("%s was held across work that must not run under it", name)
		}
		unlock()
		time.Sleep(time.Millisecond)
	}
}

// A caller holding an older version must be satisfied by a list built from a
// newer one. Under equality it rebuilt the whole thing and stamped the cache
// back to its own number, sending the next caller round again — which on the
// album build is seconds of work per caller that straddled a bump.
func TestGroupedCacheAcceptsANewerBuild(t *testing.T) {
	var c perVersion[int]
	builds := 0
	build := func() []*int {
		builds++
		n := builds
		return []*int{&n}
	}

	c.get(1, build)
	c.get(2, build)
	if builds != 2 {
		t.Fatalf("two versions asked for, %d builds", builds)
	}
	got := c.get(1, build)
	if builds != 2 {
		t.Fatalf("an older asker rebuilt a newer list: %d builds", builds)
	}
	if len(got) != 1 || *got[0] != 2 {
		t.Fatalf("served something other than the newest build: %v", got)
	}
	if c.total() != 1 {
		t.Fatalf("total %d, want 1", c.total())
	}
}

// A file unlinked and created again under the same name — the two halves of
// an atomic rename — lands in one flush window, and the delete used to
// follow the put inside one transaction: the item stayed in the index with
// no mirrored record, so the next start had to walk the disk to find it.
func TestAFileThatComesBackKeepsItsRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Harbour.Lights.S01E01.mkv")
	write(t, path, "video")
	dbPath := filepath.Join(t.TempDir(), "media.db")

	db, err := blob.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	flushNow(l, db)

	id := PathID(path)
	if n := l.removePath(path); n != 1 {
		t.Fatalf("removed %d items, want 1", n)
	}
	if changed, _, _ := l.upsert(path, KindVideo, 5, time.Unix(2, 0), fileKey{}, false); !changed {
		t.Fatal("the file coming back changed nothing")
	}
	l.flush(db)

	l.mu.RLock()
	_, stillRemoved := l.removed[id]
	l.mu.RUnlock()
	if stillRemoved {
		t.Fatal("an item that is in the index is still marked for deletion")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := blob.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	warm := quietLib(dir)
	if n := warm.LoadFromDB(db2); n != 1 {
		t.Fatalf("a warm start restored %d items, want 1", n)
	}
	if _, ok := warm.Get(id); !ok {
		t.Fatal("the record of a live item was deleted in the same tick it was written")
	}
}

// The narrowed counts read the grouped lists before taking their own lock,
// because asking for one can run a whole album build — playlists read off
// the disk — and every other narrowed count in the process is queued behind
// that lock. Only the albums were read early when this was written.
func TestNarrowedCountsWaitForAGroupedBuildWithTheirLockFree(t *testing.T) {
	l := libForCounts(t)

	// Stand in for a build already running elsewhere: the broadcast loop
	// refreshing the chips, or another request that asked first.
	l.artists.mu.Lock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.CountsFor(CountQuery{Search: "tribute"})
	}()
	waitParkedIn(t, "library.(*Library).CountsFor(")

	heldFree(t, "the counts lock", l.counts.mu.TryLock, l.counts.mu.Unlock)

	select {
	case <-done:
		t.Fatal("the count finished while the artist list was locked: it never asked for one")
	default:
	}
	l.artists.mu.Unlock()
	<-done
}

// A listing holds the one lock every listing in the process passes through.
// Counting is not part of what that lock protects — it walks the index and
// can ask for an album build — so it happens after the lock is given back.
func TestAListingCountsWithTheQueryLockFree(t *testing.T) {
	l := libForCounts(t)
	l.List(Query{Search: "tribute"}) // warm, so only the counting can block

	l.counts.mu.Lock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.List(Query{Search: "tribute"})
	}()
	waitParkedIn(t, "library.(*Library).List(")

	heldFree(t, "the listing lock", l.queryMu.TryLock, l.queryMu.Unlock)

	select {
	case <-done:
		t.Fatal("the listing finished while the counts were locked: it never counted")
	default:
	}
	l.counts.mu.Unlock()
	<-done
}

// Everything the index lock guards is a map read; the resemblance caches are
// lazy rebuilds of the whole library and must be asked for before it is
// taken. Get is the door every by-id request comes through, the analysis
// among them, and it used to rebuild them with the read lock held — which
// stops a waiting writer, and with it every reader queued behind.
func TestGetRebuildsTheResemblancesOutsideTheIndexLock(t *testing.T) {
	l := libForCounts(t)
	id := PathID("/library/Tribute Band - Covers/01 tribute.mp3")

	l.featMu.Lock() // the analysis publishing, or another rebuild in flight

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.Get(id)
	}()
	waitParkedIn(t, "library.(*Library).Get(")

	heldFree(t, "the index lock", l.mu.TryLock, l.mu.Unlock)

	select {
	case <-done:
		t.Fatal("the item came back while the features were locked: nothing asked for them")
	default:
	}
	l.featMu.Unlock()
	<-done
}

// The popular orders read the same caches, and buildQuery asked for them
// from inside the sort — under the read lock, for the length of a rebuild.
func TestPopularSortTakesItsSnapshotsOutsideTheIndexLock(t *testing.T) {
	l := libForCounts(t)

	l.featMu.Lock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.List(Query{Sort: "popular"})
	}()
	waitParkedIn(t, "library.(*Library).buildQuery(")

	heldFree(t, "the index lock", l.mu.TryLock, l.mu.Unlock)

	select {
	case <-done:
		t.Fatal("the listing came back while the features were locked: nothing asked for them")
	default:
	}
	l.featMu.Unlock()
	<-done
}

// The chips must never say a negative number of releases. The audiobook
// count and the album total are published by one build at two moments, so a
// reader can pair a fresh count of books with a stale total — and on the
// first build of a library that holds any, the stale total is nought.
func TestAlbumChipIsNeverNegative(t *testing.T) {
	l := libForCounts(t)
	l.spokenAlbums.Store(int32(l.albums.total() + 3))
	if c := l.Counts(); c.Albums < 0 {
		t.Fatalf("albums chip reads %d", c.Albums)
	}
}

// The listing cache is the same rule as the grouped ones: the version is
// read before its lock, so two listings whose reads straddle a bump ask
// about different numbers, and under equality the one holding the older
// number rebuilt the whole filtered, sorted library behind the one that had
// just built it and stamped the cache back to its own older number — which
// sent the next caller round again, and the next.
func TestListingCacheAcceptsANewerBuild(t *testing.T) {
	l := libForCounts(t)
	l.ensureFlags()
	q := Query{Search: "tribute"}

	newer := l.cachedQuery(q, 6)
	older := l.cachedQuery(q, 5)
	if older != newer {
		t.Fatal("an older asker rebuilt a listing that had just been built")
	}
	if l.lastQuery.version != 6 {
		t.Fatalf("the cache was stamped back to %d", l.lastQuery.version)
	}
	if again := l.cachedQuery(q, 6); again != newer {
		t.Fatal("the caller at the newer version had to build it a second time")
	}
}
