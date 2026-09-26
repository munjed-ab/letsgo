package snap

import (
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"
)

// The simulator runs a Client in virtual time against a fake server and a fake
// audio device, so minutes of streaming test in milliseconds and the "true" hear
// time of every frame is known exactly.
//
// Stream content: frame k is L = (k+1)&0x7fff, R = (k+1)>>15, so any output frame
// can be traced back to its stream position (0,0 = silence).

const (
	simRate   = 44100
	simFrames = 882 // 20 ms
	simLead   = 60 * time.Millisecond
)

type simChunk struct {
	ts   time.Duration // server time of first frame
	k0   int           // stream index of first frame
	at   time.Duration // server time the chunk reaches the client
	data []byte
}

type sim struct {
	t      *testing.T
	c      *Client
	off    time.Duration // true server - client clock offset
	buffer time.Duration
	trueOut,
	assumedOut time.Duration // real device delay vs what the client is told

	chunks []simChunk
	next   int
}

func newSim(t *testing.T, buffer, trueOut, assumedOut time.Duration) *sim {
	s := &sim{t: t, off: 5*time.Second + 123456*time.Microsecond, buffer: buffer, trueOut: trueOut, assumedOut: assumedOut}
	s.c = &Client{
		Rate: simRate, Bits: 16, Channels: 2, frameBytes: 4,
		bufferDur: buffer, vol: 100, outLat: assumedOut,
		offset: s.off, haveOffset: true,
	}
	return s
}

// produce makes chunks covering [from, to) server time. pauses = [start,end) spans
// with no audio; the chunk after a pause is stamped at its end (like Player.startLocked).
// netJitter delays delivery by a random 0..netJitter, in order.
func (s *sim) produce(from, to time.Duration, pauses [][2]time.Duration, netJitter time.Duration, rng *rand.Rand) {
	ts, k := from, 0
	var lastAt time.Duration
	for ts < to {
		for _, p := range pauses {
			if ts >= p[0] && ts < p[1] {
				ts = p[1]
			}
		}
		data := make([]byte, simFrames*4)
		for i := 0; i < simFrames; i++ {
			v := k + i + 1
			le.PutUint16(data[i*4:], uint16(v&0x7fff))
			le.PutUint16(data[i*4+2:], uint16(v>>15))
		}
		at := ts - simLead
		if netJitter > 0 {
			at += time.Duration(rng.Int63n(int64(netJitter)))
		}
		at = max(at, lastAt)
		lastAt = at
		s.chunks = append(s.chunks, simChunk{ts, k, at, data})
		ts += 20 * time.Millisecond
		k += simFrames
	}
}

func (s *sim) tsOf(k int) time.Duration {
	i := sort.Search(len(s.chunks), func(i int) bool { return s.chunks[i].k0 > k }) - 1
	ch := s.chunks[i]
	return ch.ts + time.Duration(int64(k-ch.k0)*int64(time.Second)/simRate)
}

func wire(ch simChunk) []byte {
	p := make([]byte, 12+len(ch.data))
	putTV(p, ch.ts)
	le.PutUint32(p[8:], uint32(len(ch.data)))
	copy(p[12:], ch.data)
	return p
}

type simResult struct {
	errs       []time.Duration // true sync error per read with audio: heard - should-be-heard
	when       []time.Duration // device time of each err
	glitches   int             // non-contiguous output in exact mode
	underruns  int
	resyncs    int
	audioReads int
}

func (s *sim) startAt(server time.Duration) time.Duration { return server - s.off }

