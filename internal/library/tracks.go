package library

import (
	"cmp"
	"slices"
	"strings"
)

// TracksOf flattens releases into the tracks a queue plays: each release's
// tracks in their running order, release after release in the order given,
// and no more than max of them. A caller confined to part of the library is
// handed only the tracks it may see — a release is kept for one allowed
// track (AllowedAlbums), and its others may lie outside.
//
// Copies, under one read lock: a queue of ten thousand is ten thousand
// lookups, and taking the lock per track would be most of the cost.
func (l *Library) TracksOf(albums []*Album, f PathFilter, max int) []Item {
	l.ensureFlags()
	allowed := f.allower()
	st := l.stamper()
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Item, 0, min(max, 256))
	for _, a := range albums {
		for _, id := range a.TrackIDs {
			if len(out) >= max {
				return out
			}
			if it, ok := l.items[id]; ok && allowed(it.Path) {
				out = append(out, st.stamp(*it))
			}
		}
	}
	return out
}

// ReleasesOf gathers the releases of performers: performer by performer in
// the order given, and each performer's in the order they were made, since
// a discography is played through from the start. A performer is matched
// the way the artists view groups them — by name, case-insensitively — so
// what is gathered is exactly what their card counts.
func (l *Library) ReleasesOf(artists []*Artist, f PathFilter) []*Album {
	byName := map[string][]*Album{}
	for _, a := range l.AllowedAlbums(l.Albums(), f) {
		if a.Artist != "" && !a.Spoken {
			key := strings.ToLower(a.Artist)
			byName[key] = append(byName[key], a)
		}
	}
	var out []*Album
	for _, ar := range artists {
		out = append(out, discography(byName[strings.ToLower(ar.Name)])...)
	}
	return out
}

// ReleasesIn gathers the releases filed under genres, each once however many
// of the genres it carries, and a genre's releases performer by performer: a
// genre played through is its performers' discographies, one after another.
func (l *Library) ReleasesIn(genres []*Genre, f PathFilter) []*Album {
	all := l.AllowedAlbums(l.Albums(), f)
	seen := map[string]bool{}
	var out []*Album
	for _, g := range genres {
		var in []*Album
		for _, a := range all {
			if !seen[a.ID] && !a.Spoken && albumInGenre(a, g.Name) {
				seen[a.ID] = true
				in = append(in, a)
			}
		}
		out = append(out, discography(in)...)
	}
	return out
}

// discography orders releases the way a catalogue is played through:
// performer by performer, with the releases nobody is credited for last;
// within a performer dated releases before undated ones and oldest first;
// then by name and by id, so two builds cannot disagree. Sorts in place —
// callers hand it slices of their own, never the cached list.
func discography(albums []*Album) []*Album {
	slices.SortFunc(albums, func(a, b *Album) int {
		if (a.Artist == "") != (b.Artist == "") {
			if a.Artist == "" {
				return 1
			}
			return -1
		}
		if c := strings.Compare(strings.ToLower(a.Artist), strings.ToLower(b.Artist)); c != 0 {
			return c
		}
		if (a.Year == 0) != (b.Year == 0) {
			if a.Year == 0 {
				return 1
			}
			return -1
		}
		if c := cmp.Compare(a.Year, b.Year); c != 0 {
			return c
		}
		if c := strings.Compare(a.sortName, b.sortName); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return albums
}
