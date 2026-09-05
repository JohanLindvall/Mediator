package library

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

func boolp(v bool) *bool { return &v }

// flagLib indexes two of each kind and returns their ids in insertion order.
func flagLib() (*Library, []string) {
	l := quietLib("/media")
	var ids []string
	for i, k := range []Kind{KindVideo, KindVideo, KindImage, KindImage, KindAudio, KindPlaylist} {
		p := fmt.Sprintf("/media/item%02d", i)
		l.upsert(p, k, int64(100+i), time.Unix(int64(i), 0), fileKey{}, false)
		ids = append(ids, PathID(p))
	}
	return l, ids
}

func listedIDs(l *Library, q Query) map[string]bool {
	out := map[string]bool{}
	for _, it := range l.List(q).Items {
		out[it.ID] = true
	}
	return out
}

func TestHiddenItemsStayOutOfListings(t *testing.T) {
	l, ids := flagLib()
	l.SetFlags([]string{ids[0], ids[4]}, boolp(true), nil, nil, nil)
	l.SetFlags([]string{ids[1], ids[4]}, nil, boolp(true), nil, nil)

	cases := []struct {
		name string
		q    Query
		want []string
	}{
		{"default hides", Query{}, []string{ids[1], ids[2], ids[3], ids[5]}},
		{"include", Query{ShowHidden: "include"}, ids},
		{"only", Query{ShowHidden: "only"}, []string{ids[0], ids[4]}},
		{"favourites", Query{FavouritesOnly: true}, []string{ids[1]}},
		{"hidden favourites", Query{ShowHidden: "only", FavouritesOnly: true}, []string{ids[4]}},
		{"kind and hidden", Query{Kind: KindVideo}, []string{ids[1]}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := listedIDs(l, c.q)
			if len(got) != len(c.want) {
				t.Fatalf("listed %d items, want %d", len(got), len(c.want))
			}
			for _, id := range c.want {
				if !got[id] {
					t.Fatalf("item %s missing from the listing", id)
				}
			}
		})
	}

	// The judgement travels with the item, both in a listing and on its own.
	for _, it := range l.List(Query{ShowHidden: "include"}).Items {
		if want := it.ID == ids[0] || it.ID == ids[4]; it.Hidden != want {
			t.Errorf("listed item %s: hidden=%v, want %v", it.ID, it.Hidden, want)
		}
	}
	if it, ok := l.Get(ids[4]); !ok || !it.Hidden || !it.Favourite {
		t.Errorf("Get(%s) = %+v", ids[4], it)
	}
}

func TestCountsFollowFlags(t *testing.T) {
	l, ids := flagLib()

	// Only the items a default listing shows are counted.
	brute := func() Counts {
		var c Counts
		l.mu.RLock()
		defer l.mu.RUnlock()
		for id, it := range l.items {
			if l.flags[id].Hidden {
				continue
			}
			switch it.Kind {
			case KindVideo:
				c.Video++
			case KindImage:
				c.Image++
			case KindAudio:
				c.Audio++
			case KindPlaylist:
				c.Playlist++
			}
			c.Total++
		}
		return c
	}
	check := func(stage string) {
		t.Helper()
		got, want := l.Counts(), brute()
		got.Albums, got.Artists = 0, 0 // maintained by the album/artist builds
		if got != want {
			t.Fatalf("%s: counts %+v, want %+v", stage, got, want)
		}
		if got.Total != l.List(Query{}).Total {
			t.Fatalf("%s: counts total %d, listing total %d", stage, got.Total, l.List(Query{}).Total)
		}
	}
	check("nothing flagged")

	l.SetFlags([]string{ids[0], ids[2], ids[5]}, boolp(true), nil, nil, nil)
	check("after hiding three kinds")

	l.SetFlags([]string{ids[2]}, boolp(false), nil, nil, nil)
	check("after unhiding one")

	// Removing a hidden item drops it from both totals at once.
	l.removePath("/media/item00")
	l.notify()
	check("after removing a hidden item")

	// A favourite is still a visible item.
	l.SetFlags([]string{ids[1]}, nil, boolp(true), nil, nil)
	check("after marking a favourite")
}

