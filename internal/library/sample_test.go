package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/rartest"
)

func TestSampleFolderSkippedBesideTheRelease(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "Some.Release")
	write(t, filepath.Join(release, "release.mkv"), "the film")
	write(t, filepath.Join(release, "Sample", "release-sample.mkv"), "a taste")

	l := quietLib(dir)
	l.Scan(nil)
	got := names(l)
	if len(got) != 1 || got[0] != "release.mkv" {
		t.Fatalf("indexed %v, want only the release", got)
	}
}

func TestSampleFolderSkippedBesideAnArchivedRelease(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "Archived.Release")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	rartest.WriteSet(t, release, "archived.release", "release.mkv", rartest.Payload(40_000), 2, true)
	write(t, filepath.Join(release, "Sample", "release-sample.mkv"), "a taste")

	l := quietLib(dir)
	l.Scan(nil)
	got := names(l)
	if len(got) != 1 || got[0] != "release.mkv" {
		t.Fatalf("indexed %v, want only the archived release", got)
	}
}

func TestSampleFolderKeptWhenItIsAllThereIs(t *testing.T) {
	// Nothing playable beside it, so the excerpt is the only content and
	// dropping it would hide the directory entirely.
	dir := t.TempDir()
	release := filepath.Join(dir, "Nothing.Else")
	write(t, filepath.Join(release, "notes.nfo"), "text")
	write(t, filepath.Join(release, "Sample", "only-sample.mkv"), "a taste")

	l := quietLib(dir)
	l.Scan(nil)
	if got := names(l); len(got) != 1 || got[0] != "only-sample.mkv" {
		t.Fatalf("indexed %v, want the sample to be kept", got)
	}
}

func TestSampleFolderSkippedForWatcherAdds(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "Some.Release")
	write(t, filepath.Join(release, "release.mkv"), "the film")
	l := quietLib(dir)
	l.Scan(nil)

	p := filepath.Join(release, "Sample", "release-sample.mkv")
	write(t, p, "a taste")
	l.AddFile(p) // as the watcher would
	if got := names(l); len(got) != 1 {
		t.Fatalf("indexed %v after a sample appeared, want one", got)
	}
}

func TestExistingSampleDroppedOnRescan(t *testing.T) {
	// A sample indexed before the release arrived is reconciled away once
	// the real file shows up.
	dir := t.TempDir()
	release := filepath.Join(dir, "Some.Release")
	sample := filepath.Join(release, "Sample", "release-sample.mkv")
	write(t, sample, "a taste")
	l := quietLib(dir)
	l.Scan(nil)
	if got := names(l); len(got) != 1 {
		t.Fatalf("indexed %v, want the lone sample", got)
	}

	write(t, filepath.Join(release, "release.mkv"), "the film")
	l.Scan(nil)
	got := names(l)
	if len(got) != 1 || got[0] != "release.mkv" {
		t.Fatalf("indexed %v, want only the release once it arrived", got)
	}
}

// The same excerpt the sample folder holds, put beside the release instead —
// which is the other way scene releases arrange it, and the way that leaves
// a hundred megabytes of "Sample.mkv" in the listing.
func TestSampleFileBesideTheRelease(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.Join(dir, "A.Film.2019.1080p.BluRay-GRP")
	write(t, filepath.Join(rel, "a-film-1080p.mkv"), strings.Repeat("v", 5000))
	write(t, filepath.Join(rel, "Sample.mkv"), strings.Repeat("s", 100))
	// Another release whose sample is named the other common way.
	rel2 := filepath.Join(dir, "B.Film.2020.1080p.BluRay-GRP")
	write(t, filepath.Join(rel2, "b-film-1080p.mkv"), strings.Repeat("v", 5000))
	write(t, filepath.Join(rel2, "b-film-1080p-sample.mkv"), strings.Repeat("s", 100))

	l := quietLib(dir)
	l.Scan(nil)

	names := map[string]bool{}
	for _, it := range l.List(Query{}).Items {
		names[it.Name] = true
	}
	if len(names) != 2 {
		t.Errorf("indexed %v, want the two films alone", names)
	}
	for _, unwanted := range []string{"Sample.mkv", "b-film-1080p-sample.mkv"} {
		if names[unwanted] {
			t.Errorf("%s was indexed", unwanted)
		}
	}
}

