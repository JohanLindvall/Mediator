package library

// What the filter chips say.
//
// Unfiltered the totals are free: they are maintained by every insert and
// delete (`countKind`) and by the album and artist builds, so `Counts` walks
// nothing. A search is a different question — "how many albums match this?"
// cannot be answered by a running total — and answering it means passing
// over the index once.
//
// That pass is cached per (search, flags, version), exactly like the sorted
// listing beside it, so paging through results or switching between views
// costs nothing more. It is also the same walk the listing already does for
// its own query, at a fraction of the work: no sort, no copies, one integer
// per item.

import (
	"strings"
	"sync"
)

// countsCache is the last few answers, each kept while its question and the
// library are both unchanged. A few rather than one: a faced or confined
// caller with a search asks two questions per page — its own totals and the
// matching counts — and a single slot had the two evict each other on every
// page, so both missed every time and every page cost two walks of the
// index. Oldest out first. The lock is held for the whole of a miss, so a
// second caller asking the same question waits for the answer rather than
// walking beside it.
type countsCache struct {
	mu      sync.Mutex
	entries []countsEntry
	misses  int // walks of the index, for the tests that pin the caching
}

type countsEntry struct {
	key    countsKey
	counts Counts
}

// countsKeep is how many answers are kept.
const countsKeep = 8

// get answers from the cache. Caller holds the lock.
func (c *countsCache) get(key countsKey) (Counts, bool) {
	for _, e := range c.entries {
		if e.key == key {
			return e.counts, true
		}
	}
	return Counts{}, false
}

// put remembers an answer, dropping the oldest past countsKeep. Caller
// holds the lock.
func (c *countsCache) put(key countsKey, counts Counts) {
	if len(c.entries) >= countsKeep {
		c.entries = append(c.entries[:0], c.entries[1:]...)
	}
	c.entries = append(c.entries, countsEntry{key, counts})
}

// CountQuery is what the chips are counting: the search, and any narrowing
// the view has already applied. It is deliberately not a listing Query —
// counting asks a different question, over every kind at once and with no
// sort or paging to it.
type CountQuery struct {
	Search string
	// Artist narrows to one performer, as drilling into them does. Items are
	// theirs by their artist tag, which is why a picture or a film is never
	// one: the chips then say what is actually here rather than what the
	// library holds elsewhere.
	Artist string
	// Genre narrows to one genre, as drilling into it does. A genre is a
	// property of a *release*, so an item is in one by the genre on its own
	// tag — the same rule the artist filter follows, and the same reason a
	// film or a picture is never counted here.
	Genre string
	// Paths restricts the count to what lives under certain directories, the
	// same way it restricts a listing. In the key like everything else here.
	Paths PathFilter
	// Kinds is what this caller may see at all — the content face. The
	// per-kind totals could be masked afterwards, but the ones counted
	// *across* kinds cannot: how many things have been started, finished or
	// played is one number each, and a face that shows films must not be
	// told how much music somebody has been playing. Counting with the set
	// in hand is the only way those three come out right.
	Kinds      KindSet
	ShowHidden string
	Favourites bool
}

type countsKey struct {
	version  int64
	watchVer int64
	q        CountQuery
}

