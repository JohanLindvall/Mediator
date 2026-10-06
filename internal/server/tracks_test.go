// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// The tracks behind a view, as the endpoint answers them: every release
// listed in the listing's order, the music alone out of a listing, and —
// the two guards every collection has — nothing for a face without music
// and only what a confined caller may see.
func TestTracksBehindAView(t *testing.T) {
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
	// Two releases of three tracks, untagged — a directory of tracks is a
	// release — and a film beside one of them.
	for _, rel := range []string{"Gorse Beacon/Signal Fires", "Tern Signal/Harbour Lights"} {
		for i := 1; i <= 3; i++ {
			write(filepath.Join(rel, fmt.Sprintf("%02d - track.mp3", i)))
		}
	}
	write("Gorse Beacon/Signal Fires/making of.mkv")
	ts, _ := flagServer(t, dir)

	fetch := func(path, face, allowed string) (TracksResponse, int) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if face != "" {
			req.Header.Set(ContentHeader, face)
		}
		if allowed != "" {
			req.Header.Set(PathsHeader, allowed)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out TracksResponse
		if res.StatusCode == http.StatusOK {
			if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
		}
		return out, res.StatusCode
	}

	got, status := fetch("/api/tracks?of=albums&sort=name&order=asc", "", "")
	if status != http.StatusOK || len(got.Tracks) != 6 {
		t.Fatalf("every release: %d tracks (status %d), want 6", len(got.Tracks), status)
	}
	// The listing's own order, and each release's running order inside it.
	if got.Tracks[0].Name != "01 - track.mp3" || got.Tracks[3].Name != "01 - track.mp3" ||
		filepath.Base(filepath.Dir(got.Tracks[0].Rel)) != "Harbour Lights" ||
		filepath.Base(filepath.Dir(got.Tracks[3].Rel)) != "Signal Fires" {
		t.Errorf("tracks out of order: %s … %s", got.Tracks[0].Rel, got.Tracks[3].Rel)
	}
	if got.Truncated {
		t.Error("six tracks were reported cut")
	}

	// A listing gives up its films: the music alone, however the query read.
	got, _ = fetch("/api/tracks?of=items&sort=name&order=asc", "", "")
	if len(got.Tracks) != 6 {
		t.Errorf("the listing's tracks: %d, want 6 (the film left out)", len(got.Tracks))
	}
	for _, it := range got.Tracks {
		if filepath.Ext(it.Name) != ".mp3" {
			t.Errorf("%s was queued", it.Name)
		}
	}

	// A face without music has nothing to queue.
	got, status = fetch("/api/tracks?of=albums", "videos", "")
	if status != http.StatusOK || len(got.Tracks) != 0 {
		t.Errorf("a videos face was handed %d tracks (status %d)", len(got.Tracks), status)
	}

	// A confined caller gets its own tracks and nobody else's.
	got, _ = fetch("/api/tracks?of=albums", "", filepath.Join(dir, "Tern Signal"))
	if len(got.Tracks) != 3 {
		t.Errorf("a caller confined to one performer was handed %d tracks, want 3", len(got.Tracks))
	}
	for _, it := range got.Tracks {
		if filepath.Base(filepath.Dir(it.Rel)) != "Harbour Lights" {
			t.Errorf("%s reached a caller confined elsewhere", it.Rel)
		}
	}

	// And a view nothing knows is refused rather than answered with nothing.
	if _, status := fetch("/api/tracks?of=everything", "", ""); status != http.StatusBadRequest {
		t.Errorf("of=everything answered %d, want 400", status)
	}
}

