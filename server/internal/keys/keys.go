// Package keys pre-renders songs at other keys with ffmpeg + Rubber Band, so the
// TV can switch key instantly by playing a rendered audio track in sync with the
// muted karaoke video.
//
// One worker renders one track at a time, in this order:
//
//  1. a key someone just asked for on a playing song that is not ready yet (fast)
//  2. queued songs, nearest first: ±1..±3, then ±4..±6 (fast)
//  3. the same again in high quality, replacing the fast renders
//  4. every other song in the library, newest first, in high quality
//
// Original-vocal tracks are never rendered: original vocals play at their own key.
// Renders are named after the source's size and mtime; when a video changes or
// disappears, its renders are deleted on the next library scan.
package keys

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// MaxSemis is the largest key change in either direction.
const MaxSemis = 6

// Rendering order of the keys: the common ones first.
var (
	nearKeys = []int{1, -1, 2, -2, 3, -3}
	farKeys  = []int{4, -4, 5, -5, 6, -6}
)

const renderTimeout = 30 * time.Minute

// Song is a library file to render, as stored in the songs table.
type Song struct {
	ID    int64
	Path  string // relative to the media directory
	Size  int64
	Mtime int64
}

// Library lists the songs that can be rendered (present in the media directory).
type Library interface {
	KeySong(id int64) (Song, bool)
	// KeySongs returns every present song, newest first.
	KeySongs() ([]Song, error)
}

type quality byte

const (
	fast quality = 'f'
	high quality = 'q'
)

type job struct {
	song   Song
	semis  int
	q      quality
	urgent bool
	tier   string // why it is rendered now, for the log
}

func fileName(s Song, semis int, q quality) string {
	return fmt.Sprintf("%d-%d-%d-%+d-%c.m4a", s.ID, s.Size, s.Mtime, semis, q)
}

func songPrefix(s Song) string { return fmt.Sprintf("%d-%d-%d-", s.ID, s.Size, s.Mtime) }

type Renderer struct {
	ffmpeg   string // "" when ffmpeg with the rubberband filter is not installed
	nice     string // run ffmpeg at low CPU priority when available
	dir      string
	mediaDir string
	lib      Library

	queued  func() []int64     // queued song ids, nearest first
	onReady func(songID int64) // a render finished
	wake    chan struct{}
	indexed chan struct{} // closed by the first Cleanup (after the first library scan)
	once    sync.Once

	mu      sync.Mutex
	ready   map[int64]map[int]string // song -> semis -> best rendered file
	failed  map[string]bool          // files whose render failed (not retried until restart)
	urgent  []job
	running *job
	cancel  context.CancelFunc
	avgHigh time.Duration // moving average of high-quality render times
}

// New detects ffmpeg; without it (the slim image) key change is unavailable.
func New(dataDir, mediaDir string, lib Library) *Renderer {
	k := &Renderer{
		dir: filepath.Join(dataDir, "keycache"), mediaDir: mediaDir, lib: lib,
		wake: make(chan struct{}, 1), indexed: make(chan struct{}), ready: map[int64]map[int]string{}, failed: map[string]bool{},
	}
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		log.Print("key change: unavailable (no ffmpeg in this image)")
		return k
	}
	out, err := exec.Command(path, "-hide_banner", "-filters").Output()
	if err != nil || !strings.Contains(string(out), " rubberband ") {
		log.Print("key change: unavailable (ffmpeg has no rubberband filter)")
		return k
	}
	if err := os.MkdirAll(k.dir, 0o755); err != nil {
		log.Printf("key change: unavailable: %v", err)
		return k
	}
	k.ffmpeg = path
	k.nice, _ = exec.LookPath("nice")
	return k
}

func (k *Renderer) Available() bool { return k != nil && k.ffmpeg != "" }

// Start runs the worker until ctx ends.
func (k *Renderer) Start(ctx context.Context, queued func() []int64, onReady func(songID int64)) {
	if !k.Available() {
		return
	}
	k.queued, k.onReady = queued, onReady
	go func() {
		// Until the first scan has indexed the cache, nothing is known to be rendered.
		select {
		case <-k.indexed:
			k.loop(ctx)
		case <-ctx.Done():
		}
	}()
}

// Wake makes the worker re-plan (the queue changed).
func (k *Renderer) Wake() {
	if !k.Available() {
		return
	}
	select {
	case k.wake <- struct{}{}:
	default:
	}
}

// Ready lists the rendered keys of a song.
func (k *Renderer) Ready(songID int64) []int {
	if !k.Available() {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]int, 0, len(k.ready[songID]))
	for semis := range k.ready[songID] {
		out = append(out, semis)
	}
	slices.Sort(out)
	return out
}

