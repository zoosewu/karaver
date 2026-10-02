// Package library scans the media directory and serves song lookups.
package library

import (
	"database/sql"
	"errors"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"karaver/internal/config"
)

type Song struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Path   string `json:"-"`
}

type Result struct {
	Added      int    `json:"added"`
	Updated    int    `json:"updated"`
	Removed    int    `json:"removed"`
	Total      int    `json:"total"`
	FinishedAt int64  `json:"finishedAt"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

type Library struct {
	db  *sql.DB
	cfg *config.Config

	mu       sync.Mutex
	scanning bool
	last     *Result
}

func New(d *sql.DB, cfg *config.Config) *Library {
	return &Library{db: d, cfg: cfg}
}

func (l *Library) Status() (scanning bool, last *Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.scanning, l.last
}

// StartScan runs a scan in the background. It returns false if one is already running.
func (l *Library) StartScan() bool {
	l.mu.Lock()
	if l.scanning {
		l.mu.Unlock()
		return false
	}
	l.scanning = true
	l.mu.Unlock()

	go func() {
		res := l.scan()
		if res.Error != "" {
			log.Printf("library scan failed: %s", res.Error)
		} else {
			log.Printf("library scan: %d songs (+%d ~%d -%d) in %dms",
				res.Total, res.Added, res.Updated, res.Removed, res.DurationMs)
		}
		l.mu.Lock()
		l.scanning = false
		l.last = &res
		l.mu.Unlock()
	}()
	return true
}

type fileInfo struct {
	size, mtime int64
}

type existing struct {
	id                  int64
	title, artist, orig string
	size, mtime         int64
	present             bool
}

func (l *Library) scan() Result {
	start := time.Now()
	res := Result{}
	finish := func() Result {
		res.FinishedAt = time.Now().UnixMilli()
		res.DurationMs = time.Since(start).Milliseconds()
		return res
	}

	root := l.cfg.MediaDir
	files := map[string]fileInfo{}
	originals := map[string]string{} // song stem (rel path without ext) -> original rel path
	err := walkFollowingLinks(root, func(rel string, info fs.FileInfo) {
		if !slices.Contains(l.cfg.Extensions, strings.ToLower(path.Ext(rel))) {
			return
		}
		stem := strings.TrimSuffix(rel, path.Ext(rel))
		if suf := l.cfg.OriginalSuffix; suf != "" && strings.HasSuffix(stem, suf) {
			originals[strings.TrimSuffix(stem, suf)] = rel
			return
		}
		files[rel] = fileInfo{size: info.Size(), mtime: info.ModTime().Unix()}
	})
	if err != nil {
		res.Error = err.Error()
		return finish()
	}

	known := map[string]existing{}
	rows, err := l.db.Query(`SELECT id, path, title, artist, original_path, size, mtime, present FROM songs`)
	if err != nil {
		res.Error = err.Error()
		return finish()
	}
	for rows.Next() {
		var e existing
		var p string
		if err := rows.Scan(&e.id, &p, &e.title, &e.artist, &e.orig, &e.size, &e.mtime, &e.present); err != nil {
			rows.Close()
			res.Error = err.Error()
			return finish()
		}
		known[p] = e
	}
	rows.Close()

	tx, err := l.db.Begin()
	if err != nil {
		res.Error = err.Error()
		return finish()
	}
	defer tx.Rollback()

	for rel, f := range files {
		stem := strings.TrimSuffix(rel, path.Ext(rel))
		artist, title := l.parse(path.Base(stem))
		orig := originals[stem]
		search := strings.ToLower(artist + " " + title)
		if e, ok := known[rel]; ok {
			if e.present && e.size == f.size && e.mtime == f.mtime && e.title == title && e.artist == artist && e.orig == orig {
				continue
			}
			if _, err := tx.Exec(`UPDATE songs SET title=?, artist=?, search=?, size=?, mtime=?, original_path=?, present=1 WHERE id=?`,
				title, artist, search, f.size, f.mtime, orig, e.id); err != nil {
				res.Error = err.Error()
				return finish()
			}
			res.Updated++
			continue
		}
		if _, err := tx.Exec(`INSERT INTO songs(path, title, artist, search, size, mtime, original_path) VALUES(?,?,?,?,?,?,?)`,
			rel, title, artist, search, f.size, f.mtime, orig); err != nil {
			res.Error = err.Error()
			return finish()
		}
		res.Added++
	}
	for p, e := range known {
		if _, ok := files[p]; ok || !e.present {
			continue
		}
		// Soft delete: queue history keeps referencing the row.
		if _, err := tx.Exec(`UPDATE songs SET present=0 WHERE id=?`, e.id); err != nil {
			res.Error = err.Error()
			return finish()
		}
		res.Removed++
	}
	if err := tx.Commit(); err != nil {
		res.Error = err.Error()
		return finish()
	}
	res.Total = len(files)
	return finish()
}

// walkFollowingLinks calls visit for every regular file under root (paths are
// slash-separated and relative to root). Unlike filepath.WalkDir it follows
// symbolic links to files and directories. Each real directory is visited once,
// which both breaks symlink cycles and avoids listing a folder twice when two
// links point at it. Hidden entries (".name"), broken links and unreadable
// directories are skipped.
func walkFollowingLinks(root string, visit func(rel string, info fs.FileInfo)) error {
	if _, err := os.Stat(root); err != nil {
		return err
	}
	seen := map[string]bool{}
	var walk func(abs, rel string)
	walk = func(abs, rel string) {
		real, err := filepath.EvalSymlinks(abs)
		if err != nil || seen[real] {
			return
		}
		seen[real] = true
		entries, err := os.ReadDir(abs) // sorted by name, so the walk is deterministic
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			p, r := filepath.Join(abs, name), path.Join(rel, name)
			info, err := os.Stat(p) // follows links
			if err != nil {
				continue
			}
			if info.IsDir() {
				walk(p, r)
			} else if info.Mode().IsRegular() {
				visit(r, info)
			}
		}
	}
	walk(root, "")
	return nil
}

func (l *Library) parse(stem string) (artist, title string) {
	stem = strings.TrimSpace(stem)
	if l.cfg.FilenameFormat == "title" {
		return "", stem
	}
	i := strings.Index(stem, l.cfg.FilenameSeparator)
	if i < 0 {
		return "", stem
	}
	a := strings.TrimSpace(stem[:i])
	b := strings.TrimSpace(stem[i+len(l.cfg.FilenameSeparator):])
	if a == "" || b == "" {
		return "", stem
	}
	if l.cfg.FilenameFormat == "title-artist" {
		return b, a
	}
	return a, b
}

// Search does a case-insensitive substring match; every whitespace-separated term must match.
func (l *Library) Search(q string, limit, offset int) (songs []Song, more bool, err error) {
	where := []string{"present = 1"}
	args := []any{}
	for _, term := range strings.Fields(strings.ToLower(q)) {
		where = append(where, `search LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(term)+"%")
	}
	args = append(args, limit+1, offset)
	rows, err := l.db.Query(`SELECT id, title, artist FROM songs WHERE `+strings.Join(where, " AND ")+
		` ORDER BY artist, title LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	songs = []Song{}
	for rows.Next() {
		var s Song
		if err := rows.Scan(&s.ID, &s.Title, &s.Artist); err != nil {
			return nil, false, err
		}
		songs = append(songs, s)
	}
	if len(songs) > limit {
		return songs[:limit], true, rows.Err()
	}
	return songs, false, rows.Err()
}

var ErrNotFound = errors.New("song not found")

// Get returns a present song including its path.
func (l *Library) Get(id int64) (Song, error) {
	s := Song{ID: id}
	err := l.db.QueryRow(`SELECT title, artist, path FROM songs WHERE id=? AND present=1`, id).Scan(&s.Title, &s.Artist, &s.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

// OriginalPath returns the original-vocal companion file of a present song.
func (l *Library) OriginalPath(id int64) (string, error) {
	var p string
	err := l.db.QueryRow(`SELECT original_path FROM songs WHERE id=? AND present=1`, id).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && p == "") {
		return "", ErrNotFound
	}
	return p, err
}

// Favorites lists a user's favorite songs that are still in the library, newest first.
func (l *Library) Favorites(username string) ([]Song, error) {
	rows, err := l.db.Query(`SELECT s.id, s.title, s.artist FROM favorites f JOIN songs s ON s.id = f.song_id
		WHERE f.username = ? AND s.present = 1 ORDER BY f.created_at DESC`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	songs := []Song{}
	for rows.Next() {
		var s Song
		if err := rows.Scan(&s.ID, &s.Title, &s.Artist); err != nil {
			return nil, err
		}
		songs = append(songs, s)
	}
	return songs, rows.Err()
}

func (l *Library) AddFavorite(username string, songID int64) error {
	if _, err := l.Get(songID); err != nil {
		return err
	}
	_, err := l.db.Exec(`INSERT INTO favorites(username, song_id, created_at) VALUES(?,?,?)
		ON CONFLICT(username, song_id) DO NOTHING`, username, songID, time.Now().UnixMilli())
	return err
}

func (l *Library) RemoveFavorite(username string, songID int64) error {
	_, err := l.db.Exec(`DELETE FROM favorites WHERE username=? AND song_id=?`, username, songID)
	return err
}

func (l *Library) Count() (n int, err error) {
	err = l.db.QueryRow(`SELECT COUNT(*) FROM songs WHERE present=1`).Scan(&n)
	return
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
