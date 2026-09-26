package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// PlayStat is how often a track has been listened to.
type PlayStat struct {
	N    int   `json:"n"`
	Last int64 `json:"last"` // unix seconds of the latest play, breaks ties (newest first)
}

// Plays counts listens per track, for the "Most Played" playlist. A play is counted by the
// player once a track has really been listened to (see player.playedAfter), not on a skip.
// Saved after every play (atomic rename), so a crash keeps the counts.
type Plays struct {
	mu     sync.Mutex
	path   string              // "" = in memory only
	Tracks map[string]PlayStat `json:"tracks"`
}

func OpenPlays(path string) *Plays {
	p := &Plays{path: path, Tracks: map[string]PlayStat{}}
	if b, err := os.ReadFile(path); err == nil && path != "" {
		var saved Plays
		if json.Unmarshal(b, &saved) == nil && saved.Tracks != nil {
			p.Tracks = saved.Tracks
		}
	}
	return p
}

// Record counts one more listen of track.
func (p *Plays) Record(track string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.Tracks[track]
	p.Tracks[track] = PlayStat{s.N + 1, time.Now().Unix()}
	if p.path == "" {
		return
	}
	b, _ := json.Marshal(p)
	os.MkdirAll(filepath.Dir(p.path), 0o755)
	tmp := p.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, p.path)
	}
}

// Top is the most played tracks that keep says still exist, most played first (ties: the one
// played latest), at most limit of them, and how many times each was played.
func (p *Plays) Top(limit int, keep func(string) bool) ([]string, map[string]int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	top := make([]string, 0, len(p.Tracks))
	for t := range p.Tracks {
		if keep(t) {
			top = append(top, t)
		}
	}
	slices.SortFunc(top, func(a, b string) int {
		x, y := p.Tracks[a], p.Tracks[b]
		switch {
		case x.N != y.N:
			return y.N - x.N
		case x.Last != y.Last:
			return int(y.Last - x.Last)
		}
		return slices.Compare([]byte(a), []byte(b))
	})
	if len(top) > limit {
		top = top[:limit]
	}
	counts := make(map[string]int, len(top))
	for _, t := range top {
		counts[t] = p.Tracks[t].N
	}
	return top, counts
}
