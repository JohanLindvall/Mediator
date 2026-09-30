package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/JohanLindvall/Mediator/internal/library"
)

type scopeKey struct{}

type requestScope struct {
	content content
	paths   library.PathFilter
}

// protect applies browser security and parses the proxy's restrictions once
// per request. Invalid restrictions fail closed, including on /api/info where
// a permissive fallback would otherwise issue a streaming credential.
func protect(next http.Handler) http.Handler {
	guard := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if err := guard.Check(r); err != nil {
			http.Error(w, "cross-origin writes are not allowed", http.StatusForbidden)
			return
		}
		scope := requestScope{
			content: parseContent(strings.Join(r.Header.Values(ContentHeader), ",")),
			paths:   library.ParsePaths(strings.Join(r.Header.Values(PathsHeader), "\n")),
		}
		if scope.content == (content{}) || !scope.paths.Valid() {
			http.Error(w, "invalid media restrictions; check the proxy configuration", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), scopeKey{}, scope)))
	})
}

// decodeJSON accepts exactly one non-null JSON object within the route's
// byte budget. Decoding once alone silently accepts a second value or an
// oversized whitespace suffix. Publish the value only after checking EOF.
func decodeJSON[T any](w http.ResponseWriter, r *http.Request, dst *T, limit int64) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	var value *T
	if err := d.Decode(&value); err != nil {
		return err
	}
	if value == nil {
		return errors.New("a JSON object is required")
	}
	if err := d.Decode(new(json.RawMessage)); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("only one JSON object is allowed")
	}
	*dst = *value
	return nil
}

// fullLibrary is the common boundary for reading or changing directories
// and for deleting files. A restricted view cannot administer the library.
func fullLibrary(r *http.Request) (bool, string) {
	if pathsOf(r).Restricted() {
		return false, "this view is confined to part of the library"
	}
	if !contentOf(r).unrestricted() {
		return false, "this view shows part of the library"
	}
	return true, ""
}
