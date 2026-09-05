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
