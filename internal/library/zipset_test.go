package library

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/zip"
)

// spannedMembers is three members laid out so that cuts at 30,000 and 62,000
// bytes fall inside the first two: a stored film that crosses from the first
// part into the second, a deflated one that crosses from the second into the
// last, and a picture wholly in the last.
func spannedMembers() []zipMember {
	return []zipMember{
		{name: "a.mkv", data: zipPayload(60_000, 31), method: zip.Store},
		{name: "b.mkv", data: zipPayload(80_000, 32), method: zip.Deflate},
		{name: "c.jpg", data: zipPayload(30_000, 33), method: zip.Store},
	}
}

// writeSpanned writes members as an archiver writes a spanned set — stem.z01
// on, and stem.zip holding the directory — cut at the given places in the
// stream after the split signature the first part opens with. Every entry's
// place becomes a part number and an offset from that part's start, which is
// the whole of what tells a spanned set from one archive. It returns the parts
// in order.
func writeSpanned(t *testing.T, dir, stem string, cuts []int64, members ...zipMember) []string {
	t.Helper()
	b := zipBytes(t, members...)
	le := binary.LittleEndian
	eocd := bytes.LastIndex(b, []byte("PK\x05\x06"))
	cdSize, cdOff := int64(le.Uint32(b[eocd+12:])), int64(le.Uint32(b[eocd+16:]))
	stream := append([]byte("PK\x07\x08"), b[:cdOff]...)
	bounds := append([]int64{0}, cuts...)
	last := len(bounds) - 1
	cd := slices.Clone(b[cdOff : cdOff+cdSize])
	for p := 0; p < len(cd); {
		n, m, k := int(le.Uint16(cd[p+28:])), int(le.Uint16(cd[p+30:])), int(le.Uint16(cd[p+32:]))
		at := int64(le.Uint32(cd[p+42:])) + 4
		d := last
		for bounds[d] > at {
			d--
		}
		le.PutUint16(cd[p+34:], uint16(d))
		le.PutUint32(cd[p+42:], uint32(at-bounds[d]))
		p += 46 + n + m + k
	}
	end := slices.Clone(b[eocd:])
	le.PutUint16(end[4:], uint16(last))
	le.PutUint16(end[6:], uint16(last))
	le.PutUint32(end[16:], uint32(int64(len(stream))-bounds[last]))
	var parts []string
	for k := 0; k <= last; k++ {
		path := filepath.Join(dir, fmt.Sprintf("%s.z%02d", stem, k+1))
		var data []byte
		if k < last {
			data = stream[bounds[k]:bounds[k+1]]
		} else {
			path = filepath.Join(dir, stem+".zip")
			data = slices.Concat(stream[bounds[k]:], cd, end)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, path)
	}
	return parts
}

// writePieces cuts an archive's bytes into pieces at the given places and
// writes them under the given names, in order.
func writePieces(t *testing.T, dir string, b []byte, cuts []int, names ...string) []string {
	t.Helper()
	bounds := slices.Concat([]int{0}, cuts, []int{len(b)})
	var paths []string
	for i, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, b[bounds[i]:bounds[i+1]], 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return paths
}

// checkMembers says whether the library holds exactly the members named, each
// reading as it was written.
func checkMembers(t *testing.T, l *Library, want ...zipMember) {
	t.Helper()
	items := itemsByName(l)
	var names []string
	for name := range items {
		names = append(names, name)
	}
	slices.Sort(names)
	var wantNames []string
	for _, m := range want {
		wantNames = append(wantNames, m.name)
	}
	slices.Sort(wantNames)
	if !slices.Equal(names, wantNames) {
		t.Fatalf("the library holds %q, want %q", names, wantNames)
	}
	for _, m := range want {
		if got := readAllOf(t, items[m.name]); !bytes.Equal(got, m.data) {
			t.Errorf("%s read %d bytes differently from the %d written", m.name, len(got), len(m.data))
		}
	}
}

// A spanned set is read across its parts: a member's place is a part and an
// offset from that part's start, and its bytes run on into the parts after.
func TestASpannedZipIsReadAcrossItsParts(t *testing.T) {
	dir := t.TempDir()
	members := spannedMembers()
	parts := writeSpanned(t, dir, "Harbour Lights", []int64{30_000, 62_000}, members...)
	l := quietLib(dir)
	l.Scan(nil)
	checkMembers(t, l, members...)
	for _, it := range itemsByName(l) {
		if container, _, _ := strings.Cut(it.Path, "\x00"); container != parts[2] {
			t.Errorf("%s hangs off %s, want the set's last part", it.Name, container)
		}
	}
}

