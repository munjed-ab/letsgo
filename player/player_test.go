package player

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// newTestPlayer scans dir but does not start the audio loop: these tests are
// about queue logic only.
func newTestPlayer(t *testing.T, files ...string) *Player {
	dir := t.TempDir()
	for _, f := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	p := &Player{roots: []string{dir}, now: func() time.Duration { return 0 }, done: make(chan struct{})}
	p.Rescan()
	return p
}

func TestRescanFindsOnlyAudioSorted(t *testing.T) {
	p := newTestPlayer(t, "b.mp3", "a/z.FLAC", "a/notes.txt", "c.ogg", "cover.jpg")
	want := []string{"a/z.FLAC", "b.mp3", "c.ogg"}
	if got := p.Library(); !reflect.DeepEqual(got, want) {
		t.Errorf("library %v, want %v", got, want)
	}
}

func TestSetQueue(t *testing.T) {
	p := newTestPlayer(t, "a.mp3", "b.mp3", "c.mp3", "d.mp3")

	if !p.SetQueue([]string{"c.mp3", "a.mp3", "d.mp3"}, 1, "Mix") {
		t.Fatal("SetQueue failed")
	}
	if s := p.State(); s.Track != "a.mp3" || s.Index != 1 || s.Count != 3 || s.Context != "Mix" || !s.Playing {
		t.Errorf("state %+v", s)
	}

	// Unknown tracks are dropped (also keeps callers from making us open
	// arbitrary files); the index follows the track it pointed at.
	p.SetQueue([]string{"../../etc/x.mp3", "nope.mp3", "b.mp3", "d.mp3"}, 3, "Mix")
	if s := p.State(); s.Track != "d.mp3" || s.Count != 2 {
		t.Errorf("after filtering: %+v", s)
	}
	// If the chosen track is unknown, playback starts at the next playable one.
	p.SetQueue([]string{"a.mp3", "nope.mp3", "c.mp3"}, 1, "Mix")
	if s := p.State(); s.Track != "c.mp3" {
		t.Errorf("unknown start track: %+v", s)
	}
	if p.SetQueue([]string{"nope.mp3"}, 0, "Mix") {
		t.Error("an all-unknown queue was accepted")
	}
	if s := p.State(); s.Track != "c.mp3" {
		t.Error("a rejected SetQueue changed the queue")
	}

	// nil = the whole library
	p.SetQueue(nil, 2, "")
	if s := p.State(); s.Track != "c.mp3" || s.Count != 4 || s.Context != "" {
		t.Errorf("library queue %+v", s)
	}
	if p.SetQueue(nil, 99, "") {
		t.Error("out-of-range library index accepted")
	}
}

func TestNextPrevWrapAndRestart(t *testing.T) {
	p := newTestPlayer(t, "a.mp3", "b.mp3", "c.mp3")
	p.SetQueue([]string{"a.mp3", "b.mp3", "c.mp3"}, 2, "Q")
	p.Next()
	if p.State().Track != "a.mp3" {
		t.Error("Next did not wrap")
	}
	p.Prev()
	if p.State().Track != "c.mp3" {
		t.Error("Prev did not wrap")
	}
	// Past 3 s into a track, Prev restarts it instead of going back.
	p.frames = 5 * Rate
	p.Prev()
	if p.State().Track != "c.mp3" {
		t.Error("Prev after 5 s should restart the track")
	}
}

func TestShuffleNeverRepeatsCurrent(t *testing.T) {
	p := newTestPlayer(t, "a.mp3", "b.mp3", "c.mp3")
	p.SetQueue(nil, 0, "")
	p.SetShuffle(true)
	seen := map[string]bool{}
	prev := p.State().Track
	for i := 0; i < 200; i++ {
		p.Next()
		cur := p.State().Track
		if cur == prev {
			t.Fatal("shuffle repeated the same track back to back")
		}
		seen[cur], prev = true, cur
	}
	if len(seen) != 3 {
		t.Errorf("shuffle only visited %v", seen)
	}
}

