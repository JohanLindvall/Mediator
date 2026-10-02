package library

// Zip archives in more than one file, and the directory of any zip.
//
// A zip comes split two ways, and both are on these disks. A spanned set —
// name.z01, name.z02 … name.zip — is what an archiver writes when asked for
// parts: the directory is in the last part, and every member's place is a
// part number and an offset from that part's start. A byte-split set —
// name.zip.001, name.zip.002 … — is one ordinary archive cut into pieces
// after it was written, so put back together it is that archive, offsets and
// all, counted from the start of the first piece. Either way a member is a
// run of bytes that may cross from one file into the next, which is exactly a
// storedEntry's list of segments: the reader that stitches a rar set's
// volumes serves them as it is, and a deflated member is unpacked through it.
//
// The directory is read here rather than by the zip package, because that
// reads past the one field a spanned set turns on — the part a member begins
// in — and keeps nothing of it; reading an offset against the wrong part is
// what made the first version refuse such sets outright. One reader for every
// shape, the single file included, so that no shape has a path of its own.
//
// A member's own header is read before it is served, and it has to be a
// member header naming the member the directory says is there. That is the
// guard against every way an offset can be wrong — a part that is not the one
// it claims to be, a piece grouped into the wrong set, a directory that is
// simply damaged — any of which would otherwise hand out another member's
// bytes under this one's name, or a picture's under a film's. Python's own
// zip reader insists on the same agreement, which is the evidence that real
// archives keep it.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	// zipSplitRe is a piece of an archive split byte by byte: name.zip.001,
	// name.zip.002 …
	zipSplitRe = regexp.MustCompile(`(?i)^(.*)\.zip\.(\d{3,})$`)
	// zipSpanRe is an earlier part of a spanned archive: name.z01 …, the last
	// part being name.zip itself.
	zipSpanRe = regexp.MustCompile(`(?i)^(.*)\.z(\d{2,})$`)
	// hostID is what a file host appends to each piece's name, a different
	// one per piece — "name-Ab12Cd34.zip.001", "name-Zy98Xw76.zip.002" — so
	// the pieces of one set share no name at all until it is taken off.
	hostID = regexp.MustCompile(`-[A-Za-z0-9]{6,16}$`)
)

// zipMaxParts bounds how many files one set may span, as rarMaxVolumes does.
const zipMaxParts = 1000

// zipMaxDirectory bounds what is read of an archive's directory into memory
// at once: a directory of a quarter of a million names is twenty megabytes,
// and one claiming more than this is not a directory worth believing.
const zipMaxDirectory = 512 << 20

// isZipContainer says whether a file is the one a zip set is known by: an
// archive, which is also the last part of a spanned set, or the first piece
// of a byte-split one.
func isZipContainer(path string) bool {
	base := filepath.Base(path)
	if isZip(base) {
		return true
	}
	m := zipSplitRe.FindStringSubmatch(base)
	return m != nil && partNumber(m[2]) == 1
}

func partNumber(digits string) int {
	n, err := strconv.Atoi(digits)
	if err != nil {
		return -1
	}
	return n
}

// zipContainerOf is the container of the set any part belongs to — the path
// its members hang off and its lock is keyed by, spelled exactly as the walk
// spells it, since the two must agree. "" where path is no part of a zip set,
// or its set's container is not here.
func zipContainerOf(path string) string {
	dir, base := filepath.Split(path)
	switch {
	case isZip(base):
		return path
	case zipSplitRe.MatchString(base):
		return zipSplitFirst(path)
	case zipSpanRe.MatchString(base):
		// name.z01 belongs to name.zip, in whatever case that is written.
		stem := base[:strings.LastIndexByte(base, '.')]
		entries, err := os.ReadDir(filepath.Clean(dir))
		if err != nil {
			return ""
		}
		for _, e := range entries {
			if strings.EqualFold(e.Name(), stem+".zip") {
				return filepath.Join(dir, e.Name())
			}
		}
	}
	return ""
}

