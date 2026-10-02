package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"zkaraver/internal/config"
	"zkaraver/internal/db"
	"zkaraver/internal/library"
	"zkaraver/internal/room"
)

type env struct {
	t   *testing.T
	srv *httptest.Server
	lib *library.Library
}

func setup(t *testing.T) *env {
	t.Helper()
	media := t.TempDir()
	for name, body := range map[string]string{
		"A - one.mp4":          "one",
		"A - one_original.mp4": "one-original",
		"B - two.mp4":          "two",
	} {
		if err := os.WriteFile(filepath.Join(media, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{
		PublicURL: "http://ktv.test", AdminPassword: "secret", MediaDir: media,
		FilenameFormat: "artist-title", FilenameSeparator: " - ", OriginalSuffix: "_original",
		Extensions: []string{".mp4"},
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	lib := library.New(d, cfg)
	lib.StartScan()
	for deadline := time.Now().Add(5 * time.Second); ; {
		if scanning, last := lib.Status(); !scanning && last != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}

	rooms, err := room.NewManager(d, lib)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, d, lib, rooms, fstest.MapFS{"index.html": {Data: []byte("<html>spa</html>")}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, lib: lib}
}

// client is either an anonymous user (token) or an admin (cookie jar).
type client struct {
	e      *env
	http   *http.Client
	token  string
	userID string
}

func (e *env) user() *client {
	c := &client{e: e, http: http.DefaultClient}
	var s struct{ UserID, Token string }
	c.do("POST", "/api/session", map[string]string{}, 200, &s)
	c.token, c.userID = s.Token, s.UserID
	return c
}

func (e *env) admin() *client {
	jar, _ := cookiejar.New(nil)
	c := &client{e: e, http: &http.Client{Jar: jar}}
	c.do("POST", "/api/admin/login", map[string]string{"password": "secret"}, 204, nil)
	return c
}

func (e *env) anon() *client { return &client{e: e, http: http.DefaultClient} }

// do sends a request and fails the test unless the status matches.
func (c *client) do(method, path string, body any, want int, out any) string {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		c.e.t.Fatalf("%s %s = %d %s, want %d", method, path, resp.StatusCode, raw, want)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.e.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return string(raw)
}

func (e *env) songID(q string) int64 {
	songs, _, err := e.lib.Search(q, 1, 0)
	if err != nil || len(songs) == 0 {
		e.t.Fatalf("song %q not found", q)
	}
	return songs[0].ID
}

var roomSeq int

func (e *env) newRoom(adm *client) string {
	roomSeq++
	var r struct{ ID string }
	adm.do("POST", "/api/admin/rooms", map[string]string{"name": fmt.Sprintf("room-%d", roomSeq)}, 201, &r)
	return r.ID
}

func TestSessionReuse(t *testing.T) {
	e := setup(t)
	u := e.user()
	var again struct{ UserID, Token string }
	u.do("POST", "/api/session", map[string]string{"token": u.token}, 200, &again)
	if again.UserID != u.userID || again.Token != u.token {
		t.Fatal("valid token was not reused")
	}
	var fresh struct{ UserID string }
	u.do("POST", "/api/session", map[string]string{"token": "bogus"}, 200, &fresh)
	if fresh.UserID == u.userID {
		t.Fatal("bogus token reused an identity")
	}
}

func TestAdminAuth(t *testing.T) {
	e := setup(t)
	anon := e.anon()
	anon.do("GET", "/api/admin/rooms", nil, 401, nil)
	anon.do("POST", "/api/admin/rooms", map[string]string{"name": "x"}, 401, nil)
	anon.do("POST", "/api/admin/library/scan", nil, 401, nil)
	anon.do("POST", "/api/admin/login", map[string]string{"password": "nope"}, 401, nil)

	adm := e.admin()
	var me struct{ Admin bool }
	adm.do("GET", "/api/admin/me", nil, 200, &me)
	if !me.Admin {
		t.Fatal("login did not stick")
	}
	adm.do("POST", "/api/admin/logout", nil, 204, nil)
	adm.do("GET", "/api/admin/rooms", nil, 401, nil)
}

func TestMemberPermissions(t *testing.T) {
	e := setup(t)
	adm := e.admin()
	id := e.newRoom(adm)
	base := "/api/rooms/" + id
	one, two := e.songID("one"), e.songID("two")

	alice, bob, stranger := e.user(), e.user(), e.user()
	alice.do("POST", base+"/join", map[string]string{"nickname": "Alice"}, 204, nil)
	bob.do("POST", base+"/join", map[string]string{"nickname": "alice"}, 409, nil) // taken, case-insensitive
	bob.do("POST", base+"/join", map[string]string{"nickname": "Bob"}, 204, nil)
	e.anon().do("POST", base+"/join", map[string]string{"nickname": "x"}, 401, nil)

	stranger.do("POST", base+"/queue", map[string]int64{"songId": one}, 403, nil)
	alice.do("POST", base+"/queue", map[string]int64{"songId": one}, 204, nil) // starts playing
	bob.do("POST", base+"/queue", map[string]int64{"songId": one}, 409, nil)   // duplicate
	bob.do("POST", base+"/queue", map[string]int64{"songId": two}, 204, nil)

	// The admin room list reflects the queue.
	var rooms []room.Summary
	adm.do("GET", "/api/admin/rooms", nil, 200, &rooms)
	if rooms[0].QueueLength != 1 || rooms[0].Playing != "one" {
		t.Fatalf("room summary = %+v", rooms[0])
	}

	// Controls: only the requester (Alice) or an admin.
	bob.do("POST", base+"/control", map[string]any{"action": "pause"}, 403, nil)
	alice.do("POST", base+"/control", map[string]any{"action": "pause"}, 204, nil)
	adm.do("POST", base+"/control", map[string]any{"action": "play"}, 204, nil)
	alice.do("POST", base+"/control", map[string]any{"action": "bogus"}, 400, nil)

	// Original/backing toggle needs a song that has an original file ("one" does).
	alice.do("POST", base+"/control", map[string]any{"action": "vocal", "value": 1}, 204, nil)

	// The player endpoint needs the active player's secret.
	e.anon().do("POST", base+"/player/ended", map[string]any{"itemId": 1}, 403, nil)

	// Skip: any member, but not strangers; stale item ids are rejected.
	stranger.do("POST", base+"/skip", map[string]int64{"itemId": 1}, 403, nil)
	bob.do("POST", base+"/skip", map[string]int64{"itemId": 999}, 409, nil)
	bob.do("POST", base+"/skip", map[string]int64{"itemId": 1}, 204, nil)

	// Bob's song is now playing and has no original.
	bob.do("POST", base+"/control", map[string]any{"action": "vocal", "value": 1}, 409, nil)

	// Kicked members are locked out.
	adm.do("POST", "/api/admin/rooms/"+id+"/kick", map[string]string{"userId": bob.userID}, 204, nil)
	bob.do("POST", base+"/queue", map[string]int64{"songId": one}, 403, nil)
	bob.do("POST", base+"/join", map[string]string{"nickname": "Bob2"}, 403, nil)

	e.anon().do("GET", "/api/rooms/nope", nil, 404, nil)
}

func TestFavoritesFollowNickname(t *testing.T) {
	e := setup(t)
	adm := e.admin()
	room1, room2 := e.newRoom(adm), e.newRoom(adm)
	one := e.songID("one")

	phone := e.user()
	phone.do("GET", "/api/rooms/"+room1+"/favorites", nil, 403, nil) // must join first
	phone.do("POST", "/api/rooms/"+room1+"/join", map[string]string{"nickname": "Amy"}, 204, nil)
	phone.do("PUT", "/api/rooms/"+room1+"/favorites/9999", nil, 404, nil)
	phone.do("PUT", "/api/rooms/"+room1+"/favorites/"+itoa(one), nil, 204, nil)

	// Another device using the same name (different case, different room) sees it.
	tablet := e.user()
	tablet.do("POST", "/api/rooms/"+room2+"/join", map[string]string{"nickname": "AMY"}, 204, nil)
	var favs []library.Song
	tablet.do("GET", "/api/rooms/"+room2+"/favorites", nil, 200, &favs)
	if len(favs) != 1 || favs[0].ID != one {
		t.Fatalf("favorites on tablet = %+v", favs)
	}

	// A different name has its own list.
	other := e.user()
	other.do("POST", "/api/rooms/"+room1+"/join", map[string]string{"nickname": "Ben"}, 204, nil)
	other.do("GET", "/api/rooms/"+room1+"/favorites", nil, 200, &favs)
	if len(favs) != 0 {
		t.Fatalf("Ben sees %+v", favs)
	}

	tablet.do("DELETE", "/api/rooms/"+room2+"/favorites/"+itoa(one), nil, 204, nil)
	phone.do("GET", "/api/rooms/"+room1+"/favorites", nil, 200, &favs)
	if len(favs) != 0 {
		t.Fatalf("after delete = %+v", favs)
	}
}

func TestMediaAndHealth(t *testing.T) {
	e := setup(t)
	anon := e.anon()
	one, two := e.songID("one"), e.songID("two")

	if body := anon.do("GET", "/media/"+itoa(one), nil, 200, nil); body != "one" {
		t.Fatalf("media body = %q", body)
	}
	if body := anon.do("GET", "/media/"+itoa(one)+"/original", nil, 200, nil); body != "one-original" {
		t.Fatalf("original body = %q", body)
	}
	anon.do("GET", "/media/"+itoa(two)+"/original", nil, 404, nil)
	anon.do("GET", "/media/9999", nil, 404, nil)

	// Range requests are what lets the browser stream and seek.
	req, _ := http.NewRequest("GET", e.srv.URL+"/media/"+itoa(one), nil)
	req.Header.Set("Range", "bytes=1-2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || string(b) != "ne" {
		t.Fatalf("range = %d %q", resp.StatusCode, b)
	}

	if body := anon.do("GET", "/healthz", nil, 200, nil); body != "ok" {
		t.Fatalf("healthz = %q", body)
	}
	if body := anon.do("GET", "/r/abc/player", nil, 200, nil); body != "<html>spa</html>" {
		t.Fatalf("spa fallback = %q", body)
	}
	anon.do("GET", "/api/unknown", nil, 404, nil)
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// tvScreen opens /api/tv/ws like the /tv page and returns its pairing code and
// a function that waits for the room it gets sent to.
func (e *env) tvScreen() (string, func() string) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	e.t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/api/tv/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {e.srv.URL}}})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { conn.CloseNow() })
	read := func() map[string]string {
		_, b, err := conn.Read(ctx)
		if err != nil {
			e.t.Fatal(err)
		}
		var m map[string]string
		json.Unmarshal(b, &m)
		return m
	}
	first := read()
	if first["type"] != "code" || len(first["code"]) != 4 {
		e.t.Fatalf("first message = %v", first)
	}
	return first["code"], func() string {
		m := read()
		if m["type"] != "paired" {
			e.t.Fatalf("paired message = %v", m)
		}
		return m["room"]
	}
}

