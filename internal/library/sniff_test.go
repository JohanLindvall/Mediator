// SPDX-License-Identifier: MIT

package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/rartest"
)

// A download whose name says nothing is invisible to a library that reads only
// names — members of one release, hundreds of megabytes each, plainly MP4 from
// their first eight bytes.
func TestKindOfMagic(t *testing.T) {
	// Each case is the opening bytes as the format writes them.
	mp4 := func(brand string) []byte {
		return append([]byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p'}, []byte(brand+"\x00\x00\x02\x00")...)
	}
	riff := func(form string) []byte {
		return append([]byte("RIFF\x00\x00\x00\x00"), []byte(form)...)
	}
	ts := func(packets int) []byte {
		b := make([]byte, 188*3)
		for i := range packets {
			b[188*i] = 0x47
		}
		return b
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
		{"a Video CD's MPEG, in its RIFF", riff("CDXA"), KindVideo},
		{"an MPEG program stream's pack header", []byte{0, 0, 1, 0xba, 0x44}, KindVideo},
		{"a transport stream: three packets' sync bytes", ts(3), KindVideo},
		{"one 0x47 is not a transport stream", ts(1), ""},
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

// An archive is one whatever it is called: a ZIP's first local header, a
// RAR's marker in either of its versions. Anything else is no archive here.
func TestArchiveOfMagic(t *testing.T) {
	for _, c := range []struct {
		head []byte
		want string
	}{
		{[]byte("PK\x03\x04\x14\x00"), "zip"},
		{[]byte("Rar!\x1a\x07\x00\xcf"), "rar"},
		{[]byte("Rar!\x1a\x07\x01\x00\x33"), "rar"},
		{[]byte("PK\x05\x06"), ""}, // an empty archive's end record: nothing in it
		{[]byte("7z\xbc\xaf\x27\x1c"), ""},
		{nil, ""},
	} {
		if got := archiveOfMagic(c.head); got != c.want {
			t.Errorf("archiveOfMagic(% x) = %q; want %q", c.head, got, c.want)
		}
	}
}

// What is read and what is not. A name with no extension, or with one this
// library does not know, has its opening read; a name the library knows has
// answered; a download still being written is left until it is renamed whole;
// a part of an archive set is its set's to answer for; and nothing under the
// floor is opened at all.
func TestSniffingReadsWhatItShould(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, sniffMinSize+16)
	copy(big, []byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	zip := make([]byte, sniffMinSize+16)
	copy(zip, "PK\x03\x04\x14\x00")
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, c := range []struct {
		name string
		data []byte
		want Sniffed
	}{
		{"Dep", big, Sniffed{Kind: KindVideo}},
		{"Pale.Harrow.2019.WEB", big, Sniffed{Kind: KindVideo}},
		{"Lee Shore - Saltings.a", big, Sniffed{Kind: KindVideo}},
		{"Saltings Box", zip, Sniffed{Archive: "zip"}},
		{"film.mkv", big, Sniffed{}},
		{"film.mp4.part", big, Sniffed{}},
		{"film.mp4.crdownload", big, Sniffed{}},
		{"set.z01", zip, Sniffed{}},
		{"set.zip.002", zip, Sniffed{}},
		{"small", big[:1024], Sniffed{}},
	} {
		p := write(c.name, c.data)
		if got := SniffContent(p, int64(len(c.data))); got != c.want {
			t.Errorf("%s: read as %+v; want %+v", c.name, got, c.want)
		}
	}
	if got := SniffContent(filepath.Join(dir, "gone"), sniffMinSize*2); got != (Sniffed{}) {
		t.Errorf("a missing file read as %+v", got)
	}
}

// End to end: the walk indexes what it read, under the kind it read — and
// leaves out what it should.
func TestScanIndexesANamelessFile(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, sniffMinSize+16)
	copy(big, []byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	for name, data := range map[string][]byte{
		"Dep":                  big,
		"Pale.Harrow.2019.WEB": big,
		// The two that must stay out: a download still being written, and a
		// file too small to be worth opening.
		"Pale.Harrow.2019.mp4.part": big,
		"lock":                      big[:512],
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l := quietLib(dir)
	l.Scan(nil)
	res := l.List(Query{})
	got := map[string]Kind{}
	for _, it := range res.Items {
		got[it.Name] = it.Kind
	}
	if len(got) != 2 || got["Dep"] != KindVideo || got["Pale.Harrow.2019.WEB"] != KindVideo {
		t.Errorf("indexed %v; want the two films and nothing else", got)
	}
}

// An archive is one whatever it is called. A ZIP with no extension — the shape
// a release came in, several hundred megabytes and named like its title — has
// its members indexed as any archive's are, by the walk and by the watcher.
func TestAnArchiveWithNoNameIsRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Saltings Box")
	writeZip(t, path, zipMember{name: "01 Lee Shore.mp3", data: []byte(silentFrames(2600))})
	l := quietLib(dir)
	l.Scan(nil)
	member := PathID(path + "\x00" + "01 Lee Shore.mp3")
	if it, ok := l.Get(member); !ok || it.Kind != KindAudio {
		t.Fatalf("the walk did not index the member of a nameless archive: %+v %v", it, ok)
	}

	// The watcher, for one that arrives later.
	later := filepath.Join(dir, "Windward Box")
	writeZip(t, later, zipMember{name: "02 Windward.mp3", data: []byte(silentFrames(2600))})
	l.AddFile(later)
	if _, ok := l.Get(PathID(later + "\x00" + "02 Windward.mp3")); !ok {
		t.Error("the watcher did not index the member of a nameless archive")
	}
}

// And a RAR the same way: one volume, named like the release it holds.
func TestARarWithNoNameIsRead(t *testing.T) {
	dir := t.TempDir()
	vols := rartest.WriteSet(t, dir, "night tide", "Night Tide.mkv", rartest.Payload(sniffMinSize+4096), 1, true)
	path := filepath.Join(dir, "Night Tide")
	if err := os.Rename(vols[0], path); err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.Scan(nil)
	if it, ok := l.Get(PathID(path + "\x00" + "Night Tide.mkv")); !ok || it.Kind != KindVideo {
		t.Fatalf("the member of a nameless RAR was not indexed: %+v %v", it, ok)
	}
}
