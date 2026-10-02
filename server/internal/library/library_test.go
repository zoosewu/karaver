package library

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"karaver/internal/config"
	"karaver/internal/db"
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
	for _, f := range []string{"五月天 - 倔強.mp4", "五月天 - 知足.mp4", "Queen - 100% Pure.mp4", "A_B - x.mp4"} {
		touch(t, media, f)
	}
	scan(t, l)

	if got := titles(t, l, "五月天 倔"); len(got) != 1 || got[0] != "五月天|倔強" {
		t.Fatalf("multi-term = %v", got)
	}
	if got := titles(t, l, "QUEEN"); len(got) != 1 {
		t.Fatalf("case-insensitive = %v", got)
	}
	// LIKE wildcards in the query are literal.
	if got := titles(t, l, "%"); len(got) != 1 || got[0] != "Queen|100% Pure" {
		t.Fatalf("percent = %v", got)
	}
	if got := titles(t, l, "_"); len(got) != 1 || got[0] != "A_B|x" {
		t.Fatalf("underscore = %v", got)
	}

	page, more, _ := l.Search("", 3, 0)
	rest, more2, _ := l.Search("", 3, 3)
	if len(page) != 3 || !more || len(rest) != 1 || more2 {
		t.Fatalf("paging: %d %v %d %v", len(page), more, len(rest), more2)
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
