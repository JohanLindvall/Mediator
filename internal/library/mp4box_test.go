package library

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/rartest"
)

// box builds one ISO box: size, four-character type, payload.
func box(typ string, parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out[:4], uint32(8+len(body)))
	copy(out[4:8], typ)
	return append(out, body...)
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// track builds a trak whose handler and sample format are as given.
func track(handler, format string) []byte {
	hdlr := box("hdlr",
		make([]byte, 4), // version + flags
		make([]byte, 4), // pre_defined
		[]byte(handler),
		make([]byte, 12), // reserved
		[]byte{0},        // empty name
	)
	stsd := box("stsd",
		make([]byte, 4),               // version + flags
		u32(1),                        // one entry
		box(format, make([]byte, 78)), // the sample entry itself
	)
	return box("trak", box("mdia", hdlr, box("minf", box("stbl", stsd))))
}

func mp4With(traks ...[]byte) []byte {
	f := box("ftyp", []byte("isom"), u32(512), []byte("isomiso2"))
	return append(f, box("moov", traks...)...)
}

func TestVideoSampleFormat(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"hevc as ffmpeg writes it", mp4With(track("vide", "hev1")), "hev1"},
		{"hevc as Apple wants it", mp4With(track("vide", "hvc1")), "hvc1"},
		{"h264", mp4With(track("vide", "avc1")), "avc1"},
		// A file's first track is as often the sound as the picture.
		{"sound first", mp4With(track("soun", "mp4a"), track("vide", "hev1")), "hev1"},
		{"no video track", mp4With(track("soun", "mp4a")), ""},
		{"no moov", box("ftyp", []byte("isom")), ""},
		{"not an mp4 at all", []byte("this is not a container\n"), ""},
		{"truncated", mp4With(track("vide", "hev1"))[:20], ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newBytesReaderAt(c.data)
			if got, _, _, _, _ := sampleInfo(r, int64(len(c.data))); got != c.want {
				t.Fatalf("sample format = %q, want %q", got, c.want)
			}
		})
	}
}

// And through the item, which is how the server asks.
func TestVideoSampleFormatOfAnItem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	data := mp4With(track("vide", "hev1"))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	it := Item{Path: path, Size: int64(len(data))}
	if got := VideoSampleFormat(it); got != "hev1" {
		t.Errorf("VideoSampleFormat = %q, want hev1", got)
	}
	if got := VideoSampleFormat(Item{Path: filepath.Join(dir, "gone.mp4"), Size: 10}); got != "" {
		t.Errorf("a file that is not there = %q, want empty", got)
	}
}

type bytesReaderAt []byte

func newBytesReaderAt(b []byte) bytesReaderAt { return bytesReaderAt(b) }

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(b)) {
		return 0, os.ErrInvalid
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, os.ErrInvalid
	}
	return n, nil
}

// Reading the label goes through OpenItem, so a member of a rar set answers
// as readily as a plain file — including when the box that carries it sits
// past the payload, in a later volume, and can only be reached by seeking.
func TestVideoSampleFormatInsideRarSet(t *testing.T) {
	dir := t.TempDir()
	data := append(box("ftyp", []byte("isom")), box("mdat", make([]byte, 300_000))...)
	data = append(data, box("moov", track("vide", "hev1"))...)
	rartest.WriteSet(t, dir, "movie", "Film.mp4", data, 3, true)

	lib := quietLib(dir)
	lib.Scan(nil)

	items := lib.List(Query{Kind: KindVideo}).Items
	if len(items) != 1 {
		t.Fatalf("indexed %d videos, want the archived member", len(items))
	}
	it := items[0]
	if !it.Archived() {
		t.Fatalf("item %q is not the archived member", it.Rel)
	}
	if got := VideoSampleFormat(it); got != "hev1" {
		t.Errorf("VideoSampleFormat of an archived member = %q, want hev1", got)
	}
}
