package library

import (
	"bytes"
	"io"
	"testing"
)

// The field layouts, which are where this lives or dies: one marker bit in
// the wrong place produces a file nothing will play.
func TestSCRRoundTrip(t *testing.T) {
	for _, v := range []int64{0, 1, 90_000, 1 << 20, 1<<33 - 1, 3_000_000_000} {
		b := make([]byte, 6)
		// The markers and the '01' tag a real pack header carries.
		b[0] = 0x44
		b[2] = 0x04
		b[4] = 0x04
		b[5] = 0x01
		writeSCR(b, v)
		got, ok := readSCR(b)
		if !ok || got != v {
			t.Errorf("SCR %d came back as %d (ok=%v)", v, got, ok)
		}
		// Everything that is not the value must survive untouched.
		if b[0]&0xC0 != 0x40 {
			t.Errorf("the tag was overwritten: %08b", b[0])
		}
		if b[0]&0x04 == 0 || b[2]&0x04 == 0 || b[4]&0x04 == 0 || b[5]&0x01 == 0 {
			t.Errorf("a marker bit was lost: % 08b", b)
		}
	}
}

func TestTSRoundTrip(t *testing.T) {
	for _, tag := range []byte{0x20, 0x30, 0x10} { // PTS-only, PTS-of-pair, DTS
		for _, v := range []int64{0, 1, 90_000, 1<<33 - 1, 7_777_777_777} {
			b := []byte{tag | 0x01, 0, 0x01, 0, 0x01}
			writeTS(b, v)
			got, ok := readTS(b)
			if !ok || got != v {
				t.Errorf("tag %#x: %d came back as %d (ok=%v)", tag, v, got, ok)
			}
			if b[0]&0xF0 != tag {
				t.Errorf("tag %#x became %#x", tag, b[0]&0xF0)
			}
		}
	}
}

func TestTSRejectsWhatIsNotOne(t *testing.T) {
	// Without its marker bits it is not a timestamp, and writing over it
	// would corrupt whatever it really is.
	if _, ok := readTS([]byte{0x21, 0, 0x00, 0, 0x01}); ok {
		t.Error("a missing marker bit was accepted")
	}
	if _, ok := readSCR([]byte{0x00, 0, 0, 0, 0, 0}); ok {
		t.Error("a pack header tag of 00 was accepted")
	}
}

// pack builds one 2048-byte DVD sector: a pack header with the given clock
// reference, and inside it one video packet carrying a PTS and a DTS.
func pack(scr, pts, dts int64) []byte {
	s := make([]byte, psSector)
	copy(s, psPackStart[:])
	s[4], s[6], s[8], s[9] = 0x44, 0x04, 0x04, 0x01
	writeSCR(s[4:10], scr)
	s[10], s[11], s[12] = 0x01, 0x00, 0x03 // mux rate and its markers
	s[13] = 0xF8                           // no stuffing

	p := 14
	copy(s[p:], psAnyStart[:])
	s[p+3] = 0xE0 // first video stream
	body := 3 + 10
	s[p+4], s[p+5] = byte(body>>8), byte(body)
	s[p+6] = 0x80 // MPEG-2 PES
	s[p+7] = 0xC0 // PTS and DTS both present
	s[p+8] = 10   // header data length
	b := s[p+9:]
	b[0], b[2], b[4] = 0x31, 0x01, 0x01
	writeTS(b[0:5], pts)
	b[5], b[7], b[9] = 0x11, 0x01, 0x01
	writeTS(b[5:10], dts)
	return s
}

