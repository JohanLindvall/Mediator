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
// by its tier before anything else, each tier keeping the view's own order
// whichever way it runs: what the search names comes first, then what shows
// every word on its card, then what was found further away — a track of a
// release, a release of a performer, the performers of a genre, an episode of
// a show, the folder a file is kept in — none of which the card shows.
const (
	hitName   = iota // the search is its name, word for word
	hitCard          // every word is in what its card shows
	hitBeyond        // found by something its card does not show
)

// segmentedText is searchText built in three parts — the name a card shows,
// the rest of what it shows, and everything else the thing is found by —
// with where the first two end, so hitTier can read how directly a search
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

// hitTier is how directly a segmented search text answers the words of a
// search, phrase being those words joined as tokenize joins them.
func hitTier(text string, nameEnd, cardEnd int32, words []string, phrase string) int {
	if nameEnd > 1 && text[1:nameEnd-1] == phrase {
		return hitName
	}
	if matchWords(text[:cardEnd], words) {
		return hitCard
	}
	return hitBeyond
}

// rankHits orders a sorted list by tier, each tier keeping the order it was
// sorted in.
func rankHits[T any](out []T, tier func(T) int) {
	var tiers [hitBeyond + 1][]T
	for _, x := range out {
		t := tier(x)
		tiers[t] = append(tiers[t], x)
	}
	n := 0
	for _, t := range tiers {
		n += copy(out[n:], t)
	}
}

// rankByHit is rankHits for a search: nothing moves without one.
func rankByHit[T any](out []T, words []string, text func(T) (string, int32, int32)) {
	if len(words) == 0 {
		return
	}
	phrase := strings.Join(words, " ")
	rankHits(out, func(x T) int {
		t, nameEnd, cardEnd := text(x)
		return hitTier(t, nameEnd, cardEnd, words, phrase)
	})
}
