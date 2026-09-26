package snap

import (
	"encoding/json"
	"log"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxBuffered = 5 * time.Second // jitter buffer capacity; oldest audio is dropped beyond it
	maxGapFill  = 2 * time.Second // bigger timestamp gaps/overlaps reseed the buffer instead
	contigTol   = 500 * time.Microsecond

	hardResync = 100 * time.Millisecond // |error| beyond this: skip or pad instead of slewing
	slewOn     = 3 * time.Millisecond   // start correcting when the smoothed error passes this...
	slewOff    = 1 * time.Millisecond   // ...stop when it is back inside this
	slewRate   = 0.002                  // playback speed change while correcting (0.2%, inaudible)
	errTau     = 4 * time.Second        // smoothing of the per-read error; Read call times are jittery
	clockWin   = 16                     // time-sync samples kept
	clockBest  = 4                      // lowest-RTT samples the offset is taken from
	stallLog   = 500 * time.Millisecond // log when audio stops arriving for this long
)

// Client is the receive side of the protocol: it connects to a letsgo/snapcast
// server, keeps its clock synced, and implements io.Reader so an audio device
// (oto, AudioTrack) can pull "the audio that should be coming out right now".
//
// Sync model: every WireChunk carries the server time ts of its first frame; the
// frame must be HEARD at server time ts + buffer (+ this device's latency knob).
// A frame we hand to the audio device now is heard outLat later (the device's
// queue + hardware delay, see SetOutputLatency), so each Read schedules against
// serverNow + outLat. Large errors are fixed by skipping or padding silence,
// small ones by playing 0.2% fast or slow until they are gone.
type Client struct {
	conn                 net.Conn
	Rate, Bits, Channels int

	writeMu sync.Mutex
	nextID  uint16

	clock func() time.Duration // Now, replaceable in tests

	mu         sync.Mutex
	samples    []timeSample  // recent time-sync samples
	offset     time.Duration // server clock - our clock
	haveOffset bool
	bufferDur  time.Duration
	latencyDur time.Duration // per-device fudge; + delays this device
	outLat     time.Duration // audio device queue + hardware delay
	vol        int
	muted      bool

	// jitter buffer: buf[r:w] are contiguous frames; the frame at r has server
	// time base + consumed frames. Tracking frames (not durations) keeps the
	// timeline exact: per-frame rounding would drift and look like a gap.
	buf        []byte
	r, w       int
	base       time.Duration
	consumed   int64
	frameBytes int

	// playout control
	aligned bool
	pos     float64 // fractional read position in frames, only used while slewing
	slew    int     // +1 playing slow (we are early), -1 fast (late), 0 exact
	errF    float64 // smoothed schedule error, seconds

	underruns, resyncs int
	epoch              uint16 // timeline restarts seen so far (see WireChunkMsg)

	Ready chan struct{} // closed once the codec header arrives
	live  atomic.Bool   // false once the connection drops
}

type timeSample struct{ rtt, off time.Duration }

// Stats is a snapshot for diagnostics (exposed on /api/state).
type Stats struct {
	Underruns int     `json:"underruns"` // times the device outran the buffer
	Resyncs   int     `json:"resyncs"`   // hard re-alignments after a lock
	SyncErrMs float64 `json:"syncErrMs"` // smoothed schedule error
	BufferMs  int     `json:"bufferMs"`  // audio currently buffered
	RTTMs     float64 `json:"rttMs"`     // best recent round trip to the server
	Aligned   bool    `json:"aligned"`
}

// Live reports whether the connection is still up (false after the peer drops).
func (c *Client) Live() bool { return c.live.Load() }

// Connect dials the server. name is how this device shows up in the server's
// listener list; latencyMs is a per-device offset (positive = play later).
func Connect(addr, name string, latencyMs int) (*Client, error) {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	c := &Client{
		conn:       conn,
		clock:      Now,
		latencyDur: time.Duration(latencyMs) * time.Millisecond,
		bufferDur:  time.Second, // until ServerSettings arrives
		vol:        100,
		Ready:      make(chan struct{}),
	}
	c.live.Store(true)
	c.send(HelloMsg(1, name, name))
	go c.readLoop()
	go c.pinger()
	return c, nil
}

