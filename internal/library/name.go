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
// returned as it is unless it is a misreading that can be put back
// (reinterpretParts), and unallocated, which is nearly every name.
//
// Decoding byte by byte rather than the whole string at once is what makes a
// half-broken name — most of it UTF-8, one stray byte from somewhere else —
// come out with the good part intact.
func displayText(s string) string {
	if utf8.ValidString(s) {
		return reinterpretParts(s)
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

// Text that was read in the wrong encoding and then written out again.
//
// Everything above is about bytes that are not UTF-8, which at least say by
// being invalid that something is wrong. The worse case is text that was
// read wrongly and *stored*: a download tool, an archiver or a tagger took
// some bytes for Windows-1252 (or Latin-1), and wrote what it thought it saw
// back out as perfectly valid UTF-8. Nothing is invalid any more; the letters
// are simply the wrong letters. Two shapes of it turn up here:
//
//   - **UTF-8 read as Windows-1252.** Every character that is more than one
//     byte in UTF-8 comes out as two to four Western ones — "ö" as "Ã¶", a
//     closing quote as "â€™", a Thai letter as "à¸" and one more. Measured
//     over this library's 253,575 names: 155, and every one of them a real
//     misreading — quotes and dashes, fullwidth question marks from web
//     downloads, accents, emoji, and whole names in Thai, Cyrillic, Arabic
//     and Japanese. A handful had been through it twice.
//   - **TIS-620 read as Latin-1**, the same Thai misreading the tags suffer
//     (see above), stored in a file's name: 290 pictures in three folders.
//
// Both are undone the same way: the text is turned back into the bytes the
// misreading made it from (westernBytes), and those bytes are read the way
// they were meant. It can only work where every character is one a Western
// reading produces, so genuine Thai, Cyrillic or Chinese — written properly —
// is refused at its first letter and left alone.
//
// Whether the bytes were UTF-8 is not a guess: UTF-8 has a structure — a lead
// byte and then exactly the continuation bytes it announces — which Western
// text almost never has by accident, since an accented capital would have to
// be followed immediately by a symbol from the top of the table. Almost never
// is not never, and the one false reading found here says what it looks like:
// a title tag of the shape "WolfÒ‘s Den", whose two bytes spell a Ukrainian
// letter — "Wolfґs". A letter of another alphabet against a Latin one is
// not a word in either (strandedLetter, the rule reinterpretCyrillic
// already reasons by), so a reading that leaves one is refused.

// misreadRounds is how many times one text is put back: a name misread twice
// takes two rounds ("Ã¢â‚¬â„¢" to "â€™" to a quote). None here needed more;
// the bound only stops a loop.
const misreadRounds = 3

// reinterpretParts puts back each part of a path on its own. One folder
// damaged and its neighbours not is the ordinary case, and a part that was
// never a misreading — a Thai folder written properly — must not stop the
// part that was from being put back.
func reinterpretParts(s string) string {
	if !mayBeMisread(s) {
		return s
	}
	parts := strings.Split(s, "/")
	changed := false
	for i, p := range parts {
		if r := reinterpret(p); r != p {
			parts[i] = r
			changed = true
		}
	}
	if !changed {
		return s
	}
	return strings.Join(parts, "/")
}

// mayBeMisread is the cheap question asked of every name first. A misreading
// of UTF-8 always leaves a lead byte's letter behind (U+00C2 to U+00F4), and
// one of TIS-620 is made of U+00A1 to U+00FB, so text holding nothing in
// U+00A1 to U+00FF has nothing to put back — which is nearly every name.
func mayBeMisread(s string) bool {
	for _, r := range s {
		if r >= 0xA1 && r <= 0xFF {
			return true
		}
	}
	return false
}

// reinterpret puts back one piece of text that was misread, or returns it as
// it is.
func reinterpret(s string) string {
	for range misreadRounds {
		b, ok := westernBytes(s)
		if !ok {
			return s
		}
		if !utf8.Valid(b) {
			// Not UTF-8, so possibly Thai: the tag reader's leavings, or a
			// name stored the same way. After the UTF-8 question, not
			// before, since UTF-8's Thai is made of TIS-620's range too.
			if thai := decodeTIS620(b); thai != "" {
				return thai
			}
			return s
		}
		t := string(b)
		if t == s || strandedLetter(t) {
			return s
		}
		s = t
	}
	return s
}

// westernBytes turns text back into the bytes a Windows-1252 reading would
// have made it from — a Latin-1 character is its own byte, and Windows keeps
// its quotation marks and the like in 0x80-0x9F — or says it cannot have been
// made that way: a character neither table holds is proof the text was never
// a misreading. The five bytes Windows leaves undefined arrive as the control
// characters of the same number, which is what a Windows reading makes of
// them, and go back as themselves.
func westernBytes(s string) ([]byte, bool) {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r < 0x100 {
			b = append(b, byte(r))
			continue
		}
		c, ok := cp1252Byte[r]
		if !ok {
			return nil, false
		}
		b = append(b, c)
	}
	return b, true
}

// cp1252Byte is cp1252High the other way round, for the characters above
// U+00FF that Windows-1252 puts in a single byte.
var cp1252Byte = func() map[rune]byte {
	m := make(map[rune]byte, len(cp1252High))
	for i, r := range cp1252High {
		if r >= 0x100 {
			m[r] = byte(0x80 + i)
		}
	}
	return m
}()

// strandedLetter reports whether a putting-back left a letter of another
// alphabet directly against a Latin one: Greek, Cyrillic, Armenian, Hebrew,
// Arabic — the scripts two bytes of UTF-8 reach, which are where a Western
// accident spells something. Three-byte scripts are not asked about: Chinese
// and Japanese sit against Latin letters in ordinary titles, and an accident
// spelling three bytes of UTF-8 is too unlikely to guard against.
func strandedLetter(s string) bool {
	rs := []rune(s)
	for i, r := range rs {
		if r >= 0x0370 && r <= 0x07FF && unicode.IsLetter(r) && latinNeighbour(rs, i) {
			return true
		}
	}
	return false
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