// A playlist queue must survive a library rescan; only the "everything" queue
// follows the library.
func TestRescanKeepsPlaylistQueue(t *testing.T) {
	p := newTestPlayer(t, "a.mp3", "b.mp3")
	p.SetQueue([]string{"b.mp3"}, 0, "Mix")
	os.WriteFile(filepath.Join(p.roots[0], "c.mp3"), []byte("x"), 0o644)
	p.Rescan()
	if s := p.State(); s.Count != 1 || s.Track != "b.mp3" {
		t.Errorf("playlist queue changed by rescan: %+v", s)
	}
	p.SetQueue(nil, 0, "")
	os.WriteFile(filepath.Join(p.roots[0], "d.mp3"), []byte("x"), 0o644)
	p.Rescan()
	if s := p.State(); s.Count != 4 {
		t.Errorf("library queue did not follow rescan: %+v", s)
	}
}

func TestToggleWithEmptyQueueDoesNotStart(t *testing.T) {
	p := newTestPlayer(t)
	p.Toggle()
	if p.State().Playing {
		t.Error("playing with nothing to play")
	}
}

// Several music folders: the first keeps plain ids (so saved favourites survive
// adding folders), the others are prefixed with their folder name.
func TestMultipleRoots(t *testing.T) {
	a, b, c := t.TempDir(), filepath.Join(t.TempDir(), "Downloads"), filepath.Join(t.TempDir(), "Downloads")
	for _, f := range []string{filepath.Join(a, "Rock", "x.mp3"), filepath.Join(b, "y.mp3"), filepath.Join(b, "deep", "z.flac"), filepath.Join(c, "w.ogg")} {
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte("x"), 0o644)
	}
	p := &Player{now: func() time.Duration { return 0 }, done: make(chan struct{})}
	p.SetRoots([]string{a, b, c})
	want := []string{"Downloads (2)/w.ogg", "Downloads/deep/z.flac", "Downloads/y.mp3", "Rock/x.mp3"}
	if got := p.Library(); !reflect.DeepEqual(got, want) {
		t.Fatalf("library %v, want %v", got, want)
	}

	// ids resolve to the right file, and only known ids can be queued
	if !p.SetQueue([]string{"Downloads/y.mp3", "../../etc/passwd.mp3", "Rock/x.mp3"}, 0, "Mix") {
		t.Fatal("SetQueue failed")
	}
	if got := p.abs["Rock/x.mp3"]; got != filepath.Join(a, "Rock", "x.mp3") {
		t.Errorf("Rock/x.mp3 -> %s", got)
	}
	if s := p.State(); s.Count != 2 || s.Track != "Downloads/y.mp3" {
		t.Errorf("state %+v", s)
	}

	// dropping a folder removes its tracks; the first folder's ids do not change
	p.SetRoots([]string{a})
	if got := p.Library(); !reflect.DeepEqual(got, []string{"Rock/x.mp3"}) {
		t.Errorf("after removing folders: %v", got)
	}
	p.SetRoots(nil)
	if len(p.Library()) != 0 {
		t.Error("no folders should mean an empty library")
	}
}

func TestHeardPos(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	hist := []histEntry{{ms(0), 0}, {ms(20), 882}, {ms(40), 1764}, {ms(60), 2646}}
	for _, c := range []struct {
		name     string
		hist     []histEntry
		produced int
		target   time.Duration
		want     float64
	}{
		{"no history: fall back to what was produced", nil, 44100, ms(5), 1.0},
		{"nothing of the new audio heard yet: its start", hist, 9999, ms(-500), 0},
		{"exactly at a chunk", hist, 9999, ms(20), 0.02},
		{"inside a chunk", hist, 9999, ms(30), 0.02 + 0.010},
		{"paused: caps at the end of the last chunk", hist, 9999, ms(5000), 2646/44100.0 + 0.020},
	} {
		if got := heardPos(c.hist, c.produced, c.target); math.Abs(got-c.want) > 0.0005 {
			t.Errorf("%s: %.4f, want %.4f", c.name, got, c.want)
		}
	}
}

