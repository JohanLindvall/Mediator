package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spaHandler serves the embedded frontend. Unknown paths fall back to
// index.html so client-side routes work; hashed assets get immutable caching.
func spaHandler(dist fs.FS) http.Handler {
	fileServer := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := dist.Open(p); err == nil {
			f.Close()
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				// Embedded files carry no modtime, so clients cannot
				// revalidate; forbid caching outright to keep the app shell
				// (and thus the hashed asset references) always current.
				w.Header().Set("Cache-Control", "no-store")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// An API path that reached here matched no handler — a route that
		// does not exist, or one the mux cleaned into nothing, which is what
		// a request ending in ".." becomes. Handing back the app shell tells
		// a caller expecting JSON that everything is fine and then fails
		// somewhere else; a 404 says what happened.
		if strings.HasPrefix(p, "api/") {
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, r)
			return
		}
		// An asset path is a content hash, never a client-side route (UI
		// state lives in the URL hash), so a miss here is a stale hash from
		// a build that has been replaced. Say so: handing back the app shell
		// answers a script request with HTML, which surfaces as a parse
		// error somewhere else entirely.
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, r)
			return
		}
		// SPA fallback.
		w.Header().Set("Cache-Control", "no-store")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}
