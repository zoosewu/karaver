package room

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type ClientKind int

const (
	KindMember ClientKind = iota
	KindPlayer
	KindAdmin
)

// Client is one WebSocket subscriber. Messages are full state snapshots, so only
// the latest one matters: Send has capacity 1 and older pending messages are dropped.
type Client struct {
	Kind   ClientKind
	UserID string
	Send   chan []byte
	Done   chan struct{}
	once   sync.Once

	// Player connections only. PlayerID is shown to admins; PlayerSecret is only
	// sent to the player itself and authorizes its "song ended" reports.
	PlayerID     string
	PlayerSecret string
	RemoteAddr   string
	UserAgent    string
	ConnectedAt  int64
}

func NewClient(kind ClientKind, userID string) *Client {
	return &Client{Kind: kind, UserID: userID, Send: make(chan []byte, 1), Done: make(chan struct{})}
}

func NewPlayerClient(remoteAddr, userAgent string) *Client {
	c := NewClient(KindPlayer, "")
	c.PlayerID = randomHex(6)
	c.PlayerSecret = randomHex(24)
	c.RemoteAddr = remoteAddr
	c.UserAgent = userAgent
	c.ConnectedAt = time.Now().UnixMilli()
	return c
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Client) push(b []byte) {
	for {
		select {
		case c.Send <- b:
			return
		default:
		}
		select {
		case <-c.Send:
		default:
		}
	}
}

// pushAndClose delivers a final message (e.g. "kicked") and disconnects the client.
func (c *Client) pushAndClose(b []byte) {
	c.push(b)
	c.close()
}

func (c *Client) close() { c.once.Do(func() { close(c.Done) }) }
