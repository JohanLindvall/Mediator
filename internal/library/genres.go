package library

import (
	"cmp"
	"slices"
	"strings"
)

// Genres are grouped from **albums**, exactly as artists are, and for the
// same reason: a view that grouped tracks could disagree with the album view
// about what a release is filed under, and the two are meant to be looking at
// one shelf from different ends.
//
// A release with no genre tag joins no genre. There is deliberately no
// "Unknown" bucket: it would be the largest card in the view on most
// libraries, it is not a genre anybody is looking for, and the releases in it
// are already reachable through All, the artists and the search. An artist is
// never missing — `fillAlbum` supplies "Various Artists" when the tags merely
// disagree — but a genre genuinely can be absent, and saying so by absence is
// the honest form.
//
// Cached per version like albums and artists, and searched and sorted by the
// same rules.
type Genre struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Artists is what the artist grouping does not have a counterpart for: a
	// genre's size is how many performers are in it as much as how many
	// releases, and it is the number a viewer scans the card for.
	Artists int    `json:"artists"`
	Albums  int    `json:"albums"`
	Tracks  int    `json:"tracks"`
	CoverID string `json:"coverId,omitempty"` // item whose artwork represents the genre
	// The span the dated releases in it cover. Both 0 when none are dated.
	FromYear int   `json:"fromYear,omitempty"`
	ToYear   int   `json:"toYear,omitempty"`
	Size     int64 `json:"size"`
	Duration int64 `json:"duration,omitempty"` // total playing time, ms (0 if unknown)
	Plays    int   `json:"plays,omitempty"`    // what the releases in it have been played, summed
	Likes    int   `json:"likes,omitempty"`    // the net verdict on the releases in it
	ModTime  int64 `json:"mtime"`

	lower    string // tokenized search text
	sortName string // lowercased name, for ordering

	// performers, while the genre is being built: the count comes from it and
	// so does the search text, and it is dropped once both are taken.
	performers map[string]struct{}
}

// Genres returns the grouped genres, rebuilding them when the library has
// changed. Cached per version like albums and artists; the build asks the
// album cache beneath it, which is the lock order every grouped view keeps.
func (l *Library) Genres() []*Genre {
	return l.genres.get(l.Version(), l.buildGenres)
}

func (l *Library) buildGenres() []*Genre { return genresFrom(l.Albums()) }

// genresFrom groups a set of releases; see artistsFrom for why this is
// separate from the cached build.
func genresFrom(albums []*Album) []*Genre {
	byName := map[string]*Genre{}
	for _, a := range albums {
		if a.Spoken {
			continue // an audiobook's "genre" tag is not a genre of music
		}
		// Every genre the tag names, not just the first: a release filed
		// under three of them belongs in three of them.
		for _, name := range a.Genres {
			key := strings.ToLower(name)
			g := byName[key]
			if g == nil {
				g = &Genre{ID: "g" + PathID(key), Name: name}
				byName[key] = g
			}
			g.Albums++
			g.Tracks += a.Tracks
			g.Plays += a.Plays
			g.Likes += a.Likes
			g.Size += a.Size
			if a.Artist != "" {
				if g.performers == nil {
					g.performers = map[string]struct{}{}
				}
				// The same key the artist grouping uses, or the number under
				// the card would not match what the artists view shows.
				g.performers[strings.ToLower(a.Artist)] = struct{}{}
			}
			if a.Duration > 0 && g.Duration >= 0 {
				g.Duration += a.Duration
			} else {
				g.Duration = -1 // one unmeasured album makes the total meaningless
			}
			if a.ModTime > g.ModTime {
				g.ModTime = a.ModTime
				g.CoverID = a.CoverID // artwork of the most recent release in it
			}
			if a.Year > 0 {
				if g.FromYear == 0 || a.Year < g.FromYear {
					g.FromYear = a.Year
				}
				if a.Year > g.ToYear {
					g.ToYear = a.Year
				}
			}
		}
	}

	out := make([]*Genre, 0, len(byName))
	for _, g := range byName {
		if g.Duration < 0 {
			g.Duration = 0
		}
		names := make([]string, 0, len(g.performers))
		for n := range g.performers {
			names = append(names, n)
		}
		// Sorted before they reach the search text: a map's order is not
		// stable, and two builds of one library must not differ.
		slices.Sort(names)
		g.Artists = len(names)
		g.performers = nil
		// Findable by the performers in it as well as by its own name — the
		// words someone remembers about a genre are the bands in it. Release
		// titles are deliberately left out: a genre holds hundreds, and the
		// text would be the whole library.
		g.lower = searchText(append([]string{g.Name}, names...)...)
		g.sortName = strings.ToLower(g.Name)
		out = append(out, g)
	}
	byID(out, func(g *Genre) string { return g.ID })
	return out
}

// albumInGenre reports whether a release is filed under this genre — under
// any of the genres its tag names, not merely the first.
func albumInGenre(a *Album, want string) bool {
	for _, g := range a.Genres {
		if strings.EqualFold(g, want) {
			return true
		}
	}
	return false
}

// itemInGenre is the same question of a single file, whose tag has not been
// through an album's grouping.
func itemInGenre(it *Item, want string) bool {
	for _, g := range splitGenres(it.Genre) {
		if strings.EqualFold(g, want) {
			return true
		}
	}
	return false
}

// SearchGenres filters and sorts the genre list.
func (l *Library) SearchGenres(search, sortKey string, desc bool, paths PathFilter) []*Genre {
	all := l.Genres()
	if paths.Restricted() {
		all = genresFrom(l.AllowedAlbums(l.Albums(), paths))
	}
	words := searchWords(search)
	out := make([]*Genre, 0, len(all))
	for _, g := range all {
		if matchWords(g.lower, words) {
			out = append(out, g)
		}
	}
	orderBy(out, desc,
		func(g *Genre) bool { return knownLength(sortKey, g.Duration) },
		func(a, b *Genre) int { return compareGenres(a, b, sortKey) },
		func(g *Genre) string { return g.sortName },
		func(g *Genre) string { return g.ID })
	return out
}

// compareGenres orders two genres by one key: their own, then the ones
// every collection has.
func compareGenres(a, b *Genre, sortKey string) int {
	switch sortKey {
	case "artists":
		return cmp.Compare(a.Artists, b.Artists)
	case "albums":
		return cmp.Compare(a.Albums, b.Albums)
	default:
		return compareCommon(a, b, sortKey)
	}
}
