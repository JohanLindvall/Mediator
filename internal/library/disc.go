package library

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// DVD-Video, which is the other thing on these disks that is media held
// inside something else.
//
// A DVD's picture and sound live in VOB files — MPEG-2 program streams,
// stored plainly and split at a gigabyte because that is what the format
// allows. Inside a disc image they are runs of bytes at known offsets in the
// image; unpacked into a VIDEO_TS folder they are ordinary files. Either way
// the shape is the same one a store-method rar member has — a name, a size,
// and a list of byte ranges to stitch — so both become a storedEntry and the
// reader, the loopback input that lets ffmpeg seek in them, the rule against
// persisting derived offsets and the thumbnails all come for free.
//
// What is indexed is a **title**, not a file. A feature arrives as
// VTS_01_1.VOB … VTS_01_4.VOB, which is an artefact of the format and not
// something a viewer should be asked to reassemble: the four are one film,
// stitched in the order they play. VTS_nn_0.VOB and VIDEO_TS.VOB are the
// menus and are left out, and so are the parts of the disc that are too
// small to be anything anybody wants (see discTitleFraction).
//
// Nothing is mounted and nothing is extracted. An image is read where it
// lies, exactly as an archive is.

// isoSector is the logical block size of every ISO9660 volume worth reading;
// the descriptors start at sector 16 by the standard, and the search for them
// stops at the terminator or at isoMaxDescriptors, so a file that merely ends
// in .iso cannot make us read for ever.
const (
	isoSector          = 2048
	isoFirstDescriptor = 16
	isoMaxDescriptors  = 32
	isoMaxDirBytes     = 4 << 20
)

// discTitleFraction is how much of the biggest title on the disc a title has
// to be worth before it counts as one. A DVD carries the feature and then a
// scatter of one- and two-second title sets — the distributor's logo, the
// copyright notice, the pieces the menus animate with — and indexing those
// puts a dozen tiles in the listing for every film. A twentieth of the
// feature is six minutes against a two-hour film, which keeps an extra worth
// watching and drops the furniture.
const discTitleFraction = 20

var (
	// vtsTitlePart matches a title's own VOBs: VTS_01_1.VOB and its
	// continuations. Part 0 is the menu, and is deliberately not matched.
	vtsTitlePart = regexp.MustCompile(`(?i)^VTS_(\d{2})_([1-9])\.VOB$`)
	// vtsIFO matches a title set's information file, which is where the
	// disc records how long the title actually is.
	vtsIFO = regexp.MustCompile(`(?i)^VTS_(\d{2})_0\.IFO$`)
	// vtsAnyPart matches everything a DVD's video folder holds by way of
	// VOBs, menus included: none of them is a library item of its own.
	vtsAnyPart = regexp.MustCompile(`(?i)^(VIDEO_TS|VTS_\d{2}_\d)\.VOB$`)
	// discImageExt is only a prefilter — the parse itself decides, so an
	// installer image or a boot ramdisk costs a few sector reads and is then
	// left alone. Both spellings are in use for DVD images.
	discImageExt = map[string]bool{".iso": true, ".img": true}
	// discGenericDir is the furniture a DVD is filed under, which never
	// names the release: the title is named after the first directory above
	// it that says something.
	discGenericDir = map[string]bool{"video_ts": true, "audio_ts": true, "dvd": true}
)

// isDiscImage reports whether a path is worth opening as a disc image.
func isDiscImage(path string) bool {
	return discImageExt[strings.ToLower(filepath.Ext(path))]
}

// isDVDStructure reports whether a file is one of a DVD's own VOBs, which are
// indexed as the titles they belong to rather than one by one. A .vob under
// any other name is an ordinary video and is indexed as itself.
func isDVDStructure(path string) bool {
	return vtsAnyPart.MatchString(filepath.Base(path))
}

// discTitle is one title set: its number, and the pieces of it in the order
// they play.
type discTitle struct {
	number int
	segs   []storedSeg
	size   int64
}

// titlePart is one VOB on its way to becoming part of a title. Its bytes are
// a list because a range of an image held inside an archive lands in as many
// volume files as it happens to cross.
type titlePart struct {
	name string
	segs []storedSeg
	n    int64
}

