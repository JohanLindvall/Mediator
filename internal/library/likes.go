package library

// The owner's verdict on each thing: liked, disliked, or neither.
//
// It travels exactly as the play count does (plays.go, ownerCounts) — kept
// by the state store, pushed into the library so a listing can sort on it
// under its own lock, stamped onto the copies a listing hands out — and it
// bumps the library's version for the same reason a play does: it happens
// once, by hand, and everything downstream is cached per version. Its
// generation is what the affinity cache (similar.go) compares against.

// SetLikesAll replaces everything the library knows about verdicts. Called
// once at startup with what the state store restored, before anything is
// serving, so it deliberately does not notify.
func (l *Library) SetLikesAll(all map[string]int) { l.likes.replace(all) }

// SetLike records the owner's verdict on one item — 1, -1, or 0 to withdraw
// it — and tells everyone: it changes what the popular orders show and what
// the collections sum.
func (l *Library) SetLike(id string, like int) {
	if l.likes.set(id, like) {
		l.notify()
	}
}

// likeOf reports the verdict on one item: 1, -1, or 0.
func (l *Library) likeOf(id string) int { return l.likes.get(id) }

// likesSnapshot copies the whole map, for the passes that would otherwise
// take the lock once per item.
func (l *Library) likesSnapshot() map[string]int {
	m, _ := l.likes.snapshot()
	return m
}

// popularity is the one number the popular orders sort collections on. The
// verdict outranks the count: a liked thing stands above anything merely
// played, however often, and a disliked one below anything untouched — the
// count decides only among equals. For a collection the verdict is the net
// one over what it holds, so it can be any size; forty bits are left for
// the plays, which no library will fill. Tracks have trackPopularity, which
// puts the resemblance to something judged between the two.
func popularity(like, plays int) int64 {
	return int64(like)<<40 + int64(plays)
}

// sumLikes is the net verdict over a collection's tracks, likes less
// dislikes: what the collections sort on beside their summed plays.
func sumLikes(tracks []*Item, likes map[string]int) int {
	n := 0
	for _, t := range tracks {
		n += likes[t.ID]
	}
	return n
}
