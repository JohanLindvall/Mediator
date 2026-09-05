package library

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Support for reading media out of uncompressed ("store" method) rar volume
// sets without extracting them — the classic split archive holding one huge
// video. Both RAR 4.x and RAR 5.x headers are parsed, just enough to find
// each stored member's data runs; compressed or encrypted members are
// skipped. A member spanning volumes becomes a list of (file, offset, len)
// segments that storedReader stitches into one random-access stream.

// storedSeg is one contiguous run of member data inside a volume file.
type storedSeg struct {
	path string
	off  int64
	n    int64
}

// storedEntry is content that lives inside other files as plain bytes: a
// store-method member of a rar set, or a title inside a disc image. Both are
// the same problem — a name, a size, and a list of byte ranges to stitch —
// so both are this, and one reader serves them.
type storedEntry struct {
	name string // member path as recorded in the container (slash-normalized)
	size int64  // unpacked size (== sum of segment lengths)
	segs []storedSeg
	// durationMs is what the container says the content is worth, where it
	// says anything — a DVD does, in its own information file, and it is the
	// only one that knows (see ifoTitle). 0 means nobody said.
	durationMs int64
	// seek maps moments in the content to the bytes they start at, for
	// content that cannot be seeked by timestamp. See SeekByte.
	seek []seekPoint
}

// seekPoint is one moment in the content and the byte it begins at.
type seekPoint struct {
	ms  int64
	off int64
}

var (
	rarSig4   = []byte("Rar!\x1a\x07\x00")
	rarSig5   = []byte("Rar!\x1a\x07\x01\x00")
	rarPartRe = regexp.MustCompile(`(?i)^(.*\.part)(\d+)(\.rar)$`)
	// Legacy numbering: foo.rar, foo.r00 … foo.r99, and past a hundred
	// volumes foo.s00 … — the letter advances.
	rarOldContRe = regexp.MustCompile(`(?i)\.[r-z]\d\d$`)
)

const (
	rarMaxVolumes = 1000
	rarMaxEntries = 10000
	rarMaxHeader  = 1 << 20
)

// isRarFirstVolume reports whether path names the first volume of a set (or
// a standalone .rar). Later volumes (.partN.rar with N > 1, .rNN) are
// reached through the first one and never indexed on their own.
func isRarFirstVolume(path string) bool {
	base := filepath.Base(path)
	if m := rarPartRe.FindStringSubmatch(base); m != nil {
		n, err := strconv.Atoi(m[2])
		return err == nil && n == 1
	}
	return strings.EqualFold(filepath.Ext(base), ".rar")
}

// isRarRelated reports whether path looks like any volume of a rar set.
func isRarRelated(path string) bool {
	base := filepath.Base(path)
	return strings.EqualFold(filepath.Ext(base), ".rar") || rarOldContRe.MatchString(base)
}

// rarFirstVolumeOf maps any volume path to its set's first volume.
func rarFirstVolumeOf(path string) string {
	base := filepath.Base(path)
	dir := filepath.Dir(path)
	if m := rarPartRe.FindStringSubmatch(base); m != nil {
		return filepath.Join(dir, m[1]+fmt.Sprintf("%0*d", len(m[2]), 1)+m[3])
	}
	if rarOldContRe.MatchString(base) {
		return filepath.Join(dir, base[:len(base)-4]+".rar")
	}
	return path
}

