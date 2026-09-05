package library

import (
	"slices"
	"testing"
	"time"
)

// order lists the item names a query returns, in order.
func order(l *Library, q Query) []string {
	var out []string
	for _, it := range l.List(q).Items {
		out = append(out, it.Name)
	}
	return out
}

func TestShuffleIsStablePerSeed(t *testing.T) {
	l := libWithFiles(300)

	first := order(l, Query{Sort: "random", Seed: 42})
	if len(first) != 300 {
		t.Fatalf("listed %d items, want 300", len(first))
	}
	if slices.Equal(first, order(l, Query{Sort: "name"})) {
		t.Fatal("the shuffle reproduced the name order")
	}
	// Same seed, same deal — including after the cached query is thrown
	// away, since the order comes from a hash and not from chance.
	if got := order(l, Query{Sort: "random", Seed: 42}); !slices.Equal(got, first) {
		t.Fatal("the same seed produced a different order")
	}
	l.notify()
	if got := order(l, Query{Sort: "random", Seed: 42}); !slices.Equal(got, first) {
		t.Fatal("rebuilding the query reshuffled a fixed seed")
	}
	if got := order(l, Query{Sort: "random", Seed: 43}); slices.Equal(got, first) {
		t.Fatal("a different seed produced the same order")
	}
	// Descending is the same shuffle read backwards, not a third order.
	desc := order(l, Query{Sort: "random", Seed: 42, Desc: true})
	slices.Reverse(desc)
	if !slices.Equal(desc, first) {
		t.Fatal("descending changed the shuffle instead of reversing it")
	}
}

func TestShufflePagesTileTheResult(t *testing.T) {
	// Paging a shuffle must page one shuffle: every item exactly once.
	l := libWithFiles(517)
	seen := map[string]int{}
	for off := 0; ; off += 100 {
		res := l.List(Query{Sort: "random", Seed: 7, Offset: off, Limit: 100})
		for _, it := range res.Items {
			seen[it.ID]++
		}
		if off+100 >= res.Total {
			break
		}
	}
	if len(seen) != 517 {
		t.Fatalf("pages covered %d unique items, want 517", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("item %s appeared %d times across the pages", id, n)
		}
	}
}

func TestSortByDuration(t *testing.T) {
	l := quietLib("/media")
	durations := map[string]int64{
		"long.mp4":    600_000,
		"short.mp4":   30_000,
		"middle.mp4":  120_000,
		"unknown.mp4": 0, // never measured; sorts ahead of the shortest
	}
	for name, d := range durations {
		l.upsert("/media/"+name, KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
		l.setMeta(PathID("/media/"+name), tagMeta{}, d)
	}

	got := order(l, Query{Sort: "duration"})
	want := []string{"unknown.mp4", "short.mp4", "middle.mp4", "long.mp4"}
	if !slices.Equal(got, want) {
		t.Fatalf("duration order %v, want %v", got, want)
	}
	got = order(l, Query{Sort: "duration", Desc: true})
	slices.Reverse(want)
	if !slices.Equal(got, want) {
		t.Fatalf("descending duration order %v, want %v", got, want)
	}
}
