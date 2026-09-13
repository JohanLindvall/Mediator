package library

import (
	"context"
	"path/filepath"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// The index is mirrored into the blob database so a restart can serve the
// library immediately instead of waiting for the filesystem walk. It stays
// a cache: the scan that runs right after startup reconciles every record
// against the disk, and anything gone is dropped from both.

// Archived items are deliberately not persisted: their content is addressed
// by byte offsets into rar volumes, and replaying stale offsets after the
// archive changed would serve garbage. They come back when the scan
// re-parses the volume set.
func persistable(it *Item) bool { return it.stored == nil }

// LoadFromDB fills an empty index from the persisted records. Returns the
// number of items restored.
func (l *Library) LoadFromDB(db *blob.DB) int {
	recs, err := db.Items()
	if err != nil {
		l.log.Warn("could not read the stored index", "err", err)
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, r := range recs {
		if !l.UnderRoots(r.Path) {
			continue // roots changed since the record was written
		}
		name := filepath.Base(r.Path)
		firstSeen := r.FirstSeen
		if firstSeen == 0 {
			// Written before the index recorded additions: the file's own
			// timestamp is the best guess there is, and beats stacking a
			// whole existing library at the epoch.
			firstSeen = r.MTime
		}
		// Normalised on the way back in, exactly as setMeta normalises tags
		// on the way in from a file. This is the second door into the index
		// and the one a warm start comes through: a record written before
		// these rules existed is restored as it was written, marked already
		// enriched, and so never read again — which left whole libraries
		// carrying a year of 20220519 and a genre with an invisible byte
		// order mark in front of it.
		it := &Item{
			ID: r.ID, Name: displayText(name), Rel: displayText(l.rel(r.Path)), Kind: Kind(r.Kind),
			Size: r.Size, ModTime: r.MTime, FirstSeen: firstSeen, Path: r.Path,
			Duration: r.Duration, Title: cleanTag(r.Title), Artist: cleanTag(r.Artist),
			Album: cleanTag(r.Album), Genre: cleanGenreTag(r.Genre), Track: r.Track,
			Year:   cleanYear(r.Year),
			VCodec: r.VCodec, ACodec: r.ACodec, enriched: r.Enriched, shape: r.Shape,
			Width: r.Width, Height: r.Height, FPS: r.FPS, HDR: r.HDR,
		}
		// The episode a path names is parsed rather than stored: it is
		// derived from the path, the record has no field for it, and this
		// is the door a warm start comes through — without it a library
		// that had been restarted knew about no television at all, which is
		// to say nearly always. It must run before the search text, which
		// the series name is part of.
		setEpisode(it)
		it.lower = itemSearchText(it)
		l.items[it.ID] = it
		l.byPath[it.Path] = it
		l.countKind(it.Kind, 1)
		n++
	}
	return n
}

// UnderRoots reports whether path lies inside one of the configured roots —
// the one answer to that question, component-aware (see pathUnder), for the
// server as much as for the scan.
func (l *Library) UnderRoots(path string) bool {
	l.rootsMu.RLock()
	defer l.rootsMu.RUnlock()
	for _, root := range l.roots {
		if pathUnder(path, root) {
			return true
		}
	}
	return false
}

// markDirty notes that an item needs writing. Caller must hold l.mu.
//
// It takes the id out of the removals for the same reason markRemoved takes
// it out of the dirty set: one id must never sit in both, because a flush
// hands bolt the puts and the deletions in one transaction and the deletion
// is applied last. A file dropped and indexed again inside one two-second
// tick — a rename away and back, a reconciliation that ran while a disk was
// slow, a torrent client replacing a file in place — therefore had its fresh
// record written and then deleted, and both maps were cleared, so nothing
// ever wrote it again: the mirror was missing a file that is in the library
// until something changed it on disk, and a warm start inside that window
// served a library without it.
func (l *Library) markDirty(id string) {
	if l.dirty != nil {
		delete(l.removed, id)
		l.dirty[id] = struct{}{}
	}
}

// markRemoved notes that an item's records should be deleted. Caller must
// hold l.mu.
func (l *Library) markRemoved(id string) {
	if l.dirty != nil {
		delete(l.dirty, id)
		l.removed[id] = struct{}{}
	}
}

// PersistLoop mirrors index changes into db until ctx is done. Writes are
// batched: a burst of scan activity becomes a handful of transactions.
func (l *Library) PersistLoop(ctx context.Context, db *blob.DB) {
	l.mu.Lock()
	if l.dirty == nil { // no SetMetaDB: start tracking now
		l.dirty = make(map[string]struct{})
		l.removed = make(map[string]struct{})
		for id, it := range l.items {
			if persistable(it) {
				l.dirty[id] = struct{}{}
			}
		}
	}
	l.mu.Unlock()

	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			l.flush(db) // keep whatever the last scan learned
			return
		case <-t.C:
			l.flush(db)
		}
	}
}

