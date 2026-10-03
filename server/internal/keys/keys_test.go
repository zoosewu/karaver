package keys

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

type fakeLib struct{ songs []Song } // newest first

func (f *fakeLib) KeySong(id int64) (Song, bool) {
	for _, s := range f.songs {
		if s.ID == id {
			return s, true
		}
	}
	return Song{}, false
}

func (f *fakeLib) KeySongs() ([]Song, error) { return f.songs, nil }

// newTest returns a renderer that plans and indexes but never runs ffmpeg.
func newTest(t *testing.T, lib *fakeLib, queued ...int64) *Renderer {
	k := New(t.TempDir(), "", lib)
	k.ffmpeg = "ffmpeg" // pretend it is installed
	os.MkdirAll(k.dir, 0o755)
	k.queued = func() []int64 { return queued }
	return k
}

// done records a render as if the worker had finished it.
func (k *Renderer) done(t *testing.T, j job) {
	t.Helper()
	name := fileName(j.song, j.semis, j.q)
	if err := os.WriteFile(filepath.Join(k.dir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	if k.ready[j.song.ID] == nil {
		k.ready[j.song.ID] = map[int]string{}
	}
	if prev, ok := k.ready[j.song.ID][j.semis]; !ok || j.q == high {
		if ok {
			os.Remove(filepath.Join(k.dir, prev))
		}
		k.ready[j.song.ID][j.semis] = name
	}
	k.mu.Unlock()
}

type step struct {
	song  int64
	semis int
	q     quality
}

func TestRenderOrder(t *testing.T) {
	lib := &fakeLib{songs: []Song{{ID: 3, Size: 1, Mtime: 3}, {ID: 2, Size: 1, Mtime: 2}, {ID: 1, Size: 1, Mtime: 1}}}
	k := newTest(t, lib, 1, 2) // song 1 playing, song 2 next; song 3 only in the library

	var got []step
	for {
		j, ok := k.next()
		if !ok {
			break
		}
		got = append(got, step{j.song.ID, j.semis, j.q})
		k.done(t, j)
	}
	keysOf := func(group []int, songs []int64, q quality) []step {
		var out []step
		for _, s := range songs {
			for _, semis := range group {
				out = append(out, step{s, semis, q})
			}
		}
		return out
	}
	var want []step
	want = append(want, keysOf(nearKeys, []int64{1, 2}, fast)...) // queued, fast: ±1..3 of both songs
	want = append(want, keysOf(farKeys, []int64{1, 2}, fast)...)  // then ±4..6
	want = append(want, keysOf(nearKeys, []int64{1, 2}, high)...) // queued, high quality
	want = append(want, keysOf(farKeys, []int64{1, 2}, high)...)
	want = append(want, keysOf(nearKeys, []int64{3}, high)...) // the rest of the library, high quality only
	want = append(want, keysOf(farKeys, []int64{3}, high)...)
	if !slices.Equal(got, want) {
		t.Fatalf("render order:\n got %v\nwant %v", got, want)
	}

	// High-quality renders replaced the fast ones on disk.
	entries, _ := os.ReadDir(k.dir)
	if len(entries) != 36 {
		t.Fatalf("%d files in the cache, want 36 (3 songs × 12 keys)", len(entries))
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".m4a" || e.Name()[len(e.Name())-6:] != "-q.m4a" {
			t.Fatalf("leftover render %s", e.Name())
		}
	}
}

func TestWantJumpsTheLine(t *testing.T) {
	lib := &fakeLib{songs: []Song{{ID: 2, Size: 1, Mtime: 2}, {ID: 1, Size: 1, Mtime: 1}}}
	k := newTest(t, lib, 1)
	k.Want(1, -5)
	if j, _ := k.next(); j.song.ID != 1 || j.semis != -5 || j.q != fast || !j.urgent {
		t.Fatalf("first job %+v, want song 1 at -5 (fast, urgent)", j)
	}
	k.done(t, job{song: lib.songs[1], semis: -5, q: fast})
	if j, _ := k.next(); j.urgent || j.semis != 1 {
		t.Fatalf("after the urgent key: %+v, want the normal order (+1)", j)
	}
	if got := k.Ready(1); !slices.Equal(got, []int{-5}) {
		t.Fatalf("ready %v", got)
	}
	k.Want(1, -5) // already rendered: nothing to do
	if len(k.urgent) != 0 {
		t.Fatalf("urgent %v", k.urgent)
	}
}

func TestCleanupDropsChangedVideos(t *testing.T) {
	lib := &fakeLib{songs: []Song{{ID: 1, Size: 10, Mtime: 100}, {ID: 2, Size: 20, Mtime: 200}}}
	k := newTest(t, lib)
	write := func(name string) {
		os.WriteFile(filepath.Join(k.dir, name), nil, 0o644)
	}
	write(fileName(lib.songs[0], 2, high))
	write(fileName(lib.songs[0], 3, fast))
	write(fileName(lib.songs[0], 3, high)) // replaces the fast +3
	write(fileName(lib.songs[1], -1, fast))
	write(fileName(Song{ID: 2, Size: 20, Mtime: 150}, 1, high)) // an older version of song 2's video
	write(fileName(Song{ID: 9, Size: 1, Mtime: 1}, 1, high))    // a song that left the library
	write("1-10-100-+4-q.m4a.part")                             // interrupted render

	k.Cleanup()
	entries, _ := os.ReadDir(k.dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	want := []string{"1-10-100-+2-q.m4a", "1-10-100-+3-q.m4a", "2-20-200--1-f.m4a"}
	if !slices.Equal(names, want) {
		t.Fatalf("after cleanup %v, want %v", names, want)
	}
	if got := k.Ready(1); !slices.Equal(got, []int{2, 3}) {
		t.Fatalf("song 1 ready %v", got)
	}
	if p, ok := k.Path(2, -1); !ok || filepath.Base(p) != "2-20-200--1-f.m4a" {
		t.Fatalf("song 2 path %q %v", p, ok)
	}
}

func TestProgress(t *testing.T) {
	lib := &fakeLib{songs: []Song{{ID: 1, Path: "a/Singer - One.mp4", Size: 1, Mtime: 1}, {ID: 2, Size: 1, Mtime: 2}}}
	k := newTest(t, lib)
	for _, semis := range append(slices.Clone(nearKeys), farKeys...) {
		k.done(t, job{song: lib.songs[0], semis: semis, q: high})
	}
	k.done(t, job{song: lib.songs[1], semis: 1, q: fast})
	k.recordHigh(20 * time.Second)
	want := "library 12/24 keys in high quality (50%), 1/2 songs complete, 1 fast renders awaiting high quality, about 4m left"
	if got := k.progress(); got != want {
		t.Fatalf("progress:\n got %q\nwant %q", got, want)
	}
	if got := songName(lib.songs[0]); got != "Singer - One" {
		t.Fatalf("song name %q", got)
	}
}
