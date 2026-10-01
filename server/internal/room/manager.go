package room

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Manager struct {
	db    *sql.DB
	mu    sync.RWMutex
	rooms map[string]*Room
}

// NewManager loads all rooms, members and pending queue items into memory.
func NewManager(d *sql.DB) (*Manager, error) {
	m := &Manager{db: d, rooms: map[string]*Room{}}

	rows, err := d.Query(`SELECT id, name, queue_mode, max_per_user, idle_clear_minutes, show_qr, volume FROM rooms`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var s Settings
		var showQR bool
		var volume int
		if err := rows.Scan(&id, &s.Name, &s.Mode, &s.MaxPerUser, &s.IdleClearMinutes, &showQR, &volume); err != nil {
			rows.Close()
			return nil, err
		}
		r := newRoom(m, id, s)
		r.showQR, r.volume = showQR, volume
		m.rooms[id] = r
	}
	rows.Close()

	rows, err = d.Query(`SELECT room_id, user_id, nickname, banned FROM room_members`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var roomID string
		var mem Member
		if err := rows.Scan(&roomID, &mem.UserID, &mem.Nickname, &mem.Banned); err != nil {
			rows.Close()
			return nil, err
		}
		if r := m.rooms[roomID]; r != nil {
			r.members[mem.UserID] = &mem
		}
	}
	rows.Close()

	rows, err = d.Query(`SELECT room_id, user_id, MAX(started_at) FROM queue_items WHERE started_at IS NOT NULL GROUP BY room_id, user_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var roomID, userID string
		var t int64
		if err := rows.Scan(&roomID, &userID, &t); err != nil {
			rows.Close()
			return nil, err
		}
		if r := m.rooms[roomID]; r != nil {
			r.lastSung[userID] = t
		}
	}
	rows.Close()

	rows, err = d.Query(`SELECT q.id, q.room_id, q.song_id, q.user_id, q.status, q.position, q.created_at, COALESCE(q.started_at, 0), s.title, s.artist
		FROM queue_items q JOIN songs s ON s.id = q.song_id
		WHERE q.status IN ('queued', 'playing')`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var roomID, status string
		it := &Item{}
		if err := rows.Scan(&it.ID, &roomID, &it.SongID, &it.UserID, &status, &it.Position, &it.CreatedAt, &it.StartedAt, &it.Title, &it.Artist); err != nil {
			rows.Close()
			return nil, err
		}
		r := m.rooms[roomID]
		if r == nil {
			continue
		}
		if status == "playing" && r.current == nil {
			r.current = it
		} else {
			r.queue = append(r.queue, it)
		}
		r.nextPos = max(r.nextPos, it.Position+1)
	}
	rows.Close()
	return m, nil
}

func (m *Manager) Get(id string) (*Room, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.rooms[id]
	return r, ok
}

func (m *Manager) List() []Summary {
	m.mu.RLock()
	rooms := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.mu.RUnlock()
	out := make([]Summary, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, r.summary())
	}
	slices.SortFunc(out, func(a, b Summary) int { return strings.Compare(a.Settings.Name, b.Settings.Name) })
	return out
}

func (m *Manager) Create(name string) (*Room, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > 40 {
		return nil, ErrNameInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id := newRoomID()
	for m.rooms[id] != nil {
		id = newRoomID()
	}
	s := Settings{Name: name, Mode: ModeFIFO}
	if _, err := m.db.Exec(`INSERT INTO rooms(id, name, queue_mode, created_at) VALUES(?,?,?,?)`, id, name, s.Mode, now()); err != nil {
		log.Printf("create room: %v", err)
		return nil, ErrInternal
	}
	r := newRoom(m, id, s)
	m.rooms[id] = r
	return r, nil
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	r, ok := m.rooms[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if _, err := m.db.Exec(`DELETE FROM rooms WHERE id=?`, id); err != nil {
		m.mu.Unlock()
		log.Printf("delete room: %v", err)
		return ErrInternal
	}
	delete(m.rooms, id)
	m.mu.Unlock()

	r.mu.Lock()
	r.deleted = true
	for c := range r.clients {
		c.pushAndClose(message("deleted"))
	}
	r.clients = map[*Client]struct{}{}
	r.players = nil
	r.mu.Unlock()
	return nil
}

// HistoryEntry is a finished song for the admin history list.
type HistoryEntry struct {
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Nickname  string `json:"nickname"`
	Status    string `json:"status"`
	StartedAt int64  `json:"startedAt"`
}

func (m *Manager) History(roomID string, limit int) ([]HistoryEntry, error) {
	rows, err := m.db.Query(`SELECT s.title, s.artist, COALESCE(rm.nickname, ''), q.status, q.started_at
		FROM queue_items q
		JOIN songs s ON s.id = q.song_id
		LEFT JOIN room_members rm ON rm.room_id = q.room_id AND rm.user_id = q.user_id
		WHERE q.room_id = ? AND q.status IN ('done', 'skipped', 'failed')
		ORDER BY q.started_at DESC LIMIT ?`, roomID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.Title, &h.Artist, &h.Nickname, &h.Status, &h.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (m *Manager) RunIdleSweeper(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.mu.RLock()
			rooms := make([]*Room, 0, len(m.rooms))
			for _, r := range m.rooms {
				rooms = append(rooms, r)
			}
			m.mu.RUnlock()
			for _, r := range rooms {
				r.sweepIdle()
			}
		}
	}
}

func (m *Manager) songInfo(id int64) (title, artist string, err error) {
	err = m.db.QueryRow(`SELECT title, artist FROM songs WHERE id=? AND present=1`, id).Scan(&title, &artist)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrSongNotFound
	}
	if err != nil {
		log.Printf("song lookup: %v", err)
		return "", "", ErrInternal
	}
	return title, artist, nil
}

const roomIDAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

func newRoomID() string {
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = roomIDAlphabet[int(b[i])%len(roomIDAlphabet)]
	}
	return string(b)
}
