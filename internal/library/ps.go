package library

import (
	"fmt"
	"io"
	"sort"
	"sync"
)

// Straightening out a DVD title's clock.
//
// A disc is authored from separate pieces, and each piece starts its own
// clock near zero. Concatenated into one title — which is what a title *is*,
// the disc holding no other copy of it — the timestamps run forwards inside
// each cell and jump backwards at every join. Measured on one release: 0.25 s
// at the start, 2501 s a gigabyte in, 1140 s at two gigabytes, 733 s at four.
//
// Nothing downstream can make sense of that. A player takes the last
// timestamp minus the first and calls it the duration — 5:40 for a title of
// 2h57m — and seeking, which is a search through timestamps, has nothing to
// search. The app itself is unaffected: it seeks by byte position from the
// disc's own cell table (see SeekByte) and reads the duration from the IFO.
// It is the *download* that suffers, being a file handed to a player that has
// none of that.
//
// So the clock is straightened as the bytes go out. Three things make it a
// rewrite rather than a conversion:
//
//   - **Every pack is one 2048-byte sector.** A DVD guarantees it, and it
//     holds across a stitched title — verified at four points spanning four
//     gigabytes, sixteen sectors of sixteen at each. So there is no scanning
//     for start codes: step 2048 bytes and the next pack is there.
//   - **The fields are fixed width.** SCR in the pack header, PTS and DTS in
//     the PES headers, all 33-bit values at known offsets with marker bits
//     between the pieces. Rewriting one cannot change its length, so the file
//     that comes out is the same size as the one that went in — which is what
//     keeps Content-Length honest and Range requests working.
//   - **The joins are known, not guessed.** The cell table already read from
//     the IFO says where each cell begins and how long it plays, so the
//     correction at each join is arithmetic. Detecting discontinuities by
//     looking for backward jumps would be a heuristic, and a wrong guess
//     would put a whole cell in the wrong place.
//
// What is deliberately left alone: the navigation packs (stream 0xBF), whose
// PCI and DSI structures carry presentation times of their own. They matter
// to a DVD player walking the disc's menus and mean nothing in a bare title
// file, which is what this produces.

const (
	// psSector is a DVD pack: exactly one 2048-byte sector, always.
	psSector = 2048
	// psClock is the 90 kHz clock every timestamp in a program stream counts.
	psClock = 90
	// psMask is the width of one: 33 bits, wrapping after about 26.5 hours,
	// which is longer than any title.
	psMask = int64(1)<<33 - 1
)

var (
	psPackStart = [4]byte{0x00, 0x00, 0x01, 0xBA}
	psAnyStart  = [3]byte{0x00, 0x00, 0x01}
)

// psFix rewrites the clock of a stitched DVD title as it is read.
//
// It is an io.ReaderAt as well as a reader: http.ServeContent asks for
// ranges, and a rewrite that could only be read from the beginning would
// cost the download its resumability to buy it a seek.
type psFix struct {
	src  File
	size int64
	// cells are where each one begins, in bytes and in the 90 kHz clock the
	// stream counts. Taken from the disc's own table.
	cells []seekPoint

	mu    sync.Mutex
	pos   int64
	delta map[int]int64 // per cell, what to add to every timestamp in it
}

// FixTimestamps wraps content whose clock does not run straight so that what
// comes out of it does. Anything else is handed back untouched: this is for
// DVD titles, which are the only thing here assembled out of pieces that each
// start counting from zero.
func FixTimestamps(it Item, f File) File {
	if it.stored == nil || len(it.stored.seek) < 2 {
		return f
	}
	return &psFix{src: f, size: it.stored.size, cells: it.stored.seek, delta: map[int]int64{}}
}

func (r *psFix) Close() error { return r.src.Close() }

func (r *psFix) Seek(off int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch whence {
	case io.SeekStart:
		r.pos = off
	case io.SeekCurrent:
		r.pos += off
	case io.SeekEnd:
		r.pos = r.size + off
	}
	if r.pos < 0 {
		return 0, fmt.Errorf("negative position")
	}
	return r.pos, nil
}

func (r *psFix) Read(p []byte) (int, error) {
	r.mu.Lock()
	pos := r.pos
	r.mu.Unlock()
	n, err := r.ReadAt(p, pos)
	r.mu.Lock()
	r.pos += int64(n)
	r.mu.Unlock()
	return n, err
}

// ReadAt reads through the source and corrects whatever packs the answer
// covers. A request rarely lands on a sector boundary, so the enclosing
// sectors are read whole, corrected, and the wanted part sliced out of them.
func (r *psFix) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	lo := off - off%psSector
	hi := off + int64(len(p))
	if rem := hi % psSector; rem != 0 {
		hi += psSector - rem
	}
	if hi > r.size {
		hi = r.size
	}
	buf := make([]byte, hi-lo)
	n, err := r.src.ReadAt(buf, lo)
	buf = buf[:n]
	for s := 0; s+psSector <= len(buf); s += psSector {
		r.fixPack(buf[s:s+psSector], lo+int64(s))
	}
	// What the caller asked for, out of what was read.
	from := off - lo
	if from >= int64(len(buf)) {
		if err == nil {
			err = io.EOF
		}
		return 0, err
	}
	copied := copy(p, buf[from:])
	if copied < len(p) && err == nil {
		err = io.EOF
	}
	return copied, err
}

