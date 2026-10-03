package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"zkaraver/internal/library"
	"zkaraver/internal/room"
)

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"publicUrl": s.cfg.PublicURL,
	})
}

// handleSession validates a stored token or issues a new anonymous identity.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	_ = readJSON(r, &body)
	if id, ok := s.lookupUser(body.Token); ok {
		writeJSON(w, http.StatusOK, map[string]string{"userId": id, "token": body.Token})
		return
	}
	id, token, err := s.createUser()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"userId": id, "token": token})
}

func (s *Server) handleSongs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	// ?sort=artist|title|new|popular, and ?artist=<name> to browse one artist
	// ("" is the unknown artist, so presence matters, not the value).
	songs, more, err := s.lib.SearchWith(library.SearchOptions{
		Query:     q.Get("q"),
		Sort:      q.Get("sort"),
		Artist:    q.Get("artist"),
		HasArtist: q.Has("artist"),
	}, limit, max(offset, 0))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": songs, "more": more})
}

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	song, err := s.lib.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.serveMediaFile(w, r, song.Path)
}

// handleOriginal serves the original-vocal companion; the player uses only its audio.
func (s *Server) handleOriginal(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.lib.OriginalPath(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.serveMediaFile(w, r, p)
}

func (s *Server) serveMediaFile(w http.ResponseWriter, r *http.Request, rel string) {
	s.serveFile(w, r, filepath.Join(s.cfg.MediaDir, filepath.FromSlash(rel)))
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Browsers may cache the video but must ask first: a replaced file keeps the
	// same URL, and a fixed max-age would keep playing the old one. ServeContent
	// answers that question with 304 (via Last-Modified) when nothing changed.
	w.Header().Set("Cache-Control", "no-cache")
	// ServeContent handles Range requests, so the browser streams and seeks without transcoding.
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

// ---- room (members) ----

func (s *Server) handleRoomInfo(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": rm.ID, "url": s.cfg.PublicURL + "/r/" + rm.ID})
}

func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	png, cached := s.qr.Load(rm.ID)
	if !cached {
		b, err := roomQR(s.cfg.PublicURL + "/r/" + rm.ID)
		if err != nil {
			writeErr(w, err)
			return
		}
		png, _ = s.qr.LoadOrStore(rm.ID, b)
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(png.([]byte))
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	userID, ok := s.userID(r)
	if !ok {
		writeErr(w, room.ErrUnauthorized)
		return
	}
	var body struct {
		Nickname string `json:"nickname"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := rm.Join(userID, body.Nickname); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	userID, ok := s.userID(r)
	if !ok {
		writeErr(w, room.ErrUnauthorized)
		return
	}
	var body struct {
		SongID int64 `json:"songId"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := rm.Enqueue(userID, body.SongID); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	userID, admin, ok := s.actor(w, r)
	if !ok {
		return
	}
	itemID, err := pathInt(r, "item")
	if err != nil {
		writeErr(w, room.ErrInvalid)
		return
	}
	if err := rm.Remove(itemID, userID, admin); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleSkip(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	userID, admin, ok := s.actor(w, r)
	if !ok {
		return
	}
	var body struct {
		ItemID int64 `json:"itemId"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := rm.Skip(body.ItemID, userID, admin); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	userID, admin, ok := s.actor(w, r)
	if !ok {
		return
	}
	var body struct {
		Action string `json:"action"`
		Value  int    `json:"value"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := rm.Control(userID, admin, body.Action, body.Value); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handlePlayerEnded(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	var body struct {
		PlayerSecret string `json:"playerSecret"`
		ItemID       int64  `json:"itemId"`
		Failed       bool   `json:"failed"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := rm.PlayerEnded(body.PlayerSecret, body.ItemID, body.Failed); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleKickPlayer(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	var body struct {
		PlayerID string `json:"playerId"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := rm.KickPlayer(body.PlayerID); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

// ---- admin ----

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if !s.checkPassword(body.Password) {
		time.Sleep(500 * time.Millisecond) // slow down guessing
		writeErr(w, &room.Error{Status: http.StatusUnauthorized, Code: "wrong_password"})
		return
	}
	s.setAdminCookie(w, r)
	writeOK(w)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearAdminCookie(w)
	writeOK(w)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"admin": s.isAdmin(r)})
}

func (s *Server) handleListRooms(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.rooms.List())
}

func (s *Server) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	rm, err := s.rooms.Create(body.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": rm.ID})
}

func (s *Server) handleUpdateRoom(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	var body room.Settings
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := rm.UpdateSettings(body); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleDeleteRoom(w http.ResponseWriter, r *http.Request) {
	if err := s.rooms.Delete(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	s.qr.Delete(r.PathValue("id"))
	writeOK(w)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	limit, cur := historyQuery(r)
	h, err := s.rooms.History(rm.ID, limit, cur)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	var body struct {
		ItemID int64 `json:"itemId"`
		Index  int   `json:"index"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := rm.Move(body.ItemID, body.Index); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) userAction(fn func(rm *room.Room, userID string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rm, ok := s.room(w, r)
		if !ok {
			return
		}
		var body struct {
			UserID string `json:"userId"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, err)
			return
		}
		if err := fn(rm, body.UserID); err != nil {
			writeErr(w, err)
			return
		}
		writeOK(w)
	}
}

func (s *Server) handleKick(w http.ResponseWriter, r *http.Request) {
	s.userAction((*room.Room).Kick)(w, r)
}

func (s *Server) handleUnban(w http.ResponseWriter, r *http.Request) {
	s.userAction((*room.Room).Unban)(w, r)
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	if err := rm.Clear(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleLibraryStatus(w http.ResponseWriter, r *http.Request) {
	scanning, last := s.lib.Status()
	n, err := s.lib.Count()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scanning": scanning, "last": last, "songs": n})
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if !s.lib.StartScan() {
		writeErr(w, &room.Error{Status: http.StatusConflict, Code: "scan_running"})
		return
	}
	writeOK(w)
}

// ---- favorites (keyed by the member's nickname) ----

func (s *Server) memberNickname(w http.ResponseWriter, r *http.Request) (string, bool) {
	rm, ok := s.room(w, r)
	if !ok {
		return "", false
	}
	userID, ok := s.userID(r)
	if !ok {
		writeErr(w, room.ErrUnauthorized)
		return "", false
	}
	nick, err := rm.Nickname(userID)
	if err != nil {
		writeErr(w, err)
		return "", false
	}
	return nick, true
}

func (s *Server) handleFavorites(w http.ResponseWriter, r *http.Request) {
	nick, ok := s.memberNickname(w, r)
	if !ok {
		return
	}
	songs, err := s.lib.Favorites(nick)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, songs)
}

func (s *Server) handleAddFavorite(w http.ResponseWriter, r *http.Request) {
	nick, ok := s.memberNickname(w, r)
	if !ok {
		return
	}
	songID, err := pathInt(r, "song")
	if err != nil {
		writeErr(w, room.ErrInvalid)
		return
	}
	if err := s.lib.AddFavorite(nick, songID); err != nil {
		if errors.Is(err, library.ErrNotFound) {
			err = room.ErrSongNotFound
		}
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleRemoveFavorite(w http.ResponseWriter, r *http.Request) {
	nick, ok := s.memberNickname(w, r)
	if !ok {
		return
	}
	songID, err := pathInt(r, "song")
	if err != nil {
		writeErr(w, room.ErrInvalid)
		return
	}
	if err := s.lib.RemoveFavorite(nick, songID); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}

// handleRoomHistory serves the room's recent history to members (and admins).
func (s *Server) handleRoomHistory(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	userID, admin, ok := s.actor(w, r)
	if !ok {
		return
	}
	if !admin {
		if _, err := rm.Nickname(userID); err != nil {
			writeErr(w, err)
			return
		}
	}
	limit, cur := historyQuery(r)
	h, err := s.rooms.History(rm.ID, limit, cur)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleReorder(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	userID, admin, ok := s.actor(w, r)
	if !ok {
		return
	}
	itemID, err := pathInt(r, "item")
	if err != nil {
		writeErr(w, room.ErrInvalid)
		return
	}
	var body struct {
		To string `json:"to"` // "up" or "top"
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := rm.Reorder(itemID, body.To, userID, admin); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleReplayAll(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	userID, admin, ok := s.actor(w, r)
	if !ok {
		return
	}
	n, err := rm.ReplayAll(userID, admin)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"queued": n})
}

// historyQuery reads ?limit=50&before_started=…&before_id=… (the cursor of the
// oldest entry already shown) for paging through history.
func historyQuery(r *http.Request) (int, room.HistoryCursor) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var cur room.HistoryCursor
	cur.StartedAt, _ = strconv.ParseInt(q.Get("before_started"), 10, 64)
	cur.ID, _ = strconv.ParseInt(q.Get("before_id"), 10, 64)
	return limit, cur
}

func (s *Server) handleClearHistory(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	userID, admin, ok := s.actor(w, r)
	if !ok {
		return
	}
	n, err := rm.ClearHistory(userID, admin)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
}

// handleArtists lists artists with their song counts: ?q=…&sort=name|count.
func (s *Server) handleArtists(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	artists, more, err := s.lib.Artists(q.Get("q"), q.Get("sort"), limit, max(offset, 0))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": artists, "more": more})
}