// Path returns the best rendered file of a song at a key.
func (k *Renderer) Path(songID int64, semis int) (string, bool) {
	if !k.Available() {
		return "", false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	name, ok := k.ready[songID][semis]
	if !ok {
		return "", false
	}
	return filepath.Join(k.dir, name), true
}

// Want puts a key that is needed right now (the song is playing) first in line,
// interrupting a less urgent render.
func (k *Renderer) Want(songID int64, semis int) {
	if !k.Available() || semis == 0 || semis < -MaxSemis || semis > MaxSemis {
		return
	}
	k.mu.Lock()
	if _, ok := k.ready[songID][semis]; ok {
		k.mu.Unlock()
		return
	}
	if !slices.ContainsFunc(k.urgent, func(j job) bool { return j.song.ID == songID && j.semis == semis }) {
		k.urgent = append(k.urgent, job{song: Song{ID: songID}, semis: semis, q: fast, urgent: true})
	}
	if k.running != nil && !k.running.urgent && k.cancel != nil {
		k.cancel()
	}
	k.mu.Unlock()
	k.Wake()
}

// Cleanup re-indexes the cache against the library: renders of changed or
// removed videos, leftovers of interrupted renders, and fast renders that have a
// high-quality replacement are deleted. Called after every library scan.
func (k *Renderer) Cleanup() {
	if !k.Available() {
		return
	}
	songs, err := k.lib.KeySongs()
	if err != nil {
		log.Printf("key change: %v", err)
		return
	}
	current := make(map[int64]string, len(songs))
	for _, s := range songs {
		current[s.ID] = songPrefix(s)
	}
	entries, err := os.ReadDir(k.dir)
	if err != nil {
		log.Printf("key change: %v", err)
		return
	}
	ready := map[int64]map[int]string{}
	removed := 0
	remove := func(name string) {
		if os.Remove(filepath.Join(k.dir, name)) == nil {
			removed++
		}
	}
	for _, e := range entries {
		name := e.Name()
		var id, size, mtime int64
		var semis int
		var q quality
		if n, _ := fmt.Sscanf(name, "%d-%d-%d-%d-%c.m4a", &id, &size, &mtime, &semis, &q); n != 5 ||
			!strings.HasSuffix(name, ".m4a") || !strings.HasPrefix(name, current[id]) || current[id] == "" {
			k.mu.Lock()
			busy := k.running != nil && strings.HasPrefix(name, fileName(k.running.song, k.running.semis, k.running.q))
			k.mu.Unlock()
			if !busy {
				remove(name)
			}
			continue
		}
		if ready[id] == nil {
			ready[id] = map[int]string{}
		}
		if prev, ok := ready[id][semis]; ok {
			// Keep the high-quality render, drop the other.
			if strings.HasSuffix(prev, "-q.m4a") {
				remove(name)
				continue
			}
			remove(prev)
		}
		ready[id][semis] = name
	}
	k.mu.Lock()
	k.ready = ready
	if r := k.running; r != nil && current[r.song.ID] != songPrefix(r.song) && k.cancel != nil {
		k.cancel() // its video changed or disappeared
	}
	k.mu.Unlock()
	if removed > 0 {
		log.Printf("key change: removed %d outdated renders", removed)
	}
	k.once.Do(func() { close(k.indexed) })
	k.Wake()
}

func (k *Renderer) loop(ctx context.Context) {
	log.Printf("key change: %s", k.progress())
	busy := false
	for {
		j, ok := k.next()
		if !ok {
			if busy {
				busy = false
				log.Printf("key change: idle, %s", k.progress())
			}
			select {
			case <-ctx.Done():
				return
			case <-k.wake:
				continue
			}
		}
		busy = true
		log.Printf("key change: rendering %s %+d (%s)", songName(j.song), j.semis, j.tier)
		jobCtx, cancel := context.WithTimeout(ctx, renderTimeout)
		k.mu.Lock()
		k.running, k.cancel = &j, cancel
		k.mu.Unlock()
		start := time.Now()
		err := k.render(jobCtx, j)
		took := time.Since(start)
		cancel()
		k.mu.Lock()
		k.running, k.cancel = nil, nil
		k.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, context.Canceled):
			log.Printf("key change: interrupted %s %+d (something more urgent came up)", songName(j.song), j.semis)
		case err != nil:
			log.Printf("key change: %v", err)
		default:
			if j.q == high {
				k.recordHigh(took)
			}
			log.Printf("key change: done %s %+d in %s; %s", songName(j.song), j.semis, took.Round(100*time.Millisecond), k.progress())
		}
	}
}

// recordHigh keeps a moving average of high-quality render times for the ETA.
func (k *Renderer) recordHigh(d time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.avgHigh == 0 {
		k.avgHigh = d
	} else {
		k.avgHigh = (k.avgHigh*4 + d) / 5
	}
}

