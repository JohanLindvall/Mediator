package server

import (
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/state"
)

// The batch form of the flags route goes through the face like the single
// form does: a caller confined to music may flag only music, and the answer
// must not confirm that anything else exists.
func TestFlagsBatchIsFaced(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"film.mkv", "song.mp3"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts, _ := flagServer(t, dir)
	film := library.PathID(filepath.Join(dir, "film.mkv"))
	song := library.PathID(filepath.Join(dir, "song.mp3"))

	post := func(face string) (map[string]library.Flags, int) {
		t.Helper()
		body := `{"ids":["` + film + `","` + song + `"],"favourite":true}`
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/flags", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if face != "" {
			req.Header.Set(ContentHeader, face)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out FlagsResponse
		if res.StatusCode == http.StatusOK {
			_ = json.NewDecoder(res.Body).Decode(&out)
		}
		return out.Flags, res.StatusCode
	}
	flags, status := post("music")
	if status != http.StatusOK {
		t.Fatalf("music face: status %d", status)
	}
	if _, ok := flags[film]; ok {
		t.Error("a music face was allowed to flag a film, and told that it exists")
	}
	if f, ok := flags[song]; !ok || !f.Favourite {
		t.Errorf("the song this face may see was not flagged: %+v", flags)
	}
	// A face that may see none of them is refused outright rather than
	// answered with an empty map that says nothing.
	if _, status := post("images"); status != http.StatusBadRequest {
		t.Errorf("a face that can see nothing named got %d, want 400", status)
	}
}

// Every request somebody is waiting on marks the library as in use — the
// lowest tier of background work stands down for it — except this
// process's own loopback reads, which are not somebody waiting.
func TestUsedIsMarkedByViewersNotByOurselves(t *testing.T) {
	dir := t.TempDir()
	ts, _, lib := serverUnderTest(t, dir)
	if lib.UsedWithin(time.Minute) {
		t.Fatal("a library nobody has asked anything reports use")
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/info", nil)
	req.Header.Set(library.InternalHeader, library.InternalToken())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if lib.UsedWithin(time.Minute) {
		t.Error("our own internal read counted as somebody using the interface")
	}
	res, err = http.Get(ts.URL + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if !lib.UsedWithin(time.Minute) {
		t.Error("a viewer's request went unnoticed")
	}
}

// The queue endpoint folds copies of one recording out of every answer —
// the fold is the library's, tested there; this pins that the handler
// applies it to a listing, since the fold was added to the handler after
// the library test existed. The tags come in through the mirrored index,
// the one door another package has to a tagged item.
func TestTracksEndpointFoldsRecordings(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{ // path → title; one song in three files
		"Album/01.mp3":  "First Light",
		"Live A/03.mp3": "First Light",
		"Live B/07.mp3": "First Light",
		"Album/02.mp3":  "Second Light",
	}
	var recs []blob.Item
	for rel, title := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		fi, _ := os.Stat(path)
		recs = append(recs, blob.Item{
			ID: library.PathID(path), Path: path, Kind: string(library.KindAudio),
			Size: fi.Size(), MTime: fi.ModTime().UnixMilli(),
			Title: title, Artist: "Gorse Beacon", Album: filepath.Base(filepath.Dir(rel)),
			Enriched: true,
		})
	}
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SaveItems(recs, nil); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := library.New([]string{dir}, log)
	lib.SetMetaDB(db)
	if n := lib.LoadFromDB(db); n != len(files) {
		t.Fatalf("restored %d, want %d", n, len(files))
	}
	lib.Scan(nil) // unchanged on disk, so the restored tags stand
	st := state.Load(nil, log)
	var dist fs.FS = fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	thumbs := NewThumbnailer(nil, nil, log)
	srv := New(lib, st, thumbs, nil, nil, nil, dist, log)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/tracks?of=items&sort=name&order=asc")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out TracksResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	titles := map[string]int{}
	for _, it := range out.Tracks {
		titles[it.Title]++
	}
	if titles["First Light"] != 1 || titles["Second Light"] != 1 || len(out.Tracks) != 2 {
		t.Errorf("the queue holds %v (%d tracks), want each recording once", titles, len(out.Tracks))
	}
}
