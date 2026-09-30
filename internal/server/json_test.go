package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

func TestWritesRejectIncompleteOrMultipleJSONValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "track.mp3")
	if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, srv, _ := serverUnderTest(t, dir)
	id := library.PathID(path)
	for _, body := range []string{
		`null`, `{"t":20,"d":100} {"t":90,"d":100}`,
		`{"t":20,"d":100} trailing`, `{"t":20,"d":100}` + strings.Repeat(" ", 4096),
	} {
		srv.st.Set(id, 10, 100)
		before, _ := srv.st.Get(id)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/state/"+id, strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("invalid body answered %d", w.Code)
		}
		after, _ := srv.st.Get(id)
		if after != before {
			t.Error("invalid body changed saved state")
		}
	}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/state/"+id, strings.NewReader("{\"t\":20,\"d\":100}\n \t")))
	if w.Code != http.StatusNoContent {
		t.Fatalf("valid JSON with whitespace = %d: %s", w.Code, w.Body.String())
	}
}
