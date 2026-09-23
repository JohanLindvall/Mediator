package library

import (
	"path/filepath"
	"testing"
)

// Text that was read in the wrong encoding and written out again is valid
// UTF-8 holding the wrong letters, and is put back. The damaged forms here
// were made by Python's own Windows-1252 codec rather than by the table the
// repair uses, so a mistake in that table cannot pass both ways; the phrases
// are invented, in the shapes the library holds.
func TestAMisreadNameIsPutBack(t *testing.T) {
	cases := []struct{ in, want string }{
		// UTF-8 Thai read as Windows-1252: each letter three Western ones,
		// and the byte Windows leaves undefined as a control character —
		// the shape of the name that was reported.
		{"@à¸\u009dà¸™à¸•à¸\u0081à¸«à¸™à¸±à¸\u0081 4.mp4", "@ฝนตกหนัก 4.mp4"},
		// An accent, a closing quote and an emoji, which is most of it.
		{"KÃ¶ln.mp4", "Köln.mp4"},
		{"Itâ€™s here.mp4", "It’s here.mp4"},
		{"Beach ðŸŒŠ.webm", "Beach 🌊.webm"},
		// Whole names in other alphabets.
		{"æ\u009d±ã\u0081®æµ·.mp4", "東の海.mp4"},
		{"Ð¢Ð¸Ñ…Ð¸Ð¹_Ð±ÐµÑ€ÐµÐ³.mp4", "Тихий_берег.mp4"},
		{"Ø¨Ø­Ø± Ù‡Ø§Ø¯Ø¦.mp4", "بحر هادئ.mp4"},
		// Misread twice, and put back twice.
		{"ItÃ¢â‚¬â„¢s here.mp4", "It’s here.mp4"},
		// TIS-620 Thai read as Latin-1 and stored: the tags' old trouble,
		// found in the names of a set of pictures.
		{"½¹µ¡Ë¹Ñ¡ - 001.jpg", "ฝนตกหนัก - 001.jpg"},
		// Each part of a path on its own: a Thai folder written properly
		// does not stop the name inside it from being put back.
		{"ทางบ้าน/KÃ¶ln.mp4", "ทางบ้าน/Köln.mp4"},
	}
	for _, c := range cases {
		if got := displayText(c.in); got != c.want {
			t.Errorf("displayText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// What was never misread is left exactly as it is: Western text does not
// spell UTF-8 by accident, and text in another alphabet cannot have come out
// of a Western reading at all.
func TestTextThatWasNeverMisreadIsLeftAlone(t *testing.T) {
	for _, s := range []string{
		"Köln.mp4",
		"Smörgåsbord",
		"Grön Fyrväg",
		"ÅÄÖ",
		"It’s here",
		"Café – Été.mp4",
		"東の海.mp4",
		"ทางบ้าน 642",
		"Тихий берег",
		"plain.mp4",
		"",
		// The one false reading measured: two Western characters spelling a
		// Ukrainian letter in the middle of an English word. A letter of
		// another alphabet against a Latin one is not a word in either.
		"WolfÒ‘s Den",
	} {
		if got := displayText(s); got != s {
			t.Errorf("displayText(%q) = %q, want it untouched", s, got)
		}
	}
}

// Tags come through the same repair, since a tagger misreads exactly as a
// download tool does.
func TestAMisreadTagIsPutBack(t *testing.T) {
	for in, want := range map[string]string{
		"SchneerÃ¤umer": "Schneeräumer",
		"WolfÒ‘s Den":   "WolfÒ‘s Den",
		// And a tag misread as Cyrillic is still the Cyrillic repair's.
		"Fyrsnц": "Fyrsnö",
	} {
		if got := cleanTag(in); got != want {
			t.Errorf("cleanTag(%q) = %q, want %q", in, got, want)
		}
	}
}

// On disk: the name is shown and searched as it was meant, and the file
// still opens, its path being the filesystem's own bytes.
func TestAMisreadFileNameIsShownAndFound(t *testing.T) {
	dir := t.TempDir()
	raw := "@à¸\u009dà¸™à¸•à¸\u0081à¸«à¸™à¸±à¸\u0081 4.mp4"
	write(t, filepath.Join(dir, raw), "video")
	l := quietLib(dir)
	l.Scan(nil)

	res := l.List(Query{Search: "ฝนตก"})
	if res.Total != 1 {
		t.Fatalf("a search for the Thai found %d items, want 1", res.Total)
	}
	it := res.Items[0]
	if it.Name != "@ฝนตกหนัก 4.mp4" {
		t.Errorf("name = %q, want it put back", it.Name)
	}
	if filepath.Base(it.Rel) != "@ฝนตกหนัก 4.mp4" {
		t.Errorf("display path = %q, want it put back too", it.Rel)
	}
	if filepath.Base(it.Path) != raw {
		t.Errorf("path = %q, want the name as the filesystem spells it", it.Path)
	}
	f, err := OpenItem(it)
	if err != nil {
		t.Fatalf("the file no longer opens: %v", err)
	}
	f.Close()
}
