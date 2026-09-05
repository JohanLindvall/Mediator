package library

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/rartest"
)

// --- tests --------------------------------------------------------------------

func testRarRoundtrip(t *testing.T, v5 bool) {
	dir := t.TempDir()
	payload := rartest.Payload(300_000)
	vols := rartest.WriteSet(t, dir, "movie", "Big.Movie.mkv", payload, 3, v5)

	entries, _, err := parseRarSet(vols[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.name != "Big.Movie.mkv" || e.size != int64(len(payload)) || len(e.segs) != 3 {
		t.Fatalf("entry: %+v", e)
	}

	r := newStoredReader(e)
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("stitched content differs from original")
	}
	// Random access across a segment boundary.
	buf := make([]byte, 1000)
	if _, err := r.ReadAt(buf, 99_500); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf, payload[99_500:100_500]) {
		t.Fatal("ReadAt across segment boundary differs")
	}
}

func TestRar4Roundtrip(t *testing.T) { testRarRoundtrip(t, false) }
func TestRar5Roundtrip(t *testing.T) { testRarRoundtrip(t, true) }

// TestRarFixturesAgainstUnrar proves the handcrafted volumes are real rar
// files by extracting them with the reference unrar and comparing bytes.
func TestRarFixturesAgainstUnrar(t *testing.T) {
	unrar, err := exec.LookPath("unrar")
	if err != nil {
		t.Skip("unrar not installed")
	}
	for _, v5 := range []bool{false, true} {
		dir := t.TempDir()
		payload := rartest.Payload(300_000)
		vols := rartest.WriteSet(t, dir, "movie", "Big.Movie.mkv", payload, 3, v5)
		out, err := exec.Command(unrar, "p", "-inul", vols[0]).Output()
		if err != nil {
			t.Fatalf("unrar (v5=%v) rejected fixture: %v", v5, err)
		}
		if !bytes.Equal(out, payload) {
			t.Fatalf("unrar (v5=%v) extracted %d bytes, want %d (content mismatch)",
				v5, len(out), len(payload))
		}
	}
}

func TestRarLibraryIntegration(t *testing.T) {
	dir := t.TempDir()
	payload := rartest.Payload(120_000)
	rartest.WriteSet(t, dir, "show", "Episode.One.mkv", payload, 2, true)

	lib := New([]string{dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	lib.Scan(nil)

	res := lib.List(Query{})
	if res.Total != 1 {
		t.Fatalf("indexed %d items, want 1", res.Total)
	}
	it := res.Items[0]
	if it.Kind != KindVideo || it.Name != "Episode.One.mkv" || it.Size != int64(len(payload)) {
		t.Fatalf("item: %+v", it)
	}
	if !it.Archived() {
		t.Fatal("item should be archived")
	}

	f, err := OpenItem(it)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("OpenItem content differs")
	}

	// Search finds it by member-name words.
	res = lib.List(Query{Search: "episode one"})
	if res.Total != 1 {
		t.Fatal("search should find the archived episode")
	}

	// Deleting a later volume truncates the set: the member drops out.
	if err := os.Remove(filepath.Join(dir, "show.part2.rar")); err != nil {
		t.Fatal(err)
	}
	lib.Scan(nil)
	if res := lib.List(Query{}); res.Total != 0 {
		t.Fatalf("truncated set still lists %d items", res.Total)
	}
}

// openVolumeFDs counts this process's descriptors pointing inside dir.
func openVolumeFDs(t *testing.T, dir string) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("/proc/self/fd unavailable")
	}
	n := 0
	for _, e := range ents {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(target, dir+string(filepath.Separator)) {
			n++
		}
	}
	return n
}

func TestRarReaderCapsOpenFiles(t *testing.T) {
	// More volumes than the cap, whatever the cap is set to, so the
	// assertions below always have eviction to catch.
	parts := rarMaxOpenFiles * 3
	dir := t.TempDir()
	payload := rartest.Payload(20_000 * parts)
	vols := rartest.WriteSet(t, dir, "many", "Huge.Movie.mkv", payload, parts, true)

	entries, _, err := parseRarSet(vols[0])
	if err != nil {
		t.Fatal(err)
	}
	e := entries[0]
	if len(e.segs) != parts {
		t.Fatalf("got %d segments, want %d", len(e.segs), parts)
	}

	r := newStoredReader(e)
	defer r.Close()

	// check asserts both halves of the contract: within the documented cap,
	// and strictly fewer than one descriptor per volume (eviction happened).
	check := func(stage string) {
		t.Helper()
		if n := len(r.files); n > rarMaxOpenFiles {
			t.Fatalf("%s: %d files open, cap is %d", stage, n, rarMaxOpenFiles)
		}
		if n := openVolumeFDs(t, dir); n > rarMaxOpenFiles || n >= parts {
			t.Fatalf("%s: %d descriptors open for %d volumes, cap is %d",
				stage, n, parts, rarMaxOpenFiles)
		}
	}

	// Reading straight through every volume must not accumulate descriptors.
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("sequential content differs")
	}
	check("after sequential read")

	// Random access jumping between distant volumes stays capped too.
	step := int64(len(payload) / (parts + 1))
	for _, k := range []int{parts - 1, 1, parts / 2, 3, parts - 2, 0} {
		off := int64(k) * step
		buf := make([]byte, 1_000)
		if _, err := r.ReadAt(buf, off); err != nil {
			t.Fatalf("ReadAt(%d): %v", off, err)
		}
		if !bytes.Equal(buf, payload[off:off+1_000]) {
			t.Fatalf("content differs at %d", off)
		}
		check(fmt.Sprintf("after seek to %d", off))
	}

	// Close releases everything.
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if n := openVolumeFDs(t, dir); n != 0 {
		t.Fatalf("%d descriptors still open after Close", n)
	}
}