// rarVolumes enumerates the existing volume files of the set, in order.
func rarVolumes(first string) []string {
	vols := []string{first}
	base := filepath.Base(first)
	dir := filepath.Dir(first)
	if m := rarPartRe.FindStringSubmatch(base); m != nil {
		width := len(m[2])
		for n := 2; n <= rarMaxVolumes; n++ {
			p := filepath.Join(dir, m[1]+fmt.Sprintf("%0*d", width, n)+m[3])
			if _, err := os.Stat(p); err != nil {
				break
			}
			vols = append(vols, p)
		}
		return vols
	}
	stem := base[:len(base)-len(filepath.Ext(base))]
	// Legacy naming: foo.rar, foo.r00 … foo.r99, then foo.s00 … — a set of
	// more than a hundred volumes moves on to the next letter, and the
	// first name that is not there ends the set.
	for letter := 'r'; letter <= 'z'; letter++ {
		for n := 0; n <= 99; n++ {
			if len(vols) > rarMaxVolumes {
				return vols
			}
			p := filepath.Join(dir, fmt.Sprintf("%s.%c%02d", stem, letter, n))
			if _, err := os.Stat(p); err != nil {
				return vols
			}
			vols = append(vols, p)
		}
	}
	return vols
}

// parseRarSet parses all volumes of the set starting at first and returns
// its stored, unencrypted members. Entries whose segments do not add up to
// the recorded size (truncated or still-downloading sets) are dropped.
// rarSkip is a member the set holds that cannot be indexed, and why.
//
// It exists to answer "why is this release not in the library?", which is
// otherwise unanswerable from outside: a set whose only member is compressed
// parses without error and yields nothing, so it looks exactly like one that
// was never scanned. Twice now that question could only be settled by
// reading the archive by hand.
type rarSkip struct{ name, why string }

// parseRarSet reads the volume set and returns the members that can be
// served — store-method, unencrypted, complete — along with the ones that
// cannot and the reason, which the caller logs.
func parseRarSet(first string) ([]*storedEntry, []rarSkip, error) {
	var order []*storedEntry
	var skips []rarSkip
	open := make(map[string]*storedEntry)
	// One member, one reason, however many volumes it spans: a set of
	// thousands of unservable members used to walk the list per report.
	said := make(map[string]bool)
	skip := func(name, why string) {
		if said[name] {
			return
		}
		said[name] = true
		if len(skips) < rarMaxEntries {
			skips = append(skips, rarSkip{name, why})
		}
	}
	// A second member under a name already seen would share the first's
	// id — an id is hashed from the path — and silently replace it in the
	// index. It is refused, and so are its continuations.
	twice := make(map[string]bool)
	add := func(name string, unpSize int64, seg storedSeg, splitBefore bool) {
		if twice[name] {
			return
		}
		e := open[name]
		if e != nil && !splitBefore {
			twice[name] = true
			skip(name, "duplicate: a second member of this name in the set")
			return
		}
		if e == nil {
			if len(open) >= rarMaxEntries {
				return
			}
			e = &storedEntry{name: name, size: unpSize}
			open[name] = e
			order = append(order, e)
		}
		e.segs = append(e.segs, seg)
	}

	for _, vol := range rarVolumes(first) {
		if err := parseRarVolume(vol, add, skip); err != nil {
			return nil, skips, fmt.Errorf("%s: %w", filepath.Base(vol), err)
		}
	}

	var out []*storedEntry
	for _, e := range order {
		var got int64
		for _, s := range e.segs {
			got += s.n
		}
		if got != e.size || e.size == 0 {
			// The volumes hold less than the member says it is: a set still
			// arriving, or one missing a part. Not an error — the next scan
			// finds the rest — but worth saying, since from outside it looks
			// the same as a release nobody ever walked.
			skip(e.name, fmt.Sprintf("incomplete: %d of %d bytes across %d volumes", got, e.size, len(e.segs)))
			continue
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b *storedEntry) int { return strings.Compare(a.name, b.name) })
	return out, skips, nil
}