// groupTitles gathers VOBs into the titles they belong to, largest first —
// the feature being what anybody opening a film is looking for, and the rest
// being trailers and extras. Titles too small to be either are dropped.
func groupTitles(parts []titlePart) []discTitle {
	type numbered struct {
		part int
		of   titlePart
	}
	byTitle := map[int][]numbered{}
	for _, p := range parts {
		m := vtsTitlePart.FindStringSubmatch(p.name)
		if m == nil {
			continue
		}
		title, _ := strconv.Atoi(m[1])
		part, _ := strconv.Atoi(m[2])
		byTitle[title] = append(byTitle[title], numbered{part, p})
	}

	out := make([]discTitle, 0, len(byTitle))
	for number, ns := range byTitle {
		// In the order they play, which is the order of the part numbers and
		// not the order the directory happens to list them in.
		slices.SortFunc(ns, func(a, b numbered) int { return cmp.Compare(a.part, b.part) })
		t := discTitle{number: number}
		for _, n := range ns {
			if n.of.n <= 0 {
				continue
			}
			t.segs = append(t.segs, n.of.segs...)
			t.size += n.of.n
		}
		if t.size > 0 {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b discTitle) int {
		if c := cmp.Compare(b.size, a.size); c != 0 { // largest first
			return c
		}
		return cmp.Compare(a.number, b.number)
	})

	// Everything worth a tile of its own, measured against the feature.
	for i, t := range out {
		if i > 0 && t.size < out[0].size/discTitleFraction {
			return out[:i]
		}
	}
	return out
}

// titleName is what a title is called in the library: the release's own name,
// with the title number added only where the disc offers more than one — a
// number nobody has to read is a number worth leaving out.
func titleName(release string, t discTitle, only bool) string {
	if only {
		return release + ".vob"
	}
	return fmt.Sprintf("%s - Title %d.vob", release, t.number)
}

// releaseName is what to call what is on the disc. For an image that is the
// image's own name; for an unpacked folder it is the first directory at or
// above it that is not DVD furniture, since "VIDEO_TS" names nothing.
func releaseName(container string, dir bool) string {
	if !dir {
		base := filepath.Base(container)
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	at := container
	for i := 0; i < 3; i++ {
		base := filepath.Base(at)
		if !discGenericDir[strings.ToLower(base)] {
			return base
		}
		parent := filepath.Dir(at)
		if parent == at {
			break
		}
		at = parent
	}
	return filepath.Base(at)
}

// discEntries reads the titles of a disc image or of an unpacked VIDEO_TS
// folder. It answers nothing for a file that is not an ISO9660 image holding
// DVD-Video, which includes every image whose only filesystem is UDF: that is
// a real limitation and a deliberate one, since DVD-Video images are written
// as a bridge with an ISO9660 tree describing the same data, and the ones
// that are not are Blu-ray, whose streams are not VOBs at all.
func discEntries(container string, dir bool) ([]*storedEntry, error) {
	var (
		titles  []discTitle
		readIFO func(title int) []byte
		err     error
	)
	if dir {
		titles, readIFO, err = folderTitles(container)
	} else {
		f, ferr := os.Open(container)
		if ferr != nil {
			return nil, ferr
		}
		defer f.Close()
		info, ierr := f.Stat()
		if ierr != nil {
			return nil, ierr
		}
		titles, readIFO, err = imageTitles(f, info.Size(), func(off, n int64) []storedSeg {
			return []storedSeg{{path: container, off: off, n: n}}
		})
	}
	if err != nil {
		return nil, err
	}
	return discTitleEntries(titles, readIFO, releaseName(container, dir), filepath.Base(container))
}

// discEntriesIn reads the titles of a disc image that is itself content
// inside something else — the usual arrangement for a DVD release, which
// ships as one image split across seventy rar volumes.
//
// A title is then two mappings deep: a range of the image, and the volume
// files that range crosses. Nothing else changes — the parse reads through
// the same stitching reader, and what comes out is segments like any other.
func discEntriesIn(image *storedEntry, release string) ([]*storedEntry, error) {
	r := newStoredReader(image)
	defer r.Close()
	titles, readIFO, err := imageTitles(r, image.size, func(off, n int64) []storedSeg {
		return placeInStored(image, off, n)
	})
	if err != nil {
		return nil, err
	}
	return discTitleEntries(titles, readIFO, release, image.name)
}

// discTitleEntries turns a disc's titles into the items they will be.
func discTitleEntries(titles []discTitle, readIFO func(int) []byte, release, what string) ([]*storedEntry, error) {
	if len(titles) == 0 {
		return nil, fmt.Errorf("disc: no titles in %s", what)
	}
	out := make([]*storedEntry, 0, len(titles))
	for _, t := range titles {
		e := &storedEntry{
			name: titleName(release, t, len(titles) == 1),
			size: t.size,
			segs: t.segs,
		}
		var index []seekPoint
		var covers int64
		e.durationMs, index, covers = ifoTitle(readIFO(t.number))
		// The index is only worth having where the cells account for exactly
		// the bytes the title holds. Measured, five of six titles did; the
		// sixth was short by a megabyte, and an index that does not line up
		// would put every seek somewhere else.
		if covers == t.size {
			e.seek = index
		}
		out = append(out, e)
	}
	return out, nil
}

// placeInStored maps a range of one piece of stored content onto the segments
// of the files it actually lives in — the second of the two mappings a title
// inside an archived disc image goes through.
func placeInStored(e *storedEntry, off, n int64) []storedSeg {
	var out []storedSeg
	var at int64 // where this segment starts, in the content's own coordinates
	for _, s := range e.segs {
		if n <= 0 {
			break
		}
		if off >= at+s.n {
			at += s.n
			continue
		}
		skip := off - at
		take := min(s.n-skip, n)
		out = append(out, storedSeg{path: s.path, off: s.off + skip, n: take})
		off += take
		n -= take
		at += s.n
	}
	return out
}

// folderTitles reads an unpacked VIDEO_TS directory.
func folderTitles(dir string) ([]discTitle, func(int) []byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var parts []titlePart
	ifos := map[int]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if m := vtsIFO.FindStringSubmatch(e.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			ifos[n] = filepath.Join(dir, e.Name())
			continue
		}
		if !vtsTitlePart.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		parts = append(parts, titlePart{
			name: e.Name(),
			segs: []storedSeg{{path: filepath.Join(dir, e.Name()), n: info.Size()}},
			n:    info.Size(),
		})
	}
	readIFO := func(title int) []byte {
		path, ok := ifos[title]
		if !ok {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxIFOBytes))
		if err != nil {
			return nil
		}
		return data
	}
	return groupTitles(parts), readIFO, nil
}

