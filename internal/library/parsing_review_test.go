package library

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/rartest"
)

// A member header with no bytes of the member behind it — a split at an
// exact volume boundary, a set still arriving — is a zero-length segment,
// and a read that landed on one read nothing and asked again for ever.
func TestStoredReaderSkipsEmptySegments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vol")
	data := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	e := &storedEntry{name: "member", size: int64(len(data)), segs: []storedSeg{
		{path: path, off: 0, n: 10},
		{path: path, off: 10, n: 0}, // the empty one
		{path: path, off: 10, n: 26},
	}}
	r := newStoredReader(e)
	defer r.Close()
	got := make([]byte, len(data))
	if _, err := r.ReadAt(got, 0); err != nil {
		t.Fatalf("ReadAt across the empty segment: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read %q, want the whole member", got)
	}
	// And a read starting exactly where the empty segment sat.
	tail := make([]byte, 5)
	if _, err := r.ReadAt(tail, 10); err != nil || string(tail) != "abcde" {
		t.Fatalf("read at the seam = %q, %v", tail, err)
	}
	// An entry that claims more than its segments hold ends with an error,
	// not a loop.
	short := &storedEntry{name: "short", size: 100, segs: []storedSeg{{path: path, off: 0, n: 10}}}
	rs := newStoredReader(short)
	defer rs.Close()
	if _, err := rs.ReadAt(make([]byte, 50), 0); err == nil {
		t.Fatal("a claim past the segments read without error")
	}
}

// Two members of one name would share an id, the id being hashed from the
// path, and the second would silently replace the first in the index. The
// second is refused and reported; the first is served.
func TestRarDuplicateMemberIsRefused(t *testing.T) {
	dir := t.TempDir()
	first := rartest.Payload(3000)
	second := rartest.Payload(4000)
	write := func(name string, payload []byte, part int) {
		data := rartest.Volume4("Same.mkv", int64(len(payload)), payload, 0, false, false)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("twice.part%d.rar", part)), data, 0o644); err != nil {
			t.Fatal(err)
		}
		_ = name
	}
	write("Same.mkv", first, 1)
	write("Same.mkv", second, 2)
	entries, skipped, err := parseRarSet(filepath.Join(dir, "twice.part1.rar"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].size != int64(len(first)) {
		t.Fatalf("served %d entries, want the first member alone", len(entries))
	}
	if len(skipped) != 1 || skipped[0].name != "Same.mkv" {
		t.Fatalf("skipped = %+v, want the duplicate reported once", skipped)
	}
}

// The legacy numbering runs .r00 to .r99 and then moves to the next letter;
// a set of more than a hundred volumes used to stop at ninety-nine.
func TestRarLegacyVolumesPastAHundred(t *testing.T) {
	dir := t.TempDir()
	touch := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	touch("set.rar")
	for n := 0; n <= 99; n++ {
		touch(fmt.Sprintf("set.r%02d", n))
	}
	touch("set.s00")
	touch("set.s01")
	vols := rarVolumes(filepath.Join(dir, "set.rar"))
	if len(vols) != 103 || filepath.Base(vols[102]) != "set.s01" {
		t.Fatalf("%d volumes, last %s; want 103 ending in set.s01", len(vols), filepath.Base(vols[len(vols)-1]))
	}
	if !isRarRelated(filepath.Join(dir, "set.s01")) {
		t.Error("a volume past the hundredth is not recognised as one")
	}
	if got := rarFirstVolumeOf(filepath.Join(dir, "set.s01")); filepath.Base(got) != "set.rar" {
		t.Errorf("first volume of set.s01 = %s", filepath.Base(got))
	}
	// The other scheme pads its numbers, and the first volume keeps the width.
	if got := rarFirstVolumeOf(filepath.Join(dir, "release.part07.rar")); filepath.Base(got) != "release.part01.rar" {
		t.Errorf("first volume of part07 = %s", filepath.Base(got))
	}
	if !isRarFirstVolume(filepath.Join(dir, "release.part001.rar")) || isRarFirstVolume(filepath.Join(dir, "release.part002.rar")) {
		t.Error("the first volume is the one numbered one, however padded")
	}
}

func mvhdBytes(version byte, scale uint32, dur uint64) []byte {
	b := make([]byte, 32)
	b[0] = version
	if version == 1 {
		binary.BigEndian.PutUint32(b[20:24], scale)
		binary.BigEndian.PutUint64(b[24:32], dur)
	} else {
		binary.BigEndian.PutUint32(b[12:16], scale)
		binary.BigEndian.PutUint32(b[16:20], uint32(dur))
	}
	return b
}

// The 64-bit movie header, the "unknown" sentinel, and a corrupt duration
// that would have wrapped into a plausible length.
func TestMP4MovieHeaderVariants(t *testing.T) {
	if got := mp4Mvhd(sliceReaderAt(mvhdBytes(1, 1000, 90_000)), 0); got != 90_000 {
		t.Errorf("version 1: %d ms, want 90000", got)
	}
	if got := mp4Mvhd(sliceReaderAt(mvhdBytes(0, 1000, 0xFFFFFFFF)), 0); got != 0 {
		t.Errorf("unknown duration: %d ms, want 0", got)
	}
	if got := mp4Mvhd(sliceReaderAt(mvhdBytes(1, 1000, math.MaxUint64-5)), 0); got != 0 {
		t.Errorf("a corrupt duration read as %d ms", got)
	}
}

