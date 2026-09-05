package server

import (
	"encoding/json"
	"net/http"
)

// handleLike records the owner's verdict on one thing: 1, -1, or 0 to
// withdraw it. It goes where the play count goes — the state store keeps it
// beside the count, the library is told so the popular orders can sort on
// it under their own lock — and it bumps the version the way a play does,
// since the collections sum it when they are built.
func (s *Server) handleLike(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.item(r, id); !ok {
		http.NotFound(w, r)
		return
	}
	var body LikeUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil ||
		body.Like < -1 || body.Like > 1 {
		http.Error(w, "like must be 1, -1 or 0", http.StatusBadRequest)
		return
	}
	like := s.st.Like(id, body.Like)
	s.lib.SetLike(id, like)
	writeJSON(w, LikeResponse{Like: like})
}
