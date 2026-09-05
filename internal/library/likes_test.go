package library

import (
	"slices"
	"testing"
	"time"
)

func idsOf(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

// The verdict outranks the count: a liked track stands above one played
// fifty times, and a disliked one sinks below tracks nobody has touched,
// however often it was played. The count decides among equals.
func TestLikesOutrankPlays(t *testing.T) {
	l := quietLib("/m")
	add := func(name string) string {
		path := "/m/" + name
		l.upsert(path, KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
		return PathID(path)
	}
	often, liked, disliked, never := add("often.mp3"), add("liked.mp3"), add("disliked.mp3"), add("never.mp3")
	l.SetPlaysAll(map[string]int{often: 50, liked: 1, disliked: 90})
	l.SetLikesAll(map[string]int{liked: 1, disliked: -1})
	_ = never

	got := idsOf(l.List(Query{Sort: "popular", Desc: true, Limit: 10}).Items)
	want := []string{"liked.mp3", "often.mp3", "never.mp3", "disliked.mp3"}
	if !slices.Equal(got, want) {
		t.Errorf("popular order = %v, want %v", got, want)
	}
	// The key's old name still sorts the same way, for addresses minted
	// before the rename.
	if old := idsOf(l.List(Query{Sort: "plays", Desc: true, Limit: 10}).Items); !slices.Equal(old, want) {
		t.Errorf("sort=plays = %v, want the popular order", old)
	}
	// The popular listing is what has been played or judged: the untouched
	// track is not in it, the disliked one is — at the bottom.
	got = idsOf(l.List(Query{Sort: "popular", Desc: true, Played: true, Limit: 10}).Items)
	want = []string{"liked.mp3", "often.mp3", "disliked.mp3"}
	if !slices.Equal(got, want) {
		t.Errorf("popular listing = %v, want %v", got, want)
	}
	// And the copies a listing hands out carry the verdict.
	for _, it := range l.List(Query{Limit: 10}).Items {
		switch it.Name {
		case "liked.mp3":
			if it.Like != 1 {
				t.Errorf("liked track carries %d", it.Like)
			}
		case "disliked.mp3":
			if it.Like != -1 {
				t.Errorf("disliked track carries %d", it.Like)
			}
		default:
			if it.Like != 0 {
				t.Errorf("%s carries a verdict nobody gave: %d", it.Name, it.Like)
			}
		}
	}
}

// A verdict bumps the library's version like a play does — the collections
// sum it at build time — and a liked track that was never played is still
// what the Popular chip counts.
func TestALikeBumpsTheVersionAndCountsAsPopular(t *testing.T) {
	l := quietLib("/m")
	l.upsert("/m/track.mp3", KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
	id := PathID("/m/track.mp3")
	v0 := l.Version()
	l.SetLike(id, 1)
	if l.Version() == v0 {
		t.Fatal("a verdict must bump the library version")
	}
	v1 := l.Version()
	l.SetLike(id, 1)
	if l.Version() != v1 {
		t.Error("the same verdict again must not")
	}
	if got := l.playedTotal(); got != 1 {
		t.Errorf("popular total = %d, want the liked track counted", got)
	}
	if got := l.CountsFor(CountQuery{Search: "track"}).Played; got != 1 {
		t.Errorf("popular count for a search = %d, want 1", got)
	}
	l.SetLike(id, 0)
	if got := l.playedTotal(); got != 0 {
		t.Errorf("popular total after withdrawing = %d, want 0", got)
	}
}

func TestPopularityArithmetic(t *testing.T) {
	if popularity(1, 0) <= popularity(0, 1_000_000) {
		t.Error("a like must outrank a million plays")
	}
	if popularity(-1, 1_000_000) >= popularity(0, 0) {
		t.Error("a dislike must sink below the untouched")
	}
	if popularity(0, 3) <= popularity(0, 2) {
		t.Error("among equals the count decides")
	}
	if popularity(2, 0) <= popularity(1, 500) {
		t.Error("a collection's net verdict counts")
	}
}

// The collections sum the verdict as they sum the plays, and sort on the
// two the same way: a release with one liked track stands above one whose
// tracks have merely been played, and so do its performer and its genre.
func TestCollectionsSumTheVerdict(t *testing.T) {
	l := libForQueue(t)
	plays := map[string]int{}
	for i := 1; i <= 2; i++ {
		plays[PathID("/library/Tern Signal/Harbour Lights/0"+string(rune('0'+i))+" track.mp3")] = 10
	}
	l.SetPlaysAll(plays)
	l.SetLikesAll(map[string]int{PathID("/library/Gorse Beacon/Demos/01 track.mp3"): 1})

	albums := l.SearchAlbums(AlbumQuery{Sort: "popular", Desc: true})
	if len(albums) < 2 || albums[0].Name != "Demos" || albums[1].Name != "Harbour Lights" {
		t.Errorf("popular releases = %v", albumNames(albums))
	}
	if albums[0].Likes != 1 || albums[1].Plays != 20 {
		t.Errorf("sums: %+v / %+v", albums[0], albums[1])
	}
	artists := l.SearchArtists("", "popular", true, PathFilter{})
	if len(artists) != 2 || artists[0].Name != "Gorse Beacon" || artists[0].Likes != 1 {
		t.Errorf("popular performers = %+v", artists)
	}
	genres := l.SearchGenres("", "popular", true, PathFilter{})
	if len(genres) != 2 || genres[0].Name != "Rock" || genres[0].Likes != 1 {
		t.Errorf("popular genres = %+v", genres)
	}
}