// Nothing is dropped on the strength of its name: where the excerpt is all
// there is, it is the release.
func TestSampleFileKeptWhenItIsAllThereIs(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Clips", "Sample.mkv"), strings.Repeat("s", 100))
	// And where the file named like one is no smaller than its neighbour, it
	// is not an excerpt of anything.
	write(t, filepath.Join(dir, "Session", "sample.mkv"), strings.Repeat("s", 4000))
	write(t, filepath.Join(dir, "Session", "take2.mkv"), strings.Repeat("v", 5000))

	l := quietLib(dir)
	l.Scan(nil)
	if n := l.List(Query{}).Total; n != 3 {
		t.Errorf("indexed %d items, want all three kept", n)
	}
}

func TestNamedLikeASample(t *testing.T) {
	for _, name := range []string{
		"Sample.mkv", "sample.mp4", "a-film-1080p-sample.mkv",
		"a.film.sample.mkv", "sample-a-film.mkv", "SAMPLE.MKV",
		// The word in the middle, which is how scene samples are named as
		// often as not.
		"group-sample.film68.avi", "a.film-sample.1080p.mkv",
	} {
		if !namedLikeASample(name) {
			t.Errorf("%q should read as a sample", name)
		}
	}
	for _, name := range []string{
		"Sample Text.mkv", "The Sampler.mkv", "resample.mkv",
		"samples-of-2019.mkv", "film.mkv",
	} {
		if namedLikeASample(name) {
			t.Errorf("%q should not read as a sample", name)
		}
	}
}

// A film split over discs keeps nothing in the release directory itself, so
// a rule that read only the parent's files found no release, concluded the
// excerpt was all there was, and indexed the sample. This is the real shape
// that exposed it: CD1, CD2, an nfo, and a Sample folder.
func TestSampleFolderSkippedBesideADiscSplitRelease(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "Some.Release.DVDRip-GROUP")
	write(t, filepath.Join(release, "CD1", "group-film-a.avi"), "the first half")
	write(t, filepath.Join(release, "CD2", "group-film-b.avi"), "the second half")
	write(t, filepath.Join(release, "group-film.nfo"), "notes")
	write(t, filepath.Join(release, "Sample", "group-sample.film.avi"), "a taste")

	l := quietLib(dir)
	l.Scan(nil)
	got := names(l)
	if len(got) != 2 {
		t.Fatalf("indexed %v, want both discs and no sample", got)
	}
	for _, n := range got {
		if strings.Contains(n, "sample") {
			t.Fatalf("indexed %v, want no sample", got)
		}
	}
}

// A disc that is itself a folder of parts is one level further down again.
func TestSampleFolderSkippedBesideANestedRelease(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "Some.Release")
	write(t, filepath.Join(release, "CD1", "parts", "film.mkv"), "the film")
	write(t, filepath.Join(release, "Sample", "sample.avi"), "a taste")

	l := quietLib(dir)
	l.Scan(nil)
	for _, n := range names(l) {
		if strings.Contains(n, "sample") {
			t.Fatalf("indexed %v, want no sample", names(l))
		}
	}
}

// The bound is not decoration: below it, this stops being "the release this
// sample belongs to" and becomes "some video somewhere underneath".
func TestSampleFolderKeptWhenTheReleaseIsTooDeep(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "Odd.Layout")
	write(t, filepath.Join(release, "a", "b", "c", "film.mkv"), "the film")
	write(t, filepath.Join(release, "Sample", "sample.mkv"), "a taste")

	l := quietLib(dir)
	l.Scan(nil)
	var kept bool
	for _, n := range names(l) {
		if strings.Contains(n, "sample") {
			kept = true
		}
	}
	if !kept {
		t.Fatal("a release four levels down is not what this sample is an excerpt of")
	}
}

// One excerpt does not make another one redundant: a folder of samples
// beside a folder of samples keeps both.
func TestSampleFolderNotJustifiedByAnotherSample(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "Only.Samples")
	write(t, filepath.Join(release, "Samples", "one-sample.mkv"), "a taste")
	write(t, filepath.Join(release, "Sample", "two-sample.mkv"), "another")

	l := quietLib(dir)
	l.Scan(nil)
	if got := names(l); len(got) != 2 {
		t.Fatalf("indexed %v, want both excerpts kept", got)
	}
}