func (c *Client) send(b []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	StampSent(b) // stamp our clock the instant before the bytes leave
	c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_, err := c.conn.Write(b)
	return err
}

func (c *Client) pinger() {
	// A quick burst locks the clock fast, then one per second keeps it.
	for i := 0; i < 8; i++ {
		if c.ping() != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		if c.ping() != nil {
			return
		}
	}
}

func (c *Client) ping() error {
	c.writeMu.Lock()
	c.nextID++
	id := c.nextID
	c.writeMu.Unlock()
	return c.send(TimePing(id))
}

func (c *Client) readLoop() {
	defer c.live.Store(false)
	defer c.conn.Close()
	buf := make([]byte, 0, 8192) // reused across messages; no per-chunk garbage
	var lastChunk time.Time
	for {
		c.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		h, p, err := ReadMsgInto(c.conn, buf) // h.Received stamped on our clock here
		if err != nil {
			return
		}
		buf = p[:0] // keep the (possibly grown) backing array for next read
		switch h.Type {
		case TypeServerSettings:
			c.applySettings(p)
		case TypeCodecHeader:
			if codec, rate, bits, ch, ok := ParseCodecHeader(p); ok && codec == "pcm" && bits == 16 && ch > 0 && rate > 0 {
				c.mu.Lock()
				c.Rate, c.Bits, c.Channels = rate, bits, ch
				c.frameBytes = ch * bits / 8
				c.mu.Unlock()
				select {
				case <-c.Ready:
				default:
					close(c.Ready)
				}
			}
		case TypeTime:
			// NTP: T0=our send, T1=server recv, T2=server send, T3=our recv.
			// payload = T1-T0 ; h.Sent = T2 ; h.Received = T3.
			payload := ParseTimeReply(p)
			c.addTimeSample(payload+(h.Received-h.Sent), (payload+(h.Sent-h.Received))/2)
		case TypeWireChunk:
			if gap := time.Since(lastChunk); !lastChunk.IsZero() && gap > stallLog {
				log.Printf("audio gap: nothing from the server for %v (a pause, or a network stall)", gap.Round(time.Millisecond))
			}
			lastChunk = time.Now()
			c.pushChunkEpoch(p, h.RefersTo)
		}
	}
}

// addTimeSample records one ping (rtt on our clock, offset = server - us). The
// offset is the median of the few lowest-RTT recent samples: a fast round trip
// has the least queuing noise, and the median shrugs off Wi-Fi asymmetry outliers.
func (c *Client) addTimeSample(rtt, off time.Duration) {
	if rtt < 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, timeSample{rtt, off})
	if len(c.samples) > clockWin {
		c.samples = c.samples[1:]
	}
	best := append([]timeSample(nil), c.samples...)
	sort.Slice(best, func(i, j int) bool { return best[i].rtt < best[j].rtt })
	best = best[:min(clockBest, len(best))]
	sort.Slice(best, func(i, j int) bool { return best[i].off < best[j].off })
	c.offset, c.haveOffset = best[len(best)/2].off, true
}

func (c *Client) applySettings(p []byte) {
	if len(p) < 4 {
		return
	}
	var s struct {
		BufferMs int  `json:"bufferMs"`
		Volume   *int `json:"volume"`
		Muted    bool `json:"muted"`
	}
	if json.Unmarshal(p[4:], &s) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.BufferMs > 0 && time.Duration(s.BufferMs)*time.Millisecond != c.bufferDur {
		c.bufferDur = time.Duration(s.BufferMs) * time.Millisecond
		c.aligned = false
	}
	if s.Volume != nil {
		c.vol = max(0, min(100, *s.Volume))
	}
	c.muted = s.Muted
}

// ---- jitter buffer (all called with c.mu held) ----

func (c *Client) frameDur(n int64) time.Duration {
	return time.Duration(n * int64(time.Second) / int64(c.Rate))
}
func (c *Client) frames(d time.Duration) int64 { return int64(d) * int64(c.Rate) / int64(time.Second) }
func (c *Client) avail() int                   { return (c.w - c.r) / c.frameBytes }
func (c *Client) headTS() time.Duration        { return c.base + c.frameDur(c.consumed) }
func (c *Client) endTS() time.Duration         { return c.base + c.frameDur(c.consumed+int64(c.avail())) }
func (c *Client) serverNow() time.Duration     { return c.clock() + c.offset }

