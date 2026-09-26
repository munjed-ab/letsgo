// Package player decodes local files and feeds timestamped chunks to a sink
// (the snap server). This is the part to hack your ideas into.
package player

import (
	"encoding/binary"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	Rate     = 44100 // stream format: most music is 44.1k, so most files need zero resampling
	Channels = 2
	Bits     = 16

	chunkDur    = 20 * time.Millisecond
	chunkFrames = Rate * int(chunkDur) / int(time.Second) // 882
	lead        = 60 * time.Millisecond                   // produce this far ahead of "now"

	// startLead is how soon after pressing play / next / seek the new audio is heard. It has to
	// cover the Wi-Fi hop and the phone's own audio queue (about 300 ms) so every device can
	// still start together; anything longer just makes the buttons feel slow.
	startLead = 350 * time.Millisecond

	// A track counts as played once this much of it has been listened to (or half of it, if
	// it is shorter than a minute): skipping through songs does not add plays.
	playedAfter = 30 * time.Second
)

// Sink receives audio. Implemented by snap.Server.
type Sink interface {
	Broadcast(ts time.Duration, pcm []byte)
	Flush() // listeners drop the audio they have queued: a new timeline starts
}

type State struct {
	Playing  bool    `json:"playing"`
	Index    int     `json:"index"` // position in the queue
	Track    string  `json:"track"`
	Elapsed  float64 `json:"elapsed"`  // seconds into the track as HEARD on synced devices
	Duration float64 `json:"duration"` // track length in seconds, 0 if unknown
	Shuffle  bool    `json:"shuffle"`
	Count    int     `json:"count"`   // tracks in the queue
	Context  string  `json:"context"` // what the queue is: "" = whole library, else a label set by the caller
	Started  bool    `json:"started"` // something has been played here (an idle queue does not count)
}

type Player struct {
	sink Sink
	now  func() time.Duration

	mu      sync.Mutex
	roots   []string          // music folders; the first one's tracks keep plain relative ids
	abs     map[string]string // track id -> file on disk
	lib     []string          // every track id found under the roots
	queue   []string          // what plays: the library, a playlist, a folder, favourites...
	ctx     string            // label for the queue, echoed in State
	idx     int               // position in queue
	src     source
	playing bool
	shuffle bool
	bag     []int         // shuffle: the deck still to deal this round; the next track is the last element
	trail   []int         // shuffle: tracks played before the current one, so Prev goes back to what you heard
	frames  int           // frames produced so far in the current track
	dur     float64       // current track length in seconds, 0 = unknown
	buffer  time.Duration // clients play a chunk this long after its timestamp
	hist    []histEntry   // recent chunks, to work out what is being heard now
	seekTo  int
	seeking bool // a Seek is waiting for the audio loop to apply it
	started bool // playback has been started at least once
	nextTS  time.Duration
	flush   bool // the next chunk starts a fresh timeline: flush the listeners first
	catchup bool // producing a burst that makes up the time between nextTS and now
	sent    bool // listeners hold audio from us (so a pause has something to flush)
	heard   int  // frames of the current track produced so far (seeking does not add to it)
	counted bool // the current track has been counted as played
	played  func(track string)
	jump    bool // user picked a track: drop current source
	done    chan struct{}
}

func New(roots []string, sink Sink, now func() time.Duration) *Player {
	p := &Player{roots: roots, sink: sink, now: now, done: make(chan struct{})}
	p.Rescan()
	go p.loop()
	return p
}

// histEntry remembers where in the track a chunk started, so the position shown
// can be that of the chunk being HEARD (timestamp + buffer) rather than the one
// just produced, which is a buffer ahead.
type histEntry struct {
	ts  time.Duration
	pos int // frames into the track at the start of the chunk
}

// SetBuffer tells the player how long after a chunk's timestamp it is heard.
// listenedEnough reports whether heard frames of a track dur seconds long (0 = unknown) count as a play.
func listenedEnough(heard int, dur float64) bool {
	need := playedAfter
	if dur > 0 {
		need = min(need, time.Duration(dur/2*float64(time.Second)))
	}
	return time.Duration(heard)*time.Second/Rate >= need
}

// SetPlayedHook makes f be called (from the audio loop, without holding any lock) with
// the id of each track once it counts as played.
func (p *Player) SetPlayedHook(f func(track string)) {
	p.mu.Lock()
	p.played = f
	p.mu.Unlock()
}