// The whole point: a title whose cells each start counting from zero comes
// out counting straight through.
func TestFixTimestampsStraightensTheClock(t *testing.T) {
	// Two cells. The second restarts its clock near zero, which is what a
	// disc authored from separate pieces does and what makes a player
	// report five minutes for a film of three hours.
	first := pack(0, 3_600, 3_600)
	second := pack(90_000, 93_600, 93_600) // one second into the first cell
	third := pack(0, 3_600, 3_600)         // the second cell, back to zero
	fourth := pack(90_000, 93_600, 93_600)
	raw := bytes.Join([][]byte{first, second, third, fourth}, nil)

	e := &storedEntry{
		name: "Feature.vob",
		size: int64(len(raw)),
		seek: []seekPoint{
			{ms: 0, off: 0},
			{ms: 10_000, off: 2 * psSector}, // the second cell starts ten seconds in
			{ms: 20_000, off: 4 * psSector},
		},
	}
	it := Item{stored: e}
	f := FixTimestamps(it, &fakeFile{data: raw})
	out, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(raw) {
		t.Fatalf("the file changed length: %d, was %d", len(out), len(raw))
	}

	// Every clock reading, in order, in 90 kHz units.
	var got []int64
	for s := 0; s+psSector <= len(out); s += psSector {
		v, ok := readSCR(out[s+4 : s+10])
		if !ok {
			t.Fatalf("sector %d is no longer a pack", s/psSector)
		}
		got = append(got, v)
	}
	want := []int64{0, 90_000, 900_000, 990_000}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pack %d: clock %d, want %d", i, got[i], want[i])
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Errorf("the clock still goes backwards at pack %d: %d then %d", i, got[i-1], got[i])
		}
	}
	// And the presentation times moved with it.
	if pts, ok := readTS(out[2*psSector+23 : 2*psSector+28]); !ok || pts != 903_600 {
		t.Errorf("the third pack's PTS is %d (ok=%v), want 903600", pts, ok)
	}
}

// A range request has to come back corrected too, and identical to the same
// bytes read any other way — that is what keeps a resumed download whole.
func TestFixTimestampsReadsRanges(t *testing.T) {
	raw := bytes.Join([][]byte{
		pack(0, 3_600, 3_600), pack(90_000, 93_600, 93_600),
		pack(0, 3_600, 3_600), pack(90_000, 93_600, 93_600),
	}, nil)
	e := &storedEntry{size: int64(len(raw)), seek: []seekPoint{
		{ms: 0, off: 0}, {ms: 10_000, off: 2 * psSector}, {ms: 20_000, off: 4 * psSector},
	}}
	whole, err := io.ReadAll(FixTimestamps(Item{stored: e}, &fakeFile{data: raw}))
	if err != nil {
		t.Fatal(err)
	}
	// Every offset and length, including ones that begin and end inside a
	// sector, which is what a player's range requests actually look like.
	for _, c := range []struct{ off, n int }{
		{0, 100}, {1, 100}, {2047, 2}, {2048, 2048}, {3000, 4000}, {5000, 3192},
	} {
		f := FixTimestamps(Item{stored: e}, &fakeFile{data: raw})
		buf := make([]byte, c.n)
		n, err := f.ReadAt(buf, int64(c.off))
		if err != nil && err != io.EOF {
			t.Fatalf("ReadAt(%d, %d): %v", c.off, c.n, err)
		}
		if !bytes.Equal(buf[:n], whole[c.off:c.off+n]) {
			t.Errorf("range %d+%d differs from the whole read", c.off, c.n)
		}
	}
}

// Anything that is not a stitched title is handed back exactly as it was.
func TestFixTimestampsLeavesOtherThingsAlone(t *testing.T) {
	f := &fakeFile{data: []byte("not a program stream")}
	if got := FixTimestamps(Item{}, f); got != File(f) {
		t.Error("a plain file was wrapped")
	}
	// A rar member has no cell table, so there is nothing to straighten.
	e := &storedEntry{size: 10}
	if got := FixTimestamps(Item{stored: e}, f); got != File(f) {
		t.Error("content with no cell table was wrapped")
	}
}

// fakeFile is content in memory, with the shape OpenItem hands back.
type fakeFile struct {
	data []byte
	pos  int64
}

func (f *fakeFile) Read(p []byte) (int, error) {
	n, err := f.ReadAt(p, f.pos)
	f.pos += int64(n)
	return n, err
}

func (f *fakeFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeFile) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.pos = off
	case io.SeekCurrent:
		f.pos += off
	case io.SeekEnd:
		f.pos = int64(len(f.data)) + off
	}
	return f.pos, nil
}

func (f *fakeFile) Close() error { return nil }
