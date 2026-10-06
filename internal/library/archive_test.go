// SPDX-License-Identifier: MIT

package library

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/rartest"
	"github.com/klauspost/compress/zip"
)

// zipMember is one member of an archive a test writes.
type zipMember struct {
	name   string
	data   []byte
	method uint16
	flags  uint16
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// writeZip writes an archive of the members at path. Method 12 is written as
// it is, standing for a method nothing here can unpack.
func writeZip(t *testing.T, path string, members ...zipMember) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, zipBytes(t, members...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// zipBytes is an archive of the members, as writeZip writes it.
func zipBytes(t *testing.T, members ...zipMember) []byte {
	t.Helper()
	var f bytes.Buffer
	w := zip.NewWriter(&f)
	w.RegisterCompressor(12, func(out io.Writer) (io.WriteCloser, error) { return nopWriteCloser{out}, nil })
	for _, m := range members {
		hw, err := w.CreateHeader(&zip.FileHeader{Name: m.name, Method: m.method, Flags: m.flags, Modified: time.Unix(1_700_000_000, 0)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := hw.Write(m.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Bytes()
}

// zipPayload is content that deflates into real blocks: mostly a pattern, one
// byte in seven noise.
func zipPayload(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	for i := range b {
		if i%7 == 0 {
			b[i] = byte(r.Intn(256))
		} else {
			b[i] = byte(i / 97)
		}
	}
	return b
}

func readAllOf(t *testing.T, it Item) []byte {
	t.Helper()
	f, err := OpenItem(it)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func itemsByName(l *Library) map[string]Item {
	out := map[string]Item{}
	for _, it := range l.List(Query{Limit: 500}).Items {
		out[it.Name] = it
	}
	return out
}

// A zip's media is indexed and read whichever way it is stored: a member kept
// as it is reads in place, a deflated one is unpacked as it is read. What is
// not media is passed over; what cannot be read — encrypted, or a method
// nothing here unpacks — is left out and reported.
func TestAZipsMediaIsIndexedAndRead(t *testing.T) {
	dir := t.TempDir()
	song, clip := zipPayload(200_000, 1), zipPayload(150_000, 2)
	path := filepath.Join(dir, "Harbour Lights.zip")
	writeZip(t, path,
		zipMember{name: "Harbour Lights/01 Low Water.mp3", data: song, method: zip.Deflate},
		zipMember{name: "clip.mp4", data: clip, method: zip.Store},
		zipMember{name: "notes.txt", data: []byte("liner notes"), method: zip.Deflate},
		zipMember{name: "locked.jpg", data: clip[:100], method: zip.Store, flags: 1},
		zipMember{name: "odd.jpg", data: clip[:100], method: 12},
	)
	l := quietLib(dir)
	l.Scan(nil)
	items := itemsByName(l)
	if len(items) != 2 {
		t.Fatalf("indexed %d items, want the song and the clip: %v", len(items), slices.Collect(func(yield func(string) bool) {
			for n := range items {
				yield(n)
			}
		}))
	}
	s, c := items["01 Low Water.mp3"], items["clip.mp4"]
	if s.Kind != KindAudio || s.Size != int64(len(song)) || !s.Archived() || !s.packed() {
		t.Errorf("song: %+v", s)
	}
	if c.Kind != KindVideo || c.Size != int64(len(clip)) || !c.Archived() || c.packed() {
		t.Errorf("clip: %+v", c)
	}
	if !bytes.Equal(readAllOf(t, s), song) {
		t.Error("the deflated member read differently")
	}
	if !bytes.Equal(readAllOf(t, c), clip) {
		t.Error("the stored member read differently")
	}
	if got := l.List(Query{Search: "harbour lights low water"}).Total; got != 1 {
		t.Errorf("a search through the folder inside the archive found %d", got)
	}
	_, skipped, _, err := parseZip(path)
	if err != nil {
		t.Fatal(err)
	}
	var why []string
	for _, s := range skipped {
		why = append(why, s.name+": "+s.why)
	}
	slices.Sort(why)
	if len(why) != 2 || !strings.HasPrefix(why[0], "locked.jpg: encrypted") || !strings.Contains(why[1], "method 12") {
		t.Errorf("reported %q, want the encrypted member and the unreadable method", why)
	}
}

// A packed member reads like a file: any byte from anywhere, forward and
// back, through ReadAt, Seek and Read alike, from several readers at once.
func TestAPackedMemberReadsLikeAFile(t *testing.T) {
	dir := t.TempDir()
	data := zipPayload(1_000_003, 3)
	writeZip(t, filepath.Join(dir, "a.zip"), zipMember{name: "film.mkv", data: data, method: zip.Deflate})
	l := quietLib(dir)
	l.Scan(nil)
	it := itemsByName(l)["film.mkv"]
	f, err := OpenItem(it)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, ok := f.(*packedReader); !ok {
		t.Fatalf("opened as %T, want it unpacked as it is read", f)
	}
	if n, err := f.Seek(0, io.SeekEnd); err != nil || n != int64(len(data)) {
		t.Fatalf("its end is at %d (%v), want %d", n, err, len(data))
	}
	r := rand.New(rand.NewSource(4))
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			r := rand.New(rand.NewSource(seed))
			for range 25 {
				off := r.Int63n(int64(len(data)))
				buf := make([]byte, 1+r.Intn(5000))
				n, err := f.ReadAt(buf, off)
				end := min(off+int64(len(buf)), int64(len(data)))
				if int64(n) != end-off || (err != nil && !(errors.Is(err, io.EOF) && end == int64(len(data)))) {
					t.Errorf("ReadAt(%d, %d) = %d, %v", len(buf), off, n, err)
					return
				}
				if !bytes.Equal(buf[:n], data[off:end]) {
					t.Errorf("ReadAt at %d read the wrong bytes", off)
					return
				}
			}
		}(int64(g))
	}
	wg.Wait()
	for range 20 {
		off := r.Int63n(int64(len(data)))
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 777)
		n, _ := io.ReadFull(f, buf)
		if !bytes.Equal(buf[:n], data[off:off+int64(n)]) {
			t.Fatalf("Seek(%d) and Read read the wrong bytes", off)
		}
	}
	if _, err := f.ReadAt(make([]byte, 1), int64(len(data))); !errors.Is(err, io.EOF) {
		t.Errorf("a read at the end answered %v, want EOF", err)
	}
}

// An archive holding more media than the cap is left out whole — never the
// first so many of its members — and one that grows past it loses what it
// had; with the cap off, everything is taken in.
func TestAnArchiveOverTheCapIsLeftOutWhole(t *testing.T) {
	t.Cleanup(func() { SetArchiveMax(DefaultArchiveMax) })
	SetArchiveMax(3)
	dir := t.TempDir()
	path := filepath.Join(dir, "tiles.zip")
	members := func(n int) []zipMember {
		var out []zipMember
		for i := range n {
			out = append(out, zipMember{name: fmt.Sprintf("z/%d.jpg", i), data: zipPayload(100, int64(i)), method: zip.Store})
		}
		return out
	}
	l := quietLib(dir)
	writeZip(t, path, members(3)...)
	l.Scan(nil)
	if n := l.Size(); n != 3 {
		t.Fatalf("an archive at the cap brought in %d, want all 3", n)
	}
	writeZip(t, path, members(4)...)
	l.Scan(nil)
	if n := l.Size(); n != 0 {
		t.Fatalf("an archive past the cap kept %d members, want none", n)
	}
	SetArchiveMax(0)
	l.Scan(nil)
	if n := l.Size(); n != 4 {
		t.Fatalf("with the cap off it brought in %d, want all 4", n)
	}
	// Past ten entries per member allowed, the archive is over the cap on
	// its end record alone — however few of the entries are media.
	SetArchiveMax(1)
	many := []zipMember{{name: "one.jpg", data: []byte("x"), method: zip.Store}}
	for i := range 11 {
		many = append(many, zipMember{name: fmt.Sprintf("meta/%d.json", i), data: []byte("{}"), method: zip.Store})
	}
	writeZip(t, path, many...)
	var over tooManyMembers
	if _, _, _, err := parseZip(path); !errors.As(err, &over) {
		t.Errorf("an archive of 12 entries under a cap of 1 answered %v", err)
	}
}

// The count comes off the end record, the zip64 one where the ordinary one
// has run out of digits.
func TestZipEntryCountReadsTheEndRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("writes an archive of seventy thousand entries")
	}
	for _, n := range []int{0, 5, 70_000} {
		path := filepath.Join(t.TempDir(), "a.zip")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		w := zip.NewWriter(f)
		for i := range n {
			if _, err := w.CreateHeader(&zip.FileHeader{Name: fmt.Sprint(i), Method: zip.Store}); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		info, _ := f.Stat()
		got, err := zipEnd(f, info.Size())
		f.Close()
		if err != nil || got.entries != int64(n) || got.disk != 0 {
			t.Errorf("an archive of %d entries read as %+v (%v)", n, got, err)
		}
	}
}

// standInRarDecoder answers for rardecode, which nothing here can feed:
// every open of a member is counted, and hands over the payload the set was
// written with.
func standInRarDecoder(t *testing.T, payload []byte) *int {
	t.Helper()
	opens := 0
	var mu sync.Mutex
	was := openRarMember
	openRarMember = func(string, int64, *storedEntry) (io.ReadCloser, error) {
		mu.Lock()
		opens++
		mu.Unlock()
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	t.Cleanup(func() { openRarMember = was })
	return &opens
}

// A compressed member of a rar set is served now rather than reported, in
// both generations of the format; a solid one, compressed against the
// members before it, still cannot be, and says so; one still arriving is
// incomplete, as a stored one is.
func TestACompressedRarMemberIsServed(t *testing.T) {
	payload := rartest.Payload(90_000)
	for _, v5 := range []bool{false, true} {
		t.Run(fmt.Sprintf("v5=%v", v5), func(t *testing.T) {
			dir := t.TempDir()
			vols := rartest.WriteSetWith(t, dir, "show", "Episode.One.mkv", payload, 3, v5, rartest.Options{Compressed: true})
			entries, skipped, err := parseRarSet(vols[0])
			if err != nil || len(entries) != 1 || len(skipped) != 0 {
				t.Fatalf("entries %d, skipped %+v, err %v", len(entries), skipped, err)
			}
			if e := entries[0]; e.pack == nil || e.pack.format != packRar || e.size != int64(len(payload)) || len(e.segs) != 3 {
				t.Fatalf("entry: %+v", e)
			}
			opens := standInRarDecoder(t, payload)
			l := quietLib(dir)
			l.Scan(nil)
			it := itemsByName(l)["Episode.One.mkv"]
			if !it.packed() || it.Size != int64(len(payload)) {
				t.Fatalf("item: %+v", it)
			}
			if !bytes.Equal(readAllOf(t, it), payload) {
				t.Fatal("the member read differently")
			}
			f, _ := OpenItem(it)
			defer f.Close()
			buf := make([]byte, 10)
			if _, err := f.ReadAt(buf, 50_000); err != nil || !bytes.Equal(buf, payload[50_000:50_010]) {
				t.Fatalf("a read half way: %v", err)
			}
			if _, err := f.ReadAt(buf, 10); err != nil || !bytes.Equal(buf, payload[10:20]) {
				t.Fatalf("a read back near the start: %v", err)
			}
			if *opens != 3 {
				t.Errorf("unpacked from the start %d times, want 3: once for the whole, once forward, once back", *opens)
			}

			solid := t.TempDir()
			vols = rartest.WriteSetWith(t, solid, "box", "Episode.Two.mkv", payload, 2, v5, rartest.Options{Compressed: true, Solid: true})
			entries, skipped, _ = parseRarSet(vols[0])
			if len(entries) != 0 || len(skipped) != 1 || !strings.HasPrefix(skipped[0].why, "solid") {
				t.Errorf("a solid member: entries %d, skipped %+v", len(entries), skipped)
			}

			partial := t.TempDir()
			vols = rartest.WriteSetWith(t, partial, "late", "Episode.Three.mkv", payload, 3, v5, rartest.Options{Compressed: true})
			if err := os.Remove(vols[2]); err != nil {
				t.Fatal(err)
			}
			entries, skipped, _ = parseRarSet(vols[0])
			if len(entries) != 0 || len(skipped) != 1 || !strings.HasPrefix(skipped[0].why, "incomplete") {
				t.Errorf("a set short of its last volume: entries %d, skipped %+v", len(entries), skipped)
			}
		})
	}
}

// testScratch is a scratch space with a budget, as server.Scratch is.
type testScratch struct {
	dir   string
	limit int64
	mu    sync.Mutex
	used  map[string]int64
}

func (s *testScratch) Sub(name string) (string, error) {
	d := filepath.Join(s.dir, name)
	return d, os.MkdirAll(d, 0o755)
}

func (s *testScratch) Report(owner string, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used[owner] = n
}

func (s *testScratch) Excess() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, n := range s.used {
		total += n
	}
	if s.limit <= 0 || total <= s.limit {
		return 0
	}
	return total - s.limit
}

func (s *testScratch) Limit() int64 { return s.limit }

// withUnpacker gives a test an unpacker of its own over a scratch space, and
// puts the process's back afterwards.
func withUnpacker(t *testing.T, space *testScratch) {
	t.Helper()
	keep := unpackKeepFor
	unpackKeepFor = 0 // a test asks within the second; see the in-use test
	t.Cleanup(func() { unpackKeepFor = keep })
	was := unpacks
	unpacks = newUnpacker()
	if err := SetScratch(space, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		CloseScratch()
		unpacks = was
	})
}

func unpackDirFiles(t *testing.T, space *testScratch) []string {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(space.dir, unpackSub))
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// A packed member played is unpacked once into the scratch space, checked,
// and served from there; every reader after that opens the copy.
func TestAPackedMemberIsUnpackedOnceForPlayback(t *testing.T) {
	space := &testScratch{dir: t.TempDir(), used: map[string]int64{}}
	withUnpacker(t, space)
	dir := t.TempDir()
	payload := rartest.Payload(80_000)
	rartest.WriteSetWith(t, dir, "show", "Episode.One.mkv", payload, 2, true, rartest.Options{Compressed: true})
	opens := standInRarDecoder(t, payload)
	l := quietLib(dir)
	l.Scan(nil)
	it := itemsByName(l)["Episode.One.mkv"]

	var wg sync.WaitGroup
	paths := make([]string, 8)
	errs := make([]error, 8)
	for i := range paths {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			paths[i], errs[i] = Unpacked(context.Background(), it)
		}(i)
	}
	wg.Wait()
	for i := range paths {
		if errs[i] != nil || paths[i] != paths[0] {
			t.Fatalf("asker %d: %q, %v", i, paths[i], errs[i])
		}
	}
	if *opens != 1 {
		t.Errorf("eight askers unpacked it %d times, want once", *opens)
	}
	if b, _ := os.ReadFile(paths[0]); !bytes.Equal(b, payload) {
		t.Error("the copy differs from the member")
	}
	if got := space.used[unpackOwner]; got != int64(len(payload)) {
		t.Errorf("the budget was told %d bytes, want %d", got, len(payload))
	}
	f, err := OpenItem(it)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.(*os.File); !ok {
		t.Errorf("with a copy made, OpenItem opened %T, want the copy", f)
	}
	f.Close()
	was := unpackFrom
	unpackFrom = 1
	t.Cleanup(func() { unpackFrom = was })
	f, err = OpenForPlayback(context.Background(), it)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.(*os.File); !ok {
		t.Errorf("playback opened %T, want the copy", f)
	}
	f.Close()
	if *opens != 1 {
		t.Errorf("the member was unpacked %d times in all, want once", *opens)
	}
}

// What does not check out is not believed: a copy whose checksum differs from
// the archive's is thrown away and not remembered, so the next ask tries
// again rather than inheriting the refusal.
func TestAnUnpackingThatDoesNotCheckOutIsThrownAway(t *testing.T) {
	space := &testScratch{dir: t.TempDir(), used: map[string]int64{}}
	withUnpacker(t, space)
	dir := t.TempDir()
	writeZip(t, filepath.Join(dir, "a.zip"), zipMember{name: "film.mkv", data: zipPayload(50_000, 5), method: zip.Deflate})
	l := quietLib(dir)
	l.Scan(nil)
	it := itemsByName(l)["film.mkv"]
	it.stored.pack.crc ^= 1
	var first error
	for i := range 2 {
		_, err := Unpacked(context.Background(), it)
		if !errors.Is(err, ErrDamagedMember) || !strings.Contains(err.Error(), "checksum") {
			t.Fatalf("a copy that did not check out answered %v", err)
		}
		if i == 0 {
			first = err
		} else if err != first {
			t.Error("the second ask unpacked the member again rather than remembering it is damaged")
		}
	}
	if files := unpackDirFiles(t, space); len(files) != 0 {
		t.Errorf("left %q in the scratch space", files)
	}
}

// A member whose stream breaks off is damaged, said in those words — the
// decoder's own are no use to a viewer — and remembered, since asking again
// would unpack the same bytes to fail at the same place.
func TestADamagedMemberIsSaidOnce(t *testing.T) {
	space := &testScratch{dir: t.TempDir(), used: map[string]int64{}}
	withUnpacker(t, space)
	dir := t.TempDir()
	path := filepath.Join(dir, "a.zip")
	writeZip(t, path, zipMember{name: "film.mkv", data: zipPayload(400_000, 6), method: zip.Deflate})
	l := quietLib(dir)
	l.Scan(nil)
	it := itemsByName(l)["film.mkv"]
	// Overwrite the middle of the deflated stream, as a download that went
	// wrong does: the archive's directory still says what it held.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	seg := it.stored.segs[0]
	for i := seg.off + seg.n/2; i < seg.off+seg.n/2+64; i++ {
		b[i] = 0xFF
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Unpacked(context.Background(), it)
	if !errors.Is(err, ErrDamagedMember) {
		t.Fatalf("a stream that breaks off answered %v", err)
	}
	if _, again := Unpacked(context.Background(), it); again != err {
		t.Errorf("asked again, it answered %v rather than the verdict it had", again)
	}
	if files := unpackDirFiles(t, space); len(files) != 0 {
		t.Errorf("left %q in the scratch space", files)
	}
}

// The budget is the rewrapper's: a member larger than all of it is read as
// it is packed instead, and making room frees the least recently wanted copy.
func TestUnpackedCopiesKeepToTheBudget(t *testing.T) {
	space := &testScratch{dir: t.TempDir(), limit: 150_000, used: map[string]int64{}}
	withUnpacker(t, space)
	dir := t.TempDir()
	a, b, big := zipPayload(90_000, 6), zipPayload(90_000, 7), zipPayload(200_000, 8)
	writeZip(t, filepath.Join(dir, "a.zip"),
		zipMember{name: "a.mkv", data: a, method: zip.Deflate},
		zipMember{name: "b.mkv", data: b, method: zip.Deflate},
		zipMember{name: "big.mkv", data: big, method: zip.Deflate},
	)
	l := quietLib(dir)
	l.Scan(nil)
	items := itemsByName(l)
	if _, err := Unpacked(context.Background(), items["big.mkv"]); !errors.Is(err, ErrNoUnpack) {
		t.Errorf("a member larger than the budget answered %v", err)
	}
	was := unpackFrom
	unpackFrom = 1
	t.Cleanup(func() { unpackFrom = was })
	f, err := OpenForPlayback(context.Background(), items["big.mkv"])
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(f); !bytes.Equal(got, big) {
		t.Error("played as it is packed, it read differently")
	}
	f.Close()

	pa, err := Unpacked(context.Background(), items["a.mkv"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Unpacked(context.Background(), items["b.mkv"]); err != nil {
		t.Fatal(err)
	}
	if exists(pa) {
		t.Error("the older copy is still there, over the budget")
	}
	if _, ok := unpacks.ready(items["a.mkv"]); ok {
		t.Error("the copy freed is still offered")
	}
}

// What an earlier run unpacked is taken in, and what it left half written
// is cleared away.
func TestUnpackedCopiesOutliveARestart(t *testing.T) {
	space := &testScratch{dir: t.TempDir(), used: map[string]int64{}}
	withUnpacker(t, space)
	dir := t.TempDir()
	writeZip(t, filepath.Join(dir, "a.zip"), zipMember{name: "film.mkv", data: zipPayload(40_000, 9), method: zip.Deflate})
	l := quietLib(dir)
	l.Scan(nil)
	it := itemsByName(l)["film.mkv"]
	p, err := Unpacked(context.Background(), it)
	if err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(filepath.Dir(p), strings.Repeat("a", 40)+".mkv.part")
	if err := os.WriteFile(stray, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	withUnpacker(t, space) // the next run, over the same space
	if got, ok := unpacks.ready(it); !ok || got != p {
		t.Errorf("after a restart the copy is %q, %v", got, ok)
	}
	if exists(stray) {
		t.Error("a half-written copy survived the restart")
	}
	if got := space.used[unpackOwner]; got != it.Size {
		t.Errorf("the budget was told %d bytes after the restart, want %d", got, it.Size)
	}
}

// Shutdown stops an unpacking in flight, and what it was writing goes.
func TestShutdownStopsAnUnpacking(t *testing.T) {
	space := &testScratch{dir: t.TempDir(), used: map[string]int64{}}
	withUnpacker(t, space)
	dir := t.TempDir()
	payload := rartest.Payload(60_000)
	rartest.WriteSetWith(t, dir, "show", "Episode.One.mkv", payload, 1, true, rartest.Options{Compressed: true})
	release := make(chan struct{})
	started := make(chan struct{})
	was := openRarMember
	openRarMember = func(string, int64, *storedEntry) (io.ReadCloser, error) {
		close(started)
		return io.NopCloser(io.MultiReader(bytes.NewReader(payload[:10]), blocked{release})), nil
	}
	t.Cleanup(func() { openRarMember = was; close(release) })
	l := quietLib(dir)
	l.Scan(nil)
	it := itemsByName(l)["Episode.One.mkv"]
	done := make(chan error, 1)
	go func() {
		_, err := Unpacked(context.Background(), it)
		done <- err
	}()
	<-started
	CloseScratch()
	select {
	case err := <-done:
		if err == nil {
			t.Error("an unpacking stopped at shutdown was handed over")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not stop the unpacking")
	}
	for range 100 {
		if len(unpackDirFiles(t, space)) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("left %q in the scratch space", unpackDirFiles(t, space))
}

// blocked is a reader that answers nothing until it is released, a byte at a
// time after that so the context is asked between reads.
type blocked struct{ release chan struct{} }

func (b blocked) Read(p []byte) (int, error) {
	select {
	case <-b.release:
		return 0, io.EOF
	case <-time.After(5 * time.Millisecond):
		if len(p) == 0 {
			return 0, nil
		}
		p[0] = 0
		return 1, nil
	}
}

// Room is made before an unpacking is written, not after: the budget is what
// the disk may hold at its fullest, and a copy is the size of a film. When
// the second copy begins, the first has gone already.
func TestRoomIsMadeBeforeAnUnpacking(t *testing.T) {
	space := &testScratch{dir: t.TempDir(), limit: 150_000, used: map[string]int64{}}
	withUnpacker(t, space)
	dir := t.TempDir()
	payload := rartest.Payload(90_000)
	rartest.WriteSetWith(t, dir, "one", "First.mkv", payload, 1, true, rartest.Options{Compressed: true})
	rartest.WriteSetWith(t, dir, "two", "Second.mkv", payload, 1, true, rartest.Options{Compressed: true})
	l := quietLib(dir)
	l.Scan(nil)
	items := itemsByName(l)
	var first string
	firstThere := true
	was := openRarMember
	openRarMember = func(_ string, _ int64, e *storedEntry) (io.ReadCloser, error) {
		if e.name == "Second.mkv" {
			firstThere = exists(first)
		}
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	t.Cleanup(func() { openRarMember = was })
	var err error
	if first, err = Unpacked(context.Background(), items["First.mkv"]); err != nil {
		t.Fatal(err)
	}
	if _, err := Unpacked(context.Background(), items["Second.mkv"]); err != nil {
		t.Fatal(err)
	}
	if firstThere {
		t.Error("the first copy was still there when the second began: the disk held both")
	}
}

// Deleting a member of a zip deletes the archive, since nothing can be taken
// out of one, and names what goes with it — as a rar set's members do.
func TestDeletingAZipMemberTakesTheArchive(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "Releases", "Harbour Lights")
	path := filepath.Join(release, "Harbour Lights.zip")
	writeZip(t, path,
		zipMember{name: "01 Low Water.mp3", data: zipPayload(5000, 10), method: zip.Deflate},
		zipMember{name: "02 High Water.mp3", data: zipPayload(5000, 11), method: zip.Deflate},
	)
	if err := os.WriteFile(filepath.Join(dir, "Releases", "keep.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.Scan(nil)
	song := itemsByName(l)["01 Low Water.mp3"]
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: song.ID})
	if err != nil {
		t.Fatal(err)
	}
	planned := slices.ContainsFunc(plan.Files, func(f PlannedFile) bool { return f.Path == path })
	if !planned {
		t.Fatalf("the archive is not in the plan: folders %v, files %v", plan.Folders, plan.Files)
	}
	if !slices.Contains(plan.Others, "02 High Water.mp3") {
		t.Errorf("the other member is not named: %v", plan.Others)
	}
	l.DeleteNow(plan)
	if exists(path) {
		t.Error("the archive is still there")
	}
	if n := l.Size(); n != 1 {
		t.Errorf("%d items left, want only the track outside the archive", n)
	}
}

// A copy asked for lately is not pruned, however far over the budget making
// room would have to go: a viewer's next seek opens it again, and pruned in
// between it would be unpacked all over again in the middle of the film.
func TestACopyBeingWatchedIsNotPruned(t *testing.T) {
	space := &testScratch{dir: t.TempDir(), limit: 150_000, used: map[string]int64{}}
	withUnpacker(t, space)
	unpackKeepFor = time.Hour
	dir := t.TempDir()
	writeZip(t, filepath.Join(dir, "a.zip"),
		zipMember{name: "a.mkv", data: zipPayload(90_000, 12), method: zip.Deflate},
		zipMember{name: "b.mkv", data: zipPayload(90_000, 13), method: zip.Deflate},
	)
	l := quietLib(dir)
	l.Scan(nil)
	items := itemsByName(l)
	pa, err := Unpacked(context.Background(), items["a.mkv"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Unpacked(context.Background(), items["b.mkv"]); err != nil {
		t.Fatal(err)
	}
	if !exists(pa) {
		t.Error("a copy asked for a moment ago was pruned to make room")
	}
}
