package library

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// Two questions asked in turn — a confined caller's own totals and the
// matching counts of its search — both answer from the cache once each has
// been walked. A single slot had them evict each other on every page.
func TestCountsCacheKeepsSeveralAnswers(t *testing.T) {
	l := libForCounts(t)
	a := CountQuery{Search: "tribute"}
	b := CountQuery{Search: "tribute", Kinds: kindBit(KindAudio)}
	l.CountsFor(a)
	l.CountsFor(b)
	misses := l.counts.misses
	for range 3 {
		l.CountsFor(a)
		l.CountsFor(b)
	}
	if l.counts.misses != misses {
		t.Errorf("alternating two questions walked the index %d more times", l.counts.misses-misses)
	}
	if got := l.CountsFor(a); got.Audio != 2 {
		t.Errorf("cached answer = %+v, want the two tracks", got)
	}
}

// A favourites-only listing, or one showing the hidden, is narrowed as
// much as a search is: its chips must not read the whole library's totals.
func TestCountsForNarrowedByFlagsIsNotTheWholeLibrary(t *testing.T) {
	l := libForCounts(t)
	if got, all := l.CountsFor(CountQuery{Favourites: true}), l.Counts(); got.Total == all.Total {
		t.Errorf("favourites-only counts = %+v, the whole library's totals", got)
	}
}

// Removing a directory that held films and their subtitles sweeps the
// subtitles with the films: the sweep used to be skipped whenever the
// films' own removal had something to report.
func TestRemovingADirectorySweepsItsSubtitles(t *testing.T) {
	dir := t.TempDir()
	show := filepath.Join(dir, "Show")
	if err := os.MkdirAll(show, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(show, "Episode.mp4"), "video")
	write(t, filepath.Join(show, "Episode.en.srt"), "x")
	l := subLib(t, dir)
	video := l.List(Query{}).Items[0]
	if n := len(l.Subtitles(video)); n != 1 {
		t.Fatalf("subtitles before removal = %d, want 1", n)
	}
	if err := os.RemoveAll(show); err != nil {
		t.Fatal(err)
	}
	l.Remove(show)
	if l.Size() != 0 {
		t.Errorf("items after removing the directory = %d, want 0", l.Size())
	}
	l.mu.RLock()
	left := len(l.subsByDir[show])
	l.mu.RUnlock()
	if left != 0 {
		t.Errorf("subtitles left listed under a removed directory: %d", left)
	}
}

// A file whose kind changes moves between the totals: what the chips read
// is maintained as things come and go, and a nameless file that later reads
// as something else is one of the things that go.
func TestUpsertFollowsAChangeOfKind(t *testing.T) {
	l := quietLib("/m")
	l.upsert("/m/nameless", KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
	if c := l.Counts(); c.Audio != 1 || c.Video != 0 {
		t.Fatalf("counts after the first sighting = %+v", c)
	}
	changed, _ := l.upsert("/m/nameless", KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	if !changed {
		t.Error("a change of kind was not reported as a change")
	}
	if c := l.Counts(); c.Audio != 0 || c.Video != 1 {
		t.Errorf("counts after the kind changed = %+v, want one video and no audio", c)
	}
	if it, _ := l.Get(PathID("/m/nameless")); it.Kind != KindVideo {
		t.Errorf("kind = %q, want video", it.Kind)
	}
}

// A write that fails keeps its changes pending, so the next tick tries
// again; cleared beforehand, a deleted file's removal was lost for good.
func TestFailedFlushKeepsItsChangesPending(t *testing.T) {
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	l := quietLib("/m")
	l.SetMetaDB(db)
	l.upsert("/m/a.mp3", KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
	db.Close() // every write from here on fails
	flushNow(l, db)
	l.mu.RLock()
	pending := len(l.dirty)
	l.mu.RUnlock()
	if pending != 1 {
		t.Errorf("pending after a failed write = %d, want the one item still dirty", pending)
	}
}

// A file outside the roots is not indexed by the watcher's hand, however it
// came to be reported: a settle timer armed before the directories changed
// can name one.
func TestAddFileRefusesWhatIsOutsideTheRoots(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	l := quietLib(root)
	p := filepath.Join(elsewhere, "clip.mp4")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l.AddFile(p)
	if l.Size() != 0 {
		t.Error("a file outside the roots was indexed")
	}
}

// The tokeniser: letters of any alphabet are letters, punctuation splits,
// an empty query wants nothing.
func TestSearchTokens(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"Tide.Song-04", []string{"tide", "song", "04"}},
		{"Grön Fyrväg_2018 (live)", []string{"grön", "fyrväg", "2018", "live"}},
		{"Маяк над рекой", []string{"маяк", "над", "рекой"}},
		{"  ", nil},
		{"", nil},
	} {
		if got := tokenize(c.in); !slices.Equal(got, c.want) {
			t.Errorf("tokenize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if !matchWords("tide song", searchWords("")) {
		t.Error("an empty query must match everything")
	}
	if !matchWords("grön fyrväg", searchWords("FYRVÄG")) {
		t.Error("a query is matched without regard to case in any alphabet")
	}
}

// The roots are made absolute and each is kept once.
func TestSetRootsCleansTheList(t *testing.T) {
	dir := t.TempDir()
	l := quietLib(dir)
	if err := os.Chdir(dir); err != nil {
		t.Skip("cannot change directory")
	}
	l.SetRoots([]string{".", dir, filepath.Join(dir, ".")})
	got := l.Roots()
	if len(got) != 1 || got[0] != filepath.Clean(dir) {
		t.Errorf("roots = %q, want the one directory, absolute", got)
	}
}