// flush writes pending index changes and enrichment results to the database.
func (l *Library) flush(db *blob.DB) {
	l.metaPendMu.Lock()
	metas := l.metaPending
	l.metaPending = nil
	l.metaPendMu.Unlock()
	if err := db.PutMetas(metas); err != nil {
		l.log.Warn("could not write metadata cache", "err", err)
	}

	l.mu.Lock()
	if len(l.dirty) == 0 && len(l.removed) == 0 {
		l.mu.Unlock()
		return
	}
	put := make([]blob.Item, 0, len(l.dirty))
	for id := range l.dirty {
		it, ok := l.items[id]
		if !ok || !persistable(it) {
			continue
		}
		put = append(put, blob.Item{
			ID: it.ID, Path: it.Path, Kind: string(it.Kind),
			Size: it.Size, MTime: it.ModTime, FirstSeen: it.FirstSeen,
			Duration: it.Duration,
			Title:    it.Title, Artist: it.Artist, Album: it.Album,
			Genre: it.Genre, Track: it.Track, Year: it.Year,
			VCodec: it.VCodec, ACodec: it.ACodec, Enriched: it.enriched, Shape: it.shape,
			Width: it.Width, Height: it.Height, FPS: it.FPS, HDR: it.HDR,
		})
	}
	remove := make([]string, 0, len(l.removed))
	for id := range l.removed {
		if _, live := l.items[id]; live {
			// It went and came back: the index holds it again, and the put
			// above carries its record. bolt applies this transaction's
			// deletions after its puts, so passing both would write the
			// record and delete it in the same breath — and both maps are
			// cleared here, so nothing would ever write it again. markDirty
			// keeps the two sets apart; this is the belt, at the one point
			// where the harm is done, and it also covers an id the error
			// path below put back after the file returned.
			continue
		}
		remove = append(remove, id)
	}
	clear(l.dirty)
	clear(l.removed)
	l.mu.Unlock()

	if err := db.SaveItems(put, remove); err != nil {
		l.log.Warn("could not write the stored index", "err", err)
		// Marked again, so the next tick tries again: cleared before the
		// write, a failed one used to lose the changes until the item
		// changed once more, which a deleted file never does.
		l.mu.Lock()
		for _, it := range put {
			l.dirty[it.ID] = struct{}{}
		}
		for _, id := range remove {
			l.removed[id] = struct{}{}
		}
		l.mu.Unlock()
	}
}

// PruneDB drops cached records (index, metadata, thumbnails) for files that
// are no longer in the library. Safe only after a completed scan.
func (l *Library) PruneDB(db *blob.DB) {
	l.mu.RLock()
	live := make(map[string]struct{}, len(l.items))
	for id := range l.items {
		live[id] = struct{}{}
	}
	l.mu.RUnlock()
	if len(live) == 0 {
		return // an empty index means the roots are unreadable, not empty
	}
	l.pruneTo(db, live)
}

// pruneTo is the prune and the repair of what the list missed, which are one
// step on purpose: a prune that has run is never without the repair, and
// keeping them in one function is what lets a test stand in the window
// between the two — an interleaving that cannot be produced from outside,
// since the list is taken and used inside the call.
func (l *Library) pruneTo(db *blob.DB, live map[string]struct{}) {
	n, err := db.Prune(live)
	if err != nil {
		l.log.Warn("could not prune the database", "err", err)
		return
	}
	l.remarkAfterPrune(live)
	if n > 0 {
		l.log.Info("pruned stale database entries", "keys", n)
	}
}

// remarkAfterPrune writes down again whatever the index gained after live was
// taken.
//
// The list is made under a read lock and the prune itself is a transaction
// over the whole database, and nothing orders the two against the watcher:
// a file that arrives in between is not in the list the prune works from, so
// if the persist loop's tick writes its record inside that window the prune
// deletes it a moment later — while the item is live in memory and no longer
// dirty, so nothing writes it again and a restart serves a library without
// it until the next walk finds it. Holding the index still for the length of
// a whole-database transaction would be the cure that costs more than the
// illness, so the late arrivals are simply written again: one put apiece on
// the next tick, and none at all on the ordinary run where the list was
// complete.
//
// It puts back **both** of the two things a prune deletes that nothing else
// would rewrite. The index record is the one that matters, and the metadata
// record has to go with it: the index record carries "examined", so once it
// is back nothing ever asks to read that file again (needsEnrich), and the
// cached reading the prune took would be gone for as long as the file sits
// unchanged on disk — its key is the file's own mtime and size, which is what
// makes it worth keeping and also what makes it permanent. It is rebuilt from
// the item itself, which holds every field that record holds, and only where
// the item is marked examined and its shape read at the current recipe: that
// is exactly the state a completed enrichment leaves, so nothing here can
// write down a verdict enrichment deliberately withheld — an interrupted
// probe leaves neither mark and is left to be looked at again. Thumbnails are
// the third thing the prune drops and they need nothing: they are made on
// demand, so one that has gone is one made again the next time a tile asks.
func (l *Library) remarkAfterPrune(live map[string]struct{}) {
	l.mu.RLock()
	var late []string
	for id, it := range l.items {
		if _, known := live[id]; !known && persistable(it) {
			late = append(late, id)
		}
	}
	l.mu.RUnlock()
	if len(late) == 0 {
		return
	}
	metas := make(map[string]blob.Meta, len(late))
	l.mu.Lock()
	for _, id := range late {
		it, still := l.items[id]
		if !still {
			continue
		}
		l.markDirty(id)
		if it.enriched && it.shape >= shapeVersion {
			metas[id] = blob.Meta{
				MTime: it.ModTime, Size: it.Size, Duration: it.Duration,
				Title: it.Title, Artist: it.Artist, Album: it.Album,
				Genre: it.Genre, Track: it.Track, Year: it.Year,
				VCodec: it.VCodec, ACodec: it.ACodec,
				Width: it.Width, Height: it.Height, FPS: it.FPS, HDR: it.HDR,
				Shape: it.shape,
			}
		}
	}
	l.mu.Unlock()
	// Queued outside the index lock: the pending buffer has a lock of its own
	// and nothing else here takes the two in that order.
	for id, m := range metas {
		l.queueMeta(id, m)
	}
}