func TestTVPairing(t *testing.T) {
	e := setup(t)
	adm := e.admin()
	id := e.newRoom(adm)
	base := "/api/rooms/" + id
	guest := e.user()
	guest.do("POST", base+"/join", map[string]string{"nickname": "guest"}, 204, nil)

	// No player yet: any member can pair a TV.
	code, waitRoom := e.tvScreen()
	e.user().do("POST", base+"/tv/pair", map[string]string{"code": code}, 403, nil) // not a member
	guest.do("POST", base+"/tv/pair", map[string]string{"code": "0000x"}, 404, nil)
	guest.do("POST", base+"/tv/pair", map[string]string{"code": " " + code + " "}, 204, nil)
	if got := waitRoom(); got != id {
		t.Fatalf("TV sent to %q, want %q", got, id)
	}
	guest.do("POST", base+"/tv/pair", map[string]string{"code": code}, 404, nil) // codes are single use

	// The paired TV connects as a player; now only admins may pair more TVs.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	player, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+base+"/player/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {e.srv.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer player.CloseNow()
	player.Read(ctx) // first state snapshot: the player is registered

	code2, waitRoom2 := e.tvScreen()
	guest.do("POST", base+"/tv/pair", map[string]string{"code": code2}, 409, nil)
	e.anon().do("POST", "/api/admin/rooms/"+id+"/tv/pair", map[string]string{"code": code2}, 401, nil)
	adm.do("POST", "/api/admin/rooms/"+id+"/tv/pair", map[string]string{"code": code2}, 204, nil)
	if got := waitRoom2(); got != id {
		t.Fatalf("admin-paired TV sent to %q", got)
	}
}

