package room

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"zkaraver/internal/db"
)

func setup(t *testing.T) (*sql.DB, *Manager) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for i := 1; i <= 20; i++ {
		if _, err := d.Exec(`INSERT INTO songs(id, path, title, artist, search, size, mtime) VALUES(?,?,?,?,?,0,0)`,
			i, fmt.Sprintf("s%d.mp4", i), fmt.Sprintf("song%d", i), "artist", "x"); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []string{"a", "b", "c"} {
		if _, err := d.Exec(`INSERT INTO users(id, token_hash, created_at) VALUES(?,?,0)`, u, "h"+u); err != nil {
			t.Fatal(err)
		}
	}
	m, err := NewManager(d, dbSongs{d})
	if err != nil {
		t.Fatal(err)
	}
	return d, m
}

func newTestRoom(t *testing.T, m *Manager, mode string) *Room {
	t.Helper()
	r, err := m.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"a", "b", "c"} {
		if err := r.Join(u, "nick-"+u); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.UpdateSettings(Settings{Name: "test", Mode: mode}); err != nil {
		t.Fatal(err)
	}
	return r
}

func order(r *Room) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, it := range r.orderedLocked() {
		out = append(out, fmt.Sprintf("%s%d", it.UserID, it.SongID))
	}
	return out
}

func current(r *Room) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return ""
	}
	return fmt.Sprintf("%s%d", r.current.UserID, r.current.SongID)
}