func TestFlagFiltersAreInTheQueryCacheKey(t *testing.T) {
	l, ids := flagLib()
	l.SetFlags([]string{ids[0]}, boolp(true), nil, nil, nil)

	l.List(Query{Sort: "name"})
	cached := l.lastQuery
	if cached == nil {
		t.Fatal("query result was not cached")
	}
	// The default spelled out is the same query, not a second one.
	l.List(Query{Sort: "name", ShowHidden: "exclude"})
	if l.lastQuery != cached {
		t.Fatal("spelling out the default rebuilt the query")
	}
	l.List(Query{Sort: "name", ShowHidden: "include"})
	if l.lastQuery == cached {
		t.Fatal("changing ShowHidden did not rebuild")
	}
	cached = l.lastQuery
	l.List(Query{Sort: "name", ShowHidden: "include", FavouritesOnly: true})
	if l.lastQuery == cached {
		t.Fatal("FavouritesOnly did not rebuild")
	}
	// A flag change has to invalidate the cache as surely as an added file.
	cached = l.lastQuery
	l.SetFlags([]string{ids[1]}, boolp(true), nil, nil, nil)
	l.List(Query{Sort: "name", ShowHidden: "include", FavouritesOnly: true})
	if l.lastQuery == cached {
		t.Fatal("a flag change did not invalidate the cached listing")
	}
}

func TestSetFlagsBatch(t *testing.T) {
	l, ids := flagLib()

	version := l.Version()
	out := l.SetFlags([]string{ids[0], ids[1], ids[2], "deadbeefdeadbeef"}, boolp(true), nil, nil, nil)
	if len(out) != 3 {
		t.Fatalf("judged %d items, want the 3 the index knows: %+v", len(out), out)
	}
	for _, id := range ids[:3] {
		if !out[id].Hidden {
			t.Fatalf("item %s not hidden in the response: %+v", id, out[id])
		}
	}
	if l.Version() == version {
		t.Fatal("a flag change must bump the version so other clients refresh")
	}
	if total := l.List(Query{}).Total; total != 3 {
		t.Fatalf("listing shows %d items, want 3", total)
	}

	// Writing the same values again changes nothing.
	version = l.Version()
	l.SetFlags(ids[:3], boolp(true), nil, nil, nil)
	if l.Version() != version {
		t.Fatal("a no-op flag write bumped the version")
	}

	// One flag at a time: clearing hidden must not clear favourite.
	l.SetFlags([]string{ids[0]}, nil, boolp(true), nil, nil)
	l.SetFlags([]string{ids[0]}, boolp(false), nil, nil, nil)
	if f := l.Flags(ids[0]); f.Hidden || !f.Favourite {
		t.Fatalf("flags = %+v, want favourite only", f)
	}
	l.SetFlags(ids[:3], boolp(false), nil, nil, nil)
	if total := l.List(Query{ShowHidden: "only"}).Total; total != 0 {
		t.Fatalf("%d items still hidden", total)
	}
}

func TestFlagsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	hidden := filepath.Join(dir, "One.mkv")
	write(t, hidden, "video")
	write(t, filepath.Join(dir, "Two.mkv"), "video")
	dbPath := filepath.Join(t.TempDir(), "media.db")

	db, err := blob.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	id := PathID(hidden)
	l.SetFlags([]string{id}, boolp(true), boolp(true), nil, nil)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := blob.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	l2 := quietLib(dir)
	l2.SetMetaDB(db2)
	l2.Scan(nil)

	if it, ok := l2.Get(id); !ok || !it.Hidden || !it.Favourite {
		t.Fatalf("after restart Get = %+v (ok=%v)", it, ok)
	}
	if res := l2.List(Query{}); res.Total != 1 {
		t.Fatalf("after restart the default listing shows %d items, want 1", res.Total)
	}
	if c := l2.Counts(); c.Video != 1 {
		t.Fatalf("after restart counts = %+v, want 1 visible video", c)
	}
}

func TestFlagsOutliveTheItem(t *testing.T) {
	dir := t.TempDir()
	movie := filepath.Join(dir, "One.mkv")
	write(t, movie, "video")
	write(t, filepath.Join(dir, "Two.mkv"), "video") // pruning skips an empty index
	dbPath := filepath.Join(t.TempDir(), "media.db")

	db, err := blob.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	id := PathID(movie)
	l.SetFlags([]string{id}, boolp(true), nil, nil, nil)

	// The file disappears and the caches are reconciled against the disk.
	if err := os.Remove(movie); err != nil {
		t.Fatal(err)
	}
	l.Scan(nil)
	flushNow(l, db)
	l.PruneDB(db)
	if f, ok := db.GetFlags(id); !ok || !f.Hidden {
		t.Fatal("pruning threw away the owner's judgement")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// It comes back — a remounted disk, a rename undone — and the judgement
	// is still attached, for a library that has never seen the file before.
	write(t, movie, "video")
	db2, err := blob.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	l2 := quietLib(dir)
	l2.SetMetaDB(db2)
	l2.Scan(nil)
	if it, ok := l2.Get(id); !ok || !it.Hidden {
		t.Fatalf("returning file = %+v (ok=%v), want hidden", it, ok)
	}
	if res := l2.List(Query{ShowHidden: "only"}); res.Total != 1 || res.Items[0].ID != id {
		t.Fatalf("hidden listing = %+v", res.Items)
	}
}