func TestRoomNamesOverHTTP(t *testing.T) {
	e := setup(t)
	adm := e.admin()
	adm.do("POST", "/api/admin/rooms", map[string]string{"name": "客廳"}, 400, nil)
	adm.do("POST", "/api/admin/rooms", map[string]string{"name": "has space"}, 400, nil)
	var r struct{ ID string }
	adm.do("POST", "/api/admin/rooms", map[string]string{"name": "Party_Room-1"}, 201, &r)
	if r.ID != "Party_Room-1" {
		t.Fatalf("id = %q", r.ID)
	}
	adm.do("POST", "/api/admin/rooms", map[string]string{"name": "party_room-1"}, 409, nil)

	// Any casing in the URL reaches the room; the canonical name is reported back.
	var info struct{ ID, URL string }
	e.anon().do("GET", "/api/rooms/PARTY_ROOM-1", nil, 200, &info)
	if info.ID != "Party_Room-1" || info.URL != "http://ktv.test/r/Party_Room-1" {
		t.Fatalf("info = %+v", info)
	}
}

func TestQueueManagementOverHTTP(t *testing.T) {
	e := setup(t)
	adm := e.admin()
	id := e.newRoom(adm)
	base := "/api/rooms/" + id
	one, two := e.songID("one"), e.songID("two")
	a, b := e.user(), e.user()
	a.do("POST", base+"/join", map[string]string{"nickname": "A"}, 204, nil)
	b.do("POST", base+"/join", map[string]string{"nickname": "B"}, 204, nil)
	e.anon().do("GET", base+"/history", nil, 401, nil)
	e.user().do("GET", base+"/history", nil, 403, nil) // not a member

	a.do("POST", base+"/queue", map[string]int64{"songId": one}, 204, nil) // playing (item 1)
	b.do("POST", base+"/queue", map[string]int64{"songId": two}, 204, nil) // queued (item 2)
	a.do("POST", base+"/queue/2/move", map[string]string{"to": "top"}, 204, nil)
	a.do("POST", base+"/queue/2/move", map[string]string{"to": "sideways"}, 400, nil)
	a.do("DELETE", base+"/queue/2", nil, 403, nil)  // not A's song
	a.do("POST", base+"/replay-all", nil, 409, nil) // queue not empty

	b.do("POST", base+"/skip", map[string]int64{"itemId": 1}, 204, nil) // one -> history, two playing
	b.do("POST", base+"/skip", map[string]int64{"itemId": 2}, 204, nil) // two -> history
	var hist []room.HistoryEntry
	a.do("GET", base+"/history", nil, 200, &hist)
	if len(hist) != 2 || hist[0].SongID != two || hist[0].Nickname != "B" || !hist[0].Present {
		t.Fatalf("history = %+v", hist)
	}
	var res struct{ Queued int }
	a.do("POST", base+"/replay-all", nil, 200, &res)
	if res.Queued != 2 {
		t.Fatalf("replay queued %d", res.Queued)
	}
}