func (p *Player) SetBuffer(d time.Duration) {
	p.mu.Lock()
	p.buffer = d
	p.mu.Unlock()
}

// Seek moves playback to sec seconds into the current track. It works while
// playing or paused; on synced devices it is heard one buffer later, like every
// other command.
func (p *Player) Seek(sec float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 || sec != sec { // NaN guard
		return
	}
	f := int(max(sec, 0) * Rate)
	if p.dur > 0 {
		f = min(f, int(p.dur*Rate))
	}
	p.seekTo, p.seeking, p.frames, p.hist = f, true, f, nil
	if p.playing {
		p.restartLocked()
	}
}

// restartLocked makes the next chunk the start of a fresh timeline that is heard startLead
// from now, not a whole buffer from now: listeners drop what they have queued (see flush)
// and the chunks are stamped early and sent in a burst. Every device schedules by these
// stamps, so they all still start together.
func (p *Player) restartLocked() {
	p.nextTS = p.now() - p.buffer + min(startLead, p.buffer)
	p.flush, p.catchup = true, true
}

// heardPos is the position in seconds of the chunk audible at server time
// target, given the recent chunk history. produced is the fallback.
func heardPos(hist []histEntry, produced int, target time.Duration) float64 {
	if len(hist) == 0 {
		return float64(produced) / Rate
	}
	i := sort.Search(len(hist), func(i int) bool { return hist[i].ts > target }) - 1
	if i < 0 {
		return float64(hist[0].pos) / Rate // the new material has not been heard yet
	}
	return float64(hist[i].pos)/Rate + min(chunkDur, target-hist[i].ts).Seconds()
}

// Close stops the audio loop and releases the open file.
func (p *Player) Close() {
	close(p.done)
	p.mu.Lock()
	if p.src != nil {
		p.src.Close()
		p.src = nil
	}
	p.playing = false
	p.mu.Unlock()
}

// sleep waits d, returning false if the player was closed meanwhile.
func (p *Player) sleep(d time.Duration) bool {
	select {
	case <-p.done:
		return false
	case <-time.After(d):
		return true
	}
}

// SetRoots changes the music folders and rescans.
func (p *Player) SetRoots(roots []string) {
	p.mu.Lock()
	p.roots = slices.Clone(roots)
	p.mu.Unlock()
	p.Rescan()
}

// Rescan walks the music folders. Unreadable dirs are skipped, not fatal.
//
// Track ids are paths relative to their folder ("Rock/song.mp3"). The first
// folder keeps plain ids, so favourites and playlists made with one folder stay
// valid when more are added; every further folder is prefixed with its own name
// ("Downloads/song.mp3"), which shows up as a top-level folder in the UI.
func (p *Player) Rescan() {
	p.mu.Lock()
	roots := slices.Clone(p.roots)
	p.mu.Unlock()

	abs := map[string]string{}
	var lib []string
	labels := map[string]bool{}
	for i, root := range roots {
		prefix := ""
		if i > 0 {
			label := filepath.Base(root)
			for n := 2; label == "" || label == "." || label == string(filepath.Separator) || labels[label]; n++ {
				label = fmt.Sprintf("%s (%d)", filepath.Base(root), n)
			}
			labels[label] = true
			prefix = label + "/"
		}
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() && Exts[strings.ToLower(filepath.Ext(path))] {
				rel, _ := filepath.Rel(root, path)
				id := prefix + filepath.ToSlash(rel)
				if _, dup := abs[id]; dup {
					log.Printf("library: %q exists in two folders, keeping the first", id)
					return nil
				}
				abs[id] = path
				lib = append(lib, id)
			}
			return nil
		})
	}
	sort.Strings(lib)
	p.mu.Lock()
	p.lib, p.abs = lib, abs
	if p.ctx == "" { // playing "everything": follow the library
		p.queue, p.bag, p.trail = lib, nil, nil
	}
	p.idx = min(p.idx, max(0, len(p.queue)-1))
	p.mu.Unlock()
	log.Printf("library: %d tracks in %d folder(s)", len(lib), len(roots))
}

func (p *Player) Library() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lib...)
}

func (p *Player) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := State{Playing: p.playing, Index: p.idx, Shuffle: p.shuffle, Count: len(p.queue),
		Context: p.ctx, Started: p.started, Duration: p.dur, Elapsed: heardPos(p.hist, p.frames, p.now()-p.buffer)}
	if p.idx < len(p.queue) {
		s.Track = p.queue[p.idx]
	}
	return s
}

