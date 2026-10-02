package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"zkaraver/internal/room"
)

func (s *Server) handleMemberWS(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	userID, ok := s.userID(r)
	if !ok {
		writeErr(w, room.ErrUnauthorized)
		return
	}
	s.serveWS(w, r, rm, room.NewClient(room.KindMember, userID))
}

func (s *Server) handlePlayerWS(w http.ResponseWriter, r *http.Request) {
	if rm, ok := s.room(w, r); ok {
		s.serveWS(w, r, rm, room.NewPlayerClient(clientAddr(r), r.UserAgent()))
	}
}

// clientAddr is informational only (shown to admins), so trusting
// X-Forwarded-For from a reverse proxy is fine here.
func clientAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) handleAdminWS(w http.ResponseWriter, r *http.Request) {
	if rm, ok := s.room(w, r); ok {
		s.serveWS(w, r, rm, room.NewClient(room.KindAdmin, ""))
	}
}

// Close codes 4000+ carry the error code as reason so the client can tell
// "kicked/banned/not a member" apart from a network drop.
const closeRejected websocket.StatusCode = 4003

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request, rm *room.Room, c *room.Client) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{s.publicHost()}})
	if err != nil {
		return
	}
	if err := rm.Attach(c); err != nil {
		code := "internal"
		var re *room.Error
		if errors.As(err, &re) {
			code = re.Code
		}
		conn.Close(closeRejected, code)
		return
	}
	defer rm.Detach(c)

	ctx := conn.CloseRead(r.Context())
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()

	write := func(msg []byte) error {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return conn.Write(wctx, websocket.MessageText, msg)
	}
	for {
		select {
		case msg := <-c.Send:
			if write(msg) != nil {
				return
			}
		case <-c.Done:
			select {
			case msg := <-c.Send:
				_ = write(msg)
			default:
			}
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
