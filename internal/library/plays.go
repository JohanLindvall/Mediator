package library

// How often each thing has been played.
//
// Like the watch positions beside them, the counts themselves belong to the
// state store — they are the owner's data, not the file's — and what the
// library keeps is a copy pushed in (ownerCounts), because a listing sorts
// and filters under the index's own lock and must not reach across into
// another store to do it.
//
// Unlike a position, a play is **not** saved every few seconds: it happens
// once when something has genuinely started, which is a handful of times an
// hour of listening. So a play bumps the library's own version rather than a
// counter of its own. That is what keeps everything downstream honest with no
// further machinery — the album, artist and genre lists are cached per
// version and their totals are sums over what they hold, the endpoints that
// carry those lists are revalidated by a version ETag, and the broadcast loop
// refreshes the chips. A position could not do that, which is exactly why it
// has `watchVersion` instead.

// SetPlaysAll replaces everything the library knows about plays. Called once
// at startup with what the state store restored, before anything is serving,
// so it deliberately does not notify.
func (l *Library) SetPlaysAll(all map[string]int) { l.plays.replace(all) }

// SetPlays records a new count for one item, and tells everyone: a play
// changes what the popularity views show and what the collections sum.
func (l *Library) SetPlays(id string, n int) {
	if l.plays.set(id, n) {
		l.notify()
	}
}

// playsOf reports how many times one item has been played.
func (l *Library) playsOf(id string) int { return l.plays.get(id) }

// playsSnapshot copies the whole map, for the passes that would otherwise
// take the lock once per item.
func (l *Library) playsSnapshot() map[string]int {
	m, _ := l.plays.snapshot()
	return m
}

// playedTotal counts the items the index still holds that have been played
// at all, or judged, which is what the Popular chip shows.
//
// Cached per version. The maps hold only what has ever been played or
// judged, and both outlive the files they belong to until the next prune —
// so they are intersected with the index rather than measured on their own.
func (l *Library) playedTotal() int {
	version := l.Version()

	l.playedMu.Lock()
	if l.playedValid && l.playedVersion == version {
		n := l.playedCount
		l.playedMu.Unlock()
		return n
	}
	l.playedMu.Unlock()

	plays, _ := l.plays.snapshot()
	likes, _ := l.likes.snapshot()
	total := 0
	l.mu.RLock()
	for id := range plays {
		if _, ok := l.items[id]; ok {
			total++
		}
	}
	for id := range likes {
		if _, played := plays[id]; played {
			continue // counted above
		}
		if _, ok := l.items[id]; ok {
			total++
		}
	}
	l.mu.RUnlock()

	l.playedMu.Lock()
	l.playedValid, l.playedVersion, l.playedCount = true, version, total
	l.playedMu.Unlock()
	return total
}
