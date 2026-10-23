// SPDX-License-Identifier: MIT

package library

import (
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"
)

func folderLibrary(t *testing.T) *Library {
	t.Helper()
	l := New([]string{"/library/first", "/library/second"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, file := range []struct {
		path string
		kind Kind
	}{
		{"/library/first/Shared/lantern.mp4", KindVideo},
		{"/library/second/Shared/sketch.svg", KindImage},
		{"/library/first/Shared/Deep/river.mp4", KindVideo},
		{"/library/second/Other/shared.mp4", KindVideo},
		{"/library/first/Music/Shared/song.mp3", KindAudio},
		{"/library/first/direct.mp4", KindVideo},
	} {
		l.upsert(file.path, file.kind, 100, time.Unix(100, 0), fileKey{}, false)
	}
	return l
}

func folderCounts(folders []*Folder) map[string]int {
	out := map[string]int{}
	for _, f := range folders {
		out[f.Name] = f.Items
	}
	return out
}

func TestFoldersMergeLeafNamesAndDrillDown(t *testing.T) {
	l := folderLibrary(t)
	want := map[string]int{"Shared": 3, "Deep": 1, "Other": 1, "first": 1}
	if got := folderCounts(l.SearchFolders(Query{})); !reflect.DeepEqual(got, want) {
		t.Fatalf("folders = %v, want %v", got, want)
	}
	for name, count := range want {
		q := Query{Folder: name, Sort: "name", Limit: 1}
		seen := map[string]bool{}
		for i := 0; i < count; i++ {
			q.Offset = i
			res := l.List(q)
			if res.Total != count || len(res.Items) != 1 {
				t.Fatalf("folder %q page %d = %+v", name, i, res)
			}
			it := res.Items[0]
			if leafFolder(&it) != name || seen[it.ID] {
				t.Fatalf("folder %q has an unrelated or repeated item", name)
			}
			seen[it.ID] = true
		}
	}
	// Returning to flat must not reuse the last folder's cached selection.
	if got := l.List(Query{}).Total; got != 6 {
		t.Fatalf("flat listing = %d, want 6", got)
	}
}

func TestFoldersFilterBeforeGroupingAndChoosingArtwork(t *testing.T) {
	l := folderLibrary(t)
	for _, tc := range []struct {
		query Query
		want  map[string]int
	}{
		{Query{Kind: KindAudio}, map[string]int{"Shared": 1}},
		{Query{Kind: KindImage}, map[string]int{"Shared": 1}},
		{Query{Search: "lantern"}, map[string]int{"Shared": 1}},
		{Query{Search: "shared"}, map[string]int{"Shared": 3, "Deep": 1, "Other": 1}},
		{Query{Search: "missing"}, map[string]int{}},
		{Query{Paths: ParsePaths("/library/second/Shared")}, map[string]int{"Shared": 1}},
		{Query{Kinds: KindsOf(KindAudio, KindImage)}, map[string]int{"Shared": 2}},
		{Query{Kind: KindVideo, Kinds: KindsOf(KindAudio)}, map[string]int{}},
		{Query{Paths: ParsePaths("relative")}, map[string]int{}},
	} {
		groups := l.SearchFolders(tc.query)
		if groups == nil || !reflect.DeepEqual(folderCounts(groups), tc.want) {
			t.Fatalf("%+v: got %v, want %v", tc.query, folderCounts(groups), tc.want)
		}
		for _, f := range groups {
			q := tc.query
			q.Folder = f.Name
			items := l.List(q).Items
			found := false
			for _, it := range items {
				if it.ID == f.CoverID {
					found = true
					if f.CoverTime != it.ModTime || f.CoverSVG != (it.Kind == KindImage) {
						t.Fatal("artwork must carry its own timestamp and SVG route")
					}
				}
			}
			if len(items) != f.Items || !found || f.Size != int64(f.Items)*100 {
				t.Fatalf("folder disagrees with drill-down: %+v, %d items", f, len(items))
			}
		}
	}
	// A folder named by the query ranks before a member or ancestor match.
	if got := l.SearchFolders(Query{Search: "shared", Sort: "name"})[0].Name; got != "Shared" {
		t.Fatalf("search led with %q instead of the named folder", got)
	}
}

func TestFoldersWatchFlagsAndCacheVersions(t *testing.T) {
	l := folderLibrary(t)
	id := PathID("/library/first/Shared/lantern.mp4")
	q := Query{Kind: KindVideo, Watch: "started"}
	if len(l.SearchFolders(q)) != 0 {
		t.Fatal("nothing has been started")
	}
	l.SetWatch(id, Watch{Pos: 10, Len: 100})
	if got := folderCounts(l.SearchFolders(q)); !reflect.DeepEqual(got, map[string]int{"Shared": 1}) {
		t.Fatalf("watch update did not invalidate folders: %v", got)
	}
	l.SetWatch(id, Watch{Pos: 99, Len: 100})
	if len(l.SearchFolders(q)) != 0 {
		t.Fatal("finished file remains in the started groups")
	}
	l.SetPlays(id, 1)
	q = Query{Played: true}
	if len(l.SearchFolders(q)) != 1 {
		t.Fatal("played filtering lost the file")
	}
	hidden := true
	l.SetFlags([]string{id}, FlagChange{Hidden: &hidden})
	if len(l.SearchFolders(q)) != 0 {
		t.Fatal("hidden file remains in the groups")
	}
	q.ShowHidden = "only"
	if len(l.SearchFolders(q)) != 1 {
		t.Fatal("hidden-only selection lost the file")
	}
	before := l.SearchFolders(q)[0]
	if l.SearchFolders(q)[0] != before {
		t.Fatal("unchanged query rebuilt its groups")
	}
	l.upsert("/library/first/Shared/lantern.mp4", KindVideo, 200, time.Unix(101, 0), fileKey{}, false)
	l.notifyBytes()
	if l.SearchFolders(q)[0] != before {
		t.Fatal("byte growth rebuilt the groups")
	}
	listing := q
	listing.Folder = "Shared"
	if it := l.List(listing).Items[0]; it.Size != 200 {
		t.Fatal("folder listing must still see byte growth")
	}
	l.notify()
	if after := l.SearchFolders(q)[0]; after == before || after.Size != 200 {
		t.Fatal("group change did not refresh folder totals")
	}
}

func TestLeafFolderUsesImmediateDirectory(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/library/Root/file.mp4", "Root"},
		{"/library/Root/Nested/file.mp4", "Nested"},
		{"/library/Root/set.zip\x00/Nested/file.mp4", "Nested"},
		{"/library/Root/set.zip\x00file.mp4", "Root"},
		{"/library/Root/Leaf & + #/file.mp4", "Leaf & + #"},
		{"/library/Root/CD 2/song.mp3", "CD 2"},
	} {
		if got := leafFolder(&Item{Path: tc.path}); got != tc.want {
			t.Errorf("leafFolder(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestFoldersSortAggregatesAndUnknownValues(t *testing.T) {
	l := folderLibrary(t)
	for _, tc := range []struct {
		path     string
		duration int64
		mtime    int64
	}{
		{"/library/first/Shared/lantern.mp4", 1000, 200},
		{"/library/first/Shared/Deep/river.mp4", 2000, 100},
		{"/library/second/Other/shared.mp4", 0, nowMillis() + int64(48*time.Hour/time.Millisecond)},
		{"/library/first/direct.mp4", 3000, 300},
	} {
		it := l.items[PathID(tc.path)]
		it.Duration, it.ModTime = tc.duration, tc.mtime
	}
	for _, tc := range []struct {
		sort string
		desc bool
		want []string
	}{
		{"mtime", false, []string{"Deep", "Shared", "first", "Other"}},
		{"mtime", true, []string{"first", "Shared", "Deep", "Other"}},
		{"duration", false, []string{"Shared", "Deep", "first", "Other"}},
		{"duration", true, []string{"first", "Deep", "Shared", "Other"}},
	} {
		groups := l.SearchFolders(Query{Kind: KindVideo, Sort: tc.sort, Desc: tc.desc})
		names := make([]string, len(groups))
		for i, f := range groups {
			names[i] = f.Name
		}
		if !reflect.DeepEqual(names, tc.want) {
			t.Errorf("%s desc=%v: got %v, want %v", tc.sort, tc.desc, names, tc.want)
		}
	}
	// A mixed group includes unmeasured music/images, so its playing time
	// cannot be the one video's measured time presented as the whole.
	for _, f := range l.SearchFolders(Query{}) {
		if f.Name == "Shared" && f.Duration != 0 {
			t.Fatal("partly measured group claims a complete playing time")
		}
	}
}