// fixPack corrects one sector in place, if it is a pack at all. The last
// sectors of a title need not be — padding is padding — and anything that is
// not a pack is left exactly as it was.
func (r *psFix) fixPack(sec []byte, at int64) {
	if len(sec) < 14 || [4]byte(sec[0:4]) != psPackStart {
		return
	}
	d, ok := r.deltaAt(at)
	if !ok || d == 0 {
		return
	}
	// The pack's own clock reference, then every timestamp in the packets
	// inside it.
	if scr, ok := readSCR(sec[4:10]); ok {
		writeSCR(sec[4:10], (scr+d)&psMask)
	}
	p := 14 + int(sec[13]&7)
	for p+6 <= len(sec) {
		if [3]byte(sec[p:p+3]) != psAnyStart {
			return
		}
		id := sec[p+3]
		length := int(sec[p+4])<<8 | int(sec[p+5])
		if length <= 0 || p+6+length > len(sec) {
			return
		}
		fixPES(sec[p+6:p+6+length], id, d)
		p += 6 + length
	}
}

// fixPES corrects the timestamps in one packet's header.
func fixPES(b []byte, id byte, d int64) {
	// Padding and the navigation streams carry no presentation clock worth
	// correcting — see the note at the top about what is left alone.
	if id == 0xBE || id == 0xBF || len(b) < 3 {
		return
	}
	if b[0]&0xC0 != 0x80 {
		return // not an MPEG-2 PES header
	}
	flags := b[1] >> 6
	hdrLen := int(b[2])
	if 3+hdrLen > len(b) {
		return
	}
	at := 3
	if flags&0x02 != 0 { // PTS present
		if at+5 > len(b) {
			return
		}
		if ts, ok := readTS(b[at : at+5]); ok {
			writeTS(b[at:at+5], (ts+d)&psMask)
		}
		at += 5
	}
	if flags&0x01 != 0 { // DTS present
		if at+5 > len(b) {
			return
		}
		if ts, ok := readTS(b[at : at+5]); ok {
			writeTS(b[at:at+5], (ts+d)&psMask)
		}
	}
}

// deltaAt is what every timestamp in the cell covering this byte has to have
// added to it: the difference between where the disc says the cell begins and
// where the cell's own clock happens to start.
//
// The cell's first clock reading is learned once, from its first pack, and
// remembered — a title has a couple of dozen cells, so this is a couple of
// dozen sector reads across a whole download.
func (r *psFix) deltaAt(at int64) (int64, bool) {
	i := sort.Search(len(r.cells), func(i int) bool { return r.cells[i].off > at }) - 1
	if i < 0 || i >= len(r.cells)-1 {
		return 0, false // before the first cell, or in the end sentinel
	}
	r.mu.Lock()
	d, known := r.delta[i]
	r.mu.Unlock()
	if known {
		return d, true
	}

	var sec [psSector]byte
	if _, err := r.src.ReadAt(sec[:], r.cells[i].off); err != nil {
		return 0, false // the disk may answer next time; nothing is remembered
	}
	first, ok := readSCR(sec[4:10])
	if [4]byte(sec[0:4]) != psPackStart || !ok {
		// Not a pack where the table said a cell begins: nothing to correct
		// in that cell, and the answer is kept — asking again was a two
		// kilobyte read on every sector of the cell for the whole download.
		r.mu.Lock()
		r.delta[i] = 0
		r.mu.Unlock()
		return 0, false
	}
	d = (r.cells[i].ms*psClock - first) & psMask

	r.mu.Lock()
	r.delta[i] = d
	r.mu.Unlock()
	return d, true
}

// readSCR reads the 33-bit clock reference out of a pack header. Its bits are
// laid out in three runs with marker bits between them, which is what the
// shifting below is picking apart.
func readSCR(b []byte) (int64, bool) {
	if len(b) < 6 || b[0]&0xC0 != 0x40 {
		return 0, false
	}
	v := int64(b[0]>>3&0x07)<<30 |
		int64(b[0]&0x03)<<28 |
		int64(b[1])<<20 |
		int64(b[2]>>3&0x1F)<<15 |
		int64(b[2]&0x03)<<13 |
		int64(b[3])<<5 |
		int64(b[4]>>3&0x1F)
	return v, true
}

// writeSCR puts one back, leaving every marker and the extension exactly as
// they were.
func writeSCR(b []byte, v int64) {
	b[0] = b[0]&0xC4 | byte(v>>30&0x07)<<3 | byte(v>>28&0x03)
	b[1] = byte(v >> 20)
	b[2] = b[2]&0x04 | byte(v>>15&0x1F)<<3 | byte(v>>13&0x03)
	b[3] = byte(v >> 5)
	b[4] = b[4]&0x07 | byte(v&0x1F)<<3
}

// readTS reads a 33-bit presentation or decoding timestamp from a PES header.
func readTS(b []byte) (int64, bool) {
	if len(b) < 5 || b[0]&0x01 == 0 || b[2]&0x01 == 0 || b[4]&0x01 == 0 {
		return 0, false // the marker bits say this is not one
	}
	return int64(b[0]>>1&0x07)<<30 |
		int64(b[1])<<22 |
		int64(b[2]>>1&0x7F)<<15 |
		int64(b[3])<<7 |
		int64(b[4]>>1&0x7F), true
}

// writeTS puts one back, keeping the four-bit tag that says which kind it is
// and every marker bit.
func writeTS(b []byte, v int64) {
	b[0] = b[0]&0xF0 | byte(v>>30&0x07)<<1 | 0x01
	b[1] = byte(v >> 22)
	b[2] = byte(v>>15&0x7F)<<1 | 0x01
	b[3] = byte(v >> 7)
	b[4] = byte(v&0x7F)<<1 | 0x01
}
