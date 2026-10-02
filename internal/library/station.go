package library

// Artist radio: one performer's tracks, for a radio that plays nobody else.
//
// Radio proper (Similar) answers with the nearest of the whole library, and
// fifty of the nearest of twenty thousand tracks is a field wide enough to
// be drawn from all evening. One performer is not: the bar draws from what
// it has not queued yet, and their nearest fifty are queued within a few
// top-ups. So a station is the performer's whole catalogue, nearest the
// seed first, and the bar draws from the head of what it has not played —
// measured here, 660 performers, a median of eleven tracks each and the
// largest about fifteen hundred, which is some 650 KB of answer for a
// top-up every ten songs at the very worst.
//
// The tracks the analysis has not reached are still the performer's, so
// they are in it too, after the ones that can be ranked and most popular
// first: a station must not fall silent over a performer nothing has read.
//
// One recording is answered once, as everywhere a queue is filled; the bar
// goes further and keeps one *song* (songKey in queue.ts), since a
// performer's catalogue is mostly the same songs again — measured over this
// library, 28,158 tagged tracks are 15,820 recordings and 14,938 songs once
// a live take, a demo or a remaster counts as the song it is. That fold is
// the bar's alone because only the bar knows what is already queued.

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// guests is where a credit to a guest begins in an artist tag: "feat.",
// "ft." or "featuring" after a space, or a bracket opening on any of them.
// An undotted "feat" outside a bracket is left alone, being as likely the
// end of a band's own name ("Clever Feat") as the start of a guest's.
var guests = regexp.MustCompile(`(?i)\s+(?:feat\.|ft\.|featuring\s|[(\[]\s*(?:feat|ft|featuring)\b)`)

// withoutGuests is an artist tag with its guests taken off: "A feat. B" is
// A's song.
func withoutGuests(artist string) string {
	if loc := guests.FindStringIndex(artist); loc != nil {
		artist = artist[:loc[0]]
	}
	return strings.TrimSpace(artist)
}

// performerOf is whose a track is, for artist radio: the performer its own
// tag names, with any guest taken off — and the performer its release is
// credited to only where the tag names nobody, or where it names that
// performer first and others with them (leadsWith).
//
// **The tag decides whenever it names anyone.** It used to give way to the
// release's credit wherever the library had no release by the name it
// gave, on the reasoning that such a name was a stray spelling of the
// release's own performer. Measured over every station in the library that
// reasoning held for one pair in forty-eight: the rest were split partners
// and the bands on tribute records — a split being credited by majority to
// whichever band holds more of its tracks, and the band holding fewer being,
// often enough, a band the library has nothing else by — 95 tracks in all,
// each played on another band's artist radio, which is the one thing it
// promises not to do. The one variant it caught was a letter typed two ways
// (two tracks), which is the cheaper thing to lose. The credit still names an
// untagged file, a release's own performer being the only answer there is.
//
// Known is the artists view's word for a performer, used here only to spell
// the name as it spells them; keyed by the lower-cased name.
func performerOf(tag, credit string, known map[string]string) string {
	own := withoutGuests(tag)
	who := own
	if own == "" || leadsWith(own, credit) {
		who = credit
	}
	if name, ok := known[strings.ToLower(who)]; ok {
		return name
	}
	return who
}

// leadsWith says whether a tag names the release's own performer first and
// others with them: "A, B", "A/B", "A;B", "A & B" — and "AB", which is how
// the tag reader hands over a frame holding two names, joined with nothing
// between them. What follows the performer's name has to be the start of
// something else, a separator or a capital, or a band whose name merely runs
// on from theirs ("Abcd" after "Abc") would be counted as them. Measured: 15
// tracks across the library, each a collaboration on the performer's own
// release, and every one of them theirs.
func leadsWith(tag, credit string) bool {
	if credit == "" || len(tag) <= len(credit) || !strings.EqualFold(tag[:len(credit)], credit) {
		return false
	}
	next, _ := utf8.DecodeRuneInString(tag[len(credit):])
	return !unicode.IsLower(next)
}

// Station answers a performer's station: every track of theirs the caller
// may see, of the seed's kind — music for music, speech for speech — nearest
// the seed first, and after those the tracks nothing has analysed, most
// popular first; one copy of each recording, the first in that order; never
// the seed, nor another copy of it.
//
// Named, the station is that performer's; unnamed, it is the seed's
// (performerOf), which is what keeps a station on one performer as it goes:
// every track it answers is that performer's by the same rule, so whichever
// becomes the next seed asks for the same station. With no seed the order is
// popularity alone, for the first track of a station started from a
// performer's page.
//
// Answers the performer as the library spells them, or "" where the seed is
// nobody's.
func (l *Library) Station(name, seed string, kinds KindSet, f PathFilter) (string, []Item) {
	sv := l.scaledVectors()
	known := map[string]string{}
	for _, a := range l.Artists() {
		known[strings.ToLower(a.Name)] = a.Name
	}
	// After Artists, which builds the releases if anything has changed: the
	// stamper's credits and speech verdicts are then that build's.
	st := l.stamper()
	allowed := f.allower()
	l.mu.RLock()
	defer l.mu.RUnlock()
	var from *Item
	if seed != "" {
		from = l.items[seed]
	}
	switch {
	case name != "":
		if spelled, ok := known[strings.ToLower(strings.TrimSpace(name))]; ok {
			name = spelled
		}
	case from != nil:
		name = performerOf(from.Artist, st.performers[seed], known)
	}
	if name == "" {
		return "", nil
	}
	want := strings.ToLower(name)
	spoken := from != nil && st.spoken(seed)
	seedVec := sv.vecs[seed]
	seedKey := ""
	if from != nil {
		seedKey = recordingKey(from)
	}
	type candidate struct {
		it     *Item
		ranked bool
		score  float32
		pop    int64
	}
	var cands []candidate
	for id, it := range l.items {
		if it.Kind != KindAudio || id == seed || !kinds.Has(it.Kind) || !allowed(it.Path) || st.spoken(id) != spoken {
			continue
		}
		if strings.ToLower(performerOf(it.Artist, st.performers[id], known)) != want {
			continue
		}
		c := candidate{it: it, pop: trackPopularity(st.likes[id], st.aff.bucket[id], st.plays[id])}
		if v, ok := sv.vecs[id]; ok && seedVec != nil {
			c.ranked, c.score = true, dot(seedVec, v)
		}
		cands = append(cands, c)
	}
	// Nearest first, the unranked after them by popularity, ties by id so two
	// answers cannot disagree.
	slices.SortFunc(cands, func(a, b candidate) int {
		if a.ranked != b.ranked {
			if a.ranked {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		if c := cmp.Compare(b.pop, a.pop); c != 0 {
			return c
		}
		return strings.Compare(a.it.ID, b.it.ID)
	})
	out := make([]Item, 0, len(cands))
	seen := make(map[string]bool, len(cands))
	for _, c := range cands {
		if key := recordingKey(c.it); key != "" {
			if key == seedKey || seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, st.stamp(*c.it))
	}
	return name, out
}
