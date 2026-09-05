package server

// Request logging, for diagnosing a player that will not play.
//
// A browser refusing a stream says almost nothing on its own — least of all a
// phone, where there is no console to read. What it does do is make requests,
// and the shape of those is the diagnosis: which route it took, what range it
// asked for, what it was told, whether it came back for a second segment or
// gave up after the first. This records exactly that, and only when asked
// for, because it is one line per request and playback makes many.

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// logged wraps a handler so every request and what it was answered with is
// recorded. Every request, not only the API: a page that will not load and a
// stream that will not play look the same from the other end, and which
// requests were made at all is half the answer.
func logged(h http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		h.ServeHTTP(rec, r)

		// The names are the conventional ones for an access log, so the
		// output reads like every other one and can be grepped like it.
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.n,
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"remote", remoteHost(r),
			"proto", r.Proto,
		}
		if q := r.URL.RawQuery; q != "" {
			attrs = append(attrs, "query", q)
		}
		// The range is the whole story for a media request: a player probing
		// a resource asks for the first two bytes, and one that is happy
		// comes back for the rest.
		if rg := r.Header.Get("Range"); rg != "" {
			attrs = append(attrs, "range", rg)
		}
		// What was answered, not only that something was: a redirect is only
		// legible with its target, and a range only with what came back.
		if loc := rec.Header().Get("Location"); loc != "" {
			attrs = append(attrs, "location", loc)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "" {
			attrs = append(attrs, "content_type", ct)
		}
		if cr := rec.Header().Get("Content-Range"); cr != "" {
			attrs = append(attrs, "content_range", cr)
		}
		// **Which face the request arrived as.** A restriction is set by the
		// proxy, and the failure it has is one location block that forgets
		// to set it: everything works, and one endpoint quietly answers for
		// the whole library. That is invisible from the outside — the answer
		// is well-formed, just not this face's — and it is exactly what a
		// chip flicking to a number from another library looks like. So the
		// face is logged for every request, and a request that arrived with
		// none says so rather than saying nothing.
		attrs = append(attrs, "face", faceOf(r))
		// Enough of the agent to tell which browser is asking, without a
		// line of version strings.
		if ua := r.Header.Get("User-Agent"); ua != "" {
			attrs = append(attrs, "agent", briefAgent(ua))
		}
		// Debug, not info: this is one line per request and playback makes
		// hundreds, so it belongs to the level an operator turns on when
		// something needs explaining.
		log.Debug("http", attrs...)
	})
}

// remoteHost is who asked, without the ephemeral port, which is noise.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	// Behind a proxy the peer is the proxy; the header it sets is the client,
	// and on a phone that is the only way to tell one viewer from another.
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i > 0 {
			fwd = fwd[:i]
		}
		return strings.TrimSpace(fwd)
	}
	return host
}

// briefAgent reduces a user agent to the name that matters here.
func briefAgent(ua string) string {
	switch {
	case strings.Contains(ua, "Edg/"):
		return "edge"
	case strings.Contains(ua, "Chrome/"):
		return "chrome"
	case strings.Contains(ua, "Firefox/"):
		return "firefox"
	case strings.Contains(ua, "AppleWebKit") && strings.Contains(ua, "Mobile"):
		return "safari-mobile"
	case strings.Contains(ua, "Safari/"):
		return "safari"
	case strings.Contains(ua, "AppleCoreMedia"):
		// The media loader itself, which is what fetches segments on iOS —
		// worth telling apart from the page that asked for them.
		return "applecoremedia"
	}
	return "other"
}

// statusWriter remembers what was answered.
type statusWriter struct {
	http.ResponseWriter
	status int
	n      int64
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(p []byte) (int, error) {
	s.wrote = true
	n, err := s.ResponseWriter.Write(p)
	s.n += int64(n)
	return n, err
}

// Flush keeps the streaming responses streaming: without it the converter's
// output would sit in a buffer behind this wrapper.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the writer underneath, for the
// deadlines and hijacks it offers; without it the wrapper hides them.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// faceOf describes the restrictions a request arrived with, for the log.
// "all" means no header at all — which on a face that should have one is the
// fault, and the reason this is worth a field of its own.
func faceOf(r *http.Request) string {
	content := strings.Join(contentOf(r).names(), "+")
	if content == "" {
		content = "all"
	}
	if paths := pathsOf(r); paths.Restricted() {
		return content + " under " + strings.ReplaceAll(paths.Key(), "\n", ",")
	}
	return content
}