// The real audio loop end to end: duration, heard position, and seeking while
// playing and while paused, judged by the pitch of the chunks actually broadcast.
func TestPlayerSeekEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	dir := t.TempDir()
	data, err := os.ReadFile("testdata/sweep44100.flac") // 440 Hz, then 880, then 1760, one second each
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "sweep.flac"), data, 0o644)

	var mu sync.Mutex
	var chunks [][]int16
	sink := sinkFn(func(ts time.Duration, pcm []byte) {
		v := make([]int16, len(pcm)/2)
		for i := range v {
			v[i] = int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		}
		mu.Lock()
		chunks = append(chunks, v)
		mu.Unlock()
	})
	start := time.Now()
	p := New([]string{dir}, sink, func() time.Duration { return time.Since(start) })
	defer p.Close()
	p.SetBuffer(500 * time.Millisecond)
	p.PlayIndex(0)

	time.Sleep(1200 * time.Millisecond)
	s := p.State()
	if math.Abs(s.Duration-3) > 0.1 {
		t.Errorf("duration %.2f, want ~3", s.Duration)
	}
	// 1.2 s after pressing play, the first startLead (350 ms) of that has not been heard yet
	if s.Elapsed < 0.7 || s.Elapsed > 1.0 {
		t.Errorf("heard position %.2f s, want ~0.85", s.Elapsed)
	}

	pitchAfter := func(from int, n int) float64 {
		mu.Lock()
		defer mu.Unlock()
		var pcm []int16
		for _, c := range chunks[from : from+n] {
			pcm = append(pcm, c...)
		}
		return freq(pcm)
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(chunks) }

	// seek while playing
	before := count()
	p.Seek(1.5)
	if e := p.State().Elapsed; math.Abs(e-1.5) > 0.05 {
		t.Errorf("right after Seek(1.5) the position is %.2f", e)
	}
	time.Sleep(300 * time.Millisecond)
	// chunks made after the seek: skip a few for anything already in flight
	if hz := pitchAfter(before+3, 8); math.Abs(hz-880) > 0.08*880 {
		t.Errorf("after seeking to 1.5 s the stream is %.0f Hz, want 880", hz)
	}

	// seek while paused, then resume
	p.Toggle()
	time.Sleep(100 * time.Millisecond)
	paused := count()
	p.Seek(2.2)
	if e := p.State().Elapsed; math.Abs(e-2.2) > 0.05 {
		t.Errorf("paused Seek(2.2): position %.2f", e)
	}
	time.Sleep(100 * time.Millisecond)
	if count() != paused {
		t.Error("audio kept flowing while paused")
	}
	p.Toggle()
	time.Sleep(300 * time.Millisecond)
	if hz := pitchAfter(paused+3, 8); math.Abs(hz-1760) > 0.08*1760 {
		t.Errorf("after resuming from a seek to 2.2 s the stream is %.0f Hz, want 1760", hz)
	}

	// junk is ignored
	p.Seek(math.NaN())
	p.Seek(-5)
	if e := p.State().Elapsed; e < 0 || e > 3.1 {
		t.Errorf("position after junk seeks: %.2f", e)
	}
}

type sinkFn func(ts time.Duration, pcm []byte)

func (f sinkFn) Broadcast(ts time.Duration, pcm []byte) { f(ts, pcm) }
func (f sinkFn) Flush()                                 {}

// recSink records what the audio loop sends, in order.
type recSink struct {
	mu sync.Mutex
	ev []recEvent
}
type recEvent struct {
	flush bool
	ts    time.Duration
}

