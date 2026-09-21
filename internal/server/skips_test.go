package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/library"
)

// What the library has found about an episode's credits is answered for it,
// through the face like every by-id route; a film nobody has looked at
// answers nothing rather than an error, since nothing is the ordinary case.
func TestSkipMarksAreServedThroughTheFace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Harbour Lights", "Season 1", "Harbour.Lights.S01E01.mkv")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _, lib := serverUnderTest(t, dir)
	id := library.PathID(p)
	get := func(face string) (SkipMarks, int) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/skip/"+id, nil)
		if face != "" {
			req.Header.Set(ContentHeader, face)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var m SkipMarks
		if res.StatusCode == http.StatusOK {
			_ = json.NewDecoder(res.Body).Decode(&m)
		}
		return m, res.StatusCode
	}
	if m, status := get(""); status != http.StatusOK || m != (SkipMarks{}) {
		t.Fatalf("an episode nobody has looked at answered %d %+v", status, m)
	}
	lib.SetSkip(id, blob.Skip{IntroStart: 20, IntroEnd: 95, Outro: 60})
	if m, status := get(""); status != http.StatusOK || m.IntroStart != 20 || m.IntroEnd != 95 || m.Outro != 60 {
		t.Errorf("the marks did not come back: %d %+v", status, m)
	}
	if _, status := get("music"); status != http.StatusNotFound {
		t.Errorf("a face that cannot see the film was answered %d", status)
	}
	if _, status := get(""); status != http.StatusOK {
		t.Errorf("status %d", status)
	}
}
