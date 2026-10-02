package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"karaver/internal/room"
)

// TV pairing: a TV opens /tv, gets a short code over a WebSocket and shows it.
// Someone enters the code on a phone; the server tells that TV which room's
// player page to open. Codes live only as long as the TV's connection, so
// nothing is stored and every visit to /tv pairs afresh.

type tvConn struct {
	paired chan string // receives the room id once
}

type tvPairing struct {
	mu      sync.Mutex
	waiting map[string]*tvConn
}

func newTVPairing() *tvPairing {
	return &tvPairing{waiting: map[string]*tvConn{}}
}

func (p *tvPairing) register() (string, *tvConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	digits := 4
	if len(p.waiting) > 1000 { // keep codes hard to hit by accident
		digits = 6
	}
	max := big.NewInt(1)
	for range digits {
		max.Mul(max, big.NewInt(10))
	}
	for {
		n, _ := rand.Int(rand.Reader, max)
		code := fmt.Sprintf("%0*d", digits, n)
		if _, taken := p.waiting[code]; !taken {
			c := &tvConn{paired: make(chan string, 1)}
			p.waiting[code] = c
			return code, c
		}
	}
}

func (p *tvPairing) unregister(code string) {
	p.mu.Lock()
	delete(p.waiting, code)
	p.mu.Unlock()
}

// pair sends the waiting TV with this code to roomID. It reports false for an
// unknown or already used code.
func (p *tvPairing) pair(code, roomID string) bool {
	code = strings.TrimSpace(code)
	p.mu.Lock()
	c, ok := p.waiting[code]
	if ok {
		delete(p.waiting, code)
	}
	p.mu.Unlock()
	if ok {
		c.paired <- roomID
	}
	return ok
}

func (s *Server) handleTVWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{s.publicHost()}})
	if err != nil {
		return
	}
	code, c := s.tv.register()
	defer s.tv.unregister(code)

	ctx := conn.CloseRead(r.Context())
	send := func(v any) error {
		b, _ := json.Marshal(v)
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return conn.Write(wctx, websocket.MessageText, b)
	}
	if send(map[string]string{"type": "code", "code": code}) != nil {
		return
	}
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case roomID := <-c.paired:
			_ = send(map[string]string{"type": "paired", "room": roomID})
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

func (s *Server) readPairCode(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return "", false
	}
	return strings.TrimSpace(body.Code), true
}

func (s *Server) pairOrFail(w http.ResponseWriter, code, roomID string) {
	if !s.tv.pair(code, roomID) {
		time.Sleep(300 * time.Millisecond) // slow down code guessing
		writeErr(w, room.ErrPairCode)
		return
	}
	writeOK(w)
}

// handleMemberPair lets any member connect a TV, but only while the room has no
// player at all; after that only admins can add TVs (handleAdminPair).
func (s *Server) handleMemberPair(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	userID, ok := s.userID(r)
	if !ok {
		writeErr(w, room.ErrUnauthorized)
		return
	}
	if _, err := rm.Nickname(userID); err != nil {
		writeErr(w, err)
		return
	}
	if rm.HasPlayer() {
		writeErr(w, room.ErrPlayerExists)
		return
	}
	if code, ok := s.readPairCode(w, r); ok {
		s.pairOrFail(w, code, rm.ID)
	}
}

func (s *Server) handleAdminPair(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.room(w, r)
	if !ok {
		return
	}
	if code, ok := s.readPairCode(w, r); ok {
		s.pairOrFail(w, code, rm.ID)
	}
}