// imageTitles reads the VIDEO_TS directory of a disc image, wherever the
// image itself is. place says where a range of the image really lives: for
// one lying on the disk that is the file itself, and for one inside an
// archive it is however many volume files the range crosses.
func imageTitles(f io.ReaderAt, size int64, place func(off, n int64) []storedSeg) ([]discTitle, func(int) []byte, error) {
	root, rootLen, err := isoRoot(f)
	if err != nil {
		return nil, nil, err
	}
	videoTS, videoLen, err := isoChild(f, root, rootLen, "VIDEO_TS")
	if err != nil {
		return nil, nil, err
	}
	entries, err := isoDirEntries(f, videoTS, videoLen)
	if err != nil {
		return nil, nil, err
	}

	// The information files are read now, while the image is open: they are
	// small, and there are at most a handful of them.
	ifos := map[int][]byte{}
	for _, e := range entries {
		m := vtsIFO.FindStringSubmatch(e.name)
		if e.dir || m == nil || e.size <= 0 || e.size > maxIFOBytes {
			continue
		}
		buf := make([]byte, e.size)
		if _, err := f.ReadAt(buf, e.off); err != nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		ifos[n] = buf
	}

	var parts []titlePart
	for _, e := range entries {
		if e.dir || !vtsTitlePart.MatchString(e.name) {
			continue
		}
		// An image still being downloaded has its directory long before it
		// has all of its data, so what is claimed is clipped to what is
		// actually there: the film plays as far as it has arrived, and the
		// next scan picks up the rest.
		n := min(e.size, size-e.off)
		if n <= 0 {
			continue // the directory names a part the image has none of yet
		}
		parts = append(parts, titlePart{
			name: e.name,
			segs: place(e.off, n),
			n:    n,
		})
	}
	return groupTitles(parts), func(title int) []byte { return ifos[title] }, nil
}

// isoEntry is one ISO9660 directory record, reduced to what is needed here.
type isoEntry struct {
	name string
	off  int64
	size int64
	dir  bool
}

