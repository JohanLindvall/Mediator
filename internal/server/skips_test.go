package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/library"
)

// A show with two seasons of two episodes, named the way a release is, so
// the paths say which show and which season each file is.
func showUnderTest(t *testing.T, dir string) map[string]library.Item {
	t.Helper()
	for _, name := range []string{
		"Harbour Lights/Season 1/Harbour.Lights.S01E01.mkv",
		"Harbour Lights/Season 1/Harbour.Lights.S01E02.mkv",
		"Harbour Lights/Season 2/Harbour.Lights.S02E01.mkv",
		"a film that is nobody's episode.mkv",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return nil
}

func skipCall(t *testing.T, method, url, body string, face string) (SkipResponse, int) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if face != "" {
		req.Header.Set(ContentHeader, face)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out SkipResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	} else {
		io.Copy(io.Discard, res.Body)
	}
	return out, res.StatusCode
}

// Marks are kept by what they apply to — a season, the whole show, one
// episode — and every episode answers with all the scopes it belongs to.
func TestSkipMarksAreKeptByScope(t *testing.T) {
	dir := t.TempDir()
	showUnderTest(t, dir)
	ts, _, lib := serverUnderTest(t, dir)
	byName := map[string]library.Item{}
	for _, it := range lib.List(library.Query{Limit: 20}).Items {
		byName[it.Name] = it
	}
	s1e1, s1e2, s2e1, film := byName["Harbour.Lights.S01E01.mkv"], byName["Harbour.Lights.S01E02.mkv"], byName["Harbour.Lights.S02E01.mkv"], byName["a film that is nobody's episode.mkv"]
	if s1e1.Series == "" || s1e1.Season != 1 || s2e1.Season != 2 || film.Series != "" {
		t.Fatalf("the fixture was not read as a show: %+v %+v %+v", s1e1, s2e1, film)
	}
	url := func(it library.Item) string { return ts.URL + "/api/skip/" + it.ID }

	// Nothing marked yet.
	if got, status := skipCall(t, http.MethodGet, url(s1e1), "", ""); status != 200 || got.Episode != nil || got.Season != nil || got.Series != nil {
		t.Fatalf("an unmarked episode answered %d %+v", status, got)
	}
	// The season, marked from one episode, reaches its sibling and not the
	// next season.
	got, status := skipCall(t, http.MethodPut, url(s1e1), `{"scope":"season","introStart":0,"introEnd":90,"outro":60}`, "")
	if status != 200 || got.Season == nil || got.Season.IntroEnd != 90 || got.Season.Outro != 60 {
		t.Fatalf("marking the season answered %d %+v", status, got)
	}
	if got, _ := skipCall(t, http.MethodGet, url(s1e2), "", ""); got.Season == nil || got.Season.IntroEnd != 90 {
		t.Errorf("the sibling episode does not see its season's marks: %+v", got)
	}
	if got, _ := skipCall(t, http.MethodGet, url(s2e1), "", ""); got.Season != nil {
		t.Errorf("the next season inherited the first's marks: %+v", got)
	}
	// The show, marked from the second season, reaches every episode beside
	// whatever their own season says.
	skipCall(t, http.MethodPut, url(s2e1), `{"scope":"series","introEnd":80}`, "")
	if got, _ := skipCall(t, http.MethodGet, url(s1e1), "", ""); got.Series == nil || got.Series.IntroEnd != 80 || got.Season == nil {
		t.Errorf("an episode does not see both its season's and the show's marks: %+v", got)
	}
	// An episode of its own.
	skipCall(t, http.MethodPut, url(s1e2), `{"scope":"episode","introStart":30,"introEnd":120}`, "")
	if got, _ := skipCall(t, http.MethodGet, url(s1e2), "", ""); got.Episode == nil || got.Episode.IntroStart != 30 {
		t.Errorf("the episode's own marks did not come back: %+v", got)
	}
	if got, _ := skipCall(t, http.MethodGet, url(s1e1), "", ""); got.Episode != nil {
		t.Errorf("one episode's own marks leaked to another: %+v", got)
	}
	// Clearing is all three at nought.
	skipCall(t, http.MethodPut, url(s1e1), `{"scope":"season"}`, "")
	if got, _ := skipCall(t, http.MethodGet, url(s1e2), "", ""); got.Season != nil {
		t.Errorf("a cleared season still answers: %+v", got)
	}

	// A film that is nobody's episode has only its own scope.
	if _, status := skipCall(t, http.MethodPut, url(film), `{"scope":"season","introEnd":10}`, ""); status != http.StatusBadRequest {
		t.Errorf("a film accepted a season's marks: %d", status)
	}
	if _, status := skipCall(t, http.MethodPut, url(film), `{"scope":"episode","introEnd":10}`, ""); status != 200 {
		t.Errorf("a film refused marks of its own: %d", status)
	}
	// Rubbish is refused: a negative, a day and more, an intro that ends
	// before it starts is no intro.
	if _, status := skipCall(t, http.MethodPut, url(s1e1), `{"scope":"season","outro":-5}`, ""); status != http.StatusBadRequest {
		t.Errorf("a negative mark was accepted: %d", status)
	}
	if _, status := skipCall(t, http.MethodPut, url(s1e1), `{"scope":"season","introEnd":1e9}`, ""); status != http.StatusBadRequest {
		t.Errorf("a mark past a day was accepted: %d", status)
	}
	if got, _ := skipCall(t, http.MethodPut, url(s1e1), `{"scope":"season","introStart":50,"introEnd":40,"outro":30}`, ""); got.Season == nil || got.Season.IntroEnd != 0 || got.Season.IntroStart != 0 || got.Season.Outro != 30 {
		t.Errorf("an intro ending before it starts was kept as one: %+v", got)
	}
	// And through the face: a music face cannot see a film, let alone mark it.
	if _, status := skipCall(t, http.MethodGet, url(s1e1), "", "music"); status != http.StatusNotFound {
		t.Errorf("a face that cannot see the film was answered %d", status)
	}
	if _, status := skipCall(t, http.MethodPut, url(s1e1), `{"scope":"season","introEnd":5}`, "music"); status != http.StatusNotFound {
		t.Errorf("a face that cannot see the film could mark it: %d", status)
	}
}

// The marks are the owner's data, kept in the blob database like the flags:
// a new process finds them where the last one left them.
func TestSkipMarksSurviveARestart(t *testing.T) {
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first := newSkipStore(db, testLog())
	if err := first.put("s|harbour lights|1", blob.Skip{IntroEnd: 90, Outro: 60}); err != nil {
		t.Fatal(err)
	}
	if err := first.put("t|harbour lights", blob.Skip{}); err != nil { // nothing, and nothing kept
		t.Fatal(err)
	}
	second := newSkipStore(db, testLog())
	if m, ok := second.get("s|harbour lights|1"); !ok || m.IntroEnd != 90 || m.Outro != 60 {
		t.Errorf("a restart lost the season's marks: %+v %v", m, ok)
	}
	if _, ok := second.get("t|harbour lights"); ok {
		t.Error("an empty record was kept")
	}
	// And with no database at all, the run's own memory.
	none := newSkipStore(nil, testLog())
	if err := none.put("e|abc", blob.Skip{Outro: 5}); err != nil {
		t.Fatal(err)
	}
	if m, ok := none.get("e|abc"); !ok || m.Outro != 5 {
		t.Error("without a database the marks were not even kept for the run")
	}
}
