package library

import (
	"github.com/JohanLindvall/Mediator/internal/blob"
)

// Item flags are the owner's judgement about a file — keep it out of the way,
// or mark it worth coming back to. They are the only state here that is not
// derived from the media, so they live beside the index rather than on it: an
// item is a copy of what is on disk, while a flag has to survive the file
// disappearing and coming back. List and Get stamp them onto the copies they
// hand out.

// Flags is what the owner recorded about one item.
type Flags struct {
	Hidden    bool `json:"hidden,omitempty"`
	Favourite bool `json:"favourite,omitempty"`
	// Rotation is quarter turns clockwise, 0-3: the correction a file needs
	// because of how the camera was held. It belongs to the file and not to
	// the viewer, which is why it is kept here beside the other judgements
	// rather than in one browser's storage — a clip turned upright once is
	// upright on the phone as well.
	Rotation int `json:"rotation,omitempty"`
	// NoCrop keeps the black borders this file carries: they are trimmed
	// where they are found, and this is how a viewer says to leave them. A
	// film framed at 2.39:1 has borders on purpose, and cutting them off
	// takes the tops of heads with them.
	NoCrop bool `json:"nocrop,omitempty"`
}

// LoadFlags reads the stored flags into memory and reports how many there
// were. Called on first use (see ensureFlags); startup may call it as soon as
// the database is open so the first listing does not pay for the read.
func (l *Library) LoadFlags(db *blob.DB) int {
	stored, err := db.AllFlags()
	if err != nil {
		l.log.Warn("could not read item flags", "err", err)
		return 0
	}
	l.mu.Lock()
	for id, f := range stored {
		if _, ok := l.flags[id]; ok {
			continue // set while we were reading: that judgement is newer
		}
		l.flags[id] = Flags(f)
	}
	l.flagsLoaded = true
	l.mu.Unlock()
	if len(stored) > 0 {
		// Anything listed before this moment included items that are hidden.
		l.notify()
	}
	return len(stored)
}

// ensureFlags loads the stored flags the first time anything asks about one.
// The database is attached after the library is built (SetMetaDB) and the
// order of the startup steps is not this package's to choose, so the load is
// lazy rather than tied to construction. Must not be called with l.mu held:
// it reads the database.
func (l *Library) ensureFlags() {
	l.mu.RLock()
	db, loaded := l.metaDB, l.flagsLoaded
	l.mu.RUnlock()
	if loaded || db == nil {
		return
	}
	l.LoadFlags(db)
}

// Flags returns what the owner recorded about one item.
func (l *Library) Flags(id string) Flags {
	l.ensureFlags()
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.flags[id]
}

// SetFlags applies hidden and favourite (nil leaves that flag alone) to every
// given item and returns the resulting flags per item. Ids the index does not
// know are skipped. One call per multi-selection: a cull is a single request,
// a single database transaction and a single change event.
func (l *Library) SetFlags(ids []string, hidden, favourite, noCrop *bool, rotation *int) map[string]Flags {
	l.ensureFlags()
	out := make(map[string]Flags, len(ids))
	write := make(map[string]blob.Flags)

	l.mu.Lock()
	for _, id := range ids {
		if _, ok := l.items[id]; !ok {
			continue
		}
		old := l.flags[id]
		f := old
		if hidden != nil {
			f.Hidden = *hidden
		}
		if favourite != nil {
			f.Favourite = *favourite
		}
		if noCrop != nil {
			f.NoCrop = *noCrop
		}
		if rotation != nil {
			f.Rotation = ((*rotation % 4) + 4) % 4 // quarter turns, always 0-3
		}
		out[id] = f
		if f == old {
			continue
		}
		if f == (Flags{}) {
			delete(l.flags, id) // nothing left to remember
		} else {
			l.flags[id] = f
		}
		write[id] = blob.Flags(f)
	}
	db := l.metaDB
	l.mu.Unlock()

	if len(write) == 0 {
		return out
	}
	if db != nil {
		if err := db.SaveFlags(write); err != nil {
			l.log.Warn("could not store item flags", "err", err)
		}
	}
	// Hidden items drop out of every cached listing, and other clients are
	// looking at the same library: the version bump does both.
	l.notify()
	return out
}

