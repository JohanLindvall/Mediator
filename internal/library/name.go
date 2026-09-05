package library

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A path on Linux is a bag of bytes, not text, and plenty of files carry
// names that were encoded by something that never asked what encoding the
// filesystem wanted: Windows-1252 and Latin-1 are what turn up, from
// downloads and from disks that have been carried across systems. Go hands
// those bytes back exactly as they are, which is right for opening the file
// and wrong for showing it — the JSON encoder replaces every byte that is
// not valid UTF-8 with U+FFFD, so a name with an umlaut in it reaches the
// browser with a black diamond where the letter should be, and cannot be
// searched for either.
//
// So the display name and the search text are decoded, and nothing else is.
// The path kept for opening the file, the id hashed from it and the record
// written to the database all stay byte for byte what the filesystem said —
// decoding those would name a file that does not exist, and would change
// every id in the library.

// latin1Rune reads one byte the Western way: Windows-1252 where it differs
// from Latin-1, and the byte's own value everywhere above that.
func latin1Rune(c byte) rune {
	if c >= 0x80 && c < 0xA0 {
		return cp1252High[c-0x80]
	}
	return rune(c)
}

// cp1252High maps the bytes 0x80-0x9F, the range where Windows-1252 differs
// from Latin-1, to what they stand for. The five Windows leaves undefined
// keep the Latin-1 reading, which is the byte's own value.
var cp1252High = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡',
	'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—',
	'˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}