// drop discards n frames from the front (already-late audio).
func (c *Client) drop(n int) {
	c.r += n * c.frameBytes
	c.consumed += int64(n)
	if c.r == c.w {
		c.r, c.w = 0, 0
	}
	if c.consumed > 1<<27 { // renormalize so consumed*1e9 can't overflow on a marathon
		c.base += c.frameDur(c.consumed)
		c.consumed = 0
	}
}

func (c *Client) reseed(ts time.Duration) {
	c.r, c.w = 0, 0
	c.base, c.consumed = ts, 0
	c.aligned = false
}

// write appends frames (already frame-aligned), dropping the oldest on overflow.
func (c *Client) write(data []byte) {
	if c.buf == nil {
		c.buf = make([]byte, int(c.frames(maxBuffered))*c.frameBytes)
	}
	if len(data) > len(c.buf) {
		data = data[len(data)-len(c.buf):]
	}
	if c.w+len(data) > len(c.buf) {
		copy(c.buf, c.buf[c.r:c.w])
		c.w -= c.r
		c.r = 0
	}
	if over := c.w + len(data) - len(c.buf); over > 0 {
		c.drop((over + c.frameBytes - 1) / c.frameBytes)
		copy(c.buf, c.buf[c.r:c.w])
		c.w -= c.r
		c.r = 0
	}
	c.w += copy(c.buf[c.w:], data)
}

// pushChunkEpoch is pushChunk for a chunk that carries the server's timeline epoch:
// when it changes (pause, play, jump, seek) whatever is queued is dropped.
func (c *Client) pushChunkEpoch(p []byte, epoch uint16) {
	c.mu.Lock()
	if epoch != c.epoch {
		c.epoch = epoch
		c.r, c.w, c.aligned = 0, 0, false
	}
	c.mu.Unlock()
	c.pushChunk(p)
}