// A spanned set with a part missing serves what is wholly in the parts that
// are here, and says what is not.
func TestASpannedZipServesWhatItsPartsHold(t *testing.T) {
	dir := t.TempDir()
	members := spannedMembers()
	parts := writeSpanned(t, dir, "Harbour Lights", []int64{30_000, 62_000}, members...)
	if err := os.Remove(parts[0]); err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.Scan(nil)
	checkMembers(t, l, members[1:]...)
	_, skipped, _, err := parseZip(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0].name != "a.mkv" || !strings.HasPrefix(skipped[0].why, "incomplete: part 1 of 3") {
		t.Errorf("reported %v, want the first member's part missing", skipped)
	}
	// And with the middle part gone, only what is wholly in the last.
	if err := os.Remove(parts[1]); err != nil {
		t.Fatal(err)
	}
	l = quietLib(dir)
	l.Scan(nil)
	checkMembers(t, l, members[2])
}

// A byte-split set is one archive cut into pieces, its offsets counting from
// the first: read whole, wherever the cuts fall — through a member, through
// the directory, through the end record.
func TestAByteSplitZipIsOneArchive(t *testing.T) {
	dir := t.TempDir()
	members := spannedMembers()
	b := zipBytes(t, members...)
	writePieces(t, dir, b, []int{40_000, len(b) - 300, len(b) - 10},
		"Harbour Lights.zip.001", "Harbour Lights.zip.002", "Harbour Lights.zip.003", "Harbour Lights.zip.004")
	l := quietLib(dir)
	l.Scan(nil)
	checkMembers(t, l, members...)
}

// A file host names each piece apart, an identifier of its own in front of
// the number — and the pieces of one set are still one set, beside another
// set whose name differs.
func TestAByteSplitZipRenamedByItsHostIsOneSet(t *testing.T) {
	dir := t.TempDir()
	members := spannedMembers()
	b := zipBytes(t, members...)
	writePieces(t, dir, b, []int{50_000},
		"harbour-lights-zip-Ab12Cd34.zip.001", "harbour-lights-zip-Zy98Xw76.zip.002")
	other := zipMember{name: "d.mkv", data: zipPayload(9_000, 34), method: zip.Store}
	writePieces(t, dir, zipBytes(t, other), nil, "low-water-zip-Qr56St78.zip.001")
	l := quietLib(dir)
	l.Scan(nil)
	checkMembers(t, l, append(members, other)...)
}

// Two sets that read alike once the host's identifiers are off cannot be told
// apart, and are not guessed at: the piece that is a whole archive is read,
// the one that is a piece of something is not.
func TestPiecesThatCannotBeToldApartAreNotJoined(t *testing.T) {
	dir := t.TempDir()
	members := spannedMembers()
	b := zipBytes(t, members...)
	writePieces(t, dir, b, []int{50_000}, "tern-signal-Aa11Bb22.zip.001", "tern-signal-Cc33Dd44.zip.002")
	whole := zipMember{name: "e.mkv", data: zipPayload(9_000, 35), method: zip.Store}
	writePieces(t, dir, zipBytes(t, whole), nil, "tern-signal-Ee55Ff66.zip.001")
	l := quietLib(dir)
	l.Scan(nil)
	checkMembers(t, l, whole)
}

// A member's own header has to name it. A directory whose offset for one
// member lands on another's header would otherwise serve that other member's
// bytes under this one's name; one that lands in the middle of the data, on
// no header at all.
func TestAHeaderNamingAnotherMemberIsNotServed(t *testing.T) {
	dir := t.TempDir()
	a := zipMember{name: "a.mkv", data: zipPayload(9_000, 36), method: zip.Store}
	b := zipMember{name: "b.mkv", data: zipPayload(9_000, 37), method: zip.Store}
	c := zipMember{name: "c.mkv", data: zipPayload(9_000, 38), method: zip.Store}
	raw := zipBytes(t, a, b, c)
	le := binary.LittleEndian
	eocd := bytes.LastIndex(raw, []byte("PK\x05\x06"))
	cd := int(le.Uint32(raw[eocd+16:]))
	var entries []int
	for p := cd; p < eocd; {
		entries = append(entries, p)
		p += 46 + int(le.Uint16(raw[p+28:])) + int(le.Uint16(raw[p+30:])) + int(le.Uint16(raw[p+32:]))
	}
	le.PutUint32(raw[entries[1]+42:], le.Uint32(raw[entries[0]+42:]))      // b's place is a's header
	le.PutUint32(raw[entries[2]+42:], le.Uint32(raw[entries[0]+42:])+1000) // c's is inside a's bytes
	path := filepath.Join(dir, "set.zip")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.Scan(nil)
	checkMembers(t, l, a)
	_, skipped, _, _ := parseZip(path)
	var why []string
	for _, s := range skipped {
		why = append(why, s.name+": "+s.why)
	}
	slices.Sort(why)
	if len(why) != 2 || !strings.Contains(why[0], "names another member") || !strings.Contains(why[1], "no member header") {
		t.Errorf("reported %q", why)
	}
}

