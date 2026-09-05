package library

import "sync"

// ownerCounts is one of the owner's per-item numbers — how often each thing
// has been played, what they thought of it — pushed into the library from
// the state store, because a listing sorts and filters under the index's
// own lock and must not reach across into another store to do it. Both
// are this one type: a map under its own lock, a generation that moves
// with every change so a cache built from it knows when it is stale, and
// the read patterns the passes need. What differs between the two is only
// when a change is announced, which the callers decide (plays.go, likes.go).
type ownerCounts struct {
	mu  sync.RWMutex
	m   map[string]int
	gen int64
}

// replace swaps in everything at once, dropping zeros: what startup does
// with what the state store restored, before anything is serving.
func (c *ownerCounts) replace(all map[string]int) {
	m := make(map[string]int, len(all))
	for id, n := range all {
		if n != 0 {
			m[id] = n
		}
	}
	c.mu.Lock()
	c.m = m
	c.gen++
	c.mu.Unlock()
}

// set records one item's number, zero forgetting it, and reports whether
// anything changed — the caller's cue to announce it.
func (c *ownerCounts) set(id string, n int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]int{}
	}
	if c.m[id] == n {
		return false
	}
	if n != 0 {
		c.m[id] = n
	} else {
		delete(c.m, id)
	}
	c.gen++
	return true
}

func (c *ownerCounts) get(id string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m[id]
}

// snapshot copies the map, for the passes that would otherwise take the
// lock once per item, and says which generation the copy is of.
func (c *ownerCounts) snapshot() (map[string]int, int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]int, len(c.m))
	for id, n := range c.m {
		out[id] = n
	}
	return out, c.gen
}

// generation is bumped by every change: what a cache compares against
// before it decides whether its copy is still good.
func (c *ownerCounts) generation() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.gen
}
