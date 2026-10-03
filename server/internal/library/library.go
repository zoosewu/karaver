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
	"unicode"

	"zkaraver/internal/config"
	"zkaraver/internal/keys"
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

	// OnScan runs after every successful scan (set before the first StartScan).
	OnScan func()
}

// New returns a library backed by the songs table. Songs are not cached in
// memory; every lookup is a small indexed query.
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
			if l.OnScan != nil {
				l.OnScan()
			}
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
	id                          int64
	title, artist, orig, search string
	size, mtime                 int64
	present                     bool
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
	rows, err := l.db.Query(`SELECT id, path, title, artist, original_path, search, size, mtime, present FROM songs`)
	if err != nil {
		res.Error = err.Error()
		return finish()
	}
	for rows.Next() {
		var e existing
		var p string
		if err := rows.Scan(&e.id, &p, &e.title, &e.artist, &e.orig, &e.search, &e.size, &e.mtime, &e.present); err != nil {
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
		search := normalize(artist + " " + title)
		if e, ok := known[rel]; ok {
			if e.present && e.size == f.size && e.mtime == f.mtime && e.title == title && e.artist == artist && e.orig == orig && e.search == search {
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

// normalize folds text for forgiving matching: full-width ASCII becomes
// half-width, letters are lowercased, and everything that is not a letter or
// digit (spaces, dashes, punctuation, symbols) is dropped. So "五月天 - 倔強",
// "五月天倔強" and "ＢＥＹＯＮＤ" vs "beyond" compare equal. Scans store the
// normalized "artist title" in songs.search.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 0xFF01 && r <= 0xFF5E { // full-width ASCII block
			r -= 0xFEE0
		}
		r = unicode.ToLower(r)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Song sort orders for SearchOptions.Sort.
const (
	SortArtist  = "artist"  // artist, then title (default)
	SortTitle   = "title"   // title, then artist
	SortNewest  = "new"     // most recently added first
	SortPopular = "popular" // most sung (all rooms) first
)

// Artist sort orders for Artists.
const (
	ArtistSortName  = "name"
	ArtistSortCount = "count"
)

type SearchOptions struct {
	Query string
	Sort  string
	// Artist limits results to one artist (exact match; "" = unknown artist)
	// when HasArtist is set.
	Artist    string
	HasArtist bool
}

// termsWhere turns a query into "instr(search, ?) > 0" conditions: every
// whitespace-separated term, normalized, must appear. Terms that normalize to
// nothing (pure punctuation) are ignored. Normalized text has no LIKE
// wildcards, but instr() avoids escaping altogether.
func termsWhere(q string) (where []string, args []any) {
	for _, f := range strings.Fields(q) {
		if t := normalize(f); t != "" {
			where = append(where, "instr(search, ?) > 0")
			args = append(args, t)
		}
	}
	return where, args
}

// Search matches songs by query, sorted by artist (see SearchWith).
func (l *Library) Search(q string, limit, offset int) (songs []Song, more bool, err error) {
	return l.SearchWith(SearchOptions{Query: q}, limit, offset)
}

// SearchWith matches songs whose normalized "artist title" contains every
// term of the query, optionally within one artist, in the requested order.
func (l *Library) SearchWith(o SearchOptions, limit, offset int) (songs []Song, more bool, err error) {
	where, args := termsWhere(o.Query)
	where = append([]string{"present = 1"}, where...)
	if o.HasArtist {
		where = append(where, "artist = ?")
		args = append(args, o.Artist)
	}
	order := "artist, title, id"
	switch o.Sort {
	case SortTitle:
		order = "title, artist, id"
	case SortNewest:
		order = "id DESC"
	case SortPopular:
		// Songs that were actually started (sung or skipped), in any room.
		order = `(SELECT COUNT(*) FROM queue_items q WHERE q.song_id = songs.id AND q.started_at IS NOT NULL) DESC, artist, title, id`
	}
	args = append(args, limit+1, offset)
	rows, err := l.db.Query(`SELECT id, title, artist FROM songs WHERE `+strings.Join(where, " AND ")+
		` ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
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

type Artist struct {
	Name  string `json:"name"` // "" = unknown artist
	Songs int    `json:"songs"`
}

// Artists lists artists with how many songs each has. With a query, only
// artists that have a matching song (by title or artist name) are listed.
func (l *Library) Artists(q, sort string, limit, offset int) (artists []Artist, more bool, err error) {
	where, args := termsWhere(q)
	where = append([]string{"present = 1"}, where...)
	order := "artist"
	if sort == ArtistSortCount {
		order = "COUNT(*) DESC, artist"
	}
	args = append(args, limit+1, offset)
	rows, err := l.db.Query(`SELECT artist, COUNT(*) FROM songs WHERE `+strings.Join(where, " AND ")+
		` GROUP BY artist ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	artists = []Artist{}
	for rows.Next() {
		var a Artist
		if err := rows.Scan(&a.Name, &a.Songs); err != nil {
			return nil, false, err
		}
		artists = append(artists, a)
	}
	if len(artists) > limit {
		return artists[:limit], true, rows.Err()
	}
	return artists, false, rows.Err()
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

// Lookup reports a present song's display fields for queueing.
func (l *Library) Lookup(id int64) (title, artist string, hasOriginal, ok bool) {
	err := l.db.QueryRow(`SELECT title, artist, original_path != '' FROM songs WHERE id=? AND present=1`, id).
		Scan(&title, &artist, &hasOriginal)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("song lookup: %v", err)
	}
	return title, artist, hasOriginal, err == nil
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

func (l *Library) Count() (n int, err error) {
	err = l.db.QueryRow(`SELECT COUNT(*) FROM songs WHERE present=1`).Scan(&n)
	return
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

// KeySong returns a present song's file for key rendering.
func (l *Library) KeySong(id int64) (keys.Song, bool) {
	s := keys.Song{ID: id}
	err := l.db.QueryRow(`SELECT path, size, mtime FROM songs WHERE id=? AND present=1`, id).Scan(&s.Path, &s.Size, &s.Mtime)
	return s, err == nil
}

// KeySongs lists every present song for key rendering, newest file first.
func (l *Library) KeySongs() ([]keys.Song, error) {
	rows, err := l.db.Query(`SELECT id, path, size, mtime FROM songs WHERE present=1 ORDER BY mtime DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []keys.Song
	for rows.Next() {
		var s keys.Song
		if err := rows.Scan(&s.ID, &s.Path, &s.Size, &s.Mtime); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
