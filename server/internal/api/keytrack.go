package api

import (
	"net/http"
	"strconv"
)

// handleKeyTrack serves a song's audio rendered at another key
// (/media/{id}/key/{semis}). Renders happen ahead of time in package keys;
// a key that is not rendered yet is 404 and the TV keeps the original key.
func (s *Server) handleKeyTrack(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	semis, err := strconv.Atoi(r.PathValue("semis"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, ok := s.keys.Path(id, semis)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.serveFile(w, r, path)
}
