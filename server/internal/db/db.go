// Package db opens the SQLite database and applies the schema.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
	id         TEXT PRIMARY KEY,
	token_hash TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS songs (
	id            INTEGER PRIMARY KEY,
	path          TEXT NOT NULL UNIQUE,
	title         TEXT NOT NULL,
	artist        TEXT NOT NULL,
	search        TEXT NOT NULL,
	size          INTEGER NOT NULL,
	mtime         INTEGER NOT NULL,
	original_path TEXT NOT NULL DEFAULT '',
	present       INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS songs_present ON songs(present, artist, title);

CREATE TABLE IF NOT EXISTS rooms (
	id                 TEXT PRIMARY KEY,
	name               TEXT NOT NULL,
	queue_mode         TEXT NOT NULL DEFAULT 'fifo',
	max_per_user       INTEGER NOT NULL DEFAULT 0,
	idle_clear_minutes INTEGER NOT NULL DEFAULT 0,
	show_qr            INTEGER NOT NULL DEFAULT 1,
	volume             INTEGER NOT NULL DEFAULT 100,
	created_at         INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS room_members (
	room_id   TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	nickname  TEXT NOT NULL COLLATE NOCASE,
	banned    INTEGER NOT NULL DEFAULT 0,
	joined_at INTEGER NOT NULL,
	PRIMARY KEY (room_id, user_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS room_members_nickname ON room_members(room_id, nickname);

-- status: queued | playing | done | skipped | failed
CREATE TABLE IF NOT EXISTS queue_items (
	id         INTEGER PRIMARY KEY,
	room_id    TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	song_id    INTEGER NOT NULL REFERENCES songs(id),
	user_id    TEXT NOT NULL,
	status     TEXT NOT NULL,
	position   INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	started_at INTEGER,
	ended_at   INTEGER
);
CREATE INDEX IF NOT EXISTS queue_items_room_status ON queue_items(room_id, status);
`

func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Load is tiny; a single connection avoids SQLITE_BUSY between writers.
	d.SetMaxOpenConns(1)
	if _, err := d.Exec(schema); err != nil {
		d.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return d, nil
}

func Setting(d *sql.DB, key string) (string, bool, error) {
	var v string
	err := d.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func SetSetting(d *sql.DB, key, value string) error {
	_, err := d.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
