package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// A vector shaped like music of one timbre, or like a voice reading.
func shaped(timbre float32, spoken bool) []float32 {
	v := make([]float32, 56)
	for i := range 13 {
		v[i] = timbre * float32(i+1)
	}
	v[55] = 60 // a minute of sound: enough to be judged
	if spoken {
		for i := 34; i < 46; i++ {
			v[i] = 1.0 / 12
		}
		v[51], v[52], v[53], v[54] = 0.1, 0.5, 0.6, 0.1
	} else {
		v[34] = 0.5
		for i := 35; i < 46; i++ {
			v[i] = 0.05
		}
		v[51], v[52], v[53], v[54] = 0.6, 0.02, 0.15, 0.01
	}
	return v
}

// The analysis on the wire: the tracks that sound like one, the releases
// that sound like one with chips counting what is listed, and an audiobook
// on a shelf of its own — out of the records, counted apart, listed by the
// flag.
func TestWhatSoundsLikeWhat(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{"Signal Fires", "Harbour Lights", "A Reading"} {
		for i := 1; i <= 2; i++ {
			write(filepath.Join(rel, fmt.Sprintf("%02d - track.mp3", i)))
		}
	}
	ts, lib := flagServer(t, dir)
	byRelease := map[string][]library.Item{}
	for _, it := range lib.List(library.Query{Limit: 20}).Items {
		release := filepath.Base(filepath.Dir(it.Path))
		byRelease[release] = append(byRelease[release], it)
		var v []float32
		switch release {
		case "Signal Fires":
			v = shaped(1, false)
			v[0] += float32(len(it.Name)) * 0.01
		case "Harbour Lights":
			v = shaped(-1, false)
		default:
			v = shaped(1, true)
		}
		lib.SetFeatures(it.ID, it.ModTime, it.Size, v)
	}
	// The collections were built before the vectors landed; the analysis
	// announces them the same way.
	lib.Touch()
	lib.RefreshCounts()

	get := func(path string, out any) int {
		t.Helper()
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusOK {
			if err := json.NewDecoder(res.Body).Decode(out); err != nil {
				t.Fatal(err)
			}
		}
		return res.StatusCode
	}

	// Tracks like one: its own release's other track first, the other
	// release's after, the reading never.
	seed := byRelease["Signal Fires"][0]
	var tr TracksResponse
	if st := get("/api/tracks?of=similar&id="+seed.ID, &tr); st != http.StatusOK || len(tr.Tracks) != 3 {
		t.Fatalf("similar tracks: %d (status %d), want the three other music tracks", len(tr.Tracks), st)
	}
	if filepath.Base(filepath.Dir(tr.Tracks[0].Rel)) != "Signal Fires" {
		t.Errorf("first similar track is %s, want the same release's", tr.Tracks[0].Rel)
	}
	for _, it := range tr.Tracks {
		if it.Spoken {
			t.Errorf("%s, a reading, was offered after a song", it.Rel)
		}
	}
	if st := get("/api/tracks?of=similar&id=feedfacedeadbeef", &tr); st != http.StatusOK || len(tr.Tracks) != 0 {
		t.Errorf("similar to a ghost answered %d with %d tracks", st, len(tr.Tracks))
	}
	// A seed nothing has read yet answers an empty list, never null: the
	// radio reads the list, and a null would break it on every track the
	// analysis has not reached.
	unread := byRelease["Harbour Lights"][1]
	lib.SetFeatures(unread.ID, unread.ModTime, unread.Size, nil)
	unreadRes, unreadErr := http.Get(ts.URL + "/api/tracks?of=similar&id=" + unread.ID)
	if unreadErr != nil {
		t.Fatal(unreadErr)
	}
	var raw struct {
		Tracks json.RawMessage `json:"tracks"`
	}
	_ = json.NewDecoder(unreadRes.Body).Decode(&raw)
	unreadRes.Body.Close()
	if string(raw.Tracks) != "[]" {
		t.Errorf("similar to an unread track answered tracks=%s, want []", raw.Tracks)
	}

	// The shelves. The records leave the reading out and the chip counts it
	// apart; the audiobook shelf holds it alone.
	var al AlbumsResponse
	get("/api/albums?sort=name&order=asc", &al)
	var listing library.Result
	get("/api/library?limit=1", &listing)
	if len(al.Albums) != 2 || listing.Counts.Audiobooks != 1 || listing.Counts.Albums != 2 {
		t.Fatalf("records = %d, chips %+v; want two records and one audiobook counted apart", len(al.Albums), listing.Counts)
	}
	var books AlbumsResponse
	get("/api/albums?audiobooks=1", &books)
	if len(books.Albums) != 1 || !books.Albums[0].Spoken || books.Albums[0].Name != "A Reading" {
		t.Fatalf("audiobook shelf = %+v", books.Albums)
	}

	// Releases like one: the other record, with how alike, under chips that
	// count what is listed.
	var near AlbumsResponse
	get("/api/albums?near="+al.Albums[1].ID, &near) // Signal Fires sorts after Harbour Lights
	if len(near.Albums) != 1 || near.Albums[0].Name != "Harbour Lights" || near.Albums[0].Similarity == 0 {
		t.Fatalf("releases like one = %+v, want the other record with its similarity", near.Albums)
	}
	if near.Matching == nil || near.Matching.Albums != 1 || near.Matching.Audio != 2 {
		t.Errorf("chips over the similar releases = %+v, want the one release and its two tracks", near.Matching)
	}
	// The direction is honoured: with a third release seeded near the seed,
	// descending puts it first and ascending puts it last.
	for _, it := range byRelease["A Reading"] {
		v := shaped(1, false)
		v[0] += 0.001
		lib.SetFeatures(it.ID, it.ModTime, it.Size, v)
	}
	lib.Touch()
	var down, up AlbumsResponse
	get("/api/albums?near="+al.Albums[1].ID+"&order=desc", &down)
	get("/api/albums?near="+al.Albums[1].ID+"&order=asc", &up)
	if len(down.Albums) != 2 || len(up.Albums) != 2 || down.Albums[0].Name != up.Albums[1].Name {
		t.Errorf("descending %v, ascending %v: the direction was ignored", albumsNamed(down.Albums), albumsNamed(up.Albums))
	}
	// And a face without music sees no shelf of either kind.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/albums?audiobooks=1", nil)
	req.Header.Set(ContentHeader, "videos")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var none AlbumsResponse
	_ = json.NewDecoder(res.Body).Decode(&none)
	res.Body.Close()
	if len(none.Albums) != 0 {
		t.Errorf("a videos face was handed %d audiobooks", len(none.Albums))
	}
}

func albumsNamed(albums []*library.Album) []string {
	out := make([]string, 0, len(albums))
	for _, a := range albums {
		out = append(out, a.Name)
	}
	return out
}