// displayText returns s as valid UTF-8, reading what is not valid UTF-8 as
// Thai where it looks like Thai and as Windows-1252 otherwise. Valid UTF-8 is
// returned untouched and unallocated, which is nearly every name.
//
// Decoding byte by byte rather than the whole string at once is what makes a
// half-broken name — most of it UTF-8, one stray byte from somewhere else —
// come out with the good part intact.
func displayText(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	if thai := decodeTIS620([]byte(s)); thai != "" {
		return thai
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(latin1Rune(s[i]))
			i++
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// itemSearchText builds an item's indexed text from everything it carries:
// the decoded name and path, and whatever tags enrichment has found. Every
// place that touches those fields rebuilds it from here, so none of them can
// drop half the text by forgetting an argument.
func itemSearchText(it *Item) string {
	year := ""
	if it.Year > 0 {
		year = strconv.Itoa(it.Year)
	}
	return searchText(it.Name, displayText(it.Path), it.Title, it.Artist, it.Album, it.Genre, year)
}

// Thai is the other encoding that turns up in a library like this, and it is
// not Windows-1252: TIS-620 (Windows-874 is the same thing with a few
// punctuation marks added) puts the whole Thai alphabet in the high half of
// the byte range. Read as Latin-1 — which is what a tag reader does with an
// ID3 frame that declares itself Latin-1, and what the fallback above would
// do with a file name — every letter comes out as an accented Roman one, so
// a track of the shape "ฝนตกหนัก" reaches the screen as "½¹µ¡Ë¹Ñ¡".
//
// The two cannot be told apart by the bytes alone, so this goes by shape: a
// run of four or more Thai-range bytes with nothing ASCII between them. Thai
// is written that way throughout, and European text is not — its accents sit
// one or two at a time among Roman letters, and the longest all-accented
// word anyone writes is shorter than that. A run that long is the signature.

// thaiRun is how many Thai-range bytes in a row it takes to be sure.
const thaiRun = 4

// tisRune maps one TIS-620 byte to its character, or 0 if it is not one.
// 0xDB-0xDE are unassigned; below 0x80 the encoding is ASCII.
func tisRune(b byte) rune {
	switch {
	case b >= 0xA1 && b <= 0xDA, b >= 0xDF && b <= 0xFB:
		return rune(0x0E00 + int(b) - 0xA0)
	}
	return 0
}

// looksTIS620 reports whether these bytes are Thai rather than something
// Western. Anything in 0x80-0xA0 says they are not: TIS-620 leaves that
// range unassigned, while Windows-1252 keeps its quotation marks there.
func looksTIS620(b []byte) bool {
	run := 0
	for _, c := range b {
		if c >= 0x80 && c <= 0xA0 {
			return false
		}
		if tisRune(c) != 0 {
			run++
			if run >= thaiRun {
				return true
			}
			continue
		}
		run = 0
	}
	return false
}

// decodeTIS620 reads Thai out of these bytes, or returns "" if they are not.
func decodeTIS620(b []byte) string {
	if !looksTIS620(b) {
		return ""
	}
	var out strings.Builder
	out.Grow(len(b) * 2)
	for _, c := range b {
		switch {
		case c < 0x80:
			out.WriteByte(c)
		case tisRune(c) != 0:
			out.WriteRune(tisRune(c))
		default:
			// Unassigned in TIS-620 (0xDB-0xDE, 0xFC-0xFF). Read it the way
			// anything else that is not UTF-8 is read, rather than dropping
			// a byte out of a name.
			out.WriteRune(latin1Rune(c))
		}
	}
	return out.String()
}

// reinterpretThai puts back a string that was read as Latin-1 but was Thai.
//
// This is the tag reader's leavings rather than the filesystem's: by the
// time a frame reaches us it has already been decoded, so what arrives is
// valid UTF-8 made of Latin-1 letters. Turning each of those back into the
// byte it came from is only possible because every one of them is below
// U+0100 — anything else means the string was never a mis-read at all.
func reinterpretThai(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xFF {
			return s
		}
		b = append(b, byte(r))
	}
	if thai := decodeTIS620(b); thai != "" {
		return thai
	}
	return s
}

// Cyrillic where a Nordic vowel should be.
//
// CP1251 puts the Russian alphabet exactly where Latin-1 keeps its accented
// letters: byte 0xF6 is ö in one and ц in the other, 0xE4 is ä and д, 0xE5
// is å and е, 0xF8 is ø and ш. A tagger that guessed Russian on a Swedish
// release therefore writes the shape "Fyrsnц" where "Fyrsnö" was meant —
// and writes it in UTF-16, so nothing downstream can tell by looking at the
// bytes that anything went wrong. The letters are simply the wrong letters
// now.
//
// What gives it away is company. A Cyrillic letter with a Latin letter
// against it is not a word in either alphabet; it is one byte that was read
// twice differently. A Cyrillic word standing on its own — which this
// library has a great deal of, 501 items of it — is exactly what it looks
// like and is left alone.
func reinterpretCyrillic(s string) string {
	if !strings.ContainsFunc(s, isCyrillic) {
		return s
	}
	rs := []rune(s)
	out := make([]rune, len(rs))
	copy(out, rs)
	for i, r := range rs {
		if !isCyrillic(r) {
			continue
		}
		if !latinNeighbour(rs, i) {
			continue
		}
		if latin := cp1251Latin1(r); latin != 0 {
			out[i] = latin
		}
	}
	return string(out)
}

func isCyrillic(r rune) bool { return r >= 0x0400 && r <= 0x04FF }

// latinNeighbour reports whether the rune next to position i is an ASCII
// letter on either side.
func latinNeighbour(rs []rune, i int) bool {
	if i > 0 && isASCIILetter(rs[i-1]) {
		return true
	}
	return i+1 < len(rs) && isASCIILetter(rs[i+1])
}

func isASCIILetter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

// cp1251Latin1 returns the letter this Cyrillic rune's CP1251 byte means in
// Latin-1, or 0 where that byte is not a letter — the repair is for vowels
// that lost their identity, and turning a Cyrillic letter into a pilcrow
// would be a different kind of wrong.
func cp1251Latin1(r rune) rune {
	var b byte
	switch {
	case r >= 0x0410 && r <= 0x044F: // А-я, contiguous in both
		b = byte(0xC0 + (r - 0x0410))
	default:
		return 0
	}
	latin := rune(b)
	if !unicode.IsLetter(latin) {
		return 0
	}
	return latin
}