// zipSplitPieces lists the pieces in a directory that could belong to the
// set a piece is part of: those of the same name, and those of the same name
// once a file host's identifier is taken off each, by piece number.
func zipSplitPieces(path string) (stem string, exact, loose map[int][]string, err error) {
	dir, base := filepath.Split(path)
	m := zipSplitRe.FindStringSubmatch(base)
	if m == nil {
		return "", nil, nil, errors.New("not a piece of a split archive")
	}
	stem = m[1]
	key := hostID.ReplaceAllString(stem, "")
	entries, err := os.ReadDir(filepath.Clean(dir))
	if err != nil {
		return "", nil, nil, err
	}
	exact, loose = map[int][]string{}, map[int][]string{}
	for _, e := range entries {
		mm := zipSplitRe.FindStringSubmatch(e.Name())
		if mm == nil || e.IsDir() {
			continue
		}
		n := partNumber(mm[2])
		p := filepath.Join(dir, e.Name())
		if mm[1] == stem {
			exact[n] = append(exact[n], p)
		}
		if hostID.ReplaceAllString(mm[1], "") == key {
			loose[n] = append(loose[n], p)
		}
	}
	return stem, exact, loose, nil
}

// pieceRun is the pieces numbered one upwards without a gap; nil where any
// number is held by two files, which is two sets that cannot be told apart.
func pieceRun(by map[int][]string) []string {
	var run []string
	for n := 1; n <= zipMaxParts; n++ {
		switch len(by[n]) {
		case 0:
			return run
		case 1:
			run = append(run, by[n][0])
		default:
			return nil
		}
	}
	return run
}

// zipSplitParts is a byte-split set's pieces in order, from its first: the
// pieces of the same name, or — where a file host has renamed each piece
// apart — the pieces of the same name less the host's identifiers, so long as
// no number is held twice. A set of one piece is that piece.
func zipSplitParts(first string) ([]string, error) {
	_, exact, loose, err := zipSplitPieces(first)
	if err != nil {
		return nil, err
	}
	if run := pieceRun(exact); len(run) >= 2 {
		return run, nil
	}
	if run := pieceRun(loose); len(run) >= 2 && run[0] == first {
		return run, nil
	}
	return []string{first}, nil
}

// zipSplitFirst is the first piece of the set a piece belongs to, by the very
// rule zipSplitParts groups them by, so that the watcher and the walk name a
// set alike; "" where there is none.
func zipSplitFirst(path string) string {
	_, exact, loose, err := zipSplitPieces(path)
	if err != nil {
		return ""
	}
	for _, first := range loose[1] {
		if parts, err := zipSplitParts(first); err == nil && slices.Contains(parts, path) {
			return first
		}
	}
	// A piece that has just gone is in no set any more: its set is the one
	// whose first piece has its name, or the only one that could be.
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		switch {
		case len(exact[1]) == 1:
			return exact[1][0]
		case len(loose[1]) == 1:
			return loose[1][0]
		}
	}
	return ""
}

// zipSet is an archive and the files its bytes are in.
type zipSet struct {
	// parts are the files in the order the archive runs through them: the
	// one file, a byte-split set's pieces, or a spanned set's parts by
	// number — "" where one is not here.
	parts []string
	sizes []int64
	// spanned says a member's place is a part and an offset from that part's
	// start; otherwise every offset counts from the start of the first part.
	spanned bool
	// base is how far every offset falls short, for an archive with
	// something in front of it (see directory).
	base int64

	f     *os.File // the part read last, kept open for the next read
	fpath string
}

// errPastTheEnd is a place past the last byte of a set: an archive cut short.
var errPastTheEnd = errors.New("past the end of the archive")

// zipShape is an archive read without fault that is not a whole archive: no
// end record, a directory that does not parse, a set short of the part its
// directory is in. Unlike a read that failed — a disk that did not answer —
// it is a verdict on what is there, and indexZip takes it as one.
type zipShape struct{ error }

func (e zipShape) Unwrap() error { return e.error }

// broken is a zipShape.
func broken(format string, args ...any) error {
	return zipShape{fmt.Errorf(format, args...)}
}

// verdictOf makes a failure to reach bytes that are not there — past the end,
// or in a part that is missing — the verdict it is, leaving a failed read as
// it was.
func verdictOf(err error) error {
	var missing zipPartMissing
	if errors.Is(err, errPastTheEnd) || errors.As(err, &missing) {
		return zipShape{err}
	}
	return err
}

// zipPartMissing is a member whose bytes are in a part that is not here.
type zipPartMissing struct{ part, of int }

func (e zipPartMissing) Error() string {
	return fmt.Sprintf("part %d of %d is not here", e.part, e.of)
}

