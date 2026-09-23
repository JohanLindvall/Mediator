package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// The shows endpoint and the chip it carries answer a search the same way: a
// search naming an episode's title and not the show found the show in the
// count and nothing in the grid.
func TestTheShowsAndTheirChipAgreeOnASearch(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"Harbour.Lights.S01E01.The.Signal.Station.1080p-GRP.mkv",
		"Harbour.Lights.S01E02.Low.Tide.1080p-GRP.mkv",
		"Harbour.Lights.S02E01.Customs.Examinations.1080p-GRP.mkv",
		"Harbour.Lights.S02E02.Harbour.Master.1080p-GRP.mkv",
	} {
		path := filepath.Join(dir, "TV", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts, lib := flagServer(t, dir)
	lib.RefreshCounts()

	for _, c := range []struct {
		search  string
		shows   int
		matched []int
	}{
		{"harbour", 1, nil},
		{"examin", 1, []int{2}},
		{"nothing like it", 0, nil},
	} {
		res, err := http.Get(ts.URL + "/api/series?q=" + url.QueryEscape(c.search))
		if err != nil {
			t.Fatal(err)
		}
		var out SeriesResponse
		err = json.NewDecoder(res.Body).Decode(&out)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Series) != c.shows {
			t.Errorf("%q: %d shows, want %d", c.search, len(out.Series), c.shows)
			continue
		}
		if out.Matching == nil || out.Matching.Series != len(out.Series) {
			t.Errorf("%q: the chip says %+v over %d shows", c.search, out.Matching, len(out.Series))
		}
		if c.shows == 1 {
			if got := out.Series[0].Matched; len(got) != len(c.matched) || (len(got) > 0 && got[0] != c.matched[0]) {
				t.Errorf("%q: seasons on offer %v, want %v", c.search, got, c.matched)
			}
		}
	}
}