// withFlags stamps the stored judgement onto a copy of an item. Caller must
// hold l.mu.
func (l *Library) withFlags(it Item) Item {
	f := l.flags[it.ID]
	it.Hidden, it.Favourite, it.Rotation, it.NoCrop = f.Hidden, f.Favourite, f.Rotation, f.NoCrop
	// The play count travels the same way and for the same reason: it is the
	// owner's, not the file's, so it belongs on the copy that goes out and
	// not on the item the walk rebuilds.
	it.Plays = l.playsOf(it.ID)
	it.Like = l.likeOf(it.ID)
	// And what the analysis says of it, read from caches that are rebuilt
	// only when a verdict or a vector changes.
	aff := l.affinities()
	if b := aff.bucket[it.ID]; b != 0 {
		it.Affinity = b
		it.Akin = l.akinName(aff.akin[it.ID])
	}
	it.Spoken = l.spokenOf(it.ID)
	return it
}

// stamper stamps what withFlags stamps onto many copies with the locks
// taken once: withFlags takes seven per item — the counts, the verdicts,
// the affinity's caches and the release verdicts — which over a page, or a
// queue of the whole library, is most of the work of handing it out. Built
// by whoever is about to hand out a page, before taking l.mu; stamp is then
// called under it, like withFlags. It forces no album build, for the reason
// spokenOf gives: this is asked under the index's lock.
type stamper struct {
	l      *Library
	plays  map[string]int
	likes  map[string]int
	aff    *affinity
	spoken func(string) bool
}

func (l *Library) stamper() *stamper {
	plays, _ := l.plays.snapshot()
	likes, _ := l.likes.snapshot()
	sv := l.scaledVectors()
	return &stamper{l: l, plays: plays, likes: likes, aff: l.affinities(), spoken: l.spokenSet(sv)}
}

// stamp is withFlags from the snapshots. Caller must hold l.mu.
func (s *stamper) stamp(it Item) Item {
	f := s.l.flags[it.ID]
	it.Hidden, it.Favourite, it.Rotation, it.NoCrop = f.Hidden, f.Favourite, f.Rotation, f.NoCrop
	it.Plays = s.plays[it.ID]
	it.Like = s.likes[it.ID]
	if b := s.aff.bucket[it.ID]; b != 0 {
		it.Affinity = b
		it.Akin = s.l.akinName(s.aff.akin[it.ID])
	}
	it.Spoken = s.spoken(it.ID)
	return it
}

// keepFlagged reports whether an item passes a query's flag filters.
// Caller must hold l.mu.
func (l *Library) keepFlagged(id, showHidden string, favouritesOnly bool) bool {
	f := l.flags[id]
	switch showHidden {
	case "include":
	case "only":
		if !f.Hidden {
			return false
		}
	default: // hidden means hidden, unless the query says otherwise
		if f.Hidden {
			return false
		}
	}
	return !favouritesOnly || f.Favourite
}

// hiddenCounts returns the per-kind totals of the hidden items that are in
// the index, memoized per library version — so, like the album totals, at
// most one change event behind. Counts subtracts them so the chips agree
// with what a default listing shows.
//
// It is a walk of the flagged items — never of the index — so its cost is
// bounded by how much the owner has marked, and it is paid at most once per
// version. Deriving the totals here, rather than counting hidden items at
// every insert and delete, keeps them in step with the index whichever of
// those sites moved the item.
func (l *Library) hiddenCounts(version int64) Counts {
	l.hiddenMu.Lock()
	defer l.hiddenMu.Unlock()
	if l.hiddenValid && l.hiddenVersion == version {
		return l.hiddenTotals
	}
	var c Counts
	l.mu.RLock()
	for id, f := range l.flags {
		if !f.Hidden {
			continue
		}
		if it, ok := l.items[id]; ok {
			addKind(&c, it.Kind, 1)
		}
	}
	l.mu.RUnlock()
	l.hiddenTotals, l.hiddenVersion, l.hiddenValid = c, version, true
	return c
}

// visibleCounts subtracts the hidden per-kind totals from the index totals.
func visibleCounts(all, hidden Counts) Counts {
	all.Video -= hidden.Video
	all.Image -= hidden.Image
	all.Audio -= hidden.Audio
	all.Playlist -= hidden.Playlist
	all.Total -= hidden.Total
	return all
}
