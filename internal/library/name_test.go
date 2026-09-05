package library

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

func TestDisplayTextDecodesWhatIsNotUTF8(t *testing.T) {
	cases := []struct{ in, want string }{
		// Valid UTF-8 is returned untouched, umlauts included.
		{"Köln.mp4", "Köln.mp4"},
		{"plain.mp4", "plain.mp4"},
		{"", ""},
		// Latin-1: the byte is the code point.
		{"K\xf6ln.mp4", "Köln.mp4"},
		{"Priv\xe9_club.mp4", "Privé_club.mp4"},
		{"\xc5ret.mp4", "Året.mp4"},
		// Windows-1252 differs from Latin-1 exactly in 0x80-0x9F.
		{"It\x92s here.mp4", "It’s here.mp4"},
		{"a\x80b", "a€b"},
		// A name that is mostly UTF-8 with one stray byte keeps the good part.
		{"Køln – K\xf6ln.mp4", "Køln – Köln.mp4"},
	}
	for _, c := range cases {
		if got := displayText(c.in); got != c.want {
			t.Errorf("displayText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The whole point of decoding for display only: the file still opens.
func TestNonUTF8NameShownDecodedAndStillOpens(t *testing.T) {
	dir := t.TempDir()
	raw := "Shopping_In_K\xf6ln.mp4" // as the filesystem spells it
	write(t, filepath.Join(dir, raw), "video")

	l := quietLib(dir)
	l.Scan(nil)

	res := l.List(Query{})
	if res.Total != 1 {
		t.Fatalf("indexed %d items, want 1", res.Total)
	}
	it := res.Items[0]
	if it.Name != "Shopping_In_Köln.mp4" {
		t.Errorf("name = %q, want it decoded for display", it.Name)
	}
	if filepath.Base(it.Rel) != "Shopping_In_Köln.mp4" {
		t.Errorf("display path = %q, want it decoded too", it.Rel)
	}
	// The path kept for opening the file is the filesystem's own bytes.
	if filepath.Base(it.Path) != raw {
		t.Errorf("path = %q, want the raw name %q", it.Path, raw)
	}
	f, err := OpenItem(it)
	if err != nil {
		t.Fatalf("opening the item failed: %v", err)
	}
	defer f.Close()
	if b, err := io.ReadAll(f); err != nil || string(b) != "video" {
		t.Errorf("read back %q (%v), want the file's contents", b, err)
	}

	// And it can be searched for by the word as it is spelled, which the
	// undecoded bytes could never match.
	if n := l.List(Query{Search: "köln"}).Total; n != 1 {
		t.Errorf("search for the decoded word = %d hits, want 1", n)
	}
}

// Subtitles are found by comparing stems against real directory entries, so
// that match has to keep using the undecoded name.
func TestSubtitlesFoundForNonUTF8VideoName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Le_Caf\xe9.mkv"), "video")
	write(t, filepath.Join(dir, "Le_Caf\xe9.en.srt"), "1\n00:00:00,000 --> 00:00:01,000\nhi\n")

	l := quietLib(dir)
	l.Scan(nil)

	res := l.List(Query{Kind: KindVideo})
	if res.Total != 1 {
		t.Fatalf("indexed %d videos, want 1", res.Total)
	}
	subs := l.Subtitles(res.Items[0])
	if len(subs) != 1 {
		t.Fatalf("found %d subtitles, want 1", len(subs))
	}
	if subs[0].Lang != "en" {
		t.Errorf("subtitle language = %q, want en", subs[0].Lang)
	}
}

// A release directory named in Latin-1 reaches the albums view readable, and
// is searchable by the word it spells.
func TestAlbumNameDecoded(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.Join(dir, "Sj\xf6vakt - Dimman")
	write(t, filepath.Join(rel, "01 army of me.mp3"), "audio")
	write(t, filepath.Join(rel, "02 hyperballad.mp3"), "audio")

	l := quietLib(dir)
	l.Scan(nil)

	albums := l.SearchAlbums(AlbumQuery{Sort: "name", Desc: false})
	if len(albums) != 1 {
		t.Fatalf("built %d albums, want 1", len(albums))
	}
	if albums[0].Name != "Sjövakt - Dimman" {
		t.Errorf("album name = %q, want it decoded", albums[0].Name)
	}
	if n := len(l.SearchAlbums(AlbumQuery{Search: "sjövakt", Sort: "name", Desc: false})); n != 1 {
		t.Errorf("album search for the decoded word = %d, want 1", n)
	}
}

// The warm start builds names from the stored path without going through
// upsert, so it has to decode them the same way.
func TestNonUTF8NameSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "To_K\xf6ln.mp4"), "video")
	dbPath := filepath.Join(t.TempDir(), "media.db")

	db, err := openTestDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	flushNow(l, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := openTestDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	l2 := quietLib(dir)
	if n := l2.LoadFromDB(db2); n != 1 {
		t.Fatalf("restored %d items, want 1", n)
	}
	res := l2.List(Query{})
	if res.Items[0].Name != "To_Köln.mp4" {
		t.Errorf("restored name = %q, want it decoded", res.Items[0].Name)
	}
	if n := l2.List(Query{Search: "köln"}).Total; n != 1 {
		t.Errorf("restored index not searchable by the decoded word (%d hits)", n)
	}
}

// What actually happened to a library holding such files: an older build
// wrote the mirrored index as JSON, which cannot carry a byte that is not
// valid UTF-8, so the stored path came back with U+FFFD where the letter
// was. The id still hashed to the real path, so the walk found the item
// already present and left it alone — and reconciliation then dropped it,
// because nothing had walked the path it claimed. The file vanished from the
// library on the next restart.
func TestScanRepairsLossilyStoredPath(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "To_K\xf6ln.mp4")
	write(t, raw, "video")
	st, err := os.Stat(raw)
	if err != nil {
		t.Fatal(err)
	}

	// Exactly the transformation the old encoder applied.
	enc, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var damaged string
	if err := json.Unmarshal(enc, &damaged); err != nil {
		t.Fatal(err)
	}
	if damaged == raw {
		t.Fatal("the test's premise is gone: the path survived a JSON round trip")
	}

	db, err := openTestDB(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SaveItems([]blob.Item{{
		ID: PathID(raw), Path: damaged, Kind: string(KindVideo),
		Size: st.Size(), MTime: st.ModTime().UnixMilli(),
	}}, nil); err != nil {
		t.Fatal(err)
	}

	l := quietLib(dir)
	l.SetMetaDB(db)
	if n := l.LoadFromDB(db); n != 1 {
		t.Fatalf("restored %d items, want 1", n)
	}
	l.Scan(nil)

	res := l.List(Query{})
	if res.Total != 1 {
		t.Fatalf("listed %d items after the scan, want the file kept", res.Total)
	}
	it := res.Items[0]
	if it.Path != raw {
		t.Errorf("path = %q, want it repaired to %q", it.Path, raw)
	}
	if it.Name != "To_Köln.mp4" {
		t.Errorf("name = %q, want it decoded", it.Name)
	}
	if n := l.List(Query{Search: "köln"}).Total; n != 1 {
		t.Errorf("search = %d hits, want the repaired text indexed", n)
	}
	f, err := OpenItem(it)
	if err != nil {
		t.Fatalf("the repaired item does not open: %v", err)
	}
	f.Close()
}

// Thai turns up in these libraries as often as Latin-1 does, and read as
// Latin-1 it comes out as a string of accented Roman letters.
func TestThaiIsReadAsThai(t *testing.T) {
	// The bytes as TIS-620 writes them, and what they say.
	cases := []struct{ tis620, want string }{
		// Invented phrases in the real shape: unbroken runs of TIS-620
		// high bytes, exactly as Thai is written.
		{"\xbd\xb9\xb5\xa1\xcb\xb9\xd1\xa1", "ฝนตกหนัก"},
		{"\xc2\xd1\xa7\xe4\xc1\xe8\xc1\xd2", "ยังไม่มา"},
		// Thai with ASCII beside it, which is how a track is usually named —
		// the sites that tag their names onto files being half of why.
		{"\xb5\xd2\xc1\xe3\xa8\xa9\xd1\xb9 example.com", "ตามใจฉัน example.com"},
	}
	for _, c := range cases {
		if got := displayText(c.tis620); got != c.want {
			t.Errorf("displayText(% x) = %q, want %q", c.tis620, got, c.want)
		}
		// And through a tag, where the reader has already made Latin-1 of it.
		latin1 := make([]rune, 0, len(c.tis620))
		for i := 0; i < len(c.tis620); i++ {
			latin1 = append(latin1, rune(c.tis620[i]))
		}
		if got := cleanTag(string(latin1)); got != c.want {
			t.Errorf("cleanTag(%q) = %q, want %q", string(latin1), got, c.want)
		}
	}
}

// The point of the run test: European names keep their accents rather than
// being read as an alphabet they have nothing to do with.
func TestWesternNamesAreNotMistakenForThai(t *testing.T) {
	cases := []struct{ in, want string }{
		{"K\xf6ln.mp4", "Köln.mp4"},
		{"Priv\xe9_club.mp4", "Privé_club.mp4"},
		{"Sj\xf6vakt - Dimman", "Sjövakt - Dimman"},
		{"\xc5\xc4\xd6", "ÅÄÖ"},              // three in a row is not enough
		{"Sm\xf6rg\xe5sbord", "Smörgåsbord"}, // accents among letters
		{"It\x92s here", "It’s here"},        // Windows-1252 punctuation
		{"\xe9\xe8\xea caf\xe9", "éèê café"}, // three, then a break
	}
	for _, c := range cases {
		if got := displayText(c.in); got != c.want {
			t.Errorf("displayText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Thai already in UTF-8 is left exactly as it is.
	const utf8Thai = "ทางบ้าน 642"
	if got := displayText(utf8Thai); got != utf8Thai {
		t.Errorf("displayText of UTF-8 Thai = %q, want it untouched", got)
	}
	if got := cleanTag(utf8Thai); got != utf8Thai {
		t.Errorf("cleanTag of UTF-8 Thai = %q, want it untouched", got)
	}
}

// Every byte has to have a reading. TIS-620 leaves some unassigned, and a
// name carrying one of those crashed the server on the way up — the whole
// library restored through this on every start, so one such name anywhere
// was enough.
func TestThaiWithUnassignedBytes(t *testing.T) {
	for _, b := range []byte{0xDB, 0xDC, 0xDD, 0xDE, 0xFC, 0xFD, 0xFE, 0xFF} {
		in := "\xbe\xd9\xb4\xe3\xcb" + string([]byte{b}) + "\xe9\xb9"
		got := displayText(in) // must not panic, and must not drop the byte
		if got == "" {
			t.Errorf("byte %#x produced nothing", b)
		}
		if !utf8.ValidString(got) {
			t.Errorf("byte %#x produced invalid UTF-8: %q", b, got)
		}
	}
	// And the same through a tag, which arrives already read as Latin-1.
	if got := cleanTag("¾Ù´ãËÛ饹"); got == "" || !utf8.ValidString(got) {
		t.Errorf("a tag with an unassigned byte = %q", got)
	}
}