func parseRarVolume(path string, add func(string, int64, storedSeg, bool), skip func(string, string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var sig [8]byte
	if _, err := io.ReadFull(f, sig[:]); err != nil {
		return err
	}
	switch {
	case string(sig[:8]) == string(rarSig5):
		return parseRar5(f, path, add, skip)
	case string(sig[:7]) == string(rarSig4):
		return parseRar4(f, path, add, skip)
	}
	return fmt.Errorf("not a rar file")
}

// --- RAR 4.x -----------------------------------------------------------------

func parseRar4(f *os.File, path string, add func(string, int64, storedSeg, bool), skip func(string, string)) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	pos := int64(7)
	for pos+7 <= size {
		var h [7]byte
		if _, err := f.ReadAt(h[:], pos); err != nil {
			return err
		}
		typ := h[2]
		flags := binary.LittleEndian.Uint16(h[3:5])
		headSize := int64(binary.LittleEndian.Uint16(h[5:7]))
		if headSize < 7 || headSize > rarMaxHeader {
			return fmt.Errorf("bad block header size %d", headSize)
		}
		dataSize := int64(0)

		switch typ {
		case 0x74: // file header
			hdr := make([]byte, min(headSize, rarMaxHeader))
			if _, err := f.ReadAt(hdr, pos); err != nil {
				return err
			}
			b := hdr[7:]
			if len(b) < 25 {
				return fmt.Errorf("short file header")
			}
			packSize := int64(binary.LittleEndian.Uint32(b[0:4]))
			unpSize := int64(binary.LittleEndian.Uint32(b[4:8]))
			method := b[18]
			nameSize := int(binary.LittleEndian.Uint16(b[19:21]))
			nameOff := 25
			if flags&0x100 != 0 { // 64-bit sizes
				if len(b) < 33 {
					return fmt.Errorf("short file header")
				}
				packSize |= int64(binary.LittleEndian.Uint32(b[25:29])) << 32
				unpSize |= int64(binary.LittleEndian.Uint32(b[29:33])) << 32
				nameOff = 33
			}
			dataSize = packSize
			dir := flags&0xE0 == 0xE0
			encrypted := flags&0x04 != 0
			if !dir && nameOff+nameSize <= len(b) {
				name := string(b[nameOff : nameOff+nameSize])
				if flags&0x200 != 0 { // unicode variant: keep the ASCII part
					if i := strings.IndexByte(name, 0); i >= 0 {
						name = name[:i]
					}
				}
				name = strings.ReplaceAll(name, "\\", "/")
				switch {
				case encrypted:
					skip(name, "encrypted")
				case method != 0x30:
					// Compressed. Serving it would mean decompressing from
					// the first byte for every seek, which for a film is the
					// whole archive per scrub — so the answer is no, and
					// saying so is the only way anyone can find that out.
					skip(name, fmt.Sprintf("compressed (method 0x%02x), not stored", method))
				default:
					add(name, unpSize, storedSeg{path: path, off: pos + headSize, n: packSize},
						flags&0x01 != 0)
				}
			}
		case 0x7B: // end of archive
			return nil
		default:
			if flags&0x8000 != 0 { // block carries data
				var as [4]byte
				if _, err := f.ReadAt(as[:], pos+7); err != nil {
					return err
				}
				dataSize = int64(binary.LittleEndian.Uint32(as[:]))
			}
		}
		pos += headSize + dataSize
	}
	return nil
}

// --- RAR 5.x -----------------------------------------------------------------

// rarVint reads a RAR5 variable-length integer at off.
func rarVint(f io.ReaderAt, off int64) (val int64, n int, err error) {
	var b [10]byte
	m, _ := f.ReadAt(b[:], off)
	for i := 0; i < m; i++ {
		val |= int64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return val, i + 1, nil
		}
	}
	return 0, 0, fmt.Errorf("bad vint")
}