// isoRoot finds the root directory through the primary volume descriptor.
func isoRoot(f io.ReaderAt) (off, length int64, err error) {
	buf := make([]byte, isoSector)
	for i := range isoMaxDescriptors {
		if _, err := f.ReadAt(buf, int64(isoFirstDescriptor+i)*isoSector); err != nil {
			return 0, 0, err
		}
		if string(buf[1:6]) != "CD001" {
			return 0, 0, fmt.Errorf("iso: not an ISO9660 volume")
		}
		switch buf[0] {
		case 1: // primary volume descriptor, with the root's own record in it
			rec := buf[156 : 156+34]
			lba := int64(binary.LittleEndian.Uint32(rec[2:6]))
			size := int64(binary.LittleEndian.Uint32(rec[10:14]))
			if size <= 0 {
				return 0, 0, fmt.Errorf("iso: empty root directory")
			}
			return lba * isoSector, size, nil
		case 255: // terminator
			return 0, 0, fmt.Errorf("iso: no primary volume descriptor")
		}
	}
	return 0, 0, fmt.Errorf("iso: volume descriptors do not end")
}

// isoDirEntries reads one directory.
func isoDirEntries(f io.ReaderAt, off, length int64) ([]isoEntry, error) {
	if length <= 0 || length > isoMaxDirBytes {
		return nil, fmt.Errorf("iso: implausible directory of %d bytes", length)
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(io.NewSectionReader(f, off, length), data); err != nil {
		return nil, err
	}
	var out []isoEntry
	for pos := 0; pos < len(data); {
		recLen := int(data[pos])
		if recLen == 0 {
			// A record never straddles a sector, so a zero length means the
			// rest of this one is padding.
			next := (pos/isoSector + 1) * isoSector
			if next <= pos {
				break
			}
			pos = next
			continue
		}
		if recLen < 33 || pos+recLen > len(data) {
			break
		}
		rec := data[pos : pos+recLen]
		nameLen := int(rec[32])
		if 33+nameLen > len(rec) {
			break
		}
		name := string(rec[33 : 33+nameLen])
		// ISO9660 keeps a version after a semicolon, and writes "." and ".."
		// as a single byte each.
		if i := strings.IndexByte(name, ';'); i >= 0 {
			name = name[:i]
		}
		if name != "" && name[0] > 1 {
			out = append(out, isoEntry{
				name: name,
				off:  int64(binary.LittleEndian.Uint32(rec[2:6])) * isoSector,
				size: int64(binary.LittleEndian.Uint32(rec[10:14])),
				dir:  rec[25]&2 != 0,
			})
		}
		pos += recLen
	}
	return out, nil
}

// isoChild finds one named directory inside another.
func isoChild(f io.ReaderAt, off, length int64, want string) (int64, int64, error) {
	entries, err := isoDirEntries(f, off, length)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		if e.dir && strings.EqualFold(e.name, want) {
			return e.off, e.size, nil
		}
	}
	return 0, 0, fmt.Errorf("iso: no %s directory", want)
}

// How long a title is, and where in it each moment starts — which only the
// disc knows either.
//
// An MPEG-2 program stream carries no duration: ffprobe estimates one from
// the timestamps it can see, and on a measured disc that came out at 26:26
// for a title that decodes to 50:43 — out by a factor of two, which is not a
// cosmetic error. It puts a wrong length on the tile, takes the thumbnail
// from the wrong place, tells the player a film is finished half way
// through, and — worst — ffmpeg will not seek past the end it believes in,
// so the whole second half of a film is unreachable.
//
// The disc's own answer is in VTS_nn_0.IFO, and the unit that matters there
// is the **cell**, not the programme chain. A title set's VOBs store each
// cell once, however many chains reference it: one measured disc lists two
// chains of different lengths whose cells are distinct, where the total is
// the sum, and another lists the same 45 minutes twelve times over, where it
// is not. Summing the distinct cells came to within a frame of decoding the
// whole thing (3042.80 s against a measured 3042.76 s).
//
// Each cell also says which sectors it occupies, and those address the
// stitched VOB stream directly — on five of six measured titles the cells
// covered it exactly, to the byte, with no gaps. That is the seek index, and
// the sixth title is why it is only kept when the arithmetic comes out: a
// disc whose cells do not account for the bytes is one this does not
// understand, and a wrong index is worse than none.
const (
	// maxIFOBytes bounds what is read of a file that says it is an IFO. Real
	// ones are tens of kilobytes.
	maxIFOBytes = 4 << 20
	// maxIFOCells bounds the walk over a file from the wild. A DVD has a few
	// hundred cells; this is only so that a corrupt one cannot cost more
	// than a moment.
	maxIFOCells = 100_000
	// maxIFODurationMs is the point past which an answer is not one. The
	// longest DVD is a few hours.
	maxIFODurationMs = int64(24 * 60 * 60 * 1000)
)

