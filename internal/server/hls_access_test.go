package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

func TestHLSChildrenRequireTheCurrentItemScope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mkv")
	otherPath := filepath.Join(dir, "other.mkv")
	for name, body := range map[string]string{path: "video", otherPath: "other video", filepath.Join(dir, "clip.srt"): "1\n00:00:00,000 --> 00:00:01,000\ncaption\n"} {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, srv, lib := serverUnderTest(t, dir)
	it, _ := lib.Get(library.PathID(path))
	work := t.TempDir()
	for name, body := range map[string]string{"seg000000.ts": "segment", "index.m3u8": "#EXTM3U\n#EXTINF:4,\nseg000000.ts\n#EXT-X-ENDLIST\n"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	token, _, _ := srv.sign.mint(time.Now())
	for _, adopted := range []bool{false, true} {
		sess := &hlsSession{id: "session", key: hlsKey(it, 0, false, "", quality{}), dir: work, item: it}
		if adopted {
			sess.item = library.Item{}
		}
		srv.hls.byID[sess.id] = sess
		for _, prefix := range []string{"/api", "/api/signed/" + token} {
			for _, file := range []string{"seg000000.ts", "media.m3u8", "sub0.m3u8", "sub0.vtt"} {
				for _, tc := range []struct {
					id, header, value string
					status            int
				}{
					{it.ID, "", "", http.StatusOK},
					{it.ID, ContentHeader, "music", http.StatusNotFound},
					{it.ID, PathsHeader, filepath.Join(dir, "elsewhere"), http.StatusNotFound},
					{"wrong-item", "", "", http.StatusNotFound},
					{library.PathID(otherPath), "", "", http.StatusNotFound},
				} {
					r := httptest.NewRequest(http.MethodGet, prefix+"/hls/"+tc.id+"/session/"+file, nil)
					if tc.header != "" {
						r.Header.Set(tc.header, tc.value)
					}
					w := httptest.NewRecorder()
					srv.Handler().ServeHTTP(w, r)
					if w.Code != tc.status {
						t.Errorf("adopted=%v %s %s=%q id=%s: got %d, want %d", adopted, file, tc.header, tc.value, tc.id, w.Code, tc.status)
					}
				}
			}
		}
	}
	lib.Remove(path)
	r := httptest.NewRequest(http.MethodGet, "/api/hls/"+it.ID+"/session/seg000000.ts", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("removed item still serves cached segments: %d", w.Code)
	}
}