// run drives the device for d of device time. drift = device clock speed error
// (+1e-4 = consumes 100ppm faster than the system clock); callJitter = how much
// each Read call time deviates from the device's steady pace.
func (s *sim) run(d time.Duration, start time.Duration, drift float64, callJitter time.Duration, rng *rand.Rand) simResult {
	var res simResult
	const n = 2048 // frames per read, like AudioTrack/oto pulls
	out := make([]byte, n*4)
	tn := start // nominal (true) device time, client clock
	prevLast, prevExact := -2, false
	for tn < start+d {
		jit := time.Duration(0)
		if callJitter > 0 {
			jit = time.Duration((rng.Float64()*2 - 1) * float64(callJitter))
		}
		now := tn + jit
		s.c.clock = func() time.Duration { return now }
		for s.next < len(s.chunks) && s.chunks[s.next].at <= now+s.off {
			s.c.pushChunk(wire(s.chunks[s.next]))
			s.next++
		}
		slewBefore, rsBefore := s.c.slew, s.c.resyncs
		s.c.Read(out)
		exact := slewBefore == 0 && s.c.slew == 0 && s.c.resyncs == rsBefore

		first, last, cont := -1, -1, true
		for i := 0; i < n; i++ {
			l, r := le.Uint16(out[i*4:]), le.Uint16(out[i*4+2:])
			if l == 0 && r == 0 {
				continue // silence: padding before audio, or the tail of an underrun (counted separately)
			}
			k := int(l) + int(r)<<15 - 1
			if first < 0 {
				first = k
				// frame i is heard trueOut after it is handed over
				hear := tn + time.Duration(int64(i)*int64(time.Second)/simRate) + s.trueOut + s.off
				res.errs = append(res.errs, hear-(s.tsOf(k)+s.buffer))
				res.when = append(res.when, tn+s.off) // server time
				res.audioReads++
			} else if exact && k != last+1 {
				cont = false
			}
			last = k
		}
		if exact && first >= 0 {
			if !cont || (prevExact && first != prevLast+1) {
				res.glitches++
			}
		}
		prevLast, prevExact = last, exact && first >= 0 && cont
		tn += time.Duration(float64(n) * float64(time.Second) / (simRate * (1 + drift)))
	}
	res.underruns, res.resyncs = s.c.underruns, s.c.resyncs
	return res
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// maxAbs returns the largest |err| among reads whose device time is >= after.
func (r simResult) maxAbs(after time.Duration) time.Duration {
	var m time.Duration
	for i, e := range r.errs {
		if r.when[i] >= after {
			m = max(m, abs(e))
		}
	}
	return m
}

// The bug this guards: the old client rounded per-frame durations, drifted ~30us
// per second of audio, and after ~3.5 s decided the stream was "non-contiguous"
// and threw the whole buffer away. Steady playback must never resync.
func TestSteadyPlaybackNeverGlitches(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	s := newSim(t, time.Second, 130*time.Millisecond, 130*time.Millisecond)
	s.produce(10*time.Second, 200*time.Second, nil, 30*time.Millisecond, rng)
	res := s.run(90*time.Second, s.startAt(10*time.Second), 150e-6, 20*time.Millisecond, rng)

	if res.underruns != 0 {
		t.Errorf("underruns = %d, want 0", res.underruns)
	}
	if res.resyncs != 0 {
		t.Errorf("hard resyncs = %d, want 0 after the initial lock", res.resyncs)
	}
	if res.glitches != 0 {
		t.Errorf("glitches (non-contiguous output while playing exact) = %d", res.glitches)
	}
	if m := res.maxAbs(40 * time.Second); m > 5*time.Millisecond {
		t.Errorf("steady-state sync error up to %v, want <= 5ms", m)
	}
	if res.audioReads < 1500 {
		t.Errorf("only %d reads carried audio", res.audioReads)
	}
}

// Devices differ: a wrongly assumed output latency is a constant offset the
// client cannot see, and it must show up 1:1 (that is what -latency calibrates).
func TestOutputLatencyIsAccountedFor(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for _, assumed := range []time.Duration{0, 130 * time.Millisecond, 300 * time.Millisecond} {
		s := newSim(t, time.Second, 300*time.Millisecond, assumed)
		s.produce(10*time.Second, 60*time.Second, nil, 0, rng)
		res := s.run(30*time.Second, s.startAt(10*time.Second), 0, 0, rng)
		want := 300*time.Millisecond - assumed // the amount the client was misinformed by
		got := res.errs[len(res.errs)-1]
		if abs(got-want) > 2*time.Millisecond {
			t.Errorf("assumed %v: sync error %v, want %v", assumed, got, want)
		}
	}
}

// Two devices with different real latencies, each told the truth, must hear the
// same frame at the same instant: the whole point of the project.
func TestTwoDevicesStayTogether(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	phone := newSim(t, time.Second, 240*time.Millisecond, 240*time.Millisecond)
	pc := newSim(t, time.Second, 90*time.Millisecond, 90*time.Millisecond)
	pc.off = 700*time.Millisecond + 999*time.Microsecond // different clock, same stream
	pc.c.offset = pc.off
	for _, s := range []*sim{phone, pc} {
		s.produce(10*time.Second, 80*time.Second, nil, 25*time.Millisecond, rng)
	}
	rp := phone.run(40*time.Second, phone.startAt(10*time.Second), 80e-6, 15*time.Millisecond, rng)
	rc := pc.run(40*time.Second, pc.startAt(10*time.Second), -60e-6, 15*time.Millisecond, rng)
	ep, ec := rp.errs[len(rp.errs)-1], rc.errs[len(rc.errs)-1]
	if abs(ep-ec) > 6*time.Millisecond {
		t.Errorf("phone %v vs pc %v: %v apart", ep, ec, abs(ep-ec))
	}
}

// Pause: the server stops sending, clients drain and go silent, then resume with a
// timestamp jump. The client must re-lock right away, not play stale or garbled audio.
func TestPauseResume(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	s := newSim(t, time.Second, 130*time.Millisecond, 130*time.Millisecond)
	pause := [][2]time.Duration{{20*time.Second + 3*time.Millisecond, 26*time.Second + 7*time.Millisecond}}
	s.produce(10*time.Second, 60*time.Second, pause, 20*time.Millisecond, rng)
	res := s.run(45*time.Second, s.startAt(10*time.Second), 50e-6, 10*time.Millisecond, rng)

	if res.glitches != 0 {
		t.Errorf("glitches = %d", res.glitches)
	}
	if res.underruns > 1 { // the buffer running dry during the pause counts once
		t.Errorf("underruns = %d, want <= 1 (the pause)", res.underruns)
	}
	if m := res.maxAbs(36 * time.Second); m > 5*time.Millisecond { // 10s after resume
		t.Errorf("post-resume sync error %v, want <= 5ms", m)
	}
}

// A burst of late chunks (Wi-Fi stall, then everything arrives at once) must not
// play stale audio late: the client skips ahead to what is due.
func TestStallBurstSkipsToSchedule(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	s := newSim(t, time.Second, 130*time.Millisecond, 130*time.Millisecond)
	s.produce(10*time.Second, 40*time.Second, nil, 0, rng)
	// Stall: chunks produced in [20s,22s) arrive together at 22s.
	for i := range s.chunks {
		if a := s.chunks[i].at; a >= 20*time.Second && a < 22*time.Second {
			s.chunks[i].at = 22 * time.Second
		}
	}
	res := s.run(25*time.Second, s.startAt(10*time.Second), 0, 0, rng)
	if m := res.maxAbs(30 * time.Second); m > 3*time.Millisecond {
		t.Errorf("after stall, sync error %v", m)
	}
	// Nothing in the schedule may ever be played late by more than the stall recovery.
	for i, e := range res.errs {
		if res.when[i] > 23*time.Second && abs(e) > 3*time.Millisecond {
			t.Fatalf("read at %v: error %v after recovery", res.when[i], e)
		}
	}
}

func TestClockSyncTakesLowRTTMedian(t *testing.T) {
	c := &Client{}
	truth := 3*time.Second + 250*time.Microsecond
	rng := rand.New(rand.NewSource(6))
	for i := 0; i < 40; i++ {
		// Mostly noisy asymmetric samples (big RTT, offset wrong by up to rtt/2),
		// occasionally a clean fast one.
		rtt := 40*time.Millisecond + time.Duration(rng.Int63n(int64(60*time.Millisecond)))
		off := truth + time.Duration((rng.Float64()*2-1)*float64(rtt)/2)
		if i%5 == 0 {
			rtt, off = 3*time.Millisecond, truth+time.Duration((rng.Float64()*2-1)*float64(200*time.Microsecond))
		}
		c.addTimeSample(rtt, off)
	}
	if e := abs(c.offset - truth); e > time.Millisecond {
		t.Errorf("offset error %v, want < 1ms", e)
	}
	if s := c.Stats(); math.Abs(s.RTTMs-3) > 0.01 {
		t.Errorf("RTTMs = %v", s.RTTMs)
	}
}

func TestVolumeAndMute(t *testing.T) {
	s := newSim(t, time.Second, 0, 0)
	s.produce(10*time.Second, 20*time.Second, nil, 0, rand.New(rand.NewSource(7)))
	for s.next < 60 { // deliver ~1.2 s so the first frames are due
		s.c.pushChunk(wire(s.chunks[s.next]))
		s.next++
	}
	s.c.clock = func() time.Duration { return s.startAt(11*time.Second + 100*time.Millisecond) }
	out := make([]byte, 4096)
	s.c.vol = 50
	s.c.Read(out)
	peak := func() (m int) {
		for i := 0; i < len(out); i += 4 {
			m = max(m, int(le.Uint16(out[i:])))
		}
		return
	}
	if peak() == 0 {
		t.Fatal("no audio at 50%")
	}
	s.c.muted = true
	s.c.Read(out)
	if peak() != 0 {
		t.Error("muted output not silent")
	}
}

// pushChunk edge cases keep the buffer one contiguous timeline.
func TestChunkGapOverlapAndOverflow(t *testing.T) {
	c := &Client{Rate: simRate, frameBytes: 4}
	mk := func(ts time.Duration, frames int) []byte {
		data := make([]byte, frames*4)
		p := make([]byte, 12+len(data))
		putTV(p, ts)
		le.PutUint32(p[8:], uint32(len(data)))
		copy(p[12:], data)
		return p
	}
	c.pushChunk(mk(time.Second, 882))
	c.pushChunk(mk(time.Second+20*time.Millisecond, 882)) // contiguous
	if got := c.avail(); got != 1764 {
		t.Fatalf("contiguous: avail %d", got)
	}
	c.pushChunk(mk(time.Second+100*time.Millisecond, 882)) // 60ms gap -> silence filled
	if got := c.avail(); got != 1764+2646+882 {
		t.Errorf("gap: avail %d, want %d", got, 1764+2646+882)
	}
	end := c.endTS()
	c.pushChunk(mk(end-10*time.Millisecond, 882)) // 10ms overlap -> 441 new frames
	if got := c.endTS() - end; abs(got-10*time.Millisecond) > 50*time.Microsecond {
		t.Errorf("overlap: extended by %v, want 10ms", got)
	}
	c.pushChunk(mk(end+time.Hour, 882)) // absurd jump -> reseed
	if c.avail() != 882 || c.headTS() < time.Hour {
		t.Errorf("reseed: avail %d head %v", c.avail(), c.headTS())
	}
	// Overflow drops oldest audio but keeps the timeline exact.
	c = &Client{Rate: simRate, frameBytes: 4}
	ts := time.Second
	for i := 0; i < 400; i++ { // 8 s into a 5 s buffer
		c.pushChunk(mk(ts, 882))
		ts += 20 * time.Millisecond
	}
	if d := c.frameDur(int64(c.avail())); d > maxBuffered || d < maxBuffered-100*time.Millisecond {
		t.Errorf("buffered %v, want ~%v", d, maxBuffered)
	}
	if d := c.endTS() - ts; abs(d) > 50*time.Microsecond {
		t.Errorf("timeline off by %v after overflow", d)
	}
}