func (r *recSink) Broadcast(ts time.Duration, pcm []byte) {
	r.mu.Lock()
	r.ev = append(r.ev, recEvent{ts: ts})
	r.mu.Unlock()
}
func (r *recSink) Flush() { r.mu.Lock(); r.ev = append(r.ev, recEvent{flush: true}); r.mu.Unlock() }
func (r *recSink) since(n int) []recEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recEvent(nil), r.ev[n:]...)
}
func (r *recSink) n() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.ev) }

// Play, pause, resume and next must reach the listeners at once: the loop flushes them and
// stamps the first new chunk startLead from now, not a whole buffer from now.
func TestControlsAreHeardQuickly(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	dir := t.TempDir()
	data, _ := os.ReadFile("testdata/sweep44100.flac")
	os.WriteFile(filepath.Join(dir, "a.flac"), data, 0o644)
	os.WriteFile(filepath.Join(dir, "b.flac"), data, 0o644)
	rec := &recSink{}
	start := time.Now()
	now := func() time.Duration { return time.Since(start) }
	p := New([]string{dir}, rec, now)
	defer p.Close()
	const buffer = time.Second
	p.SetBuffer(buffer)

	// after a command: the first thing sent is a Flush, then chunks; the first chunk is heard
	// about startLead after the command (stamp + buffer), and nothing was skipped
	check := func(what string, pressed time.Duration, evs []recEvent) {
		t.Helper()
		if len(evs) < 10 || !evs[0].flush || evs[1].flush {
			t.Fatalf("%s: want a Flush then chunks, got %d events, first flush=%v", what, len(evs), len(evs) > 0 && evs[0].flush)
		}
		heardAt := evs[1].ts + buffer
		if lag := heardAt - pressed; lag < startLead-100*time.Millisecond || lag > startLead+100*time.Millisecond {
			t.Errorf("%s: first chunk is heard %v after the command, want ~%v", what, lag.Round(time.Millisecond), startLead)
		}
		for i := 2; i < len(evs); i++ {
			if evs[i].flush || evs[i].ts != evs[i-1].ts+chunkDur {
				t.Fatalf("%s: chunks are not one contiguous timeline at event %d", what, i)
			}
		}
	}

	mark, pressed := rec.n(), now()
	p.PlayIndex(0)
	time.Sleep(400 * time.Millisecond)
	check("play", pressed, rec.since(mark))

	mark = rec.n()
	p.Pause()
	time.Sleep(200 * time.Millisecond)
	if evs := rec.since(mark); len(evs) == 0 || !evs[0].flush {
		t.Fatalf("pause: listeners were not flushed at once: %v", evs)
	}
	after := rec.n()
	time.Sleep(200 * time.Millisecond)
	if rec.n() != after {
		t.Error("pause: chunks kept coming")
	}

	mark, pressed = rec.n(), now()
	p.Resume()
	time.Sleep(400 * time.Millisecond)
	check("resume", pressed, rec.since(mark))

	mark, pressed = rec.n(), now()
	p.Next()
	time.Sleep(400 * time.Millisecond)
	check("next", pressed, rec.since(mark))

	mark, pressed = rec.n(), now()
	p.Seek(1.5)
	time.Sleep(400 * time.Millisecond)
	check("seek", pressed, rec.since(mark))
}

