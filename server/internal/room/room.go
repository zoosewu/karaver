package room

import (
	"encoding/json"
	"log"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	ModeFIFO       = "fifo"
	ModeRoundRobin = "rr"
)

type Item struct {
	ID        int64
	SongID    int64
	Title     string
	Artist    string
	UserID    string
	Position  int64
	CreatedAt int64
	StartedAt int64
	// HasOriginal: an original-vocal companion file exists for this song.
	HasOriginal bool
}

type Member struct {
	UserID   string
	Nickname string
	Banned   bool
}

type Settings struct {
	Name             string `json:"name"`
	Mode             string `json:"mode"`
	MaxPerUser       int    `json:"maxPerUser"`
	IdleClearMinutes int    `json:"idleClearMinutes"`
}

// Room holds the live state of one room in memory; every change is written
// through to SQLite and then broadcast to subscribers as a full snapshot.
type Room struct {
	m  *Manager
	ID string

	mu           sync.Mutex
	settings     Settings
	showQR       bool
	volume       int
	paused       bool
	restartNonce int64
	vocal        bool // play the original-vocal audio over the karaoke video; reset per song
	current      *Item
	queue        []*Item // unordered; see orderedLocked
	members      map[string]*Member
	lastSung     map[string]int64 // userID -> started_at of their latest song
	nextPos      int64
	lastActivity time.Time
	clients      map[*Client]struct{}
	players      []*Client // connection order; players[0] is the active one
	deleted      bool
}

func newRoom(m *Manager, id string, s Settings) *Room {
	return &Room{
		m:            m,
		ID:           id,
		settings:     s,
		showQR:       true,
		volume:       100,
		members:      map[string]*Member{},
		lastSung:     map[string]int64{},
		lastActivity: time.Now(),
		clients:      map[*Client]struct{}{},
	}
}

func now() int64 { return time.Now().UnixMilli() }

func (r *Room) touch() { r.lastActivity = time.Now() }

func (r *Room) exec(query string, args ...any) error {
	if _, err := r.m.db.Exec(query, args...); err != nil {
		log.Printf("room %s: db: %v", r.ID, err)
		return ErrInternal
	}
	return nil
}

// orderedLocked returns the queue in play order.
//
// FIFO: by position. Round-robin: one song per person per round; within a round,
// people who sang least recently go first (never sang = first, ties by who queued first).
func (r *Room) orderedLocked() []*Item {
	items := slices.Clone(r.queue)
	slices.SortFunc(items, func(a, b *Item) int { return int(a.Position - b.Position) })
	if r.settings.Mode != ModeRoundRobin {
		return items
	}
	byUser := map[string][]*Item{}
	var users []string
	for _, it := range items {
		if _, ok := byUser[it.UserID]; !ok {
			users = append(users, it.UserID)
		}
		byUser[it.UserID] = append(byUser[it.UserID], it)
	}
	slices.SortStableFunc(users, func(a, b string) int {
		la, lb := r.lastSung[a], r.lastSung[b]
		if la != lb {
			if la < lb {
				return -1
			}
			return 1
		}
		return int(byUser[a][0].Position - byUser[b][0].Position)
	})
	out := make([]*Item, 0, len(items))
	for round := 0; len(out) < len(items); round++ {
		for _, u := range users {
			if round < len(byUser[u]) {
				out = append(out, byUser[u][round])
			}
		}
	}
	return out
}

