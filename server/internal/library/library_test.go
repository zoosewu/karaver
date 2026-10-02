package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"zkaraver/internal/config"
	"zkaraver/internal/db"
)

func newLib(t *testing.T, cfg config.Config) (*Library, string) {
	t.Helper()
	media := t.TempDir()
	cfg.MediaDir = media
	if cfg.FilenameFormat == "" {
		cfg.FilenameFormat = "artist-title"
	}
	if cfg.FilenameSeparator == "" {
		cfg.FilenameSeparator = " - "
	}
	if cfg.Extensions == nil {
		cfg.Extensions = []string{".mp4"}
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return New(d, &cfg), media
}

func touch(t *testing.T, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scan(t *testing.T, l *Library) Result {
	t.Helper()
	res := l.scan()
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	return res
}

func titles(t *testing.T, l *Library, q string) []string {
	t.Helper()
	songs, _, err := l.Search(q, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range songs {
		out = append(out, s.Artist+"|"+s.Title)
	}
	return out
}

func TestParse(t *testing.T) {
	cases := []struct {
		format, sep, stem, artist, title string
	}{
		{"artist-title", " - ", "周杰倫 - 晴天", "周杰倫", "晴天"},
		{"artist-title", " - ", "Queen - Bohemian Rhapsody - Live", "Queen", "Bohemian Rhapsody - Live"},
		{"artist-title", " - ", "沒有分隔符", "", "沒有分隔符"},
		{"artist-title", " - ", " - 只有一邊", "", "- 只有一邊"},
		{"title-artist", "_", "晴天_周杰倫", "周杰倫", "晴天"},
		{"title", " - ", "周杰倫 - 晴天", "", "周杰倫 - 晴天"},
	}
	for _, c := range cases {
		l := &Library{cfg: &config.Config{FilenameFormat: c.format, FilenameSeparator: c.sep}}
		a, ti := l.parse(c.stem)
		if a != c.artist || ti != c.title {
			t.Errorf("parse(%q, %s) = %q, %q; want %q, %q", c.stem, c.format, a, ti, c.artist, c.title)
		}
	}
}

func TestScan(t *testing.T) {
	l, media := newLib(t, config.Config{OriginalSuffix: "_original"})
	for _, f := range []string{
		"周杰倫 - 晴天.mp4",
		"周杰倫 - 晴天_original.mp4", // companion, not a song
		"sub/Queen - Bohemian Rhapsody.MP4",
		"沒有分隔符.mp4",
		".hidden.mp4",
		".trash/A - deleted.mp4",
		"notes.txt",
	} {
		touch(t, media, f)
	}

	res := scan(t, l)
	if res.Added != 3 || res.Total != 3 {
		t.Fatalf("first scan = %+v", res)
	}
	got := titles(t, l, "")
	want := []string{"|沒有分隔符", "Queen|Bohemian Rhapsody", "周杰倫|晴天"}
	if len(got) != len(want) {
		t.Fatalf("songs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("songs = %v, want %v", got, want)
		}
	}

	songs, _, _ := l.Search("晴天", 10, 0)
	orig, err := l.OriginalPath(songs[0].ID)
	if err != nil || orig != "周杰倫 - 晴天_original.mp4" {
		t.Fatalf("original = %q, %v", orig, err)
	}
	queen, _, _ := l.Search("queen", 10, 0)
	if _, err := l.OriginalPath(queen[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("song without original: %v", err)
	}

	// Rescan with nothing changed is a no-op; removing a file soft-deletes it.
	if res := scan(t, l); res.Added+res.Updated+res.Removed != 0 {
		t.Fatalf("idle rescan = %+v", res)
	}
	os.Remove(filepath.Join(media, "沒有分隔符.mp4"))
	if res := scan(t, l); res.Removed != 1 || res.Total != 2 {
		t.Fatalf("rescan after delete = %+v", res)
	}
	if n, _ := l.Count(); n != 2 {
		t.Fatalf("count = %d", n)
	}
	// Bringing it back reuses the same row.
	touch(t, media, "沒有分隔符.mp4")
	if res := scan(t, l); res.Updated != 1 || res.Added != 0 {
		t.Fatalf("rescan after restore = %+v", res)
	}
}

func TestSearch(t *testing.T) {
	l, media := newLib(t, config.Config{})
	for _, f := range []string{
		"五月天 - 倔強.mp4",
		"五月天 - 知足.mp4",
		"Queen - 100% Pure.mp4",
		"A_B - x.mp4",
		"Beyond - 海闊天空 (Live).mp4",
		"ＳＨＥ - Ｓｕｐｅｒ Ｓｔａｒ.mp4", // full-width in the filename
	} {
		touch(t, media, f)
	}
	scan(t, l)

	cases := []struct {
		q    string
		want []string
	}{
		{"五月天 倔", []string{"五月天|倔強"}},             // every term must match
		{"倔強 五月天", []string{"五月天|倔強"}},            // in any order
		{"五月天倔強", []string{"五月天|倔強"}},             // spaces and the " - " separator are ignored
		{"五月天-倔強", []string{"五月天|倔強"}},            // punctuation in the query too
		{"QUEEN", []string{"Queen|100% Pure"}},    // case-insensitive
		{"100pure", []string{"Queen|100% Pure"}},  // symbols ignored
		{"ｑｕｅｅｎ", []string{"Queen|100% Pure"}},    // full-width query
		{"she super", []string{"ＳＨＥ|Ｓｕｐｅｒ Ｓｔａｒ"}}, // full-width title
		{"海闊天空live", []string{"Beyond|海闊天空 (Live)"}},
		{"ab", []string{"A_B|x"}},
		{"不存在", nil},
	}
	for _, c := range cases {
		got := titles(t, l, c.q)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("Search(%q) = %v, want %v", c.q, got, c.want)
		}
	}
	// A query that is only punctuation matches everything.
	if got := titles(t, l, " - "); len(got) != 6 {
		t.Errorf("punctuation-only query = %v", got)
	}

	page, more, _ := l.Search("", 4, 0)
	rest, more2, _ := l.Search("", 4, 4)
	if len(page) != 4 || !more || len(rest) != 2 || more2 {
		t.Fatalf("paging: %d %v %d %v", len(page), more, len(rest), more2)
	}
}

func TestLookupFollowsScans(t *testing.T) {
	l, media := newLib(t, config.Config{})
	touch(t, media, "A - one.mp4")
	scan(t, l)
	songs, _, _ := l.Search("one", 10, 0)
	id := songs[0].ID
	if title, _, _, ok := l.Lookup(id); !ok || title != "one" {
		t.Fatalf("lookup = %q %v", title, ok)
	}

	// A second Library on the same database sees the same songs.
	l2 := New(l.db, l.cfg)
	if n, _ := l2.Count(); n != 1 {
		t.Fatalf("fresh library count = %d", n)
	}

	os.Remove(filepath.Join(media, "A - one.mp4"))
	scan(t, l)
	if _, err := l.Get(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed song still served: %v", err)
	}
	if _, _, _, ok := l.Lookup(id); ok {
		t.Fatal("removed song still queueable")
	}
}

func TestFavorites(t *testing.T) {
	l, media := newLib(t, config.Config{})
	touch(t, media, "A - one.mp4")
	touch(t, media, "B - two.mp4")
	scan(t, l)
	songs, _, _ := l.Search("", 10, 0)
	one, two := songs[0].ID, songs[1].ID

	if err := l.AddFavorite("Amy", one); err != nil {
		t.Fatal(err)
	}
	if err := l.AddFavorite("Amy", two); err != nil {
		t.Fatal(err)
	}
	if err := l.AddFavorite("Amy", one); err != nil { // duplicate is a no-op
		t.Fatal(err)
	}
	if err := l.AddFavorite("Amy", 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing song: %v", err)
	}

	// Usernames match case-insensitively.
	favs, err := l.Favorites("amy")
	if err != nil || len(favs) != 2 {
		t.Fatalf("favorites = %v, %v", favs, err)
	}
	if other, _ := l.Favorites("Bob"); len(other) != 0 {
		t.Fatalf("other user sees %v", other)
	}

	if err := l.RemoveFavorite("AMY", one); err != nil {
		t.Fatal(err)
	}
	favs, _ = l.Favorites("Amy")
	if len(favs) != 1 || favs[0].ID != two {
		t.Fatalf("after remove = %v", favs)
	}

	// Songs that left the library drop out of the list.
	os.Remove(filepath.Join(media, "B - two.mp4"))
	scan(t, l)
	if favs, _ = l.Favorites("Amy"); len(favs) != 0 {
		t.Fatalf("after delete = %v", favs)
	}
}

func TestScanFollowsSymlinks(t *testing.T) {
	l, media := newLib(t, config.Config{})
	outside := t.TempDir() // a library that lives outside MEDIA_DIR, linked in
	touch(t, outside, "Jay/周杰倫 - 晴天.mp4")
	touch(t, outside, "single/A - linked file.mp4")
	touch(t, media, "local/B - local.mp4")

	links := map[string]string{
		"linked-dir":        filepath.Join(outside, "Jay"),                           // link to a directory
		"C - file link.mp4": filepath.Join(outside, "single", "A - linked file.mp4"), // link to a file
		"local/loop":        "..",                                                    // cycle back to the root
		"local/again":       ".",                                                     // cycle to itself
		"dup-a":             filepath.Join(outside, "Jay"),                           // two links, same folder
		"broken.mp4":        filepath.Join(outside, "missing.mp4"),                   // dangling link
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(media, filepath.FromSlash(name))); err != nil {
			t.Skipf("symlinks not supported here: %v", err)
		}
	}

	res := scan(t, l)
	if res.Total != 3 {
		t.Fatalf("total = %d (%+v), want 3: the Jay folder once, the file link, the local song", res.Total, res)
	}
	got := titles(t, l, "")
	want := []string{"B|local", "C|file link", "周杰倫|晴天"}
	if len(got) != len(want) {
		t.Fatalf("songs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("songs = %v, want %v", got, want)
		}
	}

	// Files found through a link are served through the same relative path.
	songs, _, _ := l.Search("晴天", 1, 0)
	s, err := l.Get(songs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(media, filepath.FromSlash(s.Path)))
	if err != nil || string(b) != "Jay/周杰倫 - 晴天.mp4" {
		t.Fatalf("read via %q: %q, %v", s.Path, b, err)
	}
}

func TestDefaultExtensions(t *testing.T) {
	l, media := newLib(t, config.Config{Extensions: []string{".mp4", ".m4v", ".webm"}})
	for _, f := range []string{"A - a.mp4", "B - b.M4V", "C - c.webm", "D - d.mkv", "E - e.avi"} {
		touch(t, media, f)
	}
	if res := scan(t, l); res.Total != 3 {
		t.Fatalf("total = %d, want 3 (mp4, m4v, webm)", res.Total)
	}
}

// Databases written before the normalized search column get fixed by the next scan.
func TestScanRefreshesSearchKey(t *testing.T) {
	l, media := newLib(t, config.Config{})
	touch(t, media, "五月天 - 倔強.mp4")
	scan(t, l)
	if _, err := l.db.Exec(`UPDATE songs SET search = '五月天 倔強'`); err != nil { // old format
		t.Fatal(err)
	}
	if got := titles(t, l, "五月天倔強"); len(got) != 0 {
		t.Fatalf("old key unexpectedly matched: %v", got)
	}
	if res := scan(t, l); res.Updated != 1 {
		t.Fatalf("rescan = %+v, want 1 updated", res)
	}
	if got := titles(t, l, "五月天倔強"); len(got) != 1 {
		t.Fatalf("after rescan = %v", got)
	}
}
