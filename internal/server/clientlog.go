package server

// What went wrong in the browser, in the server's log.
//
// Half of what this app does happens where the server cannot see it: which
// route a film took, whether a conversion's connection came apart and why,
// what the element said when it refused to play something, a fetch that
// failed on the way. The server's log has the other half — every request,
// every conversion, every probe — and until now the two could only be put
// together by asking somebody to open a browser console and read it out.
//
// So the page says it. One endpoint, one line per fault, in the same log as
// everything else about the same viewing, with the same timestamps.
//
// Everything about it is bounded, because this is the one route where a
// client writes into the server's log: the body is small and every field is
// trimmed, control characters cannot get in (a newline in a value would
// forge a log line), and the whole endpoint is rate limited process-wide
// with one line to say when it starts dropping. There is no authentication
// here, as there is none anywhere else in this server — whoever can reach
// the port can already point the library at any directory — so the bound is
// what stands in for it.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// maxClientLogBody is the whole request, which is a handful of short
	// fields. Enforced before any parsing.
	maxClientLogBody = 4 << 10
	// maxClientLogField is how much of any one field is kept. A browser's
	// error text runs to a sentence; a stack does not belong here.
	maxClientLogField = 400
	// clientLogPerMinute is how many faults are logged before the rest are
	// dropped. A page in trouble reports a handful; a page in a loop, or a
	// caller with something else in mind, is refused after this.
	clientLogPerMinute = 60
)

// clientLogLimit is the process-wide bound on how often this route writes.
type clientLogLimit struct {
	mu      sync.Mutex
	window  time.Time
	written int
	dropped int
}

// allow reports whether one more line may be written, and how many were
// dropped since the last one that was — so the line that resumes can say
// what was missed rather than the log simply going quiet.
func (l *clientLogLimit) allow(now time.Time) (ok bool, dropped int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.window) >= time.Minute {
		l.window, l.written = now, 0
	}
	if l.written >= clientLogPerMinute {
		l.dropped++
		return false, 0
	}
	l.written++
	dropped, l.dropped = l.dropped, 0
	return true, dropped
}

// handleClientLog records one fault the page reports.
//
// It answers 204 whatever it decides, dropped or not: the page is telling
// the server something, not asking it anything, and a failure to record a
// fault must never become a second fault for the page to report.
func (s *Server) handleClientLog(w http.ResponseWriter, r *http.Request) {
	var f ClientFault
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxClientLogBody)).Decode(&f); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	f.What, f.Detail = logSafe(f.What), logSafe(f.Detail)
	f.Route, f.Where, f.Item = logSafe(f.Route), logSafe(f.Where), logSafe(f.Item)
	if f.What == "" {
		http.Error(w, "nothing said", http.StatusBadRequest)
		return
	}
	ok, dropped := s.clientLog.allow(time.Now())
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	attrs := []any{"what", f.What, "agent", briefAgent(r.UserAgent())}
	if f.Detail != "" {
		attrs = append(attrs, "detail", f.Detail)
	}
	if f.Item != "" {
		// The film, by the name the log uses everywhere else, where the
		// caller may see it at all — and by nothing at all where it may
		// not, since this route must not become a way to ask whether an
		// id exists.
		if it, allowed := s.item(r, f.Item); allowed {
			attrs = append(attrs, "path", it.Rel)
		}
	}
	if f.At > 0 {
		attrs = append(attrs, "at", f.At)
	}
	if f.Route != "" {
		attrs = append(attrs, "route", f.Route)
	}
	if f.Status != 0 {
		attrs = append(attrs, "status", f.Status)
	}
	if f.Where != "" {
		attrs = append(attrs, "where", f.Where)
	}
	if dropped > 0 {
		attrs = append(attrs, "dropped", dropped)
	}
	s.log.Log(r.Context(), levelOfFault(f.What), "the page reports a fault", attrs...)
	w.WriteHeader(http.StatusNoContent)
}

// levelOfFault keeps the ordinary course of things out of the warnings. A
// conversion picked up again is worth having in the log beside the requests
// it explains, but it is not a fault: the viewer saw nothing.
func levelOfFault(what string) slog.Level {
	if strings.HasSuffix(what, "-recovered") {
		return slog.LevelInfo
	}
	return slog.LevelWarn
}

// logSafe trims one field to something a log line can hold: bounded, and
// with no control characters, since a newline in a value is a forged line.
// The bound is applied to runes rather than bytes so a multi-byte character
// is never cut in half.
func logSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxClientLogField {
		s = string(r[:maxClientLogField]) + "…"
	}
	return s
}
