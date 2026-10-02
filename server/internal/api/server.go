// Package api exposes the HTTP, WebSocket and static-file endpoints.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"

	"zkaraver/internal/config"
	"zkaraver/internal/library"
	"zkaraver/internal/room"
)

type Server struct {
	cfg    *config.Config
	db     *sql.DB
	lib    *library.Library
	rooms  *room.Manager
	static fs.FS
	secret []byte
	users  sync.Map // token hash -> user id
	qr     sync.Map // room id -> PNG bytes
	tv     *tvPairing
}

func init() {
	// Not in Go's built-in table; browsers expect this for the PWA manifest.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

func New(cfg *config.Config, d *sql.DB, lib *library.Library, rooms *room.Manager, static fs.FS) (*Server, error) {
	secret, err := loadSecret(d)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, db: d, lib: lib, rooms: rooms, static: static, secret: secret, tv: newTVPairing()}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("POST /api/session", s.handleSession)
	mux.HandleFunc("GET /api/songs", s.handleSongs)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /media/{id}", s.handleMedia)
	mux.HandleFunc("GET /media/{id}/original", s.handleOriginal)

	mux.HandleFunc("GET /api/rooms/{id}", s.handleRoomInfo)
	mux.HandleFunc("GET /api/rooms/{id}/qr.png", s.handleQR)
	mux.HandleFunc("GET /api/rooms/{id}/ws", s.handleMemberWS)
	mux.HandleFunc("POST /api/rooms/{id}/join", s.handleJoin)
	mux.HandleFunc("POST /api/rooms/{id}/queue", s.handleEnqueue)
	mux.HandleFunc("DELETE /api/rooms/{id}/queue/{item}", s.handleRemove)
	mux.HandleFunc("POST /api/rooms/{id}/queue/{item}/move", s.handleReorder)
	mux.HandleFunc("POST /api/rooms/{id}/replay-all", s.handleReplayAll)
	mux.HandleFunc("GET /api/rooms/{id}/history", s.handleRoomHistory)
	mux.HandleFunc("POST /api/rooms/{id}/skip", s.handleSkip)
	mux.HandleFunc("POST /api/rooms/{id}/control", s.handleControl)
	mux.HandleFunc("GET /api/rooms/{id}/favorites", s.handleFavorites)
	mux.HandleFunc("PUT /api/rooms/{id}/favorites/{song}", s.handleAddFavorite)
	mux.HandleFunc("DELETE /api/rooms/{id}/favorites/{song}", s.handleRemoveFavorite)

	mux.HandleFunc("GET /api/tv/ws", s.handleTVWS)
	mux.HandleFunc("POST /api/rooms/{id}/tv/pair", s.handleMemberPair)
	mux.HandleFunc("POST /api/admin/rooms/{id}/tv/pair", s.admin(s.handleAdminPair))
	mux.HandleFunc("GET /api/rooms/{id}/player/ws", s.handlePlayerWS)
	mux.HandleFunc("POST /api/rooms/{id}/player/ended", s.handlePlayerEnded)

	mux.HandleFunc("POST /api/admin/login", s.handleLogin)
	mux.HandleFunc("POST /api/admin/logout", s.handleLogout)
	mux.HandleFunc("GET /api/admin/me", s.handleMe)
	mux.HandleFunc("GET /api/admin/rooms", s.admin(s.handleListRooms))
	mux.HandleFunc("POST /api/admin/rooms", s.admin(s.handleCreateRoom))
	mux.HandleFunc("PUT /api/admin/rooms/{id}", s.admin(s.handleUpdateRoom))
	mux.HandleFunc("DELETE /api/admin/rooms/{id}", s.admin(s.handleDeleteRoom))
	mux.HandleFunc("GET /api/admin/rooms/{id}/ws", s.admin(s.handleAdminWS))
	mux.HandleFunc("GET /api/admin/rooms/{id}/history", s.admin(s.handleHistory))
	mux.HandleFunc("POST /api/admin/rooms/{id}/move", s.admin(s.handleMove))
	mux.HandleFunc("POST /api/admin/rooms/{id}/kick", s.admin(s.handleKick))
	mux.HandleFunc("POST /api/admin/rooms/{id}/unban", s.admin(s.handleUnban))
	mux.HandleFunc("POST /api/admin/rooms/{id}/clear", s.admin(s.handleClear))
	mux.HandleFunc("POST /api/admin/rooms/{id}/players/kick", s.admin(s.handleKickPlayer))
	mux.HandleFunc("GET /api/admin/library", s.admin(s.handleLibraryStatus))
	mux.HandleFunc("POST /api/admin/library/scan", s.admin(s.handleScan))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, room.ErrNotFound) })
	mux.Handle("/", s.spa())
	return mux
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeOK(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

func writeErr(w http.ResponseWriter, err error) {
	var re *room.Error
	if errors.As(err, &re) {
		writeJSON(w, re.Status, map[string]string{"error": re.Code})
		return
	}
	log.Printf("error: %v", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
}

func readJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return room.ErrInvalid
	}
	return nil
}

func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdmin(r) {
			writeErr(w, room.ErrUnauthorized)
			return
		}
		h(w, r)
	}
}

func (s *Server) room(w http.ResponseWriter, r *http.Request) (*room.Room, bool) {
	rm, ok := s.rooms.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, room.ErrNotFound)
	}
	return rm, ok
}

// actor identifies who is acting: an admin, a user, or neither (401).
func (s *Server) actor(w http.ResponseWriter, r *http.Request) (userID string, admin bool, ok bool) {
	admin = s.isAdmin(r)
	userID, _ = s.userID(r)
	if !admin && userID == "" {
		writeErr(w, room.ErrUnauthorized)
		return "", false, false
	}
	return userID, admin, true
}

func (s *Server) spa() http.Handler {
	files := http.FileServerFS(s.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(s.static, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		b, err := fs.ReadFile(s.static, "index.html")
		if err != nil {
			http.Error(w, "frontend missing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
}

func (s *Server) publicHost() string {
	u, _ := url.Parse(s.cfg.PublicURL)
	return u.Host
}

func pathInt(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}
