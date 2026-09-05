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

	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/state"
)

func TestValidateRoots(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "inside")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("empty is refused", func(t *testing.T) {
		if _, err := validateRoots(nil); err == nil {
			t.Fatal("an empty list should be refused")
		}
	})
	t.Run("a file is not a directory", func(t *testing.T) {
		if _, err := validateRoots([]string{file}); err == nil {
			t.Fatal("a file should be refused")
		}
	})
	t.Run("what is not there is refused", func(t *testing.T) {
		if _, err := validateRoots([]string{filepath.Join(dir, "nope")}); err == nil {
			t.Fatal("a missing directory should be refused")
		}
	})
	t.Run("duplicates collapse", func(t *testing.T) {
		got, err := validateRoots([]string{dir, dir})
		if err != nil || len(got) != 1 {
			t.Fatalf("got %v, %v; want one entry", got, err)
		}
	})
	// A directory inside another on the list is walked twice and watched
	// twice, and every file under it becomes a duplicate of itself.
	t.Run("nested entries drop out", func(t *testing.T) {
		got, err := validateRoots([]string{dir, sub})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != dir {
			t.Fatalf("got %v, want just %s", got, dir)
		}
	})
}

// prefsServer wires the endpoint the way main does, including the part that
// makes a change take effect.
func prefsServer(t *testing.T, roots []string) (*httptest.Server, *library.Library) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := library.New(roots, log)
	lib.Scan(nil)
	var dist fs.FS = fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	srv := New(lib, state.Load(nil, log), NewThumbnailer(nil, nil, log), NewRemuxer("", NewScratch("", 0), log), NewHLS("", lib, NewScratch("", 0), testLog()), nil, dist, log)
	srv.AllowRootChanges(func(rs []string) ([]string, error) {
		lib.SetRoots(rs)
		lib.Scan(nil) // synchronous here, so the test can assert on the result
		return lib.Roots(), nil
	}, true)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, lib
}

func putPrefs(t *testing.T, url string, roots []string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(PrefsUpdate{Roots: roots})
	req, err := http.NewRequest(http.MethodPut, url+"/api/prefs", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The point of the whole feature: a directory that is added gets indexed, and
// a directory that is removed stops being indexed. The second half is the one
// that does not come for free — the files are still on the disk, so "it still
// exists" cannot be what decides whether they stay.
func TestPrefsAddAndRemoveDirectories(t *testing.T) {
	base := t.TempDir()
	one := filepath.Join(base, "one")
	two := filepath.Join(base, "two")
	for _, d := range []string{one, two} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "clip.mp4"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ts, lib := prefsServer(t, []string{one})
	if got := lib.List(library.Query{}).Total; got != 1 {
		t.Fatalf("started with %d items, want 1", got)
	}

	// Add.
	res := putPrefs(t, ts.URL, []string{one, two})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d: %s", res.StatusCode, msg)
	}
	var p PrefsResponse
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if len(p.Roots) != 2 {
		t.Fatalf("server reports %v, want both directories", p.Roots)
	}
	if got := lib.List(library.Query{}).Total; got != 2 {
		t.Fatalf("indexed %d items after adding, want 2", got)
	}

	// Remove. The file under `one` is untouched on disk, and must still leave
	// the index.
	res2 := putPrefs(t, ts.URL, []string{two})
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res2.StatusCode)
	}
	if got := lib.List(library.Query{}).Total; got != 1 {
		t.Fatalf("indexed %d items after removing, want 1", got)
	}
	items := lib.List(library.Query{}).Items
	if len(items) != 1 || !strings.HasPrefix(items[0].Path, two) {
		t.Fatalf("the surviving item is %v, want one under %s", items, two)
	}
	if _, err := os.Stat(filepath.Join(one, "clip.mp4")); err != nil {
		t.Fatalf("the removed directory's file should be untouched on disk: %v", err)
	}
}

func TestPrefsReportsWhatIsScanned(t *testing.T) {
	dir := t.TempDir()
	ts, _ := prefsServer(t, []string{dir})
	res, err := http.Get(ts.URL + "/api/prefs")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var p PrefsResponse
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if len(p.Roots) != 1 || p.Roots[0] != dir {
		t.Fatalf("reports %v, want %s", p.Roots, dir)
	}
	if !p.Persisted {
		t.Fatal("persisted should be true when there is somewhere to write")
	}
	if !p.Editable {
		t.Fatal("editable should be true when changes are allowed")
	}
}

// A bad list is refused with the reason, not applied and not swallowed.
func TestPrefsRefusesWhatCannotBeScanned(t *testing.T) {
	dir := t.TempDir()
	ts, lib := prefsServer(t, []string{dir})
	res := putPrefs(t, ts.URL, []string{filepath.Join(dir, "does-not-exist")})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", res.StatusCode)
	}
	if got := lib.Roots(); len(got) != 1 || got[0] != dir {
		t.Fatalf("roots changed to %v despite the refusal", got)
	}
}

// A server started with -lock reports what it is indexing and refuses every
// change. The dialog hides its controls to match, but this is the guarantee.
func TestPrefsLockedRefusesChanges(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := library.New([]string{dir}, log)
	var dist fs.FS = fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	srv := New(lib, state.Load(nil, log), NewThumbnailer(nil, nil, log), NewRemuxer("", NewScratch("", 0), log), NewHLS("", lib, NewScratch("", 0), testLog()), nil, dist, log)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	res := putPrefs(t, ts.URL, []string{dir})
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", res.StatusCode)
	}

	// And it still says what is indexed, marked as not editable so the
	// dialog can show it without offering controls that would be refused.
	get, err := http.Get(ts.URL + "/api/prefs")
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	var p PrefsResponse
	if err := json.NewDecoder(get.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Editable {
		t.Fatal("a locked server should not report itself editable")
	}
	if len(p.Roots) != 1 || p.Roots[0] != dir {
		t.Fatalf("reports %v, want %s", p.Roots, dir)
	}
}
