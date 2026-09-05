package library

import (
	"os"
	"path/filepath"
	"testing"
)

// A download that arrives with no extension at all is invisible to a library
// that reads only names — three members of one release, hundreds of
// megabytes each, plainly MP4 from their first eight bytes.
func TestKindOfMagic(t *testing.T) {
	// Each case is the opening bytes as the format writes them.
	mp4 := func(brand string) []byte {
		return append([]byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p'}, []byte(brand+"\x00\x00\x02\x00")...)
	}
	riff := func(form string) []byte {
		return append([]byte("RIFF\x00\x00\x00\x00"), []byte(form)...)
	}
	for _, c := range []struct {
		why  string
		head []byte
		want Kind
	}{
		{"the shape the report came in: ISO base media, isom brand", mp4("isom"), KindVideo},
		{"and the other brands a film arrives under", mp4("mp42"), KindVideo},
		{"an audio-only brand is the one exception", mp4("M4A "), KindAudio},
		{"EBML: Matroska or WebM, both video here", []byte{0x1a, 0x45, 0xdf, 0xa3, 1, 2, 3}, KindVideo},
		{"AVI inside a RIFF", riff("AVI "), KindVideo},
		{"and WAVE inside the same wrapper", riff("WAVE"), KindAudio},
		{"WebP too, which is a picture", riff("WEBP"), KindImage},
		{"ASF, which is what wmv and wma both are", []byte{0x30, 0x26, 0xb2, 0x75, 0, 0}, KindVideo},
		{"Ogg", []byte("OggS\x00\x02\x00\x00"), KindAudio},
		{"FLAC", []byte("fLaC\x00\x00\x00\x22"), KindAudio},
		{"an ID3 tag, which is how an mp3 usually opens", []byte("ID3\x04\x00\x00"), KindAudio},
		{"JPEG", []byte{0xff, 0xd8, 0xff, 0xe0}, KindImage},
		{"PNG", []byte("\x89PNG\r\n\x1a\n"), KindImage},
		{"GIF", []byte("GIF89a"), KindImage},
		// Nothing is guessed. A wrong answer here indexes a disk image or a
		// database as a film and hands it to a player.
		{"a zlib object, which is most of what has no extension", []byte{0x78, 0x9c, 1, 2, 3, 4, 5, 6}, ""},
		{"plain text", []byte("Once upon a time in a"), ""},
		{"a RIFF that is neither", riff("XXXX"), ""},
		{"nothing at all", nil, ""},
		{"too little to tell", []byte{0, 0, 0}, ""},
	} {
		if got := kindOfMagic(c.head); got != c.want {
			t.Errorf("kindOfMagic(% x) = %q; want %q — %s", c.head, got, c.want, c.why)
		}
	}
}

// The two guards are what make this affordable. Measured across these disks:
// 9,024 files carry no extension and 37 of them are a megabyte or more, so
// the floor turns nine thousand opens into thirty-seven — and a file that
// *did* name itself is never opened at all, having already answered.
func TestClassifyContentOnlyReadsWhatItShould(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, sniffMinSize+16)
	copy(big, []byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// The case this exists for.
	p := write("Dep", big)
	if got := ClassifyContent(p, int64(len(big))); got != KindVideo {
		t.Errorf("a nameless MP4 read as %q; want video", got)
	}

	// A name that said something has answered, whatever is inside it: a
	// .nfo is not media, and sniffing it would be a read per file for an
	// answer already given.
	p = write("notes.nfo", big)
	if got := ClassifyContent(p, int64(len(big))); got != "" {
		t.Errorf("a file with an extension was sniffed and read as %q", got)
	}

	// Under the floor, whatever it looks like: an extensionless small file
	// is far likelier to be a repository object than anything to watch.
	small := big[:1024]
	p = write("small", small)
	if got := ClassifyContent(p, int64(len(small))); got != "" {
		t.Errorf("a file under the floor was read as %q", got)
	}

	// And nothing is claimed about what cannot be opened.
	if got := ClassifyContent(filepath.Join(dir, "gone"), sniffMinSize*2); got != "" {
		t.Errorf("a missing file read as %q", got)
	}
}

// End to end: the walk indexes it, and the kind is the sniffed one.
func TestScanIndexesANamelessFile(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, sniffMinSize+16)
	copy(big, []byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	if err := os.WriteFile(filepath.Join(dir, "Dep"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	// Beside it, the two kinds that must stay out: one that named itself
	// something else, and one too small to be worth opening.
	if err := os.WriteFile(filepath.Join(dir, "release.nfo"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lock"), big[:512], 0o644); err != nil {
		t.Fatal(err)
	}

	l := quietLib(dir)
	l.Scan(nil)
	res := l.List(Query{})
	if res.Total != 1 {
		names := make([]string, 0, len(res.Items))
		for _, it := range res.Items {
			names = append(names, it.Name)
		}
		t.Fatalf("indexed %d items %v; want only the nameless film", res.Total, names)
	}
	if it := res.Items[0]; it.Name != "Dep" || it.Kind != KindVideo {
		t.Errorf("indexed %q as %q; want Dep as video", it.Name, it.Kind)
	}
}