// ---- commands (called from HTTP) ----

// PlayIndex plays track i of the whole library, and the library becomes the queue.
func (p *Player) PlayIndex(i int) { p.SetQueue(nil, i, "") }

// SetQueue replaces the queue and starts playing track idx of it. tracks nil
// means the whole library. Anything not in the library is dropped (this is what
// stops a caller from making us open arbitrary files); idx refers to the list as
// given. Returns false if nothing playable remains.
func (p *Player) SetQueue(tracks []string, idx int, ctx string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if tracks == nil {
		p.queue, p.ctx, p.bag, p.trail = p.lib, "", nil, nil
		if idx < 0 || idx >= len(p.queue) {
			return false
		}
		p.idx, p.jump, p.seeking = idx, true, false
		p.startLocked()
		return true
	}
	known := p.abs // ids we know: anything else is dropped
	q, at := make([]string, 0, len(tracks)), -1
	for i, t := range tracks {
		if i == idx {
			at = len(q) // if this track is unknown, playback starts at the next playable one
		}
		if _, ok := known[t]; ok {
			q = append(q, t)
		}
	}
	if len(q) == 0 {
		return false
	}
	if at < 0 || at >= len(q) {
		at = 0
	}
	p.queue, p.ctx, p.idx, p.jump, p.seeking = q, ctx, at, true, false
	p.bag, p.trail = nil, nil
	p.startLocked()
	return true
}

func (p *Player) Toggle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.playing {
		p.playing = false
	} else if len(p.queue) > 0 {
		p.startLocked()
	}
}

// Pause and Resume are the explicit halves of Toggle (media buttons send them separately).
func (p *Player) Pause() {
	p.mu.Lock()
	p.playing = false
	p.mu.Unlock()
}

func (p *Player) Resume() {
	p.mu.Lock()
	if !p.playing && len(p.queue) > 0 {
		p.startLocked()
	}
	p.mu.Unlock()
}

// Files maps every track id to its file on disk.
func (p *Player) Files() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := make(map[string]string, len(p.abs))
	for k, v := range p.abs {
		m[k] = v
	}
	return m
}

func (p *Player) Next() {
	p.mu.Lock()
	p.advanceLocked(1)
	p.jump, p.seeking = true, false
	if p.playing {
		p.restartLocked()
	}
	p.mu.Unlock()
}

// Prev restarts the current track if it has been playing a while, else goes back.
func (p *Player) Prev() {
	p.mu.Lock()
	if p.frames < 3*Rate {
		p.advanceLocked(-1)
	}
	p.jump, p.seeking = true, false
	if p.playing {
		p.restartLocked()
	}
	p.mu.Unlock()
}

func (p *Player) SetShuffle(on bool) {
	p.mu.Lock()
	if p.shuffle != on {
		p.shuffle, p.bag, p.trail = on, nil, nil
	}
	p.mu.Unlock()
}

func (p *Player) startLocked() {
	p.started, p.playing = true, true
	p.restartLocked() // resume or a new queue = a fresh timeline
}

func (p *Player) advanceLocked(dir int) {
	n := len(p.queue)
	if n == 0 {
		return
	}
	if p.shuffle && n > 1 {
		if dir > 0 {
			p.trail = append(p.trail, p.idx)
			if len(p.trail) > 200 {
				p.trail = p.trail[100:]
			}
			p.idx = p.dealLocked()
		} else if k := len(p.trail); k > 0 { // back to what was heard before
			p.idx, p.trail = p.trail[k-1], p.trail[:k-1]
		}
	} else {
		p.idx = (p.idx + dir + n) % n // wraps: repeat-all
	}
}

// dealLocked is the shuffle: the queue is dealt as a shuffled deck, so every track plays once
// before any plays again (picking at random each time makes songs come back within a few
// plays). A new deck holds the last few tracks played back until the end, so a track never
// returns right after the round that just ended.
func (p *Player) dealLocked() int {
	if len(p.bag) == 0 {
		n := len(p.queue)
		held := []int{p.idx}
		for i := len(p.trail) - 1; i >= 0 && len(held) < min(n/3, 8); i-- {
			if !slices.Contains(held, p.trail[i]) {
				held = append(held, p.trail[i])
			}
		}
		var rest []int
		for i := 0; i < n; i++ {
			if !slices.Contains(held, i) {
				rest = append(rest, i)
			}
		}
		swap := func(s []int) func(i, j int) { return func(i, j int) { s[i], s[j] = s[j], s[i] } }
		rand.Shuffle(len(rest), swap(rest))
		rand.Shuffle(len(held), swap(held))
		p.bag = append(held, rest...) // dealt from the end: the rest first, the held-back ones last
	}
	next := p.bag[len(p.bag)-1]
	p.bag = p.bag[:len(p.bag)-1]
	return next
}

