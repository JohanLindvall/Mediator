// SPDX-License-Identifier: MIT

package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

func TestFoldersEndpointAndRestrictedDrillDown(t *testing.T) {
	dir := t.TempDir()
	leaf := "Leaf & + #"
	for _, rel := range []string{
		"allowed/" + leaf + "/lantern.mp4",
		"allowed/" + leaf + "/song.mp3",
		"other/" + leaf + "/sketch.svg",
		"other/Elsewhere/other.mp3",
	} {
		file := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("synthetic media"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ts, lib := flagServer(t, dir)
	get := func(endpoint, face, allowed, tag string, out any) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, ts.URL+endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(ContentHeader, face)
		req.Header.Set(PathsHeader, allowed)
		req.Header.Set("If-None-Match", tag)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil {
			if res.StatusCode != http.StatusOK {
				t.Fatalf("%s returned %d", endpoint, res.StatusCode)
			}
			if err := json.NewDecoder(res.Body).Decode(out); err != nil {
				t.Fatal(err)
			}
		}
		return res
	}
	var all FoldersResponse
	first := get("/api/folders", "", "", "", &all)
	if len(all.Folders) != 2 {
		t.Fatalf("folders = %+v, want two merged names", all.Folders)
	}
	tag := first.Header.Get("ETag")
	if tag == "" || first.Header.Get("Cache-Control") != "no-cache" {
		t.Fatal("folders must revalidate like the other collections")
	}
	if res := get("/api/folders", "", "", tag, nil); res.StatusCode != http.StatusNotModified {
		t.Fatal("unchanged folders did not revalidate")
	}
	var restricted FoldersResponse
	allowed := filepath.Join(dir, "allowed")
	get("/api/folders", "music", allowed, tag, &restricted)
	track := library.PathID(filepath.Join(allowed, leaf, "song.mp3"))
	if len(restricted.Folders) != 1 || restricted.Folders[0].Name != leaf ||
		restricted.Folders[0].Items != 1 || restricted.Folders[0].CoverID != track {
		t.Fatalf("restricted group leaks a member or artwork: %+v", restricted.Folders)
	}
	if restricted.Matching == nil || restricted.Matching.Total != 1 || restricted.Matching.Video != 0 {
		t.Fatalf("restricted counts = %+v", restricted.Matching)
	}
	params := url.Values{"folder": {leaf}, "sort": {"name"}}
	var listing library.Result
	get("/api/library?"+params.Encode(), "music", allowed, "", &listing)
	if listing.Total != 1 || listing.Items[0].ID != track {
		t.Fatalf("restricted folder drill-down = %+v", listing)
	}
	var queue TracksResponse
	params.Set("of", "items")
	get("/api/tracks?"+params.Encode(), "music", "", "", &queue)
	if len(queue.Tracks) != 1 || queue.Tracks[0].ID != track {
		t.Fatalf("folder queue escaped into another folder: %+v", queue.Tracks)
	}
	var searched FoldersResponse
	get("/api/folders?q=lantern", "", "", "", &searched)
	if len(searched.Folders) != 1 || searched.Folders[0].Items != 1 || searched.Folders[0].Name != leaf {
		t.Fatalf("search did not narrow folder members: %+v", searched.Folders)
	}
	var empty FoldersResponse
	get("/api/folders?kind=video", "music", "", "", &empty)
	if empty.Folders == nil || len(empty.Folders) != 0 {
		t.Fatalf("excluded kind must answer an empty array: %+v", empty.Folders)
	}
	hidden := true
	lib.SetFlags([]string{track}, library.FlagChange{Hidden: &hidden})
	var hiddenOnly FoldersResponse
	get("/api/folders?hidden=only&q=song", "music", "", "", &hiddenOnly)
	if len(hiddenOnly.Folders) != 1 || hiddenOnly.Matching == nil || hiddenOnly.Matching.Audio != 1 {
		t.Fatalf("owner filters must reach both groups and counts: %+v", hiddenOnly)
	}
}