// ifoCell is one cell of a title: where it sits and how long it plays.
type ifoCell struct {
	first, last uint32 // sectors, from the start of the title set's VOBs
	ms          int64
}

// ifoTitle reads a title set's information file. It returns the title's
// playing time and, where the cells account for the whole of it, an index
// from time to byte offset and the length that index covers. Anything it
// cannot read with confidence comes back as zero and no index.
func ifoTitle(data []byte) (durationMs int64, index []seekPoint, covers int64) {
	cells := ifoCells(data)
	if len(cells) == 0 {
		return 0, nil, 0
	}
	// In the order the stream plays them, which is the order they are stored
	// in and not the order any chain names them.
	slices.SortFunc(cells, func(a, b ifoCell) int { return cmp.Compare(a.first, b.first) })

	var ms int64
	index = make([]seekPoint, 0, len(cells)+1)
	contiguous := cells[0].first == 0
	for i, c := range cells {
		if c.last < c.first || (i > 0 && c.first != cells[i-1].last+1) {
			contiguous = false
		}
		index = append(index, seekPoint{ms: ms, off: int64(c.first) * isoSector})
		ms += c.ms
	}
	if ms <= 0 || ms > maxIFODurationMs {
		return 0, nil, 0
	}
	if !contiguous {
		return ms, nil, 0
	}
	// The sentinel, so the last cell interpolates like every other one.
	covers = int64(cells[len(cells)-1].last+1) * isoSector
	return ms, append(index, seekPoint{ms: ms, off: covers}), covers
}

// ifoCells collects the distinct cells a title set holds.
func ifoCells(data []byte) []ifoCell {
	if len(data) < 0xD0 || string(data[:12]) != "DVDVIDEO-VTS" {
		return nil
	}
	// The programme chain table, in sectors from the start of the file.
	pgci := int(binary.BigEndian.Uint32(data[0xCC:])) * isoSector
	if pgci <= 0 || pgci+8 > len(data) {
		return nil
	}
	chains := int(binary.BigEndian.Uint16(data[pgci:]))

	type cellID struct {
		vob  uint16
		cell uint8
	}
	seen := make(map[cellID]ifoCell, 64)
	for i := range chains {
		at := pgci + 8 + i*8
		if at+8 > len(data) {
			break
		}
		pgc := pgci + int(binary.BigEndian.Uint32(data[at+4:]))
		if pgc < 0 || pgc+0xEC > len(data) {
			continue
		}
		n := int(data[pgc+3])
		play := pgc + int(binary.BigEndian.Uint16(data[pgc+0xE8:]))
		pos := pgc + int(binary.BigEndian.Uint16(data[pgc+0xEA:]))
		for c := range n {
			p, q := play+c*24, pos+c*4
			if p < 0 || p+0x18 > len(data) || q < 0 || q+4 > len(data) {
				break
			}
			if len(seen) >= maxIFOCells {
				return nil // not a DVD's worth of cells; trust none of it
			}
			// A cell referenced by several chains is one cell on the disc,
			// and the VOBs hold it once.
			seen[cellID{binary.BigEndian.Uint16(data[q:]), data[q+3]}] = ifoCell{
				first: binary.BigEndian.Uint32(data[p+8:]),
				last:  binary.BigEndian.Uint32(data[p+0x14:]),
				ms:    dvdTimeMs(data[p+4 : p+8]),
			}
		}
	}
	out := make([]ifoCell, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	return out
}

// dvdTimeMs reads the four-byte time a DVD writes: hours, minutes and seconds
// in binary-coded decimal, then the frame count with the frame rate in its
// top two bits.
func dvdTimeMs(b []byte) int64 {
	h, m, s, f := bcd(b[0]), bcd(b[1]), bcd(b[2]), bcd(b[3]&0x3F)
	if h < 0 || m < 0 || s < 0 || f < 0 {
		return 0
	}
	ms := int64(h)*3600_000 + int64(m)*60_000 + int64(s)*1000
	switch b[3] >> 6 {
	case 1: // 25 fps
		ms += int64(f) * 1000 / 25
	case 3: // 29.97 fps
		ms += int64(f) * 1001 * 1000 / 30000
	}
	return ms
}

// bcd reads one binary-coded decimal byte, or -1 where it is not one.
func bcd(b byte) int {
	hi, lo := int(b>>4), int(b&0xF)
	if hi > 9 || lo > 9 {
		return -1
	}
	return hi*10 + lo
}