func newZipSet(parts []string, spanned bool) (*zipSet, error) {
	z := &zipSet{parts: parts, sizes: make([]int64, len(parts)), spanned: spanned}
	for i, p := range parts {
		if p == "" {
			continue
		}
		info, err := os.Stat(p)
		if err == nil && !info.Mode().IsRegular() {
			err = fmt.Errorf("%s is not a regular file", p)
		}
		if err != nil {
			if !spanned {
				return nil, err
			}
			z.parts[i] = ""
			continue
		}
		z.sizes[i] = info.Size()
	}
	return z, nil
}

// zipPartsOf is every file of a container's set that is here, the container
// alone where its set cannot be made out.
func zipPartsOf(container string) []string {
	z, _, err := openZipSet(container)
	if err != nil {
		return []string{container}
	}
	defer z.close()
	var out []string
	for _, p := range z.parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// openZipSet makes out the set a container is the face of, and reads what its
// end record says of it.
func openZipSet(container string) (*zipSet, zipEndRecord, error) {
	if zipSplitRe.MatchString(filepath.Base(container)) {
		parts, err := zipSplitParts(container)
		if err != nil {
			return nil, zipEndRecord{}, err
		}
		z, err := newZipSet(parts, false)
		if err != nil {
			return nil, zipEndRecord{}, err
		}
		end, err := zipEnd(zipPartAt{z, 0}, z.total())
		if err == nil && (end.disk != 0 || end.cdDisk != 0) {
			err = broken("pieces of one part of a spanned archive")
		}
		if err != nil {
			z.close()
			return nil, zipEndRecord{}, err
		}
		return z, end, nil
	}
	z, err := newZipSet([]string{container}, false)
	if err != nil {
		return nil, zipEndRecord{}, err
	}
	end, err := zipEnd(zipPartAt{z, 0}, z.sizes[0])
	if err != nil || end.disk == 0 {
		if err != nil {
			z.close()
			return nil, zipEndRecord{}, err
		}
		return z, end, nil
	}
	z.close()
	// The last part of a spanned set: the parts before it are name.z01 on.
	if end.disk >= zipMaxParts || end.cdDisk > end.disk {
		return nil, zipEndRecord{}, broken("an archive of %d parts", end.disk+1)
	}
	dir, base := filepath.Split(container)
	stem, ext := base[:len(base)-4], base[len(base)-4:]
	letter := "z"
	if ext == strings.ToUpper(ext) {
		letter = "Z"
	}
	parts := make([]string, end.disk+1)
	for k := range int(end.disk) {
		parts[k] = filepath.Join(dir, fmt.Sprintf("%s.%s%02d", stem, letter, k+1))
	}
	parts[end.disk] = container
	if z, err = newZipSet(parts, true); err != nil {
		return nil, zipEndRecord{}, err
	}
	return z, end, nil
}

func (z *zipSet) total() int64 {
	var n int64
	for _, s := range z.sizes {
		n += s
	}
	return n
}

// span is where n bytes from offset off of part d lie, part by part — off
// counting from the start of part d, which outside a spanned set is always
// the first.
func (z *zipSet) span(d int, off, n int64) ([]storedSeg, error) {
	if off < 0 || n < 0 || d < 0 {
		return nil, errors.New("a negative place")
	}
	var segs []storedSeg
	for n > 0 {
		if d >= len(z.parts) {
			return nil, fmt.Errorf("%w by %d bytes", errPastTheEnd, n)
		}
		if z.parts[d] == "" {
			return nil, zipPartMissing{d + 1, len(z.parts)}
		}
		if off >= z.sizes[d] {
			off -= z.sizes[d]
			d++
			continue
		}
		take := min(n, z.sizes[d]-off)
		segs = append(segs, storedSeg{path: z.parts[d], off: off, n: take})
		off += take
		n -= take
	}
	return segs, nil
}

// readAt fills p from offset off of part d, crossing into the parts after it
// as the bytes do.
func (z *zipSet) readAt(d int, off int64, p []byte) error {
	segs, err := z.span(d, off, int64(len(p)))
	if err != nil {
		return err
	}
	for _, s := range segs {
		f, err := z.file(s.path)
		if err != nil {
			return err
		}
		if _, err := f.ReadAt(p[:s.n], s.off); err != nil {
			return err
		}
		p = p[s.n:]
	}
	return nil
}

func (z *zipSet) file(path string) (*os.File, error) {
	if z.f != nil && z.fpath == path {
		return z.f, nil
	}
	z.close()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	z.f, z.fpath = f, path
	return f, nil
}

func (z *zipSet) close() {
	if z.f != nil {
		z.f.Close()
		z.f = nil
	}
}

// zipPartAt reads a set from the start of one of its parts onwards.
type zipPartAt struct {
	z *zipSet
	d int
}

func (r zipPartAt) ReadAt(p []byte, off int64) (int, error) {
	if err := r.z.readAt(r.d, off, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// zipEndRecord is what an archive's end record says of the whole.
type zipEndRecord struct {
	entries int64 // how many entries the directory holds
	disk    int   // which part of a spanned set this is (0: one piece)
	cdDisk  int   // the part the directory begins in
	cdOff   int64 // where it begins, in that part
	cdSize  int64
	// dirEnd is where the record after the directory begins, in the reader
	// the end was read from: what tells an archive with something in front
	// of it, its offsets all short by that much.
	dirEnd int64
}

// zipEnd reads an archive's end record without reading the directory itself
// — the zip64 record where there is one.
func zipEnd(r io.ReaderAt, size int64) (zipEndRecord, error) {
	const endLen = 22
	tail := min(size, 0xFFFF+endLen)
	buf := make([]byte, tail)
	if _, err := r.ReadAt(buf, size-tail); err != nil && !errors.Is(err, io.EOF) {
		return zipEndRecord{}, err
	}
	// The last signature whose comment fits in what follows it, as the zip
	// package looks: a comment may hold the signature too.
	i := -1
	for j := len(buf) - endLen; j >= 0; j-- {
		if buf[j] == 'P' && buf[j+1] == 'K' && buf[j+2] == 5 && buf[j+3] == 6 &&
			j+endLen+int(zle.Uint16(buf[j+20:])) <= len(buf) {
			i = j
			break
		}
	}
	if i < 0 {
		return zipEndRecord{}, broken("no end of central directory")
	}
	end := zipEndRecord{
		disk:    int(zle.Uint16(buf[i+4:])),
		cdDisk:  int(zle.Uint16(buf[i+6:])),
		entries: int64(zle.Uint16(buf[i+10:])),
		cdSize:  int64(zle.Uint32(buf[i+12:])),
		cdOff:   int64(zle.Uint32(buf[i+16:])),
		dirEnd:  size - tail + int64(i),
	}
	j := i - 20 // a zip64 locator sits just before the end record
	if j < 0 || zle.Uint32(buf[j:]) != 0x07064b50 {
		return end, nil
	}
	at := int64(zle.Uint64(buf[j+8:]))
	var rec [56]byte
	if at < 0 || at > size-int64(len(rec)) {
		return end, nil
	}
	if _, err := r.ReadAt(rec[:], at); err != nil || zle.Uint32(rec[:]) != 0x06064b50 {
		return end, nil
	}
	return zipEndRecord{
		disk:    int(zle.Uint32(rec[16:])),
		cdDisk:  int(zle.Uint32(rec[20:])),
		entries: int64(zle.Uint64(rec[32:])),
		cdSize:  int64(zle.Uint64(rec[40:])),
		cdOff:   int64(zle.Uint64(rec[48:])),
		dirEnd:  at,
	}, nil
}

// zipDirEntry is one entry of an archive's directory, as far as serving it
// goes.
type zipDirEntry struct {
	raw    string // the name as the directory spells it
	flags  uint16
	method uint16
	crc    uint32
	packed int64
	size   int64
	disk   int   // the part its header is in
	off    int64 // where, from that part's start
	dir    bool
}

// directory reads a set's directory.
func (z *zipSet) directory(end zipEndRecord) ([]zipDirEntry, error) {
	if end.cdSize < 0 || end.cdSize > zipMaxDirectory || end.cdOff < 0 {
		return nil, broken("a directory of %d bytes at %d", end.cdSize, end.cdOff)
	}
	d, off := 0, end.cdOff
	if z.spanned {
		d = end.cdDisk
	} else if base := end.dirEnd - end.cdSize - end.cdOff; base != 0 {
		// Something in front of the archive — a self-extractor's code — and
		// every offset short by its length, unless the directory really is
		// where the record says, which the zip package takes as the answer.
		var sig [4]byte
		if z.readAt(0, off, sig[:]) != nil || zle.Uint32(sig[:]) != 0x02014b50 {
			if base < 0 {
				return nil, broken("the directory is not where the archive says")
			}
			off += base
			z.base = base
		}
	}
	cd := make([]byte, end.cdSize)
	if err := z.readAt(d, off, cd); err != nil {
		return nil, verdictOf(err)
	}
	out, err := readZipDirectory(cd)
	if err != nil {
		return nil, zipShape{err}
	}
	// The count is sixteen bits wide outside zip64, and an archiver past it
	// that wrote no zip64 record leaves the low bits; the zip package
	// compares those too.
	if uint16(len(out)) != uint16(end.entries) {
		return nil, broken("the directory holds %d entries, the archive says %d", len(out), end.entries)
	}
	return out, nil
}

// readZipDirectory parses a central directory.
func readZipDirectory(cd []byte) ([]zipDirEntry, error) {
	var out []zipDirEntry
	for len(cd) > 0 {
		if len(cd) < 46 || zle.Uint32(cd) != 0x02014b50 {
			return nil, errors.New("not a directory entry")
		}
		n, m, k := int(zle.Uint16(cd[28:])), int(zle.Uint16(cd[30:])), int(zle.Uint16(cd[32:]))
		if 46+n+m+k > len(cd) {
			return nil, errors.New("a directory entry runs past the directory")
		}
		e := zipDirEntry{
			raw:    string(cd[46 : 46+n]),
			flags:  zle.Uint16(cd[8:]),
			method: zle.Uint16(cd[10:]),
			crc:    zle.Uint32(cd[16:]),
			packed: int64(zle.Uint32(cd[20:])),
			size:   int64(zle.Uint32(cd[24:])),
			disk:   int(zle.Uint16(cd[34:])),
			off:    int64(zle.Uint32(cd[42:])),
		}
		// Sizes, the place and the part outgrow their fields in that order,
		// and a zip64 field carries those that did, in that order.
		wantSize, wantPacked, wantOff, wantDisk := e.size == 0xFFFFFFFF, e.packed == 0xFFFFFFFF, e.off == 0xFFFFFFFF, e.disk == 0xFFFF
		for extra := cd[46+n : 46+n+m]; len(extra) >= 4; {
			id, sz := zle.Uint16(extra), int(zle.Uint16(extra[2:]))
			if 4+sz > len(extra) {
				break
			}
			if id == 0x0001 {
				b := extra[4 : 4+sz]
				if wantSize && len(b) >= 8 {
					e.size, b = int64(zle.Uint64(b)), b[8:]
				}
				if wantPacked && len(b) >= 8 {
					e.packed, b = int64(zle.Uint64(b)), b[8:]
				}
				if wantOff && len(b) >= 8 {
					e.off, b = int64(zle.Uint64(b)), b[8:]
				}
				if wantDisk && len(b) >= 4 {
					e.disk = int(zle.Uint32(b))
				}
			}
			extra = extra[4+sz:]
		}
		// A directory, by its name or by its attributes as the system that
		// wrote it keeps them — the zip package's reading of the same.
		attrs := zle.Uint32(cd[38:])
		switch cd[5] {
		case 0, 11, 14: // FAT, NTFS as the zip package numbers it, VFAT
			e.dir = attrs&0x10 != 0
		case 3, 19: // Unix, macOS
			e.dir = (attrs>>16)&0o170000 == 0o040000
		}
		e.dir = e.dir || strings.HasSuffix(e.raw, "/")
		out = append(out, e)
		cd = cd[46+n+m+k:]
	}
	return out, nil
}

// dataStart is where a member's bytes begin, read off its own header — which
// has to be there, and has to name the member the directory says it is.
func (z *zipSet) dataStart(e zipDirEntry) (int, int64, error) {
	d, off := e.disk, e.off+z.base
	if !z.spanned {
		if e.disk != 0 {
			return 0, 0, fmt.Errorf("a part number (%d) in an archive in one piece", e.disk)
		}
		d = 0
	}
	h := make([]byte, 30+len(e.raw))
	if err := z.readAt(d, off, h); err != nil {
		return 0, 0, err
	}
	if zle.Uint32(h) != 0x04034b50 {
		return 0, 0, errors.New("no member header where the directory says")
	}
	n, m := int(zle.Uint16(h[26:])), int64(zle.Uint16(h[28:]))
	if n != len(e.raw) || string(h[30:]) != e.raw {
		return 0, 0, errors.New("the header there names another member")
	}
	return d, off + 30 + int64(n) + m, nil
}

// zle reads the little-endian fields every zip record is made of.
var zle = binary.LittleEndian