// renumberLocked persists the given order as positions 0..n-1.
func (r *Room) renumberLocked(order []*Item) error {
	tx, err := r.m.db.Begin()
	if err != nil {
		log.Printf("room %s: db: %v", r.ID, err)
		return ErrInternal
	}
	defer tx.Rollback()
	for i, it := range order {
		if _, err := tx.Exec(`UPDATE queue_items SET position=? WHERE id=?`, i, it.ID); err != nil {
			log.Printf("room %s: db: %v", r.ID, err)
			return ErrInternal
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("room %s: db: %v", r.ID, err)
		return ErrInternal
	}
	for i, it := range order {
		it.Position = int64(i)
	}
	return nil
}

func (r *Room) finishLocked(status string) {
	if r.current == nil {
		return
	}
	_ = r.exec(`UPDATE queue_items SET status=?, ended_at=? WHERE id=?`, status, now(), r.current.ID)
	r.current = nil
	r.paused = false
	r.vocal = false
}

func (r *Room) advanceLocked() {
	if r.current != nil {
		return
	}
	order := r.orderedLocked()
	if len(order) == 0 {
		return
	}
	next := order[0]
	t := now()
	if err := r.exec(`UPDATE queue_items SET status='playing', started_at=? WHERE id=?`, t, next.ID); err != nil {
		return
	}
	r.queue = slices.DeleteFunc(r.queue, func(it *Item) bool { return it == next })
	next.StartedAt = t
	r.lastSung[next.UserID] = t
	r.current = next
	r.paused = false
	r.vocal = false
}

// ---- subscribers ----

func (r *Room) Attach(c *Client) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleted {
		return ErrNotFound
	}
	if c.Kind == KindMember {
		mem, ok := r.members[c.UserID]
		if !ok {
			return ErrNotMember
		}
		if mem.Banned {
			return ErrBanned
		}
	}
	if c.Kind == KindPlayer {
		r.players = append(r.players, c)
	}
	r.clients[c] = struct{}{}
	r.broadcastLocked()
	return nil
}

func (r *Room) Detach(c *Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.clients[c]; !ok {
		return
	}
	r.dropClientLocked(c)
	r.broadcastLocked()
}

func (r *Room) dropClientLocked(c *Client) {
	delete(r.clients, c)
	if c.Kind == KindPlayer {
		// The next waiting player (if any) becomes active automatically.
		r.players = slices.DeleteFunc(r.players, func(p *Client) bool { return p == c })
	}
}

// activePlayer is the first connected player; the rest wait in line.
func (r *Room) activePlayer() *Client {
	if len(r.players) == 0 {
		return nil
	}
	return r.players[0]
}

// KickPlayer disconnects a player, active or waiting.
func (r *Room) KickPlayer(playerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := slices.IndexFunc(r.players, func(p *Client) bool { return p.PlayerID == playerID })
	if i < 0 {
		return ErrNotFound
	}
	p := r.players[i]
	p.pushAndClose(message("kicked"))
	r.dropClientLocked(p)
	r.broadcastLocked()
	return nil
}

// ---- member actions ----

