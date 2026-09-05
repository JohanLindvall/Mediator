package library

import "testing"

// A release directory is very often named with the year in front of it,
// because a file listing has nowhere else to put one. A card does, so it is
// lifted out — but only where it really is a year in front of a title.
func TestSplitYear(t *testing.T) {
	for _, c := range []struct {
		in   string
		year int
		rest string
	}{
		{"2018 - Nattravn (Single)", 2018, "Nattravn (Single)"},
		{"2000 - Nattvind [demo]", 2000, "Nattvind [demo]"},
		{"1994 Fennmoor", 1994, "Fennmoor"},
		{"(2018) Nattravn", 2018, "Nattravn"},
		{"[1996] Skymning", 1996, "Skymning"},
		{"2018-Nattravn", 2018, "Nattravn"},
		{"2018. Nattravn", 2018, "Nattravn"},
		{"2018_Nattravn", 2018, "Nattravn"},
		{"(1997)Stjärnfall", 1997, "Stjärnfall"},
		{"  1991 – Eld Och Aska  ", 1991, "Eld Och Aska"},

		// Left alone, each for its own reason.
		{"1979", 0, ""},               // the title is the year
		{"(2018)", 0, ""},             // nothing but the year
		{"2018Nattravn", 0, ""},       // a word that starts with digits
		{"1899 - Too Early", 0, ""},   // not a year a record has
		{"2100 - Too Late", 0, ""},    // nor that one
		{"44 Winters", 0, ""},         // two digits is not a year
		{"1984 - 1984", 1984, "1984"}, // both: the prefix goes, the title stays
		{"Nattravn", 0, ""},           // no year at all
		{"", 0, ""},                   // nothing at all
		{"01 - Track", 0, ""},         // a track number is not a year
		{"2018", 0, ""},               // a release called by its year
	} {
		year, rest := splitYear(c.in)
		if year != c.year || rest != c.rest {
			t.Errorf("splitYear(%q) = %d, %q; want %d, %q", c.in, year, rest, c.year, c.rest)
		}
	}
}

// A year at the end has to be bracketed. A name ending in a bare number is
// far more likely to mean it — a sequel, a catalogue number, a title that is
// simply a number.
func TestSplitTrailingYear(t *testing.T) {
	for _, c := range []struct {
		in   string
		year int
		rest string
	}{
		{"Nattravn (2018)", 2018, "Nattravn"},
		{"Skymning [1996]", 1996, "Skymning"},
		{"Stjärnfall  (1997)", 1997, "Stjärnfall"},

		{"Marsh Road 2049", 0, ""},      // a title that ends in a number
		{"Nattravn (2018", 0, ""},       // not closed
		{"(2018)", 0, ""},               // nothing left
		{"Nattravn (18)", 0, ""},        // not a year
		{"Nattravn (2018) Live", 0, ""}, // not at the end
	} {
		year, rest := splitTrailingYear(c.in)
		if year != c.year || rest != c.rest {
			t.Errorf("splitTrailingYear(%q) = %d, %q; want %d, %q", c.in, year, rest, c.year, c.rest)
		}
	}
}

// The tags outrank the directory: someone typed the folder name, and a year
// in a tag came off the release. But the name loses its prefix either way —
// it is the same fact written twice, and the card has a place for it.
func TestLiftYear(t *testing.T) {
	a := &Album{Name: "2018 - Nattravn (Single)"}
	liftYear(a)
	if a.Name != "Nattravn (Single)" || a.Year != 2018 {
		t.Errorf("got %q / %d", a.Name, a.Year)
	}

	tagged := &Album{Name: "2018 - Nattravn", Year: 2019}
	liftYear(tagged)
	if tagged.Name != "Nattravn" || tagged.Year != 2019 {
		t.Errorf("the tags must win the year: got %q / %d", tagged.Name, tagged.Year)
	}

	plain := &Album{Name: "Nattravn", Year: 2018}
	liftYear(plain)
	if plain.Name != "Nattravn" || plain.Year != 2018 {
		t.Errorf("nothing to lift: got %q / %d", plain.Name, plain.Year)
	}
}
