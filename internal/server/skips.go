package server

// Where the credits are, so they can be skipped.
//
// Television has an opening and a closing that are the same every episode,
// and nobody watching a season wants either more than once. Nothing in the
// file says where they are, and nobody is asked to: the library finds them
// from the sound, by what recurs across the episodes of a season
// (library/skipdetect.go). This route answers what has been found for one
// episode, and asking is also what puts that episode's season at the front
// of the work — somebody is opening it.

import (
	"net/http"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// handleSkipGet answers where a video's intro and credits are, where they
// have been found. Through the face, like every by-id route.
func (s *Server) handleSkipGet(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok || it.Kind != library.KindVideo {
		http.NotFound(w, r)
		return
	}
	s.lib.WantSkips(it)
	m, _ := s.lib.SkipFor(it.ID)
	writeJSON(w, SkipMarks{IntroStart: m.IntroStart, IntroEnd: m.IntroEnd, Outro: m.Outro})
}