func (c *Client) pushChunk(p []byte) {
	if len(p) < 12 {
		return
	}
	ts := getTV(p)
	size := int(le.Uint32(p[8:]))
	if size < 0 || len(p) < 12+size {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frameBytes == 0 {
		return
	}
	data := p[12 : 12+size]
	data = data[:len(data)/c.frameBytes*c.frameBytes]

	if c.w == c.r {
		c.reseed(ts) // empty (start, or underrun): begin a new timeline here
	} else if d := ts - c.endTS(); d > contigTol && d <= maxGapFill {
		// Gap (pause, dropped chunks): fill with silence so the buffer stays one
		// contiguous timeline and the schedule logic just skips or plays through it.
		gap := make([]byte, int(c.frames(d))*c.frameBytes)
		c.write(gap)
	} else if d < -contigTol && d >= -maxGapFill {
		// Overlap: drop the part we already have.
		skip := int(c.frames(-d)) * c.frameBytes
		if skip >= len(data) {
			return
		}
		data = data[skip:]
	} else if d > maxGapFill || d < -maxGapFill {
		c.reseed(ts)
	}
	c.write(data)
}

// ---- playout ----

// Read is the audio callback: it fills out with the audio due to be heard
// outLat from now (silence if there is none). It always fills all of out.
func (c *Client) Read(out []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(out)
	fb := c.frameBytes
	if fb == 0 || !c.haveOffset {
		return len(out), nil // no format or no clock lock yet
	}
	if c.w == c.r { // nothing buffered
		if c.aligned {
			c.underruns++
			c.aligned = false
		}
		return len(out), nil
	}
	outFrames := len(out) / fb
	dt := c.frameDur(int64(outFrames))

	// The frame at the head is heard at now+outLat and should be heard at
	// ts+buffer+latency, so it is "early" by:
	err := c.headTS() - (c.serverNow() + c.outLat - c.bufferDur - c.latencyDur)

	pad := 0
	if !c.aligned || err > hardResync || err < -hardResync {
		if c.aligned {
			c.resyncs++
		}
		c.errF, c.slew, c.pos = 0, 0, 0
		if err < 0 { // late: skip the stale frames
			skip := c.frames(-err)
			if skip >= int64(c.avail()) {
				c.drop(c.avail())
				c.aligned = false
				return len(out), nil
			}
			c.drop(int(skip))
		} else { // early: silence until it is due
			p := c.frames(err)
			if p >= int64(outFrames) {
				c.aligned = false // keep padding exactly until it is due
				return len(out), nil
			}
			pad = int(p)
		}
		c.aligned = true
	} else {
		a := float64(dt) / float64(errTau+dt)
		c.errF += a * (err.Seconds() - c.errF)
		switch {
		case c.slew == 0 && c.errF > slewOn.Seconds():
			c.slew = 1
		case c.slew == 0 && c.errF < -slewOn.Seconds():
			c.slew = -1
		case c.slew == 1 && c.errF < slewOff.Seconds(), c.slew == -1 && c.errF > -slewOff.Seconds():
			c.slew = 0
		}
	}

	want := outFrames - pad
	dst := out[pad*fb:]
	got := 0
	if c.slew == 0 {
		c.pos = 0
		got = min(want, c.avail())
		copy(dst, c.buf[c.r:c.r+got*fb])
		c.drop(got)
	} else {
		r := 1 - slewRate*float64(c.slew) // early: slower, late: faster
		c.errF -= (1 - r) * dt.Seconds()  // what this read's speed change buys us
		got = c.resample(dst, want, r)
	}
	if got < want {
		c.underruns++
		c.aligned = false // re-align when audio flows again
	}
	c.applyVolume(out[pad*fb : (pad+got)*fb])
	return len(out), nil
}

// resample writes up to want frames into dst reading the buffer at speed r
// (linear interpolation) and returns how many it wrote.
func (c *Client) resample(dst []byte, want int, r float64) int {
	ch := c.frameBytes / 2
	avail := c.avail()
	src := c.buf[c.r:c.w]
	n := 0
	for ; n < want; n++ {
		ip := int(c.pos)
		if ip+1 >= avail {
			break
		}
		f := c.pos - float64(ip)
		for k := 0; k < ch; k++ {
			a := float64(int16(le.Uint16(src[(ip*ch+k)*2:])))
			b := float64(int16(le.Uint16(src[((ip+1)*ch+k)*2:])))
			le.PutUint16(dst[(n*ch+k)*2:], uint16(int16(a+(b-a)*f)))
		}
		c.pos += r
	}
	used := min(int(c.pos), avail)
	c.pos -= float64(used)
	c.drop(used)
	return n
}

func (c *Client) applyVolume(b []byte) {
	if c.muted {
		clear(b)
		return
	}
	if c.vol >= 100 {
		return
	}
	for i := 0; i+1 < len(b); i += 2 {
		s := int32(int16(le.Uint16(b[i:])))
		le.PutUint16(b[i:], uint16(int16(s*int32(c.vol)/100)))
	}
}

// SetLatency adjusts the per-device delay live (calibration knob).
func (c *Client) SetLatency(ms int) {
	c.mu.Lock()
	c.latencyDur = time.Duration(ms) * time.Millisecond
	c.aligned = false // re-align exactly instead of slewing a big step
	c.mu.Unlock()
}

// SetOutputLatency tells the client how long audio handed to the device takes to
// be heard: the device's queued audio plus hardware delay. Platform glue knows
// this (AudioTrack timestamps on Android, buffer sizes on desktop). It may be
// updated on every read; the client only re-aligns hard if the error gets large.
func (c *Client) SetOutputLatency(d time.Duration) {
	c.mu.Lock()
	c.outLat = d
	c.mu.Unlock()
}

// Stats returns a diagnostics snapshot.
func (c *Client) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Stats{Underruns: c.underruns, Resyncs: c.resyncs, SyncErrMs: c.errF * 1000, Aligned: c.aligned}
	if c.frameBytes > 0 {
		s.BufferMs = int(c.frameDur(int64(c.avail())) / time.Millisecond)
	}
	if len(c.samples) > 0 {
		best := c.samples[0].rtt
		for _, x := range c.samples {
			best = min(best, x.rtt)
		}
		s.RTTMs = float64(best) / float64(time.Millisecond)
	}
	return s
}

// Close stops the client.
func (c *Client) Close() error { return c.conn.Close() }