// An archive with something in front of it — a self-extractor's own code —
// has every offset short by that much, and is read as the zip package reads
// it.
func TestAnArchiveWithSomethingInFrontIsRead(t *testing.T) {
	dir := t.TempDir()
	members := spannedMembers()
	b := slices.Concat(bytes.Repeat([]byte{0x90}, 4096), zipBytes(t, members...))
	if err := os.WriteFile(filepath.Join(dir, "set.zip"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.Scan(nil)
	checkMembers(t, l, members...)
}

// The watcher names a set as the walk does, from any of its parts — or the
// lock that holds a container's read and reconcile together holds nothing —
// and a piece that arrives late, or goes, reads the set again.
func TestTheWatcherNamesASplitSetAsTheWalkDoes(t *testing.T) {
	dir := t.TempDir()
	members := spannedMembers()
	spanned := writeSpanned(t, dir, "Harbour Lights", []int64{30_000, 62_000}, members...)
	for _, p := range spanned {
		if got := zipContainerOf(p); got != spanned[2] {
			t.Errorf("%s names its set %s, want %s", filepath.Base(p), got, spanned[2])
		}
	}

	split := t.TempDir()
	b := zipBytes(t, members...)
	pieces := writePieces(t, split, b, []int{40_000, 90_000},
		"low-water-zip-Ab12Cd34.zip.001", "low-water-zip-Ef56Gh78.zip.002", "low-water-zip-Ij90Kl12.zip.003")
	for _, p := range pieces {
		if got := zipContainerOf(p); got != pieces[0] {
			t.Errorf("%s names its set %s, want %s", filepath.Base(p), got, pieces[0])
		}
	}

	// The last piece arrives after the walk.
	last, err := os.ReadFile(pieces[2])
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(pieces[2])
	l := quietLib(split)
	l.Scan(nil)
	if n := l.Size(); n != 0 {
		t.Fatalf("a set short of its last piece gave %d items", n)
	}
	if err := os.WriteFile(pieces[2], last, 0o644); err != nil {
		t.Fatal(err)
	}
	l.AddFile(pieces[2])
	checkMembers(t, l, members...)
	// And goes again.
	os.Remove(pieces[2])
	l.Remove(pieces[2])
	if n := l.Size(); n != 0 {
		t.Errorf("a set that lost its last piece kept %d items", n)
	}
}

// Deleting a member of a split set takes every part of it: the set is what
// the member's bytes are in.
func TestDeletingASpannedMemberTakesEveryPart(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	parts := writeSpanned(t, filepath.Join(dir, "Releases"), "Harbour Lights", []int64{30_000, 62_000}, spannedMembers()...)
	l := quietLib(dir)
	l.Scan(nil)
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: itemsByName(l)["b.mkv"].ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		if !slices.ContainsFunc(plan.Files, func(f PlannedFile) bool { return f.Path == p }) {
			t.Errorf("%s is not in the plan: %v", filepath.Base(p), plan.Files)
		}
	}
}

// The fields of an entry too large for their sixteen and thirty-two bits are
// carried by a zip64 field, in the order they outgrew them.
func TestADirectoryEntryReadsItsZip64Field(t *testing.T) {
	le := binary.LittleEndian
	name := "big.mkv"
	extra := make([]byte, 4+8+8+8+4)
	le.PutUint16(extra, 0x0001)
	le.PutUint16(extra[2:], 28)
	le.PutUint64(extra[4:], 6<<30)
	le.PutUint64(extra[12:], 5<<30)
	le.PutUint64(extra[20:], 7<<30)
	le.PutUint32(extra[28:], 3)
	e := make([]byte, 46)
	le.PutUint32(e, 0x02014b50)
	le.PutUint16(e[10:], 8)
	le.PutUint32(e[20:], 0xFFFFFFFF)
	le.PutUint32(e[24:], 0xFFFFFFFF)
	le.PutUint16(e[28:], uint16(len(name)))
	le.PutUint16(e[30:], uint16(len(extra)))
	le.PutUint16(e[34:], 0xFFFF)
	le.PutUint32(e[42:], 0xFFFFFFFF)
	got, err := readZipDirectory(slices.Concat(e, []byte(name), extra))
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	if g := got[0]; g.raw != name || g.size != 6<<30 || g.packed != 5<<30 || g.off != 7<<30 || g.disk != 3 || g.method != 8 {
		t.Errorf("read %+v", g)
	}
}
