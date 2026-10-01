package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"karaver/internal/db"
)

const (
	adminCookie = "karaver_admin"
	adminTTL    = 30 * 24 * time.Hour
)

func loadSecret(d *sql.DB) ([]byte, error) {
	v, ok, err := db.Setting(d, "session_secret")
	if err != nil {
		return nil, err
	}
	if ok {
		return hex.DecodeString(v)
	}
	b := randomBytes(32)
	return b, db.SetSetting(d, "session_secret", hex.EncodeToString(b))
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// ---- admin: stateless HMAC-signed cookie ----

func (s *Server) adminSig(exp int64) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("admin:" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) isAdmin(r *http.Request) bool {
	c, err := r.Cookie(adminCookie)
	if err != nil {
		return false
	}
	expStr, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(s.adminSig(exp)))
}

func (s *Server) checkPassword(pw string) bool {
	a := sha256.Sum256([]byte(pw))
	b := sha256.Sum256([]byte(s.cfg.AdminPassword))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) setAdminCookie(w http.ResponseWriter, r *http.Request) {
	exp := time.Now().Add(adminTTL).Unix()
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookie,
		Value:    strconv.FormatInt(exp, 10) + "." + s.adminSig(exp),
		Path:     "/",
		MaxAge:   int(adminTTL.Seconds()),
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearAdminCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

// ---- anonymous users: public id + secret bearer token ----

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func requestToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token") // WebSocket can't set headers
}

func (s *Server) lookupUser(token string) (string, bool) {
	if token == "" || len(token) > 128 {
		return "", false
	}
	h := hashToken(token)
	if v, ok := s.users.Load(h); ok {
		return v.(string), true
	}
	var id string
	err := s.db.QueryRow(`SELECT id FROM users WHERE token_hash=?`, h).Scan(&id)
	if err != nil {
		return "", false
	}
	s.users.Store(h, id)
	return id, true
}

func (s *Server) userID(r *http.Request) (string, bool) {
	return s.lookupUser(requestToken(r))
}

func (s *Server) createUser() (id, token string, err error) {
	id = hex.EncodeToString(randomBytes(12))
	token = base64.RawURLEncoding.EncodeToString(randomBytes(32))
	_, err = s.db.Exec(`INSERT INTO users(id, token_hash, created_at) VALUES(?,?,?)`, id, hashToken(token), time.Now().UnixMilli())
	if err != nil {
		return "", "", errors.Join(errors.New("create user"), err)
	}
	s.users.Store(hashToken(token), id)
	return id, token, nil
}
