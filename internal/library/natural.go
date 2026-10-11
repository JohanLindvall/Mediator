// SPDX-License-Identifier: MIT

package library

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// naturalKey is a name as it sorts: compared byte for byte, two keys order
// their names the way a reader counts. A run of digits is the number it
// spells ("Part 2" before "Part 10", "007" beside "7"), case is ignored, and
// a run of spaces, underscores, dots and hyphens is one separator, so
// "Part_2", "Part.2" and "Part - 2" sit where "Part 2" does; nothing at
// either end of a name counts. Otherwise the order is the code points',
// which keeps ASCII's classes: a separator first, then punctuation, then
// numbers, then letters.
//
// Built once per name (the index keeps an item's in nameKey, a collection's
// in sortName) rather than compared in place: a listing sorts the whole
// library, and a comparison that reads the names token by token measured ten
// times slower over 150,000 of them than one of prepared keys.
//
// A separator is a space; a number is a zero, a byte giving how many digits
// follow (a longer number is a larger one; a name holds no more than 255),
// and the digits without their leading zeros; anything else is the
// character in lower case. Neither a space nor a digit is ever written as
// itself, so no character reads as one of the others.
func naturalKey(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	sep := false
	for i := 0; i < len(s); {
		c := s[i]
		if '0' <= c && c <= '9' {
			k := i
			for k < len(s) && '0' <= s[k] && s[k] <= '9' {
				k++
			}
			digits := strings.TrimLeft(s[i:k], "0")
			if digits == "" {
				digits = "0"
			}
			if sep && b.Len() > 0 {
				b.WriteByte(' ')
			}
			sep = false
			b.WriteByte('0')
			b.WriteByte(byte(min(len(digits), 255)))
			b.WriteString(digits)
			i = k
			continue
		}
		r, size := rune(c), 1
		if c >= utf8.RuneSelf {
			r, size = utf8.DecodeRuneInString(s[i:])
		}
		i += size
		if naturalSeparator(r) {
			sep = true
			continue
		}
		if sep && b.Len() > 0 {
			b.WriteByte(' ')
		}
		sep = false
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// naturalSeparator is what parts the words of a name: white space, and the
// underscores, dots and hyphens file names put in its place.
func naturalSeparator(r rune) bool {
	switch r {
	case ' ', '_', '.', '-':
		return true
	}
	return unicode.IsSpace(r)
}