// progress summarizes the whole library for the log, e.g.
// "library 14/36 keys in high quality (39%), 2 songs complete, about 7m left".
func (k *Renderer) progress() string {
	songs, err := k.lib.KeySongs()
	if err != nil {
		return err.Error()
	}
	total := len(songs) * 2 * MaxSemis
	k.mu.Lock()
	var highDone, fastOnly, complete int
	for _, s := range songs {
		n := 0
		for _, name := range k.ready[s.ID] {
			if strings.HasSuffix(name, "-q.m4a") {
				n++
			} else {
				fastOnly++
			}
		}
		highDone += n
		if n == 2*MaxSemis {
			complete++
		}
	}
	avg := k.avgHigh
	k.mu.Unlock()
	if total == 0 {
		return "library is empty"
	}
	msg := fmt.Sprintf("library %d/%d keys in high quality (%d%%), %d/%d songs complete",
		highDone, total, highDone*100/total, complete, len(songs))
	if fastOnly > 0 {
		msg += fmt.Sprintf(", %d fast renders awaiting high quality", fastOnly)
	}
	if left := total - highDone; left > 0 && avg > 0 {
		msg += ", about " + roughDuration(avg*time.Duration(left)) + " left"
	}
	return msg
}

func roughDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()+0.5))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// songName is the file name without folder and extension.
func songName(s Song) string {
	base := path.Base(s.Path)
	return strings.TrimSuffix(base, path.Ext(base))
}

// next picks the most urgent missing render.
func (k *Renderer) next() (job, bool) {
	k.mu.Lock()
	urgent := slices.Clone(k.urgent)
	k.mu.Unlock()

	var pick *job
	done := 0 // leading requests that are rendered or impossible
	for _, u := range urgent {
		s, ok := k.lib.KeySong(u.song.ID)
		if ok && !k.has(s, u.semis, false) && !k.hasFailed(s, u.semis, fast) {
			pick = &job{song: s, semis: u.semis, q: fast, urgent: true, tier: "asked for while playing"}
			break
		}
		done++
	}
	k.mu.Lock()
	k.urgent = k.urgent[min(done, len(k.urgent)):]
	k.mu.Unlock()
	if pick != nil {
		return *pick, true
	}

	var queued []Song
	for _, id := range k.queued() {
		if s, ok := k.lib.KeySong(id); ok {
			queued = append(queued, s)
		}
	}
	if j, ok := k.firstMissing(queued, fast, "queued, fast"); ok {
		return j, true
	}
	if j, ok := k.firstMissing(queued, high, "queued, high quality"); ok {
		return j, true
	}
	all, err := k.lib.KeySongs()
	if err != nil {
		log.Printf("key change: %v", err)
		return job{}, false
	}
	return k.firstMissing(all, high, "library, high quality")
}

// firstMissing walks the songs' ±1..±3, then their ±4..±6.
func (k *Renderer) firstMissing(songs []Song, q quality, tier string) (job, bool) {
	for _, group := range [][]int{nearKeys, farKeys} {
		for _, s := range songs {
			for _, semis := range group {
				if !k.has(s, semis, q == high) && !k.hasFailed(s, semis, q) {
					return job{song: s, semis: semis, q: q, tier: tier}, true
				}
			}
		}
	}
	return job{}, false
}

// has reports whether a render exists (onlyHigh: a high-quality one).
func (k *Renderer) has(s Song, semis int, onlyHigh bool) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	name, ok := k.ready[s.ID][semis]
	return ok && (!onlyHigh || strings.HasSuffix(name, "-q.m4a"))
}

func (k *Renderer) hasFailed(s Song, semis int, q quality) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.failed[fileName(s, semis, q)]
}

func (k *Renderer) render(ctx context.Context, j job) error {
	name := fileName(j.song, j.semis, j.q)
	dst := filepath.Join(k.dir, name)
	tmp := dst + ".part"
	src := filepath.Join(k.mediaDir, filepath.FromSlash(j.song.Path))
	pitchq := "quality"
	if j.q == fast {
		pitchq = "speed"
	}
	args := []string{"-nostdin", "-v", "error", "-i", src, "-vn", "-map", "0:a:0",
		"-af", fmt.Sprintf("rubberband=pitch=%.6f:pitchq=%s:formant=preserved", math.Pow(2, float64(j.semis)/12), pitchq),
		"-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", "-f", "mp4", "-y", tmp}
	cmd := exec.CommandContext(ctx, k.ffmpeg, args...)
	if k.nice != "" {
		cmd = exec.CommandContext(ctx, k.nice, append([]string{"-n", "10", k.ffmpeg}, args...)...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmp)
		if ctx.Err() != nil {
			return context.Canceled
		}
		k.mu.Lock()
		k.failed[name] = true
		k.mu.Unlock()
		return fmt.Errorf("render %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}

	k.mu.Lock()
	if k.ready[j.song.ID] == nil {
		k.ready[j.song.ID] = map[int]string{}
	}
	prev, had := k.ready[j.song.ID][j.semis]
	if had && prev != name && j.q == high {
		os.Remove(filepath.Join(k.dir, prev)) // the fast render it replaces
	}
	if !had || j.q == high {
		k.ready[j.song.ID][j.semis] = name
	}
	k.mu.Unlock()
	if !had && k.onReady != nil {
		k.onReady(j.song.ID)
	}
	return nil
}