func parseRar5(f *os.File, path string, add func(string, int64, storedSeg, bool), skip func(string, string)) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	pos := int64(8)
	for pos+7 <= size {
		headSize, n, err := rarVint(f, pos+4) // after header CRC32
		if err != nil {
			return err
		}
		if headSize <= 0 || headSize > rarMaxHeader {
			return fmt.Errorf("bad block header size %d", headSize)
		}
		headStart := pos + 4 + int64(n)
		hdr := make([]byte, headSize)
		if _, err := f.ReadAt(hdr, headStart); err != nil {
			return err
		}
		p := 0
		next := func() (int64, error) {
			v, vn, err := rarVint(sliceReaderAt(hdr), int64(p))
			p += vn
			return v, err
		}
		typ, err := next()
		if err != nil {
			return err
		}
		flags, err := next()
		if err != nil {
			return err
		}
		extraSize := int64(0)
		if flags&0x01 != 0 {
			if extraSize, err = next(); err != nil {
				return err
			}
		}
		dataSize := int64(0)
		if flags&0x02 != 0 {
			if dataSize, err = next(); err != nil {
				return err
			}
		}

		switch typ {
		case 2: // file header
			fileFlags, err := next()
			if err != nil {
				return err
			}
			unpSize, err := next()
			if err != nil {
				return err
			}
			if _, err = next(); err != nil { // attributes
				return err
			}
			if fileFlags&0x02 != 0 {
				p += 4 // mtime
			}
			if fileFlags&0x04 != 0 {
				p += 4 // data crc
			}
			comp, err := next()
			if err != nil {
				return err
			}
			if _, err = next(); err != nil { // host os
				return err
			}
			nameLen, err := next()
			if err != nil {
				return err
			}
			if nameLen < 0 || nameLen > 2048 || p+int(nameLen) > len(hdr) {
				return fmt.Errorf("bad name length")
			}
			name := strings.ReplaceAll(string(hdr[p:p+int(nameLen)]), "\\", "/")
			dir := fileFlags&0x01 != 0
			method := comp >> 7 & 7
			encrypted := hasRar5ExtraCrypt(hdr, headSize-extraSize, extraSize)
			if !dir {
				switch {
				case encrypted:
					skip(name, "encrypted")
				case method != 0:
					skip(name, fmt.Sprintf("compressed (method %d), not stored", method))
				default:
					add(name, unpSize, storedSeg{path: path, off: headStart + headSize, n: dataSize},
						flags&0x08 != 0) // split-before
				}
			}
		case 4: // archive encryption: headers unreadable from here on
			return fmt.Errorf("encrypted archive")
		case 5: // end of archive
			return nil
		}
		pos = headStart + headSize + dataSize
	}
	return nil
}

// hasRar5ExtraCrypt scans a header's extra area for a file-encryption record.
func hasRar5ExtraCrypt(hdr []byte, extraOff, extraSize int64) bool {
	if extraSize <= 0 || extraOff < 0 || extraOff+extraSize > int64(len(hdr)) {
		return false
	}
	p := extraOff
	for p < extraOff+extraSize {
		recSize, n, err := rarVint(sliceReaderAt(hdr), p)
		if err != nil || recSize <= 0 {
			return false
		}
		recType, m, err := rarVint(sliceReaderAt(hdr), p+int64(n))
		if err != nil {
			return false
		}
		_ = m
		if recType == 0x01 {
			return true
		}
		p += int64(n) + recSize
	}
	return false
}

// sliceReaderAt adapts a byte slice to io.ReaderAt for rarVint.
type sliceReaderAt []byte