func mustEnqueue(t *testing.T, r *Room, user string, song int64) {
	t.Helper()
	if err := r.Enqueue(user, song); err != nil {
		t.Fatalf("enqueue %s %d: %v", user, song, err)
	}
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFIFO(t *testing.T) {
	_, m := setup(t)
	r := newTestRoom(t, m, ModeFIFO)
	mustEnqueue(t, r, "a", 1) // starts playing immediately
	mustEnqueue(t, r, "a", 2)
	mustEnqueue(t, r, "a", 3)
	mustEnqueue(t, r, "b", 4)
	if current(r) != "a1" {
		t.Fatalf("current = %s", current(r))
	}
	eq(t, order(r), "a2", "a3", "b4")
}

func TestRoundRobin(t *testing.T) {
	_, m := setup(t)
	r := newTestRoom(t, m, ModeRoundRobin)
	mustEnqueue(t, r, "a", 1) // a sings first
	mustEnqueue(t, r, "a", 2)
	mustEnqueue(t, r, "a", 3)
	mustEnqueue(t, r, "b", 4)
	mustEnqueue(t, r, "b", 5)
	mustEnqueue(t, r, "c", 6)
	// a just sang, so b and c (never sang) go first in each round.
	eq(t, order(r), "b4", "c6", "a2", "b5", "a3")

	finishCurrent(t, r)
	if current(r) != "b4" {
		t.Fatalf("current = %s", current(r))
	}
	eq(t, order(r), "c6", "a2", "b5", "a3")

	// Whoever sang longest ago goes next.
	finishCurrent(t, r) // c6 starts
	eq(t, order(r), "a2", "b5", "a3")
}

func TestModeSwitchKeepsOrder(t *testing.T) {
	_, m := setup(t)
	r := newTestRoom(t, m, ModeRoundRobin)
	mustEnqueue(t, r, "a", 1)
	mustEnqueue(t, r, "a", 2)
	mustEnqueue(t, r, "a", 3)
	mustEnqueue(t, r, "b", 4)
	before := order(r)
	if err := r.UpdateSettings(Settings{Name: "test", Mode: ModeFIFO}); err != nil {
		t.Fatal(err)
	}
	eq(t, order(r), before...)
}

func TestMoveRequiresFIFO(t *testing.T) {
	_, m := setup(t)
	r := newTestRoom(t, m, ModeRoundRobin)
	mustEnqueue(t, r, "a", 1)
	mustEnqueue(t, r, "a", 2)
	if err := r.Move(r.queue[0].ID, 0); !errors.Is(err, ErrReorderNeedsFIFO) {
		t.Fatalf("err = %v", err)
	}
	r.UpdateSettings(Settings{Name: "test", Mode: ModeFIFO})
	mustEnqueue(t, r, "b", 3)
	mustEnqueue(t, r, "c", 4)
	eq(t, order(r), "a2", "b3", "c4")
	var c4 int64
	for _, it := range r.queue {
		if it.SongID == 4 {
			c4 = it.ID
		}
	}
	if err := r.Move(c4, 0); err != nil {
		t.Fatal(err)
	}
	eq(t, order(r), "c4", "a2", "b3")
}

func TestRules(t *testing.T) {
	_, m := setup(t)
	r := newTestRoom(t, m, ModeFIFO)
	mustEnqueue(t, r, "a", 1)
	if err := r.Enqueue("b", 1); !errors.Is(err, ErrDuplicateSong) {
		t.Fatalf("duplicate of current: %v", err)
	}
	mustEnqueue(t, r, "a", 2)
	if err := r.Enqueue("b", 2); !errors.Is(err, ErrDuplicateSong) {
		t.Fatalf("duplicate in queue: %v", err)
	}
	if err := r.Enqueue("zzz", 3); !errors.Is(err, ErrNotMember) {
		t.Fatalf("non-member: %v", err)
	}
	if err := r.Enqueue("a", 999); !errors.Is(err, ErrSongNotFound) {
		t.Fatalf("missing song: %v", err)
	}

	r.UpdateSettings(Settings{Name: "test", Mode: ModeFIFO, MaxPerUser: 2})
	mustEnqueue(t, r, "a", 3)
	if err := r.Enqueue("a", 4); !errors.Is(err, ErrQueueLimit) {
		t.Fatalf("limit: %v", err)
	}

	if err := r.Join("b", "NICK-A"); !errors.Is(err, ErrNicknameTaken) {
		t.Fatalf("nickname: %v", err)
	}

	// Only the requester or an admin may remove, and only the requester or admin may control.
	id := r.queue[0].ID
	if err := r.Remove(id, "b", false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("remove other: %v", err)
	}
	if err := r.Control("b", false, "pause", 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("control other: %v", err)
	}
	if err := r.Control("a", false, "pause", 0); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove(id, "a", false); err != nil {
		t.Fatal(err)
	}

	// Anyone may skip, but only the song that is actually playing.
	cur := r.current.ID
	if err := r.Skip(cur+1000, "b", false); !errors.Is(err, ErrNotCurrent) {
		t.Fatalf("skip stale: %v", err)
	}
	if err := r.Skip(cur, "b", false); err != nil {
		t.Fatal(err)
	}
	if err := r.Skip(cur, "c", false); !errors.Is(err, ErrNotCurrent) {
		t.Fatalf("double skip: %v", err)
	}
}

func TestKickAndReload(t *testing.T) {
	d, m := setup(t)
	r := newTestRoom(t, m, ModeRoundRobin)
	mustEnqueue(t, r, "a", 1)
	mustEnqueue(t, r, "b", 2)
	mustEnqueue(t, r, "c", 3)
	mustEnqueue(t, r, "c", 4)

	c := NewClient(KindMember, "c")
	if err := r.Attach(c); err != nil {
		t.Fatal(err)
	}
	if err := r.Kick("c"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done:
	default:
		t.Fatal("kicked client not closed")
	}
	eq(t, order(r), "b2")
	if err := r.Enqueue("c", 5); !errors.Is(err, ErrBanned) {
		t.Fatalf("banned enqueue: %v", err)
	}
	if err := r.Attach(NewClient(KindMember, "c")); !errors.Is(err, ErrBanned) {
		t.Fatalf("banned attach: %v", err)
	}

	// State survives a restart.
	m2, err := NewManager(d, dbSongs{d})
	if err != nil {
		t.Fatal(err)
	}
	r2, ok := m2.Get(r.ID)
	if !ok {
		t.Fatal("room not reloaded")
	}
	if current(r2) != "a1" {
		t.Fatalf("reloaded current = %s", current(r2))
	}
	eq(t, order(r2), "b2")
	if !r2.members["c"].Banned {
		t.Fatal("ban not persisted")
	}
	mustEnqueue(t, r2, "a", 6)
	eq(t, order(r2), "b2", "a6")
}

// finishCurrent reports the current song as ended from the active player,
// connecting a player first if the room has none.
func finishCurrent(t *testing.T, r *Room) {
	t.Helper()
	if r.activePlayer() == nil {
		if err := r.Attach(NewPlayerClient("", "")); err != nil {
			t.Fatal(err)
		}
	}
	r.mu.Lock()
	secret, id := r.players[0].PlayerSecret, r.current.ID
	r.mu.Unlock()
	if err := r.PlayerEnded(secret, id, false); err != nil {
		t.Fatal(err)
	}
}

func TestPlayerLine(t *testing.T) {
	_, m := setup(t)
	r := newTestRoom(t, m, ModeFIFO)
	p1 := NewPlayerClient("1.1.1.1", "")
	p2 := NewPlayerClient("2.2.2.2", "")
	p3 := NewPlayerClient("3.3.3.3", "")
	for _, p := range []*Client{p1, p2, p3} {
		if err := r.Attach(p); err != nil {
			t.Fatal(err)
		}
	}
	if r.activePlayer() != p1 {
		t.Fatal("first player should be active")
	}

	// Only the active player may report the end of a song.
	mustEnqueue(t, r, "a", 1)
	mustEnqueue(t, r, "a", 2)
	cur := r.current.ID
	if err := r.PlayerEnded(p2.PlayerSecret, cur, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("waiting player ended: %v", err)
	}
	if err := r.PlayerEnded("", cur, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("empty secret ended: %v", err)
	}
	if current(r) != "a1" {
		t.Fatalf("current changed to %s", current(r))
	}

	// Active player leaves: the next one in line takes over.
	r.Detach(p1)
	if r.activePlayer() != p2 {
		t.Fatal("p2 should take over")
	}
	if err := r.PlayerEnded(p2.PlayerSecret, cur, false); err != nil {
		t.Fatal(err)
	}
	if current(r) != "a2" {
		t.Fatalf("current = %s", current(r))
	}

	// Admin kicks a waiting player, then the active one.
	if err := r.KickPlayer(p3.PlayerID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p3.Done:
	default:
		t.Fatal("kicked player not closed")
	}
	if r.activePlayer() != p2 || len(r.players) != 1 {
		t.Fatal("kicking a waiting player changed the active one")
	}
	if err := r.KickPlayer(p2.PlayerID); err != nil {
		t.Fatal(err)
	}
	if r.activePlayer() != nil {
		t.Fatal("no player should remain")
	}
	if err := r.KickPlayer(p2.PlayerID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double kick: %v", err)
	}
	r.Detach(p2) // late detach after kick is a no-op
}

func TestPlayerSnapshotSelf(t *testing.T) {
	_, m := setup(t)
	r := newTestRoom(t, m, ModeFIFO)
	p1 := NewPlayerClient("", "")
	p2 := NewPlayerClient("", "")
	r.Attach(p1)
	r.Attach(p2)
	for i, p := range []*Client{p1, p2} {
		var s struct {
			Self    *selfView          `json:"self"`
			Players []playerClientView `json:"players"`
		}
		if err := json.Unmarshal(<-p.Send, &s); err != nil {
			t.Fatal(err)
		}
		if s.Self == nil || s.Self.Position != i || s.Self.Secret != p.PlayerSecret {
			t.Fatalf("player %d self = %+v", i, s.Self)
		}
		if s.Players != nil {
			t.Fatal("player list leaked to a player connection")
		}
	}
}

func TestVocalToggle(t *testing.T) {
	d, m := setup(t)
	if _, err := d.Exec(`UPDATE songs SET original_path='s1_original.mp4' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	r := newTestRoom(t, m, ModeFIFO)
	if err := r.Control("a", false, "vocal", 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("nothing playing: %v", err)
	}
	mustEnqueue(t, r, "a", 1)
	mustEnqueue(t, r, "b", 2)
	if !r.current.HasOriginal {
		t.Fatal("HasOriginal not loaded")
	}
	if err := r.Control("b", false, "vocal", 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other member: %v", err)
	}
	if err := r.Control("a", false, "vocal", 1); err != nil || !r.vocal {
		t.Fatalf("vocal on: %v %v", err, r.vocal)
	}
	// The next song starts as backing track again, and has no original to switch to.
	finishCurrent(t, r)
	if r.vocal {
		t.Fatal("vocal not reset for the next song")
	}
	if err := r.Control("b", false, "vocal", 1); !errors.Is(err, ErrNoOriginal) {
		t.Fatalf("song without original: %v", err)
	}
}

// dbSongs reads songs straight from the test database (the real server uses
// the library package).
type dbSongs struct{ d *sql.DB }

func (s dbSongs) Lookup(id int64) (title, artist string, hasOriginal, ok bool) {
	err := s.d.QueryRow(`SELECT title, artist, original_path != '' FROM songs WHERE id=? AND present=1`, id).
		Scan(&title, &artist, &hasOriginal)
	return title, artist, hasOriginal, err == nil
}

func TestRoomNames(t *testing.T) {
	d, m := setup(t)
	for _, bad := range []string{"", "客廳", "living room", "a/b", "x?", "%41", strings.Repeat("a", 33)} {
		if _, err := m.Create(bad); !errors.Is(err, ErrNameInvalid) {
			t.Errorf("Create(%q) = %v, want ErrNameInvalid", bad, err)
		}
	}
	r, err := m.Create("Living-Room_2")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "Living-Room_2" || r.settings.Name != "Living-Room_2" {
		t.Fatalf("id/name = %q/%q", r.ID, r.settings.Name)
	}
	if _, err := m.Create("living-room_2"); !errors.Is(err, ErrRoomExists) {
		t.Fatalf("case-insensitive duplicate: %v", err)
	}
	if got, ok := m.Get("LIVING-ROOM_2"); !ok || got != r {
		t.Fatal("lookup is not case-insensitive")
	}
	// The name cannot be changed through settings.
	if err := r.UpdateSettings(Settings{Name: "other", Mode: ModeRoundRobin}); err != nil {
		t.Fatal(err)
	}
	if r.settings.Name != "Living-Room_2" || r.settings.Mode != ModeRoundRobin {
		t.Fatalf("settings = %+v", r.settings)
	}
	// Survives a restart and can be deleted by any casing.
	m2, err := NewManager(d, dbSongs{d})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := m2.Get("living-room_2"); !ok || got.ID != "Living-Room_2" {
		t.Fatal("room not reloaded by name")
	}
	if err := m2.Delete("LIVING-room_2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m2.Get("Living-Room_2"); ok {
		t.Fatal("room still present after delete")
	}
}

func TestVolumeWrittenOnFlush(t *testing.T) {
	d, m := setup(t)
	r := newTestRoom(t, m, ModeFIFO)
	mustEnqueue(t, r, "a", 1)
	if err := r.Control("a", false, "volume", 40); err != nil {
		t.Fatal(err)
	}
	if err := r.Control("a", false, "qr", 0); err != nil {
		t.Fatal(err)
	}
	var vol int
	var qr bool
	d.QueryRow(`SELECT volume, show_qr FROM rooms WHERE id=?`, r.ID).Scan(&vol, &qr)
	if vol != 100 || !qr {
		t.Fatalf("written before flush: volume=%d qr=%v", vol, qr)
	}
	m.Flush()
	d.QueryRow(`SELECT volume, show_qr FROM rooms WHERE id=?`, r.ID).Scan(&vol, &qr)
	if vol != 40 || qr {
		t.Fatalf("after flush: volume=%d qr=%v", vol, qr)
	}
}

func idOf(r *Room, user string, song int64) int64 {
	for _, it := range r.queue {
		if it.UserID == user && it.SongID == song {
			return it.ID
		}
	}
	return -1
}

func TestReorderFIFO(t *testing.T) {
	_, m := setup(t)
	r := newTestRoom(t, m, ModeFIFO)
	mustEnqueue(t, r, "a", 1) // playing
	mustEnqueue(t, r, "a", 2)
	mustEnqueue(t, r, "b", 3)
	mustEnqueue(t, r, "c", 4)

	// Any member may move anyone's song.
	if err := r.Reorder(idOf(r, "c", 4), "up", "b", false); err != nil {
		t.Fatal(err)
	}
	eq(t, order(r), "a2", "c4", "b3")
	if err := r.Reorder(idOf(r, "b", 3), "top", "c", false); err != nil {
		t.Fatal(err)
	}
	eq(t, order(r), "b3", "a2", "c4")
	// Already first: no-op. Non-members and bad directions are rejected.
	if err := r.Reorder(idOf(r, "b", 3), "up", "a", false); err != nil {
		t.Fatal(err)
	}
	eq(t, order(r), "b3", "a2", "c4")
	if err := r.Reorder(idOf(r, "a", 2), "up", "zzz", false); !errors.Is(err, ErrNotMember) {
		t.Fatalf("stranger: %v", err)
	}
	if err := r.Reorder(idOf(r, "a", 2), "down", "a", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad direction: %v", err)
	}
	if err := r.Reorder(999, "up", "a", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing item: %v", err)
	}
}

func TestReorderRoundRobin(t *testing.T) {
	_, m := setup(t)
	r := newTestRoom(t, m, ModeRoundRobin)
	mustEnqueue(t, r, "c", 9) // c sings first, so a and b lead the rounds
	mustEnqueue(t, r, "a", 1)
	mustEnqueue(t, r, "a", 2)
	mustEnqueue(t, r, "a", 3)
	mustEnqueue(t, r, "b", 4)
	mustEnqueue(t, r, "b", 5)
	eq(t, order(r), "a1", "b4", "a2", "b5", "a3")

	// Only your own songs, and only among themselves: the rounds stay fair.
	if err := r.Reorder(idOf(r, "a", 3), "top", "b", false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other's song: %v", err)
	}
	if err := r.Reorder(idOf(r, "a", 3), "top", "a", false); err != nil {
		t.Fatal(err)
	}
	eq(t, order(r), "a3", "b4", "a1", "b5", "a2")
	if err := r.Reorder(idOf(r, "a", 2), "up", "a", false); err != nil {
		t.Fatal(err)
	}
	eq(t, order(r), "a3", "b4", "a2", "b5", "a1")
	// Admins may reorder anyone's songs, still within that person's set.
	if err := r.Reorder(idOf(r, "b", 5), "top", "", true); err != nil {
		t.Fatal(err)
	}
	eq(t, order(r), "a3", "b5", "a2", "b4", "a1")
}

func TestReplayAll(t *testing.T) {
	_, m := setup(t)
	r := newTestRoom(t, m, ModeFIFO)
	if _, err := r.ReplayAll("a", false); !errors.Is(err, ErrNoHistory) {
		t.Fatalf("empty history: %v", err)
	}
	mustEnqueue(t, r, "a", 1)
	mustEnqueue(t, r, "b", 2)
	mustEnqueue(t, r, "c", 3)
	mustEnqueue(t, r, "a", 4)
	if _, err := r.ReplayAll("a", false); !errors.Is(err, ErrQueueNotEmpty) {
		t.Fatalf("queue not empty: %v", err)
	}
	for range 3 {
		finishCurrent(t, r) // a1, b2, c3 done; a4 now playing
	}
	if err := r.Kick("c"); err != nil {
		t.Fatal(err)
	}
	if err := r.Kick("c"); err != nil { // idempotent enough for the test
		t.Fatal(err)
	}
	n, err := r.ReplayAll("b", false)
	if err != nil {
		t.Fatal(err)
	}
	// a1 and b2 come back under their original requesters; c3 (kicked) is
	// skipped, and a4 is skipped because it is playing right now.
	if n != 2 {
		t.Fatalf("queued %d, want 2", n)
	}
	eq(t, order(r), "a1", "b2")
	if current(r) != "a4" {
		t.Fatalf("current = %s", current(r))
	}

	// A song sung twice is queued once.
	finishCurrent(t, r) // a4 done, a1 (replayed) playing
	finishCurrent(t, r) // a1 done, b2 playing
	finishCurrent(t, r) // b2 done, nothing left
	n, err = r.ReplayAll("a", false)
	if err != nil || n != 3 {
		t.Fatalf("second replay: n=%d err=%v", n, err)
	}
	if current(r) != "a1" { // nothing was playing, so the first one starts
		t.Fatalf("current after replay = %s", current(r))
	}
	eq(t, order(r), "b2", "a4")
}

func TestClearHistory(t *testing.T) {
	d, m := setup(t)
	r := newTestRoom(t, m, ModeFIFO)
	if _, err := r.ClearHistory("a", false); !errors.Is(err, ErrNoHistory) {
		t.Fatalf("nothing to clear: %v", err)
	}
	mustEnqueue(t, r, "a", 1)
	mustEnqueue(t, r, "b", 2)
	mustEnqueue(t, r, "c", 3)
	finishCurrent(t, r) // a1 -> history, b2 playing, c3 waiting
	if _, err := r.ClearHistory("a", false); !errors.Is(err, ErrQueueNotEmpty) {
		t.Fatalf("queue not empty: %v", err)
	}
	finishCurrent(t, r) // b2 -> history, c3 playing, queue empty
	if _, err := r.ClearHistory("zzz", false); !errors.Is(err, ErrNotMember) {
		t.Fatalf("stranger: %v", err)
	}
	rev := r.historyRev
	n, err := r.ClearHistory("a", false)
	if err != nil || n != 2 {
		t.Fatalf("clear: n=%d err=%v", n, err)
	}
	if r.historyRev != rev+1 {
		t.Fatal("history revision not bumped")
	}
	// The playing song is untouched; history is empty.
	if current(r) != "c3" {
		t.Fatalf("current = %s", current(r))
	}
	h, _ := m.History(r.ID, 10, HistoryCursor{})
	if len(h) != 0 {
		t.Fatalf("history left: %v", h)
	}
	var left int
	d.QueryRow(`SELECT COUNT(*) FROM queue_items WHERE room_id=?`, r.ID).Scan(&left)
	if left != 1 {
		t.Fatalf("rows left = %d, want only the playing one", left)
	}
}

func TestRateAndSeek(t *testing.T) {
	_, m := setup(t)
	r := newTestRoom(t, m, ModeFIFO)
	if r.ratePct != 100 {
		t.Fatalf("default rate = %d", r.ratePct)
	}
	if err := r.Control("a", false, "seek", 3); !errors.Is(err, ErrForbidden) {
		t.Fatalf("seek with nothing playing: %v", err)
	}
	mustEnqueue(t, r, "a", 1)
	mustEnqueue(t, r, "b", 2)

	// Same permission as pause: the requester or an admin.
	if err := r.Control("b", false, "rate", 120); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other member: %v", err)
	}
	for _, c := range []struct{ in, want int }{{125, 125}, {10, 50}, {400, 150}} {
		if err := r.Control("a", false, "rate", c.in); err != nil || r.ratePct != c.want {
			t.Fatalf("rate %d -> %d (%v), want %d", c.in, r.ratePct, err, c.want)
		}
	}
	nonce := r.seekNonce
	if err := r.Control("", true, "seek", -3); err != nil || r.seekNonce != nonce+1 || r.seekDelta != -3 {
		t.Fatalf("seek back: %v nonce=%d delta=%d", err, r.seekNonce, r.seekDelta)
	}
	if err := r.Control("", true, "seek", 999); err != nil || r.seekDelta != 30 {
		t.Fatalf("seek clamp: %v delta=%d", err, r.seekDelta)
	}
	if err := r.Control("", true, "seek", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("seek 0: %v", err)
	}

	// The next song starts at normal speed.
	finishCurrent(t, r)
	if r.ratePct != 100 {
		t.Fatalf("rate not reset for the next song: %d", r.ratePct)
	}
}

func TestKeyShift(t *testing.T) {
	_, m := setup(t)
	r := newTestRoom(t, m, ModeFIFO)
	mustEnqueue(t, r, "a", 1)
	mustEnqueue(t, r, "b", 2)
	if err := r.Control("b", false, "key", 2); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other member: %v", err)
	}
	for _, c := range []struct{ in, want int }{{2, 2}, {-3, -3}, {9, 6}, {-20, -6}, {0, 0}} {
		if err := r.Control("a", false, "key", c.in); err != nil || r.keyShift != c.want {
			t.Fatalf("key %d -> %d (%v), want %d", c.in, r.keyShift, err, c.want)
		}
	}
	r.Control("a", false, "key", -2)
	finishCurrent(t, r)
	if r.keyShift != 0 {
		t.Fatalf("key not reset for the next song: %d", r.keyShift)
	}
}