// CountsFor reports what each chip would show for this query: the same
// totals as Counts, but of what is actually being looked at.
func (l *Library) CountsFor(q CountQuery) Counts {
	if q.ShowHidden != "include" && q.ShowHidden != "only" {
		q.ShowHidden = "" // one spelling of the default, as List does
	}
	if q.Search == "" && q.Artist == "" && q.Genre == "" && !q.Paths.Restricted() && q.Kinds == 0 &&
		q.ShowHidden == "" && !q.Favourites {
		// Nothing is being narrowed, so the running totals are the answer
		// and no pass over the index is needed. A favourites-only listing,
		// or one showing the hidden, is narrowed as much as a search is.
		return l.Counts()
	}
	l.ensureFlags()

	l.mu.RLock()
	version := l.version
	l.mu.RUnlock()
	key := countsKey{version, l.watchVersion(), q}

	l.counts.mu.Lock()
	defer l.counts.mu.Unlock()
	if c, ok := l.counts.get(key); ok {
		return c
	}
	l.counts.misses++

	words := searchWords(q.Search)
	allowed := q.Paths.allower()
	// Read once for the whole pass: asking per item would take three locks
	// for every file in the library.
	plays := l.playsSnapshot()
	likes := l.likesSnapshot()
	watch := l.watchSnapshot()
	// The episodes of each show that this caller can actually see. Counted
	// here rather than read off the grouped list, because that list is of
	// the whole library: a face restricted to films-only, or to part of the
	// disk, has its own answer and the running total is not it.
	episodes := map[string]int{}
	var out Counts
	l.mu.RLock()
	for _, it := range l.items {
		if !allowed(it.Path) {
			continue
		}
		if !q.Kinds.Has(it.Kind) {
			continue
		}
		if !l.keepFlagged(it.ID, q.ShowHidden, q.Favourites) {
			continue
		}
		if q.Artist != "" && !strings.EqualFold(it.Artist, q.Artist) {
			continue
		}
		if q.Genre != "" && !itemInGenre(it, q.Genre) {
			continue
		}
		if !matchWords(it.lower, words) {
			continue
		}
		addKind(&out, it.Kind, 1)
		if it.Series != "" {
			episodes[SeriesKey(it.Series)]++
		}
		if plays[it.ID] > 0 || likes[it.ID] != 0 {
			out.Played++
		}
		switch watch[it.ID] {
		case WatchStarted:
			out.Started++
		case WatchDone:
			out.Watched++
		}
	}
	l.mu.RUnlock()

	// Albums and artists are their own collections and are matched as such —
	// an album whose name matches counts once, however many of its tracks do.
	// Both are already built and cached per version; asking for them here
	// takes their own locks, which is why it happens outside the one above.
	// The performers and genres of the releases that match, gathered in the
	// album pass: inside a genre the Artists chip has to say how many
	// performers are in *it*, and inside a performer the Genres chip has to
	// say how many genres *they* span. Neither answer is in the artist or
	// genre lists, which know nothing of the other narrowing.
	performers := map[string]struct{}{}
	genres := map[string]struct{}{}
	music := q.Kinds.Has(KindAudio)
	albums := []*Album{}
	if music {
		albums = l.AllowedAlbums(l.Albums(), q.Paths)
	}
	for _, a := range albums {
		if q.Artist != "" && !strings.EqualFold(a.Artist, q.Artist) {
			continue
		}
		if q.Genre != "" && !albumInGenre(a, q.Genre) {
			continue
		}
		if !matchWords(a.lower, words) {
			continue
		}
		if a.Spoken {
			out.Audiobooks++
			continue
		}
		out.Albums++
		if a.Artist != "" {
			performers[strings.ToLower(a.Artist)] = struct{}{}
		}
		for _, g := range a.Genres {
			genres[strings.ToLower(g)] = struct{}{}
		}
	}
	// Television is video's own, and a show is still a show only where more
	// than one of its episodes is in front of the viewer — the same rule the
	// grouping itself applies, applied to what this caller can see.
	if q.Kinds.Has(KindVideo) {
		for _, n := range episodes {
			if n >= 2 {
				out.Series++
			}
		}
	}
	if !music {
		out.Artists, out.Genres = 0, 0
	} else if q.Genre != "" || q.Paths.Restricted() {
		// Narrowed to a genre — or to part of the library — the performers
		// gathered above are the answer. The cached artist list is of
		// everything and would count performers this caller cannot see.
		out.Artists = len(performers)
	} else {
		for _, ar := range l.Artists() {
			if q.Artist != "" && !strings.EqualFold(ar.Name, q.Artist) {
				continue
			}
			if matchWords(ar.lower, words) {
				out.Artists++
			}
		}
	}
	if !music {
		// Nothing to do: both were zeroed above.
	} else if q.Artist != "" || q.Paths.Restricted() {
		// And narrowed to a performer, the genres they are filed under.
		out.Genres = len(genres)
	} else {
		for _, g := range l.Genres() {
			if q.Genre != "" && !strings.EqualFold(g.Name, q.Genre) {
				continue
			}
			if matchWords(g.lower, words) {
				out.Genres++
			}
		}
	}

	l.counts.put(key, out)
	return out
}