func (s sliceReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(s)) {
		return 0, io.EOF
	}
	n := copy(p, s[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// --- stitched reader ------------------------------------------------------------

// rarMaxOpenFiles bounds how many volume files one reader holds open at a
// time. Sequential playback needs one (two while crossing a boundary), so a
// small cache keeps every realistic access pattern hitting an open file
// while a 100-volume set can never pin 100 descriptors per viewer.
const rarMaxOpenFiles = 4

// storedReader exposes a member's segments as one random-access stream. Volume
// files are opened lazily and closed least-recently-used first once more
// than rarMaxOpenFiles are open. Safe for concurrent use, as io.ReaderAt
// requires.
type storedReader struct {
	e *storedEntry
	// segs is the entry's list without its empty segments. A volume that
	// holds a member header and no bytes of it — a split at an exact
	// boundary, a set still arriving — yields one, and a read that lands on
	// it read nothing and asked again for ever.
	segs []storedSeg
	// cum is where each segment starts in the stitched stream, which readAt
	// binary-searches. It belongs to the reader rather than to the entry so
	// that an entry cannot be built without one — the reader is the only
	// thing that wants it, and a missing index is a panic in here rather
	// than an error anywhere near whoever assembled the segments.
	cum []int64

	mu    sync.Mutex
	pos   int64
	files map[int]*os.File
	lru   []int // segment indices, least recently used first
}

func newStoredReader(e *storedEntry) *storedReader {
	segs := make([]storedSeg, 0, len(e.segs))
	cum := make([]int64, 0, len(e.segs))
	var c int64
	for _, s := range e.segs {
		if s.n <= 0 {
			continue
		}
		segs = append(segs, s)
		cum = append(cum, c)
		c += s.n
	}
	return &storedReader{e: e, segs: segs, cum: cum, files: make(map[int]*os.File)}
}

func (r *storedReader) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readAt(p, off)
}

// readAt implements ReadAt; the caller holds r.mu.
func (r *storedReader) readAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	total := 0
	for len(p) > 0 && off < r.e.size {
		i := sort.Search(len(r.cum), func(i int) bool { return r.cum[i] > off }) - 1
		if i < 0 || i >= len(r.segs) {
			return total, io.ErrUnexpectedEOF // the entry claims bytes no segment holds
		}
		seg := r.segs[i]
		segOff := off - r.cum[i]
		n := min(int64(len(p)), seg.n-segOff)
		if n <= 0 {
			return total, io.ErrUnexpectedEOF
		}
		// file() marks i most-recently-used, so the descriptor in hand is
		// never the one an eviction closes.
		f, err := r.file(i)
		if err != nil {
			return total, err
		}
		read, err := f.ReadAt(p[:n], seg.off+segOff)
		total += read
		off += int64(read)
		p = p[read:]
		if err != nil {
			return total, err
		}
	}
	if len(p) > 0 {
		return total, io.EOF
	}
	return total, nil
}

// file returns the open volume holding segment i, opening it (and closing
// the least recently used volume) if needed. The caller holds r.mu.
func (r *storedReader) file(i int) (*os.File, error) {
	if f, ok := r.files[i]; ok {
		r.touch(i)
		return f, nil
	}
	f, err := os.Open(r.segs[i].path)
	if err != nil {
		return nil, err
	}
	r.files[i] = f
	r.lru = append(r.lru, i)
	for len(r.lru) > rarMaxOpenFiles {
		evict := r.lru[0]
		r.lru = r.lru[1:]
		if old, ok := r.files[evict]; ok {
			old.Close()
			delete(r.files, evict)
		}
	}
	return f, nil
}

// touch moves segment i to the most-recently-used end of the LRU order.
func (r *storedReader) touch(i int) {
	for j, v := range r.lru {
		if v == i {
			r.lru = append(append(r.lru[:j], r.lru[j+1:]...), i)
			return
		}
	}
	r.lru = append(r.lru, i)
}

func (r *storedReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pos >= r.e.size {
		return 0, io.EOF
	}
	n, err := r.readAt(p, r.pos)
	r.pos += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

func (r *storedReader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch whence {
	case io.SeekStart:
		r.pos = offset
	case io.SeekCurrent:
		r.pos += offset
	case io.SeekEnd:
		r.pos = r.e.size + offset
	}
	if r.pos < 0 {
		return 0, fmt.Errorf("negative position")
	}
	return r.pos, nil
}

func (r *storedReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.files {
		f.Close()
	}
	r.files = map[int]*os.File{}
	r.lru = nil
	return nil
}