// The stem is matched at rune boundaries and set off by one of three
// separators; a longer word that merely begins with it is not a match.
func TestMatchStemSeparators(t *testing.T) {
	for _, c := range []struct {
		base, stem, suffix string
		ok                 bool
	}{
		{"An Episode", "An Episode", "", true},
		{"An Episode.srt", "An Episode", "srt", true},
		{"An Episode.en.srt", "An Episode", "en.srt", true},
		{"An Episode_sv.srt", "An Episode", "sv.srt", true},
		{"An Episode-forced.srt", "An Episode", "forced.srt", true},
		{"an episode.EN.srt", "An Episode", "EN.srt", true},
		{"Sjövakt.sv.srt", "Sjövakt", "sv.srt", true},
		{"Sjövakts.srt", "Sjövakt", "", false},
		{"Tides.srt", "Tide", "", false},
		{"Sjövakt", "Sjö", "", false},
	} {
		suffix, ok := matchStem(c.base, c.stem)
		if ok != c.ok || suffix != c.suffix {
			t.Errorf("matchStem(%q, %q) = %q, %v; want %q, %v", c.base, c.stem, suffix, ok, c.suffix, c.ok)
		}
	}
}

// A seek before the first indexed moment is the beginning, and one past the
// sentinel clamps to it.
func TestSeekByteOutsideTheIndex(t *testing.T) {
	it := Item{stored: &storedEntry{seek: []seekPoint{{ms: 60_000, off: 4096}, {ms: 120_000, off: 8192}}}}
	if _, ok := SeekByte(it, 30); ok {
		t.Error("before the first point is the beginning, not a seek")
	}
	if got, ok := SeekByte(it, 90); !ok || got != 6144 {
		t.Errorf("half way between two points = %d, %v; want 6144", got, ok)
	}
	if got, ok := SeekByte(it, 600); !ok || got != 8192 {
		t.Errorf("past the end = %d, %v; want the last point", got, ok)
	}
}

// The disc's clock at 29.97 frames a second, and a byte that is not BCD.
func TestDVDTimeVariants(t *testing.T) {
	ntsc := []byte{0x01, 0x02, 0x03, 3<<6 | 0x15} // 1:02:03 and 15 frames
	want := int64(3723_000) + 15*1001*1000/30000
	if got := dvdTimeMs(ntsc); got != want {
		t.Errorf("29.97 fps: %d, want %d", got, want)
	}
	if got := dvdTimeMs([]byte{0x01, 0xAB, 0x03, 1 << 6}); got != 0 {
		t.Errorf("an invalid BCD byte read as %d", got)
	}
}

// The VBRI header, which some encoders write instead of Xing, at its fixed
// offset; and a FLAC whose STREAMINFO is not the first metadata block.
func TestMP3DurationVBRI(t *testing.T) {
	frame := make([]byte, 200)
	frame[0], frame[1], frame[2], frame[3] = 0xFF, 0xFB, 0x90, 0x40 // MPEG1 L3 44.1 kHz stereo
	copy(frame[36:40], "VBRI")
	binary.BigEndian.PutUint32(frame[50:54], 2000)
	want := int64(2000) * 1152 * 1000 / 44100
	if got := mp3FromFrame(frame, 0); got != want {
		t.Errorf("vbri: %d, want %d", got, want)
	}
}

func TestFLACStreamInfoNotFirst(t *testing.T) {
	si := make([]byte, 34)
	si[10], si[11], si[12] = 0x0A, 0xC4, 0x40 // 44100
	binary.BigEndian.PutUint32(si[14:18], 882_000)
	var buf bytes.Buffer
	buf.WriteString("fLaC")
	buf.Write([]byte{1, 0, 0, 8}) // a PADDING block first
	buf.Write(make([]byte, 8))
	buf.Write([]byte{0x80, 0, 0, 34}) // STREAMINFO, last block
	buf.Write(si)
	if got := flacDuration(sliceReaderAt(buf.Bytes()), int64(buf.Len())); got != 20_000 {
		t.Errorf("flac with padding first: %d, want 20000", got)
	}
}

// The timestamp fixer refuses a negative position as the stored reader does.
func TestProgramStreamSeekRefusesNegative(t *testing.T) {
	r := &psFix{src: nopFile{}, size: 100, delta: map[int]int64{}}
	if _, err := r.Seek(-1, io.SeekStart); err == nil {
		t.Error("a negative position was accepted")
	}
	if pos, err := r.Seek(-10, io.SeekEnd); err != nil || pos != 90 {
		t.Errorf("Seek(-10, end) = %d, %v", pos, err)
	}
}

type nopFile struct{}

func (nopFile) ReadAt([]byte, int64) (int, error) { return 0, io.EOF }
func (nopFile) Read([]byte) (int, error)          { return 0, io.EOF }
func (nopFile) Seek(int64, int) (int64, error)    { return 0, nil }
func (nopFile) Close() error                      { return nil }
