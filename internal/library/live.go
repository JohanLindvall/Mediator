package library

// LiveIDs is every id the index holds right now, for the stores that keep
// things by id and prune them by it — the state store's positions and
// verdicts, the blob database's caches. Nil, not empty, when the index is
// empty: an empty index means the roots are unreadable, not that the
// library emptied itself, and a caller given nil prunes nothing.
func (l *Library) LiveIDs() map[string]struct{} {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if len(l.items) == 0 {
		return nil
	}
	live := make(map[string]struct{}, len(l.items))
	for id := range l.items {
		live[id] = struct{}{}
	}
	return live
}
