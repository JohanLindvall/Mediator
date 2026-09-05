package library

import "sync"

// How far things have been watched.
//
// The positions themselves belong to the player and live in the state store,
// which is the server's; what the library keeps is the one bit the listing
// needs — started, finished, or neither. It is pushed in rather than looked
// up, because a listing filters under the index's own lock and must not be
// reaching across into another store to do it.
//
// The rule matches the marks on the grid exactly, and has to: a chip that
// counted one thing while the tiles showed another would be worse than no
// chip. Under five seconds is not a start, and past `WatchedFraction` is
// finished — which is also the point at which the player stops offering to
// resume, so "watched" means the same word everywhere.

// WatchState is what has happened to an item, as far as anyone can tell from
// where it was left.
type WatchState uint8

const (
	WatchNone    WatchState = iota // never opened, or barely
	WatchStarted                   // begun and not finished
	WatchDone                      // watched to the end
)

const (
	// watchFloorSec is how far in it has to have got to count as begun.
	watchFloorSec = 5.0
	// WatchedFraction is how much of a file counts as all of it. Credits
	// run, players stop short, and nobody wants a film they finished last
	// night sitting in "in progress" for the sake of forty seconds.
	WatchedFraction = 0.96
)

// Watch is one saved position: where it was left, and how long the file was
// when that was written.
type Watch struct {
	Pos float64
	Len float64
}

// State reduces a saved position to what the listing asks about.
func (w Watch) State() WatchState {
	if w.Len <= 0 || w.Pos < watchFloorSec {
		return WatchNone
	}
	if w.Pos/w.Len > WatchedFraction {
		return WatchDone
	}
	return WatchStarted
}

// SetWatchAll replaces everything the library knows about watching. Called
// once at startup with what the state store restored.
func (l *Library) SetWatchAll(all map[string]Watch) {
	states := make(map[string]WatchState, len(all))
	for id, w := range all {
		if st := w.State(); st != WatchNone {
			states[id] = st
		}
	}
	l.watchMu.Lock()
	l.watch = states
	l.watchVer++
	l.watchMu.Unlock()
}

// SetWatch records where one item was left, which is what happens every few
// seconds while something is playing — so it costs one map write and a
// counter, and never a pass over anything.
func (l *Library) SetWatch(id string, w Watch) {
	st := w.State()
	l.watchMu.Lock()
	defer l.watchMu.Unlock()
	if l.watch == nil {
		l.watch = map[string]WatchState{}
	}
	if l.watch[id] == st {
		return // nothing the listing would notice
	}
	if st == WatchNone {
		delete(l.watch, id)
	} else {
		l.watch[id] = st
	}
	l.watchVer++
}

// watchStateOf reports what is known about one item.
func (l *Library) watchStateOf(id string) WatchState {
	l.watchMu.RLock()
	defer l.watchMu.RUnlock()
	return l.watch[id]
}

// watchVersion is the counter that tells a cached listing its filter may
// have gone stale. It is separate from the library's own version because a
// position saved every few seconds must not make every client refetch the
// whole library — it only means something to a query that filters on it.
func (l *Library) watchVersion() int64 {
	l.watchMu.RLock()
	defer l.watchMu.RUnlock()
	return l.watchVer
}

// keepWatched reports whether an item passes a watch filter.
func keepWatched(st WatchState, want string) bool {
	switch want {
	case "started":
		return st == WatchStarted
	case "done":
		return st == WatchDone
	}
	return true
}

// watchSnapshot copies the whole map, for the passes that would otherwise
// take the lock once per item.
func (l *Library) watchSnapshot() map[string]WatchState {
	l.watchMu.RLock()
	defer l.watchMu.RUnlock()
	out := make(map[string]WatchState, len(l.watch))
	for id, st := range l.watch {
		out[id] = st
	}
	return out
}

// watchTotals counts what the two chips show: items the index still holds
// that have been started, and ones watched through. Cached per (version,
// watchVer) — the map is small, being only what has ever been played, but
// this is asked for on every listing.
func (l *Library) watchTotals() (started, done int) {
	version := l.Version()
	watchVer := l.watchVersion()

	l.watchMu.Lock()
	if l.totalsValid && l.totalsVersion == version && l.totalsWatchVer == watchVer {
		started, done = l.totalsStarted, l.totalsDone
		l.watchMu.Unlock()
		return started, done
	}
	states := make(map[string]WatchState, len(l.watch))
	for id, st := range l.watch {
		states[id] = st
	}
	l.watchMu.Unlock()

	// Positions outlive the files they belong to until the next prune, so
	// the index has the last word on what is still there.
	l.mu.RLock()
	for id, st := range states {
		if _, ok := l.items[id]; !ok {
			continue
		}
		switch st {
		case WatchStarted:
			started++
		case WatchDone:
			done++
		}
	}
	l.mu.RUnlock()

	l.watchMu.Lock()
	l.totalsValid, l.totalsVersion, l.totalsWatchVer = true, version, watchVer
	l.totalsStarted, l.totalsDone = started, done
	l.watchMu.Unlock()
	return started, done
}

// watchFields is the part of Library this file owns.
type watchFields struct {
	watchMu  sync.RWMutex
	watch    map[string]WatchState
	watchVer int64

	totalsValid    bool
	totalsVersion  int64
	totalsWatchVer int64
	totalsStarted  int
	totalsDone     int
}
