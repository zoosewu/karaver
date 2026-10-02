package room

import (
	"context"
	"database/sql"
	"log"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// SongSource resolves song ids for queueing (the library).
type SongSource interface {
	Lookup(id int64) (title, artist string, hasOriginal, ok bool)
}

type Manager struct {
	db    *sql.DB
	songs SongSource
	mu    sync.RWMutex
	rooms map[string]*Room // keyed by roomKey(id)
}

// Room ids are their names: 1-32 of [A-Za-z0-9_-], case-insensitive.
var roomIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

func roomKey(id string) string { return strings.ToLower(id) }

// NewManager loads all rooms, members and pending queue items into memory.
func NewManager(d *sql.DB, songs SongSource) (*Manager, error) {
	m := &Manager{db: d, songs: songs, rooms: map[string]*Room{}}

	rows, err := d.Query(`SELECT id, queue_mode, max_per_user, idle_clear_minutes, show_qr, volume FROM rooms`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var s Settings
		var showQR bool
		var volume int
		if err := rows.Scan(&id, &s.Mode, &s.MaxPerUser, &s.IdleClearMinutes, &showQR, &volume); err != nil {
			rows.Close()
			return nil, err
		}
		s.Name = id
		r := newRoom(m, id, s)
		r.showQR, r.volume = showQR, volume
		m.rooms[roomKey(id)] = r
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
		if r := m.rooms[roomKey(roomID)]; r != nil {
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
		if r := m.rooms[roomKey(roomID)]; r != nil {
			r.lastSung[userID] = t
		}
	}
	rows.Close()

	rows, err = d.Query(`SELECT q.id, q.room_id, q.song_id, q.user_id, q.status, q.position, q.created_at, COALESCE(q.started_at, 0), s.title, s.artist, s.original_path != ''
		FROM queue_items q JOIN songs s ON s.id = q.song_id
		WHERE q.status IN ('queued', 'playing')`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var roomID, status string
		it := &Item{}
		if err := rows.Scan(&it.ID, &roomID, &it.SongID, &it.UserID, &status, &it.Position, &it.CreatedAt, &it.StartedAt, &it.Title, &it.Artist, &it.HasOriginal); err != nil {
			rows.Close()
			return nil, err
		}
		r := m.rooms[roomKey(roomID)]
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
	r, ok := m.rooms[roomKey(id)]
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

// Create makes a room whose name is also its id and URL. Names cannot change later.
func (m *Manager) Create(name string) (*Room, error) {
	id := strings.TrimSpace(name)
	if !roomIDPattern.MatchString(id) {
		return nil, ErrNameInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rooms[roomKey(id)] != nil {
		return nil, ErrRoomExists
	}
	s := Settings{Name: id, Mode: ModeFIFO}
	if _, err := m.db.Exec(`INSERT INTO rooms(id, name, queue_mode, created_at) VALUES(?,?,?,?)`, id, id, s.Mode, now()); err != nil {
		log.Printf("create room: %v", err)
		return nil, ErrInternal
	}
	r := newRoom(m, id, s)
	m.rooms[roomKey(id)] = r
	return r, nil
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	r, ok := m.rooms[roomKey(id)]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if _, err := m.db.Exec(`DELETE FROM rooms WHERE id=?`, r.ID); err != nil {
		m.mu.Unlock()
		log.Printf("delete room: %v", err)
		return ErrInternal
	}
	delete(m.rooms, roomKey(id))
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

func (m *Manager) songInfo(id int64) (Item, error) {
	title, artist, hasOriginal, ok := m.songs.Lookup(id)
	if !ok {
		return Item{}, ErrSongNotFound
	}
	return Item{SongID: id, Title: title, Artist: artist, HasOriginal: hasOriginal}, nil
}

// Flush writes player settings that are kept in memory while running (volume,
// QR overlay) for rooms that changed. Called on shutdown.
func (m *Manager) Flush() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.rooms {
		r.flush()
	}
}
