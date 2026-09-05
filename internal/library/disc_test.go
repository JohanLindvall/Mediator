package library

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// The names here are invented; the shapes are the ones DVDs actually have.
// See CLAUDE.md on why that rule exists.

// isoFile is one file to put in a synthetic image's VIDEO_TS directory.
type isoFile struct {
	name string
	size int64
}

// buildISO writes the smallest ISO9660 volume that has DVD-Video in it: a
// primary volume descriptor, a terminator, the root directory, VIDEO_TS, and
// the files' own data laid out sector by sector after them.
func buildISO(t *testing.T, files []isoFile) string {
	t.Helper()
	const (
		pvdLBA  = 16
		termLBA = 17
		rootLBA = 18
		vtsLBA  = 19
		dataLBA = 20
	)

	// Where each file's data goes, and how big the volume ends up.
	offsets := make([]int64, len(files))
	at := int64(dataLBA)
	for i, f := range files {
		offsets[i] = at
		at += (f.size + isoSector - 1) / isoSector
	}

	img := make([]byte, at*isoSector)
	sector := func(lba int64) []byte { return img[lba*isoSector : (lba+1)*isoSector] }
	// Each file gets a pattern of its own, so what is read back can be
	// checked against what was written.
	for i, f := range files {
		for b := range f.size {
			img[offsets[i]*isoSector+b] = byte('a' + i)
		}
	}

	// A directory record, as ISO9660 writes one: both-endian numbers, the
	// name last, and every record padded to an even length.
	record := func(name string, lba, size int64, dir bool) []byte {
		rec := make([]byte, 33+len(name))
		binary.LittleEndian.PutUint32(rec[2:], uint32(lba))
		binary.BigEndian.PutUint32(rec[6:], uint32(lba))
		binary.LittleEndian.PutUint32(rec[10:], uint32(size))
		binary.BigEndian.PutUint32(rec[14:], uint32(size))
		if dir {
			rec[25] = 2
		}
		binary.LittleEndian.PutUint16(rec[28:], 1)
		binary.BigEndian.PutUint16(rec[30:], 1)
		rec[32] = byte(len(name))
		copy(rec[33:], name)
		if len(rec)%2 == 1 {
			rec = append(rec, 0)
		}
		rec[0] = byte(len(rec))
		return rec
	}
	writeDir := func(lba int64, recs [][]byte) {
		s := sector(lba)
		pos := 0
		for _, r := range recs {
			copy(s[pos:], r)
			pos += len(r)
		}
	}

	pvd := sector(pvdLBA)
	pvd[0] = 1
	copy(pvd[1:], "CD001")
	pvd[6] = 1
	copy(pvd[156:], record("\x00", rootLBA, isoSector, true))

	term := sector(termLBA)
	term[0] = 255
	copy(term[1:], "CD001")
	term[6] = 1

	writeDir(rootLBA, [][]byte{
		record("\x00", rootLBA, isoSector, true),
		record("\x01", rootLBA, isoSector, true),
		record("VIDEO_TS", vtsLBA, isoSector, true),
	})

	vts := [][]byte{
		record("\x00", vtsLBA, isoSector, true),
		record("\x01", rootLBA, isoSector, true),
	}
	for i, f := range files {
		// ISO9660 keeps a version number after a semicolon.
		vts = append(vts, record(f.name+";1", offsets[i], f.size, false))
	}
	writeDir(vtsLBA, vts)

	path := filepath.Join(t.TempDir(), "gorse.beacon.1998.pal.dvdr-grp.iso")
	if err := os.WriteFile(path, img, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImageTitles(t *testing.T) {
	// One feature split at the format's gigabyte, a menu that is not part of
	// it, and a trailer too small to be a title of its own.
	path := buildISO(t, []isoFile{
		{"VIDEO_TS.VOB", 4 * isoSector},
		{"VTS_01_0.VOB", 6 * isoSector},
		{"VTS_01_1.VOB", 500 * isoSector},
		{"VTS_01_2.VOB", 300 * isoSector},
		{"VTS_02_1.VOB", 8 * isoSector},
	})
	entries, err := discEntries(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d titles, want the feature only", len(entries))
	}
	e := entries[0]
	if want := "gorse.beacon.1998.pal.dvdr-grp.vob"; e.name != want {
		t.Errorf("name %q, want %q", e.name, want)
	}
	if want := int64(800 * isoSector); e.size != want {
		t.Errorf("size %d, want %d", e.size, want)
	}
	if len(e.segs) != 2 {
		t.Fatalf("got %d segments, want the two parts", len(e.segs))
	}
	if e.segs[0].n != 500*isoSector || e.segs[1].n != 300*isoSector {
		t.Errorf("parts out of order: %+v", e.segs)
	}
	if Classify(e.name) != KindVideo {
		t.Errorf("a title has to classify as video, got %q", Classify(e.name))
	}
}

// The parts play in the order the disc numbers them, not the order the
// directory happens to list them in: a film that stitched them the other way
// round would start half way through.
func TestImageTitleOrdersItsParts(t *testing.T) {
	// Laid down back to front, and each a different length so the order they
	// come back in is visible.
	path := buildISO(t, []isoFile{
		{"VTS_01_3.VOB", 300 * isoSector},
		{"VTS_01_1.VOB", 100 * isoSector},
		{"VTS_01_2.VOB", 200 * isoSector},
	})
	entries, err := discEntries(path, false)
	if err != nil {
		t.Fatal(err)
	}
	segs := entries[0].segs
	if len(segs) != 3 {
		t.Fatalf("got %d segments, want 3", len(segs))
	}
	for i, want := range []int64{100, 200, 300} {
		if segs[i].n != want*isoSector {
			t.Errorf("segment %d is %d bytes, want part %d's %d",
				i, segs[i].n, i+1, want*isoSector)
		}
	}
}

// A disc that really does hold more than one thing says which is which.
func TestImageNamesSeveralTitles(t *testing.T) {
	path := buildISO(t, []isoFile{
		{"VTS_01_1.VOB", 500 * isoSector},
		{"VTS_02_1.VOB", 400 * isoSector},
	})
	entries, err := discEntries(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d titles, want 2", len(entries))
	}
	for i, want := range []string{
		"gorse.beacon.1998.pal.dvdr-grp - Title 1.vob",
		"gorse.beacon.1998.pal.dvdr-grp - Title 2.vob",
	} {
		if entries[i].name != want {
			t.Errorf("title %d named %q, want %q", i, entries[i].name, want)
		}
	}
}

// An image that is not a DVD is not one, however it is spelled.
func TestNotADisc(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ name, content string }{
		{"installer.iso", "not an image at all"},
		{"initrd.img", "nor this"},
	} {
		path := filepath.Join(dir, c.name)
		// Long enough to reach where the descriptors would be.
		body := append([]byte(c.content), bytes.Repeat([]byte{0}, 40*isoSector)...)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := discEntries(path, false); err == nil {
			t.Errorf("%s was read as a DVD", c.name)
		}
	}
}

// The other half of the world: a DVD that has been unpacked. The titles are
// the same titles; only where the bytes are has changed.
func TestFolderTitles(t *testing.T) {
	for _, c := range []struct {
		name    string
		holder  string // where the VOBs sit, relative to the release
		release string
	}{
		{"the files are simply in the release directory", ".", "Larkspur.Nights.2004.PAL.DVDR-GRP"},
		{"a VIDEO_TS folder", "VIDEO_TS", "Larkspur.Nights.2004.PAL.DVDR-GRP"},
		{"a dvd folder above it", filepath.Join("dvd", "VIDEO_TS"), "Larkspur.Nights.2004.PAL.DVDR-GRP"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), c.release)
			holder := filepath.Join(root, c.holder)
			if err := os.MkdirAll(holder, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, size := range map[string]int{
				"VIDEO_TS.IFO": 12, "VIDEO_TS.VOB": 40,
				"VTS_01_0.VOB": 30, "VTS_01_1.VOB": 900, "VTS_01_2.VOB": 300,
			} {
				body := bytes.Repeat([]byte{'x'}, size)
				if err := os.WriteFile(filepath.Join(holder, name), body, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			entries, err := discEntries(holder, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("got %d titles, want 1", len(entries))
			}
			// Named after the release, never after the DVD's own furniture.
			if want := c.release + ".vob"; entries[0].name != want {
				t.Errorf("name %q, want %q", entries[0].name, want)
			}
			if entries[0].size != 1200 {
				t.Errorf("size %d, want the two parts", entries[0].size)
			}
		})
	}
}

// A DVD's own VOBs are the title they belong to. Left to themselves they are
// a tile per gigabyte with the menus in among them, which is the listing the
// folding exists to avoid.
func TestScanFoldsDVDFolder(t *testing.T) {
	root := t.TempDir()
	holder := filepath.Join(root, "Larkspur.Nights.2004.PAL.DVDR-GRP", "VIDEO_TS")
	if err := os.MkdirAll(holder, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{
		"VIDEO_TS.VOB": 40, "VTS_01_0.VOB": 30,
		"VTS_01_1.VOB": 900, "VTS_01_2.VOB": 300,
	} {
		if err := os.WriteFile(filepath.Join(holder, name), bytes.Repeat([]byte{'x'}, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l := quietLib(root)
	l.Scan(nil)

	items := l.List(Query{Limit: 100}).Items
	if len(items) != 1 {
		names := make([]string, 0, len(items))
		for _, it := range items {
			names = append(names, it.Name)
		}
		t.Fatalf("got %d items %v, want the one title", len(items), names)
	}
	if want := "Larkspur.Nights.2004.PAL.DVDR-GRP.vob"; items[0].Name != want {
		t.Errorf("name %q, want %q", items[0].Name, want)
	}
	if !items[0].Archived() {
		t.Error("a title is content inside other files, so it is archived")
	}
	if items[0].Size != 1200 {
		t.Errorf("size %d, want the two parts", items[0].Size)
	}

	// And a second scan does not double it, nor lose it.
	l.Scan(nil)
	if items := l.List(Query{Limit: 100}).Items; len(items) != 1 {
		t.Fatalf("got %d items after a second scan, want 1", len(items))
	}
}

// The whole point: a title reads as one stream, whichever file each byte of
// it is actually in.
func TestTitleReadsAsOneStream(t *testing.T) {
	root := t.TempDir()
	holder := filepath.Join(root, "Larkspur.Nights.2004.PAL.DVDR-GRP")
	if err := os.MkdirAll(holder, 0o755); err != nil {
		t.Fatal(err)
	}
	var want []byte
	for i, name := range []string{"VTS_01_1.VOB", "VTS_01_2.VOB", "VTS_01_3.VOB"} {
		part := bytes.Repeat([]byte{byte('a' + i)}, 1000)
		want = append(want, part...)
		if err := os.WriteFile(filepath.Join(holder, name), part, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l := quietLib(root)
	l.Scan(nil)
	items := l.List(Query{Limit: 10}).Items
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	f, err := OpenItem(items[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes, want %d, and the same ones", len(got), len(want))
	}
	// Random access across the seam, which is what a seeking ffmpeg does.
	buf := make([]byte, 100)
	if _, err := f.ReadAt(buf, 950); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf, want[950:1050]) {
		t.Errorf("across the seam: %q", buf)
	}
}

// A disc image is a container: its titles are indexed, the image is not.
func TestScanIndexesImage(t *testing.T) {
	path := buildISO(t, []isoFile{
		{"VTS_01_0.VOB", 4 * isoSector},
		{"VTS_01_1.VOB", 200 * isoSector},
	})
	l := quietLib(filepath.Dir(path))
	l.Scan(nil)
	items := l.List(Query{Limit: 10}).Items
	if len(items) != 1 {
		t.Fatalf("got %d items, want the one title", len(items))
	}
	if items[0].Kind != KindVideo {
		t.Errorf("kind %q, want video", items[0].Kind)
	}
	if want := fmt.Sprintf("%s\x00%s", path, "gorse.beacon.1998.pal.dvdr-grp.vob"); items[0].Path != want {
		t.Errorf("path %q, want %q", items[0].Path, want)
	}
	// Derived offsets are never written down: a stale one serves garbage.
	if persistable(l.items[items[0].ID]) {
		t.Error("a title's byte offsets must not be persisted")
	}
	// And the image itself is not a second copy of the film.
	if _, ok := l.byPath[path]; ok {
		t.Error("the image was indexed as an item of its own")
	}
}

// A .vob that is not one of a DVD's own is an ordinary video.
func TestStrayVOBIsItsOwnItem(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "holiday clip.vob"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := quietLib(root)
	l.Scan(nil)
	items := l.List(Query{Limit: 10}).Items
	if len(items) != 1 || items[0].Name != "holiday clip.vob" {
		t.Fatalf("got %+v, want the clip itself", items)
	}
	if items[0].Archived() {
		t.Error("a plain file is not archived")
	}
}

// buildIFO writes a title set's information file: the header, a programme
// chain table, and for each chain a cell playback table and the cell
// positions that say which cells on the disc those actually are.
func buildIFO(t *testing.T, chains [][]testCell) []byte {
	t.Helper()
	const pgciSector = 1
	data := make([]byte, 8*isoSector)
	copy(data, "DVDVIDEO-VTS")
	binary.BigEndian.PutUint32(data[0xCC:], pgciSector)

	pgci := pgciSector * isoSector
	binary.BigEndian.PutUint16(data[pgci:], uint16(len(chains)))
	// The chains themselves go after the search pointers, one 0x100-byte
	// header each followed by its two tables.
	at := 8 + len(chains)*8
	for i, cells := range chains {
		binary.BigEndian.PutUint32(data[pgci+8+i*8+4:], uint32(at))
		pgc := pgci + at
		data[pgc+3] = byte(len(cells))
		play, pos := 0x100, 0x100+len(cells)*24
		binary.BigEndian.PutUint16(data[pgc+0xE8:], uint16(play))
		binary.BigEndian.PutUint16(data[pgc+0xEA:], uint16(pos))
		for c, cell := range cells {
			copy(data[pgc+play+c*24+4:], cell.time)
			binary.BigEndian.PutUint16(data[pgc+pos+c*4:], cell.vob)
			data[pgc+pos+c*4+3] = cell.cell
		}
		at += 0x100 + len(cells)*28
	}
	return data
}

type testCell struct {
	vob  uint16
	cell uint8
	time []byte // hours, minutes, seconds BCD, then rate and frames
}

// pal writes the time a DVD writes, at 25 frames a second.
func pal(h, m, s, f int) []byte {
	dec := func(v int) byte { return byte(v/10<<4 | v%10) }
	return []byte{dec(h), dec(m), dec(s), 1<<6 | dec(f)}
}

// An MPEG-2 program stream carries no duration and the estimate from its
// timestamps was out by a factor of two on a measured disc, so the disc's own
// answer is read instead — and the unit is the cell, because that is what the
// VOBs hold exactly one of.
func TestIFODuration(t *testing.T) {
	t.Run("distinct chains are summed", func(t *testing.T) {
		// Two programmes of different lengths, no cell shared: the title set
		// holds both, and it is worth the two together.
		ifo := buildIFO(t, [][]testCell{
			{{1, 1, pal(0, 24, 16, 24)}},
			{{1, 2, pal(0, 26, 25, 21)}},
		})
		want := int64((24*60+16)*1000 + 24*1000/25 + (26*60+25)*1000 + 21*1000/25)
		if got := ifoDurationOf(ifo); got != want {
			t.Errorf("got %d ms, want %d", got, want)
		}
	})

	t.Run("the same cells listed twice are counted once", func(t *testing.T) {
		// A disc that offers one programme under a dozen entries — parental
		// variants, angles — stores those cells once and is worth them once.
		one := []testCell{{1, 1, pal(0, 44, 55, 4)}, {1, 2, pal(0, 0, 5, 0)}}
		ifo := buildIFO(t, [][]testCell{one, one, one, one})
		want := int64((44*60+55)*1000 + 4*1000/25 + 5000)
		if got := ifoDurationOf(ifo); got != want {
			t.Errorf("got %d ms, want %d — the chains were added up", got, want)
		}
	})

	t.Run("anything unreadable answers nothing rather than a guess", func(t *testing.T) {
		for _, c := range []struct {
			name string
			data []byte
		}{
			{"empty", nil},
			{"short", []byte("DVDVIDEO-VTS")},
			{"not an IFO", bytes.Repeat([]byte{'x'}, 4*isoSector)},
		} {
			if got := ifoDurationOf(c.data); got != 0 {
				t.Errorf("%s: got %d ms, want 0", c.name, got)
			}
		}
		// A chain table pointing off the end of the file is not a reason to
		// read off the end of the file.
		bad := buildIFO(t, [][]testCell{{{1, 1, pal(0, 10, 0, 0)}}})
		binary.BigEndian.PutUint32(bad[0xCC:], 1<<20)
		if got := ifoDurationOf(bad); got != 0 {
			t.Errorf("got %d ms from a table that is not there", got)
		}
	})
}

// The duration reaches the item, which is the whole point of reading it.
func TestFolderTitleCarriesItsDuration(t *testing.T) {
	root := t.TempDir()
	holder := filepath.Join(root, "Larkspur.Nights.2004.PAL.DVDR-GRP")
	if err := os.MkdirAll(holder, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"VTS_01_1.VOB": 900, "VTS_01_2.VOB": 300} {
		if err := os.WriteFile(filepath.Join(holder, name), bytes.Repeat([]byte{'x'}, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ifo := buildIFO(t, [][]testCell{{{1, 1, pal(1, 39, 29, 0)}}})
	if err := os.WriteFile(filepath.Join(holder, "VTS_01_0.IFO"), ifo, 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := discEntries(holder, true)
	if err != nil {
		t.Fatal(err)
	}
	want := int64((1*3600 + 39*60 + 29) * 1000)
	if entries[0].durationMs != want {
		t.Fatalf("title says %d ms, want %d", entries[0].durationMs, want)
	}
	// And it is what the library reports, rather than an ffprobe estimate.
	l := quietLib(root)
	l.Scan(nil)
	items := l.List(Query{Limit: 10}).Items
	if len(items) != 1 || items[0].Duration != want {
		t.Fatalf("library says %+v, want one item of %d ms", items, want)
	}
}

// The disc's own answer has to survive enrichment. Before it did, a probe
// cached against the item — from before the parser existed, or from any
// later pass — was restored over it, and that estimate is out by a factor
// of two.
func TestDiscDurationOutranksAProbe(t *testing.T) {
	root := t.TempDir()
	holder := filepath.Join(root, "Larkspur.Nights.2004.PAL.DVDR-GRP")
	if err := os.MkdirAll(holder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(holder, "VTS_01_1.VOB"), bytes.Repeat([]byte{'x'}, 900), 0o644); err != nil {
		t.Fatal(err)
	}
	ifo := buildIFO(t, [][]testCell{{{1, 1, pal(1, 39, 29, 0)}}})
	if err := os.WriteFile(filepath.Join(holder, "VTS_01_0.IFO"), ifo, 0o644); err != nil {
		t.Fatal(err)
	}
	l := quietLib(root)
	l.Scan(nil)
	items := l.List(Query{Limit: 10}).Items
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	want := int64((1*3600 + 39*60 + 29) * 1000)
	// Whatever a probe would have said, and whatever was remembered from one.
	// Both doors: the metadata cache restores one at enrichment, and the
	// ffprobe that runs when a film is opened produces another.
	l.setMeta(items[0].ID, tagMeta{}, 1585632)
	if got := l.List(Query{Limit: 10}).Items[0].Duration; got != want {
		t.Errorf("after setMeta the duration became %d ms, want the disc's own %d", got, want)
	}
	l.setProbe(items[0].ID, Probe{DurationMs: 1585632, VCodec: "mpeg2video", Probed: true})
	got := l.List(Query{Limit: 10}).Items[0]
	if got.Duration != want {
		t.Errorf("after setProbe the duration became %d ms, want the disc's own %d", got.Duration, want)
	}
	// The rest of the probe is still worth having.
	if got.VCodec != "mpeg2video" {
		t.Errorf("codec %q, want the probe's", got.VCodec)
	}
}

// ifoDurationOf is the duration half of ifoTitle, which is all most of these
// cases are about.
func ifoDurationOf(data []byte) int64 {
	ms, _, _ := ifoTitle(data)
	return ms
}

// The cells also say where they are, and that is the seek index: a DVD's
// timestamps cannot be seeked by, so the position is worked out from the
// disc's own table instead.
func TestIFOSeekIndex(t *testing.T) {
	// Three cells of ten minutes each, laid down end to end.
	const perCell = 10 * 60 * 1000
	ifo := buildIFOAt(t, []placedCell{
		{testCell{1, 1, pal(0, 10, 0, 0)}, 0, 299},
		{testCell{1, 2, pal(0, 10, 0, 0)}, 300, 599},
		{testCell{1, 3, pal(0, 10, 0, 0)}, 600, 899},
	})
	ms, index, covers := ifoTitle(ifo)
	if ms != 3*perCell {
		t.Fatalf("duration %d ms, want %d", ms, 3*perCell)
	}
	if want := int64(900 * isoSector); covers != want {
		t.Fatalf("covers %d bytes, want %d", covers, want)
	}
	if len(index) != 4 {
		t.Fatalf("%d points, want one per cell plus the end", len(index))
	}

	it := Item{stored: &storedEntry{seek: index}}
	for _, c := range []struct {
		at   float64
		want int64
	}{
		{600, 300 * isoSector},  // exactly the second cell's first byte
		{900, 450 * isoSector},  // half way through it, interpolated
		{1200, 600 * isoSector}, // the third cell
		{5000, 900 * isoSector}, // past the end clamps to it
	} {
		got, ok := SeekByte(it, c.at)
		if !ok || got != c.want {
			t.Errorf("SeekByte(%.0fs) = %d, %v; want %d", c.at, got, ok, c.want)
		}
	}
	// The start of the film is not a seek, and a file with no index has none.
	if _, ok := SeekByte(it, 0); ok {
		t.Error("the beginning is not seeked to")
	}
	if _, ok := SeekByte(Item{}, 600); ok {
		t.Error("a plain file has no index")
	}
}

// A disc whose cells do not account for the bytes is one this does not
// understand, and a wrong index puts every seek somewhere else.
func TestIFOSeekIndexNeedsToLineUp(t *testing.T) {
	for _, c := range []struct {
		name  string
		cells []placedCell
	}{
		{"a gap between two cells", []placedCell{
			{testCell{1, 1, pal(0, 10, 0, 0)}, 0, 299},
			{testCell{1, 2, pal(0, 10, 0, 0)}, 400, 699},
		}},
		{"not starting at the beginning", []placedCell{
			{testCell{1, 1, pal(0, 10, 0, 0)}, 8, 307},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ms, index, covers := ifoTitle(buildIFOAt(t, c.cells))
			if ms <= 0 {
				t.Error("the duration is still worth having")
			}
			if index != nil || covers != 0 {
				t.Errorf("got an index of %d points covering %d bytes, want none", len(index), covers)
			}
		})
	}
}

type placedCell struct {
	cell        testCell
	first, last uint32
}

// buildIFOAt is buildIFO with the cells' sector ranges filled in.
func buildIFOAt(t *testing.T, cells []placedCell) []byte {
	t.Helper()
	plain := make([]testCell, len(cells))
	for i, c := range cells {
		plain[i] = c.cell
	}
	data := buildIFO(t, [][]testCell{plain})
	pgci := 1 * isoSector
	pgc := pgci + int(binary.BigEndian.Uint32(data[pgci+8+4:]))
	play := pgc + int(binary.BigEndian.Uint16(data[pgc+0xE8:]))
	for i, c := range cells {
		binary.BigEndian.PutUint32(data[play+i*24+8:], c.first)
		binary.BigEndian.PutUint32(data[play+i*24+0x14:], c.last)
	}
	return data
}

// A DVD release ships as one image split across seventy rar volumes, so a
// title inside it is two mappings deep: a range of the image, and the volume
// files that range crosses.
func TestDiscInsideAnArchive(t *testing.T) {
	path := buildISO(t, []isoFile{
		{"VTS_01_0.VOB", 4 * isoSector},
		{"VTS_01_1.VOB", 200 * isoSector},
	})
	img, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The image, split across three volumes at sizes that do not line up
	// with anything inside it.
	dir := t.TempDir()
	var segs []storedSeg
	for i, at := 0, 0; at < len(img); i++ {
		n := min(len(img)-at, 3*isoSector+777)
		vol := filepath.Join(dir, fmt.Sprintf("release.r%02d", i))
		if err := os.WriteFile(vol, img[at:at+n], 0o644); err != nil {
			t.Fatal(err)
		}
		segs = append(segs, storedSeg{path: vol, n: int64(n)})
		at += n
	}
	image := &storedEntry{name: "grp-lark.img", size: int64(len(img)), segs: segs}

	titles, err := discEntriesIn(image, "Larkspur.Nights.2004.PAL.DVDR-GRP")
	if err != nil {
		t.Fatal(err)
	}
	if len(titles) != 1 {
		t.Fatalf("got %d titles, want the feature", len(titles))
	}
	title := titles[0]
	// Named for the release, not for whatever the packer called the image.
	if want := "Larkspur.Nights.2004.PAL.DVDR-GRP.vob"; title.name != want {
		t.Errorf("name %q, want %q", title.name, want)
	}
	if want := int64(200 * isoSector); title.size != want {
		t.Errorf("size %d, want %d", title.size, want)
	}
	if len(title.segs) < 2 {
		t.Errorf("%d segments: a title that long has to cross volumes", len(title.segs))
	}
	// And it reads back as the VOB, out of files that know nothing about it.
	r := newStoredReader(title)
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if want := bytes.Repeat([]byte{'b'}, 200*isoSector); !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes of the wrong content", len(got))
	}
}

// The mapping on its own: a range of one piece of stored content onto the
// files it really lives in.
func TestPlaceInStored(t *testing.T) {
	e := &storedEntry{
		size: 300,
		segs: []storedSeg{
			{path: "a", off: 1000, n: 100},
			{path: "b", off: 2000, n: 100},
			{path: "c", off: 3000, n: 100},
		},
	}
	for _, c := range []struct {
		name   string
		off, n int64
		want   []storedSeg
	}{
		{"inside one file", 10, 20, []storedSeg{{"a", 1010, 20}}},
		{"across a join", 90, 20, []storedSeg{{"a", 1090, 10}, {"b", 2000, 10}}},
		{"a whole middle file", 100, 100, []storedSeg{{"b", 2000, 100}}},
		{"across two joins", 50, 200, []storedSeg{{"a", 1050, 50}, {"b", 2000, 100}, {"c", 3000, 50}}},
		{"the last file only", 250, 50, []storedSeg{{"c", 3050, 50}}},
		{"the whole of it", 0, 300, e.segs},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := placeInStored(e, c.off, c.n)
			if len(got) != len(c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("segment %d: got %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// The reader is handed to http.ServeContent, whose probing is all Seek: the
// end for the length, negative offsets for validation, and a read from the
// middle for the range itself.
func TestStoredReaderSeek(t *testing.T) {
	dir := t.TempDir()
	var segs []storedSeg
	for i, n := range []int{100, 200, 300} {
		p := filepath.Join(dir, fmt.Sprintf("part%d", i))
		body := bytes.Repeat([]byte{byte('a' + i)}, n)
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		segs = append(segs, storedSeg{path: p, n: int64(n)})
	}
	r := newStoredReader(&storedEntry{size: 600, segs: segs})
	defer r.Close()

	if pos, err := r.Seek(0, io.SeekEnd); err != nil || pos != 600 {
		t.Fatalf("SeekEnd = %d, %v; want the size", pos, err)
	}
	if pos, err := r.Seek(-350, io.SeekEnd); err != nil || pos != 250 {
		t.Fatalf("SeekEnd(-350) = %d, %v", pos, err)
	}
	buf := make([]byte, 100)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	// 250..350 spans the second and third parts.
	want := append(bytes.Repeat([]byte{'b'}, 50), bytes.Repeat([]byte{'c'}, 50)...)
	if !bytes.Equal(buf, want) {
		t.Errorf("read across the seam came back wrong")
	}
	if pos, err := r.Seek(-50, io.SeekCurrent); err != nil || pos != 300 {
		t.Fatalf("SeekCurrent(-50) = %d, %v", pos, err)
	}
	if _, err := r.Seek(-1, io.SeekStart); err == nil {
		t.Error("a negative position has to be refused")
	}
	// Reading past the end is EOF, not an error.
	if _, err := r.Seek(590, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	n, err := r.Read(make([]byte, 100))
	if n != 10 || (err != nil && err != io.EOF) {
		t.Errorf("read at the end: n=%d err=%v, want the last 10 bytes", n, err)
	}
}

// The watcher's route for a disc: a VOB changing re-derives the titles, and
// the image vanishing takes them with it.
func TestReindexDisc(t *testing.T) {
	root := t.TempDir()
	holder := filepath.Join(root, "Larkspur.Nights.2004.PAL.DVDR-GRP", "VIDEO_TS")
	if err := os.MkdirAll(holder, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, size int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(holder, name), bytes.Repeat([]byte{'x'}, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("VTS_01_1.VOB", 900)
	l := quietLib(root)
	l.Scan(nil)
	if items := l.List(Query{Limit: 10}).Items; len(items) != 1 || items[0].Size != 900 {
		t.Fatalf("after the scan: %+v", items)
	}

	// A second part arrives — the download growing — and the watcher reports
	// the file. The title grows rather than doubling.
	write("VTS_01_2.VOB", 300)
	l.reindexDisc(holder, true)
	items := l.List(Query{Limit: 10}).Items
	if len(items) != 1 || items[0].Size != 1200 {
		t.Fatalf("after the second part: %d items, size %d; want one of 1200", len(items), items[0].Size)
	}

	// The disc is deleted: nothing left to derive titles from.
	if err := os.RemoveAll(filepath.Dir(holder)); err != nil {
		t.Fatal(err)
	}
	l.reindexDisc(holder, true)
	if items := l.List(Query{Limit: 10}).Items; len(items) != 0 {
		t.Fatalf("after deletion: %+v, want nothing", items)
	}
}