// ---- the audio loop ----
//
// Every chunk gets a timestamp on the server clock. Clients play it at
// timestamp + buffer. We only need to deliver chunks *before* that moment,
// so we run a tiny bit ahead (lead) and sleep the rest. CPU use is near zero
// when paused and a fraction of a percent when playing.

func (p *Player) loop() {
	pcm16 := make([]int16, chunkFrames*Channels)
	out := make([]byte, len(pcm16)*2)
	for {
		p.mu.Lock()
		if !p.playing || len(p.queue) == 0 {
			paused := p.sent
			p.sent = false
			p.mu.Unlock()
			if paused {
				p.sink.Flush() // stop now: listeners must not play out the audio they hold
			}
			if !p.sleep(20 * time.Millisecond) {
				return
			}
			continue
		}
		now := p.now()
		if p.catchup && p.nextTS >= now {
			p.catchup = false
		}
		// Fell behind by a lot (phone froze, GC, whatever)? Don't burst to
		// catch up, just jump the timeline forward. Never stall, never flood.
		// (A restart is behind on purpose: see restartLocked.)
		if !p.catchup && now-p.nextTS > 300*time.Millisecond {
			log.Printf("player fell behind by %v, skipping ahead", (now - p.nextTS).Round(time.Millisecond))
			p.nextTS = now
		}
		if p.nextTS > now+lead {
			wait := p.nextTS - now - lead
			p.mu.Unlock()
			if !p.sleep(wait) {
				return
			}
			continue
		}
		ok := p.fillLocked(pcm16)
		ts := p.nextTS
		p.nextTS += chunkDur
		flush := ok && p.flush
		if flush {
			p.flush = false
		}
		var played func(string)
		var playedTrack string
		if ok {
			p.sent = true
			p.heard += chunkFrames
			if !p.counted && listenedEnough(p.heard, p.dur) {
				p.counted = true
				played, playedTrack = p.played, p.queue[p.idx]
			}
			p.hist = append(p.hist, histEntry{ts, max(0, p.frames-chunkFrames)})
			if len(p.hist) > 400 { // ~8 s, far more than any buffer
				p.hist = p.hist[len(p.hist)-300:]
			}
		}
		p.mu.Unlock()

		if !ok {
			continue
		}
		for i, v := range pcm16 {
			binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
		}
		if played != nil {
			played(playedTrack)
		}
		if flush {
			p.sink.Flush()
		}
		p.sink.Broadcast(ts, out)
	}
}

// fillLocked fills one chunk, crossing track boundaries without a gap.
// Returns false if nothing could be played (e.g. every file is broken).
func (p *Player) fillLocked(dst []int16) bool {
	n, fails := 0, 0
	for n < len(dst) {
		if p.jump && p.src != nil {
			p.src.Close()
			p.src = nil
		}
		p.jump = false
		if p.src == nil {
			s, err := open(p.abs[p.queue[p.idx]])
			if err != nil {
				log.Printf("skip: %v", err) // broken file: skip it, keep the music going
				if fails++; fails >= len(p.queue) {
					p.playing = false
					return false
				}
				p.advanceLocked(1)
				continue
			}
			p.heard, p.counted = 0, false
			p.src, p.frames, p.hist, p.dur = newResampler(s, Rate), 0, nil, 0
			if f := p.src.Frames(); f > 0 {
				p.dur = float64(f) / Rate
			}
			log.Printf("playing %s (%d Hz)", p.queue[p.idx], s.Rate())
		}
		if p.seeking {
			p.seeking = false
			if err := p.src.SeekFrame(int64(p.seekTo)); err != nil {
				log.Printf("seek: %v", err)
			} else {
				p.frames, p.hist = p.seekTo, nil
			}
		}
		got, err := p.src.Read(dst[n:])
		n += got
		p.frames += got / Channels
		if err != nil { // EOF or decode error: next track
			p.src.Close()
			p.src = nil
			p.advanceLocked(1)
			if got == 0 { // empty/corrupt file counts as a failure too
				if fails++; fails >= len(p.queue) {
					p.playing = false
					return false
				}
			}
		}
	}
	return true
}