// taggedTrack writes a file the tag reader takes for an MP3 with an ID3v2.3
// tag naming its performer, release and title: what a station is made of.
func taggedTrack(t *testing.T, path, artist, album, title string) {
	t.Helper()
	var frames []byte
	for _, f := range [][2]string{{"TPE1", artist}, {"TALB", album}, {"TIT2", title}} {
		body := append([]byte{0}, f[1]...) // ISO-8859-1, then the text
		n := len(body)
		frames = append(frames, f[0]...)
		frames = append(frames, byte(n>>24), byte(n>>16), byte(n>>8), byte(n), 0, 0)
		frames = append(frames, body...)
	}
	n := len(frames)
	data := append([]byte("ID3\x03\x00\x00"), byte(n>>21&0x7f), byte(n>>14&0x7f), byte(n>>7&0x7f), byte(n&0x7f))
	data = append(data, frames...)
	data = append(data, 0xFF, 0xFB, 0x90, 0x40)
	data = append(data, make([]byte, 4000)...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A station on the wire: one performer's tracks for a seed, named after
// them; the same for a performer asked for by name; and the guards every
// queue has — nothing for a face without music, a seed and its station kept
// to what a confined caller may see, and a list where there is nothing.
func TestAStationOnTheWire(t *testing.T) {
	dir := t.TempDir()
	for i, title := range []string{"First Breath", "Low Tide", "Harbour Wall"} {
		taggedTrack(t, filepath.Join(dir, "Gorse Beacon", "Signal Fires", fmt.Sprintf("%02d.mp3", i+1)), "Gorse Beacon", "Signal Fires", title)
	}
	taggedTrack(t, filepath.Join(dir, "comp", "Winter Sampler", "01.mp3"), "Gorse Beacon", "Winter Sampler", "Night Ferry")
	taggedTrack(t, filepath.Join(dir, "comp", "Winter Sampler", "02.mp3"), "Tern Signal", "Winter Sampler", "Cold Light")
	for i, title := range []string{"Low Water", "High Water"} {
		taggedTrack(t, filepath.Join(dir, "Tern Signal", "Harbour Lights", fmt.Sprintf("%02d.mp3", i+1)), "Tern Signal", "Harbour Lights", title)
	}
	ts, lib := flagServer(t, dir)
	var ids []string
	for _, it := range lib.List(library.Query{Limit: 50}).Items {
		ids = append(ids, it.ID)
	}
	lib.EnrichNow(context.Background(), ids)

	fetch := func(path, face, allowed string) TracksResponse {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if face != "" {
			req.Header.Set(ContentHeader, face)
		}
		if allowed != "" {
			req.Header.Set(PathsHeader, allowed)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s answered %d", path, res.StatusCode)
		}
		var out TracksResponse
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.Tracks == nil {
			t.Fatalf("%s answered tracks=null, want a list", path)
		}
		return out
	}
	seed := library.PathID(filepath.Join(dir, "Gorse Beacon", "Signal Fires", "01.mp3"))

	got := fetch("/api/tracks?of=station&id="+seed, "", "")
	if got.Artist != "Gorse Beacon" || len(got.Tracks) != 3 {
		t.Fatalf("a seed's station: %q with %d tracks, want Gorse Beacon's three others", got.Artist, len(got.Tracks))
	}
	for _, it := range got.Tracks {
		if it.ID == seed || it.Artist != "Gorse Beacon" {
			t.Errorf("%s (%s) is in Gorse Beacon's station", it.Rel, it.Artist)
		}
	}
	if got := fetch("/api/tracks?of=station&artist=gorse+beacon", "", ""); got.Artist != "Gorse Beacon" || len(got.Tracks) != 4 {
		t.Errorf("a station asked for by name: %q with %d tracks, want all four of theirs", got.Artist, len(got.Tracks))
	}
	if got := fetch("/api/tracks?of=station&id="+seed, "videos", ""); got.Artist != "" || len(got.Tracks) != 0 {
		t.Errorf("a videos face was handed a station of %q with %d tracks", got.Artist, len(got.Tracks))
	}
	// Confined to the compilation: the seed is out of sight, and asked for by
	// name the station is the one track of theirs the caller may see.
	comp := filepath.Join(dir, "comp")
	if got := fetch("/api/tracks?of=station&id="+seed, "", comp); len(got.Tracks) != 0 {
		t.Errorf("a seed out of sight answered %d tracks", len(got.Tracks))
	}
	if got := fetch("/api/tracks?of=station&artist=Gorse+Beacon", "", comp); len(got.Tracks) != 1 || got.Tracks[0].Title != "Night Ferry" {
		t.Errorf("a confined station: %d tracks, want the one on the compilation", len(got.Tracks))
	}
	if got := fetch("/api/tracks?of=station&artist=Nobody+Here", "", ""); len(got.Tracks) != 0 {
		t.Errorf("a station of nobody answered %d tracks", len(got.Tracks))
	}
}