func (r *Room) Join(userID, nickname string) error {
	nickname = strings.TrimSpace(nickname)
	if n := utf8.RuneCountInString(nickname); n == 0 || n > 20 {
		return ErrNicknameInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleted {
		return ErrNotFound
	}
	mem, ok := r.members[userID]
	if ok && mem.Banned {
		return ErrBanned
	}
	if ok && mem.Nickname == nickname {
		return nil
	}
	for _, other := range r.members {
		if other.UserID != userID && strings.EqualFold(other.Nickname, nickname) {
			return ErrNicknameTaken
		}
	}
	if err := r.exec(`INSERT INTO room_members(room_id, user_id, nickname, joined_at) VALUES(?,?,?,?)
		ON CONFLICT(room_id, user_id) DO UPDATE SET nickname=excluded.nickname`, r.ID, userID, nickname, now()); err != nil {
		return err
	}
	if ok {
		mem.Nickname = nickname
	} else {
		r.members[userID] = &Member{UserID: userID, Nickname: nickname}
	}
	r.broadcastLocked()
	return nil
}

// Nickname returns the member's nickname in this room, which also identifies
// their favorites across rooms and devices.
func (r *Room) Nickname(userID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkMemberLocked(userID); err != nil {
		return "", err
	}
	return r.members[userID].Nickname, nil
}

func (r *Room) checkMemberLocked(userID string) error {
	mem, ok := r.members[userID]
	if !ok {
		return ErrNotMember
	}
	if mem.Banned {
		return ErrBanned
	}
	return nil
}

func (r *Room) Enqueue(userID string, songID int64) error {
	song, err := r.m.songInfo(songID)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleted {
		return ErrNotFound
	}
	if err := r.checkMemberLocked(userID); err != nil {
		return err
	}
	if r.current != nil && r.current.SongID == songID {
		return ErrDuplicateSong
	}
	mine := 0
	for _, it := range r.queue {
		if it.SongID == songID {
			return ErrDuplicateSong
		}
		if it.UserID == userID {
			mine++
		}
	}
	if r.settings.MaxPerUser > 0 && mine >= r.settings.MaxPerUser {
		return ErrQueueLimit
	}
	it := &song
	it.UserID, it.Position, it.CreatedAt = userID, r.nextPos, now()
	res, err := r.m.db.Exec(`INSERT INTO queue_items(room_id, song_id, user_id, status, position, created_at) VALUES(?,?,?,'queued',?,?)`,
		r.ID, songID, userID, it.Position, it.CreatedAt)
	if err != nil {
		log.Printf("room %s: db: %v", r.ID, err)
		return ErrInternal
	}
	it.ID, _ = res.LastInsertId()
	r.nextPos++
	r.queue = append(r.queue, it)
	r.touch()
	r.advanceLocked()
	r.broadcastLocked()
	return nil
}

// Remove deletes a queued (not yet playing) item. Members may only remove their own.
func (r *Room) Remove(itemID int64, userID string, admin bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := slices.IndexFunc(r.queue, func(it *Item) bool { return it.ID == itemID })
	if idx < 0 {
		return ErrNotFound
	}
	if !admin && r.queue[idx].UserID != userID {
		return ErrForbidden
	}
	if err := r.exec(`DELETE FROM queue_items WHERE id=?`, itemID); err != nil {
		return err
	}
	r.queue = slices.Delete(r.queue, idx, idx+1)
	r.touch()
	r.broadcastLocked()
	return nil
}

// Skip ends the current song. itemID guards against two people skipping at once
// and accidentally skipping two songs.
func (r *Room) Skip(itemID int64, userID string, admin bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !admin {
		if err := r.checkMemberLocked(userID); err != nil {
			return err
		}
	}
	if r.current == nil || r.current.ID != itemID {
		return ErrNotCurrent
	}
	r.finishLocked("skipped")
	r.advanceLocked()
	r.touch()
	r.broadcastLocked()
	return nil
}

// PlayerEnded is reported by the active player when a video finishes or fails to play.
func (r *Room) PlayerEnded(playerSecret string, itemID int64, failed bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.activePlayer()
	if p == nil || playerSecret == "" || p.PlayerSecret != playerSecret {
		return ErrForbidden
	}
	if r.current == nil || r.current.ID != itemID {
		return nil
	}
	status := "done"
	if failed {
		status = "failed"
	}
	r.finishLocked(status)
	r.advanceLocked()
	r.touch()
	r.broadcastLocked()
	return nil
}

// Control changes playback. Allowed for admins and whoever requested the current song.
func (r *Room) Control(userID string, admin bool, action string, value int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !admin && (r.current == nil || r.current.UserID != userID) {
		return ErrForbidden
	}
	switch action {
	case "pause":
		r.paused = true
	case "play":
		r.paused = false
	case "restart":
		r.restartNonce++
		r.paused = false
	case "volume":
		r.volume = min(max(value, 0), 100)
		if err := r.exec(`UPDATE rooms SET volume=? WHERE id=?`, r.volume, r.ID); err != nil {
			return err
		}
	case "vocal":
		if r.current == nil || !r.current.HasOriginal {
			return ErrNoOriginal
		}
		r.vocal = value != 0
	case "qr":
		r.showQR = value != 0
		if err := r.exec(`UPDATE rooms SET show_qr=? WHERE id=?`, r.showQR, r.ID); err != nil {
			return err
		}
	default:
		return ErrInvalid
	}
	r.touch()
	r.broadcastLocked()
	return nil
}

// ---- admin actions ----

func (r *Room) UpdateSettings(s Settings) error {
	s.Name = strings.TrimSpace(s.Name)
	if n := utf8.RuneCountInString(s.Name); n == 0 || n > 40 {
		return ErrNameInvalid
	}
	if (s.Mode != ModeFIFO && s.Mode != ModeRoundRobin) || s.MaxPerUser < 0 || s.MaxPerUser > 99 ||
		s.IdleClearMinutes < 0 || s.IdleClearMinutes > 1440 {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settings.Mode == ModeRoundRobin && s.Mode == ModeFIFO {
		// Keep the order people currently see when switching back to FIFO.
		if err := r.renumberLocked(r.orderedLocked()); err != nil {
			return err
		}
	}
	if err := r.exec(`UPDATE rooms SET name=?, queue_mode=?, max_per_user=?, idle_clear_minutes=? WHERE id=?`,
		s.Name, s.Mode, s.MaxPerUser, s.IdleClearMinutes, r.ID); err != nil {
		return err
	}
	r.settings = s
	r.broadcastLocked()
	return nil
}

// Move places a queued item at index (0-based) of the play order. FIFO only.
func (r *Room) Move(itemID int64, index int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settings.Mode != ModeFIFO {
		return ErrReorderNeedsFIFO
	}
	order := r.orderedLocked()
	from := slices.IndexFunc(order, func(it *Item) bool { return it.ID == itemID })
	if from < 0 {
		return ErrNotFound
	}
	it := order[from]
	order = slices.Delete(order, from, from+1)
	index = min(max(index, 0), len(order))
	order = slices.Insert(order, index, it)
	if err := r.renumberLocked(order); err != nil {
		return err
	}
	r.broadcastLocked()
	return nil
}

// Kick bans a member from the room, drops their queued songs and disconnects them.
func (r *Room) Kick(userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	mem, ok := r.members[userID]
	if !ok {
		return ErrNotFound
	}
	if err := r.exec(`UPDATE room_members SET banned=1 WHERE room_id=? AND user_id=?`, r.ID, userID); err != nil {
		return err
	}
	mem.Banned = true
	if err := r.exec(`DELETE FROM queue_items WHERE room_id=? AND user_id=? AND status='queued'`, r.ID, userID); err != nil {
		return err
	}
	r.queue = slices.DeleteFunc(r.queue, func(it *Item) bool { return it.UserID == userID })
	if r.current != nil && r.current.UserID == userID {
		r.finishLocked("skipped")
		r.advanceLocked()
	}
	for c := range r.clients {
		if c.Kind == KindMember && c.UserID == userID {
			c.pushAndClose(message("kicked"))
			delete(r.clients, c)
		}
	}
	r.broadcastLocked()
	return nil
}

func (r *Room) Unban(userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	mem, ok := r.members[userID]
	if !ok {
		return ErrNotFound
	}
	if err := r.exec(`UPDATE room_members SET banned=0 WHERE room_id=? AND user_id=?`, r.ID, userID); err != nil {
		return err
	}
	mem.Banned = false
	r.broadcastLocked()
	return nil
}

// Clear removes every queued song; the current song keeps playing.
func (r *Room) Clear() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.clearQueueLocked(); err != nil {
		return err
	}
	r.broadcastLocked()
	return nil
}

func (r *Room) clearQueueLocked() error {
	if err := r.exec(`DELETE FROM queue_items WHERE room_id=? AND status='queued'`, r.ID); err != nil {
		return err
	}
	r.queue = nil
	return nil
}

// sweepIdle clears the room after IdleClearMinutes without activity, unless a
// song is still playing on a connected player.
func (r *Room) sweepIdle() {
	r.mu.Lock()
	defer r.mu.Unlock()
	mins := r.settings.IdleClearMinutes
	if mins <= 0 || r.deleted || time.Since(r.lastActivity) < time.Duration(mins)*time.Minute {
		return
	}
	if r.current == nil && len(r.queue) == 0 {
		return
	}
	if r.current != nil && r.activePlayer() != nil {
		return
	}
	r.finishLocked("skipped")
	if err := r.clearQueueLocked(); err != nil {
		return
	}
	log.Printf("room %s: cleared after %d idle minutes", r.ID, mins)
	r.broadcastLocked()
}

// ---- snapshots ----

type itemView struct {
	ID          int64  `json:"id"`
	SongID      int64  `json:"songId"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	UserID      string `json:"userId"`
	Nickname    string `json:"nickname"`
	HasOriginal bool   `json:"hasOriginal"`
}

type memberView struct {
	UserID   string `json:"userId"`
	Nickname string `json:"nickname"`
	Online   bool   `json:"online"`
	Banned   bool   `json:"banned,omitempty"`
}

type playerView struct {
	Online       bool  `json:"online"`
	Paused       bool  `json:"paused"`
	Volume       int   `json:"volume"`
	ShowQR       bool  `json:"showQR"`
	RestartNonce int64 `json:"restartNonce"`
	Vocal        bool  `json:"vocal"`
}

// playerClientView lists a player connection for admins.
type playerClientView struct {
	ID          string `json:"id"`
	Active      bool   `json:"active"`
	RemoteAddr  string `json:"remoteAddr"`
	UserAgent   string `json:"userAgent"`
	ConnectedAt int64  `json:"connectedAt"`
}

// selfView tells a player connection its own status. Position 0 = active,
// 1 = next in line, and so on.
type selfView struct {
	Secret   string `json:"secret"`
	Position int    `json:"position"`
}

type stateView struct {
	Type     string             `json:"type"`
	ID       string             `json:"id"`
	Settings Settings           `json:"settings"`
	Current  *itemView          `json:"current"`
	Queue    []itemView         `json:"queue"`
	Player   playerView         `json:"player"`
	Members  []memberView       `json:"members"`
	Players  []playerClientView `json:"players,omitempty"` // admins only
	Self     *selfView          `json:"self,omitempty"`    // player connections only
}

func message(typ string) []byte {
	b, _ := json.Marshal(map[string]string{"type": typ})
	return b
}

func (r *Room) viewItem(it *Item) itemView {
	v := itemView{ID: it.ID, SongID: it.SongID, Title: it.Title, Artist: it.Artist, UserID: it.UserID, HasOriginal: it.HasOriginal}
	if mem, ok := r.members[it.UserID]; ok {
		v.Nickname = mem.Nickname
	}
	return v
}

func (r *Room) viewLocked(admin bool) stateView {
	online := map[string]bool{}
	for c := range r.clients {
		if c.Kind == KindMember {
			online[c.UserID] = true
		}
	}
	s := stateView{
		Type:     "state",
		ID:       r.ID,
		Settings: r.settings,
		Queue:    []itemView{},
		Members:  []memberView{},
		Player: playerView{
			Online: len(r.players) > 0, Paused: r.paused, Volume: r.volume,
			ShowQR: r.showQR, RestartNonce: r.restartNonce, Vocal: r.vocal,
		},
	}
	if r.current != nil {
		v := r.viewItem(r.current)
		s.Current = &v
	}
	for _, it := range r.orderedLocked() {
		s.Queue = append(s.Queue, r.viewItem(it))
	}
	for _, mem := range r.members {
		if mem.Banned && !admin {
			continue
		}
		s.Members = append(s.Members, memberView{UserID: mem.UserID, Nickname: mem.Nickname, Online: online[mem.UserID], Banned: mem.Banned})
	}
	slices.SortFunc(s.Members, func(a, b memberView) int { return strings.Compare(a.Nickname, b.Nickname) })
	if admin {
		s.Players = []playerClientView{}
		for i, p := range r.players {
			s.Players = append(s.Players, playerClientView{
				ID: p.PlayerID, Active: i == 0, RemoteAddr: p.RemoteAddr, UserAgent: p.UserAgent, ConnectedAt: p.ConnectedAt,
			})
		}
	}
	return s
}

func (r *Room) broadcastLocked() {
	if len(r.clients) == 0 {
		return
	}
	var pub, adm []byte
	var pubView *stateView
	for c := range r.clients {
		switch c.Kind {
		case KindAdmin:
			if adm == nil {
				adm, _ = json.Marshal(r.viewLocked(true))
			}
			c.push(adm)
		case KindPlayer:
			if pubView == nil {
				v := r.viewLocked(false)
				pubView = &v
			}
			v := *pubView
			v.Self = &selfView{Secret: c.PlayerSecret, Position: slices.Index(r.players, c)}
			b, _ := json.Marshal(v)
			c.push(b)
		default:
			if pub == nil {
				pub, _ = json.Marshal(r.viewLocked(false))
			}
			c.push(pub)
		}
	}
}

// Summary is shown in the admin room list.
type Summary struct {
	ID           string   `json:"id"`
	Settings     Settings `json:"settings"`
	QueueLength  int      `json:"queueLength"`
	Playing      string   `json:"playing,omitempty"`
	PlayerOnline bool     `json:"playerOnline"`
	Online       int      `json:"online"`
}

func (r *Room) summary() Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Summary{ID: r.ID, Settings: r.settings, QueueLength: len(r.queue), PlayerOnline: len(r.players) > 0}
	if r.current != nil {
		s.Playing = r.current.Title
	}
	users := map[string]bool{}
	for c := range r.clients {
		if c.Kind == KindMember {
			users[c.UserID] = true
		}
	}
	s.Online = len(users)
	return s
}
