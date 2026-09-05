package library

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// libWithFiles indexes n synthetic items directly (no filesystem).
func libWithFiles(n int) *Library {
	l := quietLib("/media")
	for i := range n {
		kind := KindAudio
		switch i % 10 {
		case 8:
			kind = KindImage
		case 9:
			kind = KindVideo
		}
		path := fmt.Sprintf("/media/dir%03d/%02d - track %c.mp3", i/30, i%30, 'a'+rune(i%26))
		l.upsert(path, kind, int64(1000+i%977), time.Unix(int64(i), 0), fileKey{}, false)
	}
	return l
}

func TestCountsAreIncremental(t *testing.T) {
	l := libWithFiles(500)

	// The incremental totals must equal a brute-force recount.
	brute := func() Counts {
		var c Counts
		l.mu.RLock()
		for _, it := range l.items {
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
		l.mu.RUnlock()
		return c
	}
	check := func(stage string) {
		t.Helper()
		got, want := l.Counts(), brute()
		got.Albums, got.Artists = 0, 0 // maintained by the album/artist builds
		if got != want {
			t.Fatalf("%s: counts %+v, want %+v", stage, got, want)
		}
	}
	check("after inserts")

	l.removePath("/media/dir000") // whole directory
	check("after directory removal")

	// Re-upserting an existing path must not double count.
	l.upsert("/media/dir005/03 - track d.mp3", KindAudio, 555, time.Unix(9, 0), fileKey{}, false)
	check("after re-upsert")
}

func TestListUsesQueryCacheAcrossPages(t *testing.T) {
	l := libWithFiles(2000)

	first := l.List(Query{Sort: "name", Limit: 100})
	if first.Total != 2000 {
		t.Fatalf("total = %d", first.Total)
	}
	cached := l.lastQuery
	if cached == nil || len(cached.items) != 2000 {
		t.Fatal("query result was not cached")
	}
	// Paging the same query reuses the cached order.
	l.List(Query{Sort: "name", Limit: 100, Offset: 1900})
	if l.lastQuery != cached {
		t.Fatal("paging rebuilt the query")
	}
	// A different query rebuilds…
	l.List(Query{Sort: "size", Limit: 100})
	if l.lastQuery == cached {
		t.Fatal("changing the sort did not rebuild")
	}
	// …the seed pages a shuffle like any other query…
	l.List(Query{Sort: "random", Seed: 5, Limit: 100})
	cached = l.lastQuery
	l.List(Query{Sort: "random", Seed: 5, Limit: 100, Offset: 1900})
	if l.lastQuery != cached {
		t.Fatal("paging a shuffle rebuilt the query, which would reshuffle it")
	}
	// …but a new seed is a new shuffle.
	l.List(Query{Sort: "random", Seed: 6, Limit: 100})
	if l.lastQuery == cached {
		t.Fatal("changing the seed did not rebuild")
	}
	// A seed no sort uses must not cost a rebuild.
	l.List(Query{Sort: "name", Seed: 6, Limit: 100})
	cached = l.lastQuery
	l.List(Query{Sort: "name", Seed: 7, Limit: 100})
	if l.lastQuery != cached {
		t.Fatal("a seed rebuilt a query that does not shuffle")
	}
	// …and so does a library change.
	cached = l.lastQuery
	l.upsert("/media/new/track.mp3", KindAudio, 1, time.Unix(1, 0), fileKey{}, false)
	l.notify()
	l.List(Query{Sort: "size", Limit: 100})
	if l.lastQuery == cached {
		t.Fatal("a version bump did not invalidate the cache")
	}
}

func TestListPagesAreConsistent(t *testing.T) {
	// Pages assembled from the cache must tile the full result exactly.
	l := libWithFiles(1037)
	seen := map[string]int{}
	for _, sort := range []string{"name", "mtime", "size"} {
		clear(seen)
		var prev Result
		for off := 0; ; off += 100 {
			res := l.List(Query{Sort: sort, Desc: true, Offset: off, Limit: 100})
			for _, it := range res.Items {
				seen[it.ID]++
			}
			if off > 0 && len(res.Items) > 0 && prev.Items[len(prev.Items)-1].ID == res.Items[0].ID {
				t.Fatalf("sort %s: page boundary repeated an item", sort)
			}
			prev = res
			if off+100 >= res.Total {
				break
			}
		}
		if len(seen) != 1037 {
			t.Fatalf("sort %s: pages covered %d unique items, want 1037", sort, len(seen))
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("sort %s: item %s appeared %d times", sort, id, n)
			}
		}
	}
}

func TestEnrichmentWritesAreBatched(t *testing.T) {
	dir := t.TempDir()
	taggedMP3(t, filepath.Join(dir, "one.mp3"))
	taggedMP3(t, filepath.Join(dir, "two.mp3"))
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)

	ids := []string{PathID(filepath.Join(dir, "one.mp3")), PathID(filepath.Join(dir, "two.mp3"))}
	l.EnrichNow(context.Background(), ids)

	// Nothing hits the database until the persist loop flushes.
	it, _ := l.Get(ids[0])
	if _, ok := db.GetMeta(ids[0], it.ModTime, it.Size); ok {
		t.Fatal("metadata written synchronously; should be buffered")
	}
	l.flush(db)
	for _, id := range ids {
		it, _ := l.Get(id)
		m, ok := db.GetMeta(id, it.ModTime, it.Size)
		if !ok || m.Duration == 0 {
			t.Fatalf("metadata for %s not flushed (ok=%v m=%+v)", id, ok, m)
		}
	}
}

func TestVideoCodecsProbedLazily(t *testing.T) {
	// An mp4 whose duration the native parser reads gets no eager ffprobe;
	// EnsureCodecs fills the codecs in when the file is opened.
	dir := t.TempDir()
	mp4 := filepath.Join(dir, "clip.mp4")
	writeMinimalMP4(t, mp4, 90)

	l := quietLib(dir)
	l.Scan(nil)
	id := PathID(mp4)
	l.EnrichNow(context.Background(), []string{id})
	it, _ := l.Get(id)
	if it.Duration != 90_000 {
		t.Fatalf("duration = %d, want 90000 from the native parser", it.Duration)
	}
	if it.VCodec != "" {
		t.Fatalf("vcodec = %q; eager enrichment should not have probed", it.VCodec)
	}

	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed; cannot exercise the lazy probe")
	}
	l.EnsureCodecs(context.Background(), id)
	// The synthetic file has no real streams, so ffprobe may legitimately
	// find nothing — the contract under test is that EnsureCodecs runs
	// without disturbing the rest of the item.
	if it, _ = l.Get(id); it.Duration != 90_000 {
		t.Fatalf("EnsureCodecs damaged the item: %+v", it)
	}
}

func writeMinimalMP4(t *testing.T, path string, durSec int) {
	t.Helper()
	be := func(v uint32) []byte {
		return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	}
	var b []byte
	b = append(b, be(16)...)
	b = append(b, "ftypisom"...)
	b = append(b, be(0)...)
	mvhd := make([]byte, 32)
	copy(mvhd[12:16], be(1000))
	copy(mvhd[16:20], be(uint32(durSec*1000)))
	b = append(b, be(48)...)
	b = append(b, "moov"...)
	b = append(b, be(40)...)
	b = append(b, "mvhd"...)
	b = append(b, mvhd...)
	write(t, path, string(b))
}
