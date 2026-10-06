// SPDX-License-Identifier: MIT

package library

import (
	"math"
	"slices"
	"testing"
	"time"
)

func TestListOffsetBeyondEnd(t *testing.T) {
	l := quietLib("/m")
	l.upsert("/m/one.mp3", KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
	for _, offset := range []int{1, 100, math.MaxInt} {
		got := l.List(Query{Offset: offset, Limit: 200})
		if got.Total != 1 || len(got.Items) != 0 || got.Items == nil {
			t.Fatalf("offset %d: got %+v, want an empty page and total 1", offset, got)
		}
	}
}

func TestListingTiesUseID(t *testing.T) {
	l := quietLib("/one/media", "/two/media")
	paths := []string{"/one/media/track.mp3", "/two/media/track.mp3"}
	var ids []string
	for _, path := range paths {
		l.upsert(path, KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
		ids = append(ids, PathID(path))
	}
	slices.Sort(ids)
	for _, order := range []string{"name", "size", "duration", "episode", "popular"} {
		for _, desc := range []bool{false, true} {
			want := slices.Clone(ids)
			if desc {
				slices.Reverse(want)
			}
			got := l.List(Query{Sort: order, Desc: desc}).Items
			if len(got) != 2 || got[0].ID != want[0] || got[1].ID != want[1] {
				t.Fatalf("unstable tie for %s (descending %v): %v", order, desc, got)
			}
		}
	}
}

func TestClearingTagsUpdatesSearch(t *testing.T) {
	l := quietLib("/m")
	path := "/m/one.mp3"
	l.upsert(path, KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
	l.setMeta(PathID(path), tagMeta{title: "Old Title", artist: "Old Artist"}, 100)
	l.setMeta(PathID(path), tagMeta{}, 100)
	if got := l.List(Query{Search: "old"}); got.Total != 0 {
		t.Fatalf("cleared tags still match search: %+v", got.Items)
	}
	if got := l.List(Query{Search: "one"}); got.Total != 1 {
		t.Fatal("clearing tags also lost the filename from search")
	}
}
