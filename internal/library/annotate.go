package library

import (
	"sync"

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
//
// It goes through the same lock the lazy load does, so that is a safe thing
// for a startup step to do: whichever of the two arrives first reads the
// bucket and the other finds the work done. A door into this that did not
// take flagStore would be a second reader of the bucket racing a write of
// it, which is the whole of what the lock is for.
func (l *Library) LoadFlags(db *blob.DB) int {
	flagStore.Lock()
	defer flagStore.Unlock()
	return l.loadFlags(db)
}

// loadFlags is the load itself. Caller must hold flagStore, which is what
// makes one read of the bucket and one notify out of however many callers
// arrive together, and what orders the read against SetFlags writing to it.
func (l *Library) loadFlags(db *blob.DB) int {
	stored, err := db.AllFlags()
	if err != nil {
		l.log.Warn("could not read item flags", "err", err)
		return 0
	}
	l.mu.Lock()
	if l.flagsLoaded {
		// The load has already happened — a request's lazy one, or a startup
		// call, whichever reached the lock first — so what is in memory is
		// newer than what is in hand, including every judgement withdrawn
		// since. That is why this is a whole-snapshot drop and not one more
		// per-key guard: a withdrawal is spelled as a *deletion*, here and in
		// the database alike, so the key the merge below would have to skip
		// is not there to be seen. An absent key reads as "never set", the
		// old value goes back, and an item the owner had just un-hidden is
		// hidden again for the life of the process, with the database saying
		// the opposite and a restart disagreeing with both. A snapshot older
		// than a mutation cannot be merged key by key at all.
		l.mu.Unlock()
		return len(stored)
	}
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
	// One loader, however many callers arrive first. The flag is only set
	// once the read is over, so the first requests of a cold process — the
	// listing, the live stream's own counts, the album build behind them —
	// each saw "not loaded" and each read the whole bucket, and each
	// announced a change afterwards: a version bump apiece, and every
	// per-version cache discarded again for a load that had already
	// happened. The others wait here and find the answer already in memory.
	flagStore.Lock()
	defer flagStore.Unlock()
	l.mu.RLock()
	loaded = l.flagsLoaded
	l.mu.RUnlock()
	if loaded {
		return
	}
	l.loadFlags(db)
}

// flagStore serializes what touches the stored flags: the lazy load above,
// and the decision-and-writing-down in SetFlags. It is a package lock rather
// than one of the library's own because this must not be the index's lock —
// a commit and an fsync are no reason to hold every listing still, and a
// flush of a cold enrichment pass can leave bolt's writer busy for seconds.
// A process serves one library; a test with two pays only the wait.
var flagStore sync.Mutex

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

	// The database is written in the order the values were settled in, and
	// that is what this lock is for. The decision is made under the index's
	// lock and the writing-down happens after it is let go, so two presses
	// on one item — the rotation button twice, a hide and an unhide — could
	// reach bolt the other way round: memory takes the press that held l.mu
	// last and the database takes the press that entered bolt last, and
	// those are not always the same press. Nothing on screen says so, and
	// the database is the one that comes back at the next restart — a clip
	// at the rotation it was turned away from, or an item hidden again after
	// being let out, an all-false record being spelled as a deletion.
	flagStore.Lock()
	defer flagStore.Unlock()

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
	// The flags are loaded on first use, and this is where a page of them is
	// about to be handed out — so the load belongs here rather than at each
	// producer, which is exactly what the album sheet forgot: reached first
	// on a cold process, by a shortlink straight to a release or by the zip,
	// it stamped every track from an empty map and answered that nothing was
	// hidden, favourite or turned, while the same request a moment later —
	// after any other endpoint had triggered the load — answered properly.
	// Every producer builds the stamper before taking l.mu, which is where
	// this may run and nowhere else.
	l.ensureFlags()
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
// bounded by how much the owner has marked. What the memo saves is the walk
// for every caller holding the version it was counted at; a caller holding an
// older one walks for itself, because the answer it is owed is an answer
// about its own version and the only honest thing to publish is the one this
// walk actually saw. So in the moment after a change two callers still
// holding the version before it each walk, where one of them used to publish
// under that older stamp and hand the other a hidden set counted over an
// index it never asked about. A walk of what the owner has marked is the
// cheap half of Counts; being wrong about it is not.
//
// Deriving the totals here, rather than counting hidden items at every insert
// and delete, keeps them in step with the index whichever of those sites
// moved the item.
func (l *Library) hiddenCounts(version int64) Counts {
	l.hiddenMu.Lock()
	defer l.hiddenMu.Unlock()
	if l.hiddenValid && l.hiddenVersion == version {
		return l.hiddenTotals
	}
	var c Counts
	l.mu.RLock()
	// The stamp is the version this walk saw, never the one the caller asked
	// about. Counts reads its per-kind totals and the version under one lock
	// and comes here under another, so a change landing between the two
	// makes this answer newer than the question — and stored under the older
	// stamp it was then handed to every other request still holding that
	// version, which subtracted a hidden set counted over one index from
	// totals taken of another. Stamped with what it actually counted, a
	// caller behind walks for itself and nobody is told a number the library
	// never held.
	at := l.groupVersion
	for id, f := range l.flags {
		if !f.Hidden {
			continue
		}
		if it, ok := l.items[id]; ok {
			addKind(&c, it.Kind, 1)
		}
	}
	l.mu.RUnlock()
	// Which also means the stamp cannot go backwards, with nothing here to
	// check: hiddenMu is held across the whole walk, so the walks are in a
	// line, and the group version only ever rises — whatever this one saw is
	// at or ahead of what the last one wrote down.
	l.hiddenTotals, l.hiddenVersion, l.hiddenValid = c, at, true
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