// Shuffle deals a deck: every track once before any repeats, and a track never comes back soon
// after a round ends (random picks with replacement made songs circle back within a few plays).
func TestShuffleDealsEveryTrackBeforeRepeating(t *testing.T) {
	var files []string
	for i := 0; i < 30; i++ {
		files = append(files, string(rune('a'+i/10))+string(rune('a'+i%10))+".mp3")
	}
	p := newTestPlayer(t, files...)
	p.SetQueue(nil, 0, "")
	p.SetShuffle(true)
	var order []string
	for i := 0; i < 300; i++ {
		p.Next()
		order = append(order, p.State().Track)
	}
	for r := 0; r < 10; r++ {
		seen := map[string]bool{}
		for _, tr := range order[r*30 : r*30+30] {
			seen[tr] = true
		}
		if len(seen) != 30 {
			t.Fatalf("round %d played %d different tracks out of 30", r, len(seen))
		}
	}
	last := map[string]int{}
	for i, tr := range order {
		if j, ok := last[tr]; ok && i-j-1 < 8 { // the last 8 played wait until the end of the next round
			t.Fatalf("%s played again after only %d other tracks", tr, i-j-1)
		}
		last[tr] = i
	}
	// and a shuffle is not the alphabet
	inOrder := 0
	for i := 1; i < 30; i++ {
		if order[i] > order[i-1] {
			inOrder++
		}
	}
	if inOrder == 29 {
		t.Error("shuffled order is the sorted order")
	}
}

// Previous in shuffle goes back through what was actually played.
func TestShufflePrevWalksBack(t *testing.T) {
	var files []string
	for i := 0; i < 12; i++ {
		files = append(files, string(rune('a'+i))+".mp3")
	}
	p := newTestPlayer(t, files...)
	p.SetQueue(nil, 0, "")
	p.SetShuffle(true)
	played := []string{p.State().Track}
	for i := 0; i < 5; i++ {
		p.Next()
		played = append(played, p.State().Track)
	}
	for i := len(played) - 2; i >= 0; i-- {
		p.Prev()
		if got := p.State().Track; got != played[i] {
			t.Fatalf("Prev went to %s, want %s", got, played[i])
		}
	}
}

func TestListenedEnough(t *testing.T) {
	sec := func(s float64) int { return int(s * Rate) }
	for _, c := range []struct {
		heard int
		dur   float64
		want  bool
	}{
		{sec(29), 200, false}, {sec(30), 200, true}, // a long song counts after 30 s
		{sec(29), 0, false}, {sec(30), 0, true}, // unknown length: 30 s too
		{sec(4), 10, false}, {sec(5), 10, true}, // a short one after half
		{sec(1), 200, false},
	} {
		if got := listenedEnough(c.heard, c.dur); got != c.want {
			t.Errorf("listenedEnough(%.0fs, %.0fs) = %v", float64(c.heard)/Rate, c.dur, got)
		}
	}
}

// Plays are counted once per listen, not on a skip, and again when a track is played again.
func TestPlaysAreCountedOncePerListen(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	dir := t.TempDir()
	data, _ := os.ReadFile("testdata/sweep44100.flac") // 3 s long: counts after 1.5 s
	os.WriteFile(filepath.Join(dir, "a.flac"), data, 0o644)
	os.WriteFile(filepath.Join(dir, "b.flac"), data, 0o644)
	var mu sync.Mutex
	var got []string
	start := time.Now()
	p := New([]string{dir}, &recSink{}, func() time.Duration { return time.Since(start) })
	defer p.Close()
	p.SetPlayedHook(func(tr string) { mu.Lock(); got = append(got, tr); mu.Unlock() })
	counted := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) }

	p.SetQueue([]string{"a.flac", "b.flac"}, 0, "Q")
	time.Sleep(700 * time.Millisecond)
	p.Next() // skipped after 0.7 s: not a play
	time.Sleep(300 * time.Millisecond)
	if c := counted(); len(c) != 0 {
		t.Fatalf("skipped tracks were counted: %v", c)
	}
	time.Sleep(1500 * time.Millisecond) // b has now been listened to for ~1.8 s
	if c := counted(); len(c) != 1 || c[0] != "b.flac" {
		t.Fatalf("after listening to b.flac: %v", c)
	}
	p.Seek(0.1) // seeking back over the same track must not count it again
	time.Sleep(600 * time.Millisecond)
	if c := counted(); len(c) != 1 {
		t.Fatalf("seeking counted a play again: %v", c)
	}
}
