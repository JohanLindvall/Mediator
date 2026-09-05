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
func (l *Library) markDirty(id string) {
	if l.dirty != nil {
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
		})
	}
	remove := make([]string, 0, len(l.removed))
	for id := range l.removed {
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
	n, err := db.Prune(live)
	if err != nil {
		l.log.Warn("could not prune the database", "err", err)
		return
	}
	if n > 0 {
		l.log.Info("pruned stale database entries", "keys", n)
	}
}
