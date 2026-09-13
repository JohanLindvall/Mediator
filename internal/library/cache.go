package library

import (
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// The grouped views — albums, artists, genres, shows — are each one list
// rebuilt when the library changes and served as it is until then. Four
// copies of that arrangement had grown four sets of fields and four copies
// of the same locking; this is the one arrangement they share.

// perVersion is a list cached against the library's version, with the count
// its last build produced beside it for the chips: Counts is O(1) and must
// never build anything, so it reads what the last build left.
type perVersion[T any] struct {
	mu      sync.Mutex
	items   []*T
	version int64
	count   atomic.Int64
}

// get returns the list for this version, building it under the cache's own
// lock when the version has moved. The version is read by the caller before
// the lock is taken — reading it takes the index's own lock, and the order
// is this lock, then whatever locks the build takes beneath it — and the
// build may itself ask another cache, which is how the artists and genres
// are grouped from the albums.
//
// A cache newer than the asker satisfies it, and that is what the test has
// to say. The version is read before the lock, so two callers whose reads
// straddle a bump arrive here asking about different versions — and under
// equality the one holding the older number rebuilt the whole list behind
// the one that had just built it, stamped the cache back to its own older
// number, and so sent the next caller round again. Measured on the busiest
// build there is, that is up to one full rebuild per caller that straddled
// the bump, each blocking the others on this lock: precisely the repeated
// rebuild the group version exists to stop while a disk is being written
// to. It is sound because a build always reads the live index, so what is
// stored is never older than the number stamped on it — the cache can be
// under-labelled, never over-labelled.
func (c *perVersion[T]) get(version int64, build func() []*T) []*T {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items != nil && c.version >= version {
		return c.items
	}
	c.items = build()
	c.version = version
	c.count.Store(int64(len(c.items)))
	return c.items
}

// invalidate forgets the list, so the next ask rebuilds it whatever the
// version says. For tests that want to watch a rebuild.
func (c *perVersion[T]) invalidate() {
	c.mu.Lock()
	c.items = nil
	c.mu.Unlock()
}

// total is what the last build counted.
func (c *perVersion[T]) total() int { return int(c.count.Load()) }

// orderBy sorts a grouped view by one key, the way every one of them sorts.
//
// A thing that carries the value beats one that does not, whichever way the
// sort runs: tags are missing often enough that the other rule — untagged
// first ascending, last descending — turns half the orders into a screenful
// of blanks, which is not what the sort was asking for either way round.
// Equal by the chosen key, the name decides, which is the order a listing is
// readable in, and then the id so paging stays stable. desc reverses the
// whole of that, as it always has.
func orderBy[T any](out []T, desc bool, has func(T) bool, key func(a, b T) int, name, id func(T) string) {
	slices.SortStableFunc(out, func(a, b T) int {
		if ha, hb := has(a), has(b); ha != hb {
			if ha {
				return -1
			}
			return 1
		}
		c := key(a, b)
		if c == 0 {
			c = strings.Compare(name(a), name(b))
		}
		if c == 0 {
			c = strings.Compare(id(a), id(b))
		}
		if desc {
			return -c
		}
		return c
	})
}

// knownLength is the one presence rule the collections share beyond the
// albums' own: a total playing time is left at zero unless every part of it
// was measured, so ordering by it is "known" against "not known" first.
func knownLength(sortKey string, duration int64) bool {
	return sortKey != "duration" || duration > 0
}

// byID orders a built list so two builds of one library cannot disagree.
func byID[T any](out []*T, id func(*T) string) {
	slices.SortFunc(out, func(a, b *T) int { return strings.Compare(id(a), id(b)) })
}