func TestRarReaderConcurrentReadAt(t *testing.T) {
	dir := t.TempDir()
	payload := rartest.Payload(200_000)
	vols := rartest.WriteSet(t, dir, "conc", "Movie.mkv", payload, 8, false)
	entries, _, err := parseRarSet(vols[0])
	if err != nil {
		t.Fatal(err)
	}
	r := newStoredReader(entries[0])
	defer r.Close()

	const readers = 16
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for k := range readers {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			off := int64(k) * 12_000
			buf := make([]byte, 5_000)
			if _, err := r.ReadAt(buf, off); err != nil {
				errs <- fmt.Errorf("ReadAt(%d): %w", off, err)
				return
			}
			if !bytes.Equal(buf, payload[off:off+5_000]) {
				errs <- fmt.Errorf("content differs at %d", off)
			}
		}(k)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := len(r.files); n > rarMaxOpenFiles {
		t.Fatalf("%d files open after concurrent reads, cap is %d", n, rarMaxOpenFiles)
	}
}

// A set that holds nothing servable says so. The question "why is this
// release not in the library?" is otherwise unanswerable from outside: a
// compressed member parses without error and yields nothing, which looks
// exactly like a set nobody ever walked.
func TestParseRarSetReportsWhatItCannotServe(t *testing.T) {
	dir := t.TempDir()
	payload := rartest.Payload(40_000)

	t.Run("a stored member is served and reported as nothing", func(t *testing.T) {
		vols := rartest.WriteSet(t, dir, "plain", "Feature.mkv", payload, 3, false)
		entries, skipped, err := parseRarSet(vols[0])
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("got %d entries, want the member", len(entries))
		}
		if len(skipped) != 0 {
			t.Errorf("nothing was skipped, yet it reported %+v", skipped)
		}
	})

	t.Run("a set missing its later volumes says how far it got", func(t *testing.T) {
		sub := t.TempDir()
		vols := rartest.WriteSet(t, sub, "partial", "Feature.mkv", payload, 3, false)
		// The last volume has not arrived — the ordinary state of a download.
		if err := os.Remove(vols[2]); err != nil {
			t.Fatal(err)
		}
		entries, skipped, err := parseRarSet(vols[0])
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("an incomplete member was served: %+v", entries)
		}
		if len(skipped) != 1 || skipped[0].name != "Feature.mkv" {
			t.Fatalf("got %+v, want the member reported as incomplete", skipped)
		}
		if !strings.Contains(skipped[0].why, "incomplete") {
			t.Errorf("reason %q does not say what is wrong", skipped[0].why)
		}
	})
}

// The report is an answer, not a heartbeat: a member the set cannot serve is
// said once per process, however many rescans find it again. Measured before
// this, one set of three hundred compressed pictures put three hundred lines
// in the log every ten minutes.
func TestUnservableMembersAreReportedOnce(t *testing.T) {
	dir := t.TempDir()
	vols := rartest.WriteSet(t, dir, "partial", "Feature.mkv", rartest.Payload(40_000), 3, false)
	if err := os.Remove(vols[2]); err != nil { // the last volume has not arrived
		t.Fatal(err)
	}
	var logged bytes.Buffer
	l := New([]string{dir}, slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	for range 3 {
		l.Scan(nil)
	}
	out := logged.String()
	if n := strings.Count(out, "rar set holds members it cannot serve"); n != 1 {
		t.Fatalf("the set was reported %d times over three scans, want once:\n%s", n, out)
	}
	// One line for the set, saying how many and why — and not naming the
	// members, which for a set of three hundred pictures was the flood.
	if !strings.Contains(out, "skipped=1") || !strings.Contains(out, "incomplete") {
		t.Fatalf("the one report does not say how many or why:\n%s", out)
	}
	if strings.Contains(out, "Feature.mkv") {
		t.Fatalf("the report names the members:\n%s", out)
	}
}
