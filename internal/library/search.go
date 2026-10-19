// SPDX-License-Identifier: MIT

package library

import (
	"strings"
	"unicode"
)

// The search model: every item carries a token text built from its filename,
// its absolute path (every directory above it, not just the ones below the
// root) and its tag metadata; queries are tokenized the same way and every
// query word must appear somewhere in that text. Punctuation and word order
// never matter, so "song tide" finds "Tide.Song.mp3" while the user is still
// typing.
//
// The path indexed is the absolute one because the display path starts at the
// root's own base name: a library rooted deep in a mount point could not be
// searched by where it is, only by what is under it. The absolute path is a
// superset of the display path token for token, so nothing is duplicated by
// indexing it instead.

// tokenize lowercases s and splits it into letter/digit runs:
// "Tide.Song-04" → ["tide", "song", "04"].
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// searchText builds the indexed text for an item from its parts.
func searchText(parts ...string) string {
	var tokens []string
	for _, p := range parts {
		tokens = append(tokens, tokenize(p)...)
	}
	return " " + strings.Join(tokens, " ") + " "
}

// searchWords parses a user query into match words.
func searchWords(q string) []string { return tokenize(q) }

// matchWords reports whether every word occurs in text (an empty query
// matches everything).
func matchWords(text string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}

// How directly something answers a search, best first. A listing is ordered
// by its tier before anything else: what the search names comes first, then
// what shows every word on its card, then what was found further away — a
// track of a release, a release of a performer, the performers of a genre, an
// episode of a show, the folder a file is kept in — none of which the card
// shows.
//
// Within a tier a whole word beats part of one: a search for "gorse" lists a
// card reading "Gorse" before one reading "Gorseland", each tier running by
// how many of the search's words were found only inside longer words, none
// first. A whole word never lifts a thing out of its tier: every file under a
// root of that name holds it whole in its path, and a card showing the word
// at all answers more directly than a folder far up. Each rank keeps the
// view's own order, whichever way it runs.
const (
	hitName   = iota // the search is its name, word for word
	hitCard          // every word is in what its card shows
	hitBeyond        // found by something its card does not show
)

// segmentedText is searchText built in three parts — the name a card shows,
// the rest of what it shows, and everything else the thing is found by —
// with where the first two end, so hits.rank can read how directly a search
// was answered without tokenizing anything again.
func segmentedText(name, card, beyond []string) (text string, nameEnd, cardEnd int32) {
	var b strings.Builder
	b.WriteByte(' ')
	add := func(parts []string) {
		for _, p := range parts {
			for _, t := range tokenize(p) {
				b.WriteString(t)
				b.WriteByte(' ')
			}
		}
	}
	add(name)
	nameEnd = int32(b.Len())
	add(card)
	cardEnd = int32(b.Len())
	add(beyond)
	return b.String(), nameEnd, cardEnd
}

// hits is a search made ready to rank what it found: its words, each again
// as a token of its own (" w ", as segmentedText writes tokens, so a whole
// word is one substring check), and the phrase that names a thing — the
// words joined as tokenize joins them.
type hits struct {
	words  []string
	whole  []string
	phrase string
}

func newHits(words []string) *hits {
	h := &hits{words: words, whole: make([]string, len(words)), phrase: strings.Join(words, " ")}
	for i, w := range words {
		h.whole[i] = " " + w + " "
	}
	return h
}

// rank is where a thing's own segmented text puts it: its tier, then how
// many words the answer holds only inside longer words — judged on the card
// where the card shows every word, else on the whole text. Lower first.
func (h *hits) rank(text string, nameEnd, cardEnd int32) int {
	if nameEnd > 1 && text[1:nameEnd-1] == h.phrase {
		return h.at(hitName, 0)
	}
	card, partial := text[:cardEnd], 0
	for i, w := range h.words {
		if strings.Contains(card, h.whole[i]) {
			continue
		}
		if !strings.Contains(card, w) {
			return h.at(hitBeyond, h.partial(text))
		}
		partial++
	}
	return h.at(hitCard, partial)
}

// partial counts the words text does not hold as tokens of their own.
func (h *hits) partial(text string) int {
	n := 0
	for _, w := range h.whole {
		if !strings.Contains(text, w) {
			n++
		}
	}
	return n
}

// at is the rank of a tier with that many partial words: every rank of one
// tier comes before any of the next.
func (h *hits) at(tier, partial int) int {
	return tier*(len(h.words)+1) + partial
}

// rankBy orders a sorted list by rank, lowest first, each rank keeping the
// order the list was sorted in.
func rankBy[T any](out []T, rank func(T) int) {
	ranks := make([]int, len(out))
	top := 0
	for i, x := range out {
		ranks[i] = rank(x)
		top = max(top, ranks[i])
	}
	start := make([]int, top+1) // each rank's count, then where it begins
	for _, r := range ranks {
		start[r]++
	}
	n := 0
	for r, c := range start {
		start[r], n = n, n+c
	}
	ranked := make([]T, len(out))
	for i, x := range out {
		ranked[start[ranks[i]]] = x
		start[ranks[i]]++
	}
	copy(out, ranked)
}

// rankByHit is rankBy for a search: nothing moves without one. through
// answers, in a view that finds things through their members as well — a
// release through a track, a performer through a release, a show through an
// episode — for a thing its own text did not find: it ranks as found further
// away, by how many words the member's answer holds only inside longer words.
// nil where nothing is found that way.
func rankByHit[T any](out []T, words []string, text func(T) (string, int32, int32), through func(T) (int, bool)) {
	if len(words) == 0 {
		return
	}
	h := newHits(words)
	rankBy(out, func(x T) int {
		if through != nil {
			if partial, ok := through(x); ok {
				return h.at(hitBeyond, partial)
			}
		}
		t, nameEnd, cardEnd := text(x)
		return h.rank(t, nameEnd, cardEnd)
	})
}
