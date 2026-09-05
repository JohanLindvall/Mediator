package library

import (
	"cmp"
	"strings"
)

// Artists are derived from albums rather than from individual tracks, so
// that the artist view and the album view always agree: every artist here
// has albums, and opening one shows exactly those albums.

// Artist groups the albums credited to one performer.
type Artist struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Albums  int    `json:"albums"`
	Tracks  int    `json:"tracks"`
	CoverID string `json:"coverId,omitempty"` // item whose artwork represents the artist
	// Genre is what most of their releases are tagged as. A performer is not
	// one genre — but a listing that says nothing at all is worse than one
	// that says what they are mostly filed under.
	Genre string `json:"genre,omitempty"`
	// FromYear and ToYear are the span their tagged releases cover. Equal
	// when everything dated came out in one year, and both 0 when nothing is
	// dated at all.
	FromYear int   `json:"fromYear,omitempty"`
	ToYear   int   `json:"toYear,omitempty"`
	Size     int64 `json:"size"`
	Duration int64 `json:"duration,omitempty"` // total playing time, ms (0 if unknown)
	Plays    int   `json:"plays,omitempty"`    // what their releases have been played, summed
	Likes    int   `json:"likes,omitempty"`    // the net verdict on their releases
	ModTime  int64 `json:"mtime"`
	// Similarity is set only on the copies a "similar performers" answer
	// hands out: how much this performer sounds like the one asked about.
	Similarity float32 `json:"similarity,omitempty"`

	lower    string // tokenized search text
	sortName string // lowercased name, for ordering
	// Release titles and genres collected while grouping, folded into lower
	// once the grouping is done and dropped again — they are how a performer
	// is found by what they released rather than only by their own name.
	searchParts []string
	// genres counts what their releases are tagged as while the artist is
	// being built, and is dropped once the commonest is known.
	genres map[string]int
}

// Artists returns every artist in the library, cached per library version.
func (l *Library) Artists() []*Artist {
	return l.artists.get(l.Version(), l.buildArtists)
}

func (l *Library) buildArtists() []*Artist { return artistsFrom(l.Albums()) }

// artistsFrom groups a set of releases. Separate from the cached build
// because a caller restricted to part of the library has a different set,
// and grouping theirs is the only honest way to answer them — the cached
// list is of everything and cannot be filtered afterwards without counting
// releases the caller cannot see.
func artistsFrom(albums []*Album) []*Artist {
	byName := map[string]*Artist{}
	// Albums are already deduplicated, so a playlist that merely restates a
	// directory is gone by now and nothing is counted twice.
	for _, a := range albums {
		// A narrator is not a performer: audiobooks have a shelf of their
		// own and are left out of the music's groupings.
		if a.Artist == "" || a.Spoken {
			continue
		}
		key := strings.ToLower(a.Artist)
		ar := byName[key]
		if ar == nil {
			ar = &Artist{ID: "r" + PathID(key), Name: a.Artist}
			byName[key] = ar
		}
		ar.Albums++
		ar.Tracks += a.Tracks
		ar.Plays += a.Plays
		ar.Likes += a.Likes
		// A performer is worth finding by what they released, not only by
		// their own name: the release titles and the genres they were tagged
		// with are the words someone actually remembers.
		ar.searchParts = append(ar.searchParts, a.Name, a.Genre)
		ar.Size += a.Size
		if a.Duration > 0 && ar.Duration >= 0 {
			ar.Duration += a.Duration
		} else {
			ar.Duration = -1 // one unmeasured album makes the total meaningless
		}
		if a.ModTime > ar.ModTime {
			ar.ModTime = a.ModTime
			ar.CoverID = a.CoverID // artwork of their most recent release
		}
		if a.Genre != "" {
			if ar.genres == nil {
				ar.genres = map[string]int{}
			}
			ar.genres[a.Genre]++
		}
		if a.Year > 0 {
			if ar.FromYear == 0 || a.Year < ar.FromYear {
				ar.FromYear = a.Year
			}
			if a.Year > ar.ToYear {
				ar.ToYear = a.Year
			}
		}
	}

	out := make([]*Artist, 0, len(byName))
	for _, ar := range byName {
		if ar.Duration < 0 {
			ar.Duration = 0
		}
		ar.Genre, _ = mostCommon(ar.genres)
		ar.genres = nil
		ar.lower = searchText(append([]string{ar.Name}, ar.searchParts...)...)
		ar.searchParts = nil // only ever needed to build the line above
		ar.sortName = strings.ToLower(ar.Name)
		out = append(out, ar)
	}
	byID(out, func(a *Artist) string { return a.ID })
	return out
}

// compareArtists orders two performers by one key: their own, then the
// ones every collection has.
func compareArtists(a, b *Artist, sortKey string) int {
	if sortKey == "albums" {
		return cmp.Compare(a.Albums, b.Albums)
	}
	return compareCommon(a, b, sortKey)
}

// artistsFor is the performers a caller may see: the cached grouping of
// everything, or a regrouping of the releases the caller may reach — see
// artistsFrom for why that is not a filter.
func (l *Library) artistsFor(paths PathFilter) []*Artist {
	if !paths.Restricted() {
		return l.Artists()
	}
	return artistsFrom(l.AllowedAlbums(l.Albums(), paths))
}

// SearchArtists filters and sorts the artist list.
func (l *Library) SearchArtists(search, sortKey string, desc bool, paths PathFilter) []*Artist {
	all := l.artistsFor(paths)
	words := searchWords(search)
	out := make([]*Artist, 0, len(all))
	for _, a := range all {
		if matchWords(a.lower, words) {
			out = append(out, a)
		}
	}
	// The length is left at zero unless every album underneath was measured,
	// so ordering by it is "known" against "not known" first, as with albums.
	orderBy(out, desc,
		func(a *Artist) bool { return knownLength(sortKey, a.Duration) },
		func(a, b *Artist) int { return compareArtists(a, b, sortKey) },
		func(a *Artist) string { return a.sortName },
		func(a *Artist) string { return a.ID })
	return out
}
