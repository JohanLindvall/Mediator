package library

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// The search chips carry the caller's restrictions. The listing itself
// always did; its Matching counts were computed over the whole library and
// only masked afterwards — which cannot fix the numbers counted across
// kinds, and never fixed the path restriction at all. A face's chips were
// counting matches it may not see.
func TestMatchingHonoursRestrictions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "shelfA", "clip one.mp4"), "vvvv")
	writeFile(t, filepath.Join(root, "shelfA", "song one.mp3"), "aaaa")
	writeFile(t, filepath.Join(root, "shelfB", "clip two.mp4"), "vvvv")

	lib := New([]string{root}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	lib.Scan(nil)

	// Unrestricted: the search counts everything it matches.
	res := lib.List(Query{Search: "clip", Limit: 10})
	if res.Matching == nil || res.Matching.Video != 2 {
		t.Fatalf("unrestricted matching: %+v", res.Matching)
	}

	// Restricted to one directory: the other shelf's hit must not count.
	paths := ParsePaths(filepath.Join(root, "shelfA"))
	res = lib.List(Query{Search: "clip", Paths: paths, Limit: 10})
	if res.Matching == nil || res.Matching.Video != 1 || res.Matching.Total != 1 {
		t.Fatalf("path-restricted matching: %+v", res.Matching)
	}

	// Restricted to one kind: a search that also matches music counts none
	// of it for a face that is not shown music.
	res = lib.List(Query{Search: "one", Kinds: KindsOf(KindVideo), Limit: 10})
	if res.Matching == nil || res.Matching.Video != 1 || res.Matching.Audio != 0 {
		t.Fatalf("kind-restricted matching: %+v", res.Matching)
	}
	if res.Matching.Total != 1 {
		t.Fatalf("kind-restricted total: %+v", res.Matching)
	}
}
