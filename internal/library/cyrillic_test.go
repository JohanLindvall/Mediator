package library

import "testing"

// A tagger that guessed Russian on a Nordic release wrote the right byte
// under the wrong alphabet: 0xF6 is ö in Latin-1 and ц in CP1251, and by the
// time it reaches us it is a Cyrillic letter in UTF-16 with nothing in the
// bytes to say so.
func TestReinterpretCyrillic(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		// Invented phrases in the shapes the damaged tags had: every letter
		// the swap can touch, upper and lower case, alone and in company.
		{"Fyrsnц", "Fyrsnö"},
		{"Grцn Fyrvдg", "Grön Fyrväg"},
		{"Дngsfyr Slut", "Ängsfyr Slut"},
		{"Bеtfyr Gеr", "Båtfyr Går"},
		{"Hшstfyr vшg", "Høstfyr vøg"},
		{"fyrvдgens ljus", "fyrvägens ljus"},
		// Nothing to do.
		{"Nattvind Pt.1", "Nattvind Pt.1"},
		{"", ""},
	} {
		if got := reinterpretCyrillic(c.in); got != c.want {
			t.Errorf("reinterpretCyrillic(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The same library holds a great deal of real Russian, and a repair that
// took it for damage would be far worse than the damage: these have to come
// back exactly as they are. What separates them is company — a Cyrillic
// letter pressed against a Latin one is a byte read twice differently, and a
// Cyrillic word standing on its own is a word.
func TestReinterpretCyrillicLeavesRussianAlone(t *testing.T) {
	for _, s := range []string{
		// Every shape here mirrors a real one — a Cyrillic word against a
		// Latin tag with punctuation between, a site name in front of a
		// bilingual title, a whole sentence — with the names invented.
		"Маяки-LIVE-2021",
		"clipsite.example - The River - Река.flv",
		"The Gate - Ворота.flv",
		"Маяк над рекой",
		"Тихий берег",
		// A whole title in Cyrillic, punctuation and all.
		"Ночная запись (LIVE)",
	} {
		if got := reinterpretCyrillic(s); got != s {
			t.Errorf("reinterpretCyrillic(%q) = %q, want it unchanged", s, got)
		}
	}
}

// The repair is for letters that lost their identity. A Cyrillic letter whose
// CP1251 byte is punctuation in Latin-1 is left alone: turning one into a
// pilcrow is a different kind of wrong, and the byte is far more likely to
// be what it says it is.
func TestReinterpretCyrillicOnlyLetters(t *testing.T) {
	// Ё is 0xA8 in CP1251, which is a diaeresis in Latin-1.
	if got := reinterpretCyrillic("aЁb"); got != "aЁb" {
		t.Errorf("got %q, want it unchanged", got)
	}
}

// cleanTag is the single door metadata comes through, and it has to do all
// of this at once: trim, decode, and put back what was read as the wrong
// alphabet.
func TestCleanTagRepairsCyrillic(t *testing.T) {
	if got := cleanTag("  Fyrsnц "); got != "Fyrsnö" {
		t.Errorf("cleanTag = %q, want %q", got, "Fyrsnö")
	}
}

// ID3v1 numbered its genres and ID3v2 kept the number as a reference in
// front of the text, so a frame meaning "genre 138, which is Black Metal,
// and here is its name" comes back from a reader that expands the number and
// keeps the text as the name written twice.
func TestCleanGenre(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Black Metal Black Metal", "Black Metal"},
		{"Metal Metal", "Metal"},
		{"Death Metal Death Metal", "Death Metal"},
		{"black metal Black Metal", "black metal"},
		// Left alone: a genre that merely has an even number of words, one
		// that repeats only in part, and the ordinary case.
		{"Melodic Death Metal", "Melodic Death Metal"},
		{"Atmospheric Black Metal", "Atmospheric Black Metal"},
		{"Death Metal Black Metal", "Death Metal Black Metal"},
		{"Black Metal", "Black Metal"},
		{"Rock", "Rock"},
		{"", ""},
	} {
		if got := cleanGenre(c.in); got != c.want {
			t.Errorf("cleanGenre(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A genre tag is one field doing several jobs. Whitespace has to fold, or a
// double space is a second card of one album beside a card of nine hundred;
// and a tag that lists genres has to be read as the list it is.
func TestCleanGenreWhitespace(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Black  Metal", "Black Metal"},
		{"Black Metal", "Black Metal"}, // a non-breaking space looks like a space
		{"  Black Metal  ", "Black Metal"},
		{"Black\tMetal", "Black Metal"},
		// The numeric-genre expansion, in both the forms taggers write it.
		{"Black Metal Black Metal", "Black Metal"},
		{"Black Metal/Black Metal", "Black Metal"},
		{"black metal/Black Metal", "black metal"},
		// Left alone: a compound name is one genre, whatever the punctuation.
		{"Black/Death Metal", "Black/Death Metal"},
		{"Melodic Death Metal", "Melodic Death Metal"},
	} {
		if got := cleanGenre(c.in); got != c.want {
			t.Errorf("cleanGenre(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitGenres(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"Death Metal | Viking Metal", []string{"Death Metal", "Viking Metal"}},
		{"Rock;Hard Rock;Metal", []string{"Rock", "Hard Rock", "Metal"}},
		{"Black Metal, Punk", []string{"Black Metal", "Punk"}},
		// A slash is part of the name, not a separator: splitting this would
		// invent a genre called "Black", which is a truncation this library
		// already suffers from.
		{"Black/Death Metal", []string{"Black/Death Metal"}},
		{"Rock/Hard Rock/Metal", []string{"Rock/Hard Rock/Metal"}},
		// One genre named twice is one genre.
		{"Black Metal | black metal", []string{"Black Metal"}},
		{"Black Metal", []string{"Black Metal"}},
		{"", nil},
		{" | ; , ", []string{}},
	} {
		got := splitGenres(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitGenres(%q) = %q, want %q", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitGenres(%q) = %q, want %q", c.in, got, c.want)
				break
			}
		}
	}
}