func TestHistoryPaging(t *testing.T) {
	e := setup(t)
	adm := e.admin()
	id := e.newRoom(adm)
	base := "/api/rooms/" + id
	u := e.user()
	u.do("POST", base+"/join", map[string]string{"nickname": "U"}, 204, nil)
	// Alternate two songs to build up 5 history entries.
	songs := []int64{e.songID("one"), e.songID("two")}
	for i := range 5 {
		u.do("POST", base+"/queue", map[string]int64{"songId": songs[i%2]}, 204, nil)
		u.do("POST", base+"/skip", map[string]int64{"itemId": int64(i + 1)}, 204, nil)
	}
	var page1, page2, page3 []room.HistoryEntry
	u.do("GET", base+"/history?limit=2", nil, 200, &page1)
	last := page1[len(page1)-1]
	u.do("GET", fmt.Sprintf("%s/history?limit=2&before_started=%d&before_id=%d", base, last.StartedAt, last.ID), nil, 200, &page2)
	last = page2[len(page2)-1]
	u.do("GET", fmt.Sprintf("%s/history?limit=2&before_started=%d&before_id=%d", base, last.StartedAt, last.ID), nil, 200, &page3)
	var ids []int64
	for _, p := range [][]room.HistoryEntry{page1, page2, page3} {
		for _, h := range p {
			ids = append(ids, h.ID)
		}
	}
	if fmt.Sprint(ids) != "[5 4 3 2 1]" {
		t.Fatalf("paged ids = %v, want newest to oldest without gaps or repeats", ids)
	}
}

func TestLegacyAdminCookie(t *testing.T) {
	e := setup(t)
	adm := e.admin()
	u, _ := url.Parse(e.srv.URL)
	var value string
	for _, c := range adm.http.Jar.Cookies(u) {
		if c.Name == adminCookie {
			value = c.Value
		}
	}
	// The same signed value under the pre-rename cookie name still works.
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/admin/rooms", nil)
	req.AddCookie(&http.Cookie{Name: legacyAdminCookie, Value: value})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("legacy cookie: %d", resp.StatusCode)
	}
}

func TestQRCodeHasLogo(t *testing.T) {
	b, err := roomQR("http://ktv.test/r/living-room")
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	// The centre pixel belongs to the logo (coloured), not a black or white QR module.
	c := color.RGBAModel.Convert(img.At(qrSize/2, qrSize/2)).(color.RGBA)
	if c.R == c.G && c.G == c.B {
		t.Fatalf("centre pixel %v does not look like the logo", c)
	}
}
