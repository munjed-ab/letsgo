package snap

import (
	"bytes"
	"log"
	"math"
	"net"
	"time"

	"github.com/thesyncim/gopus"
)

// Opus on the wire. Uncompressed the stream is 1.4 Mbit/s; a phone in Wi-Fi power save on a busy
// 2.4 GHz channel (or downloading a YouTube video at the same time) cannot always send that, and
// a listener that gets less than real time drops out no matter how deep its buffer. At 192 kbit/s
// Opus needs a seventh of the airtime and is transparent for music.
//
// Opus only takes 48 kHz (not the stream's 44.1), so each 20 ms chunk is resampled up before it
// is encoded and back down after it is decoded: 882 frames -> 960 -> 882, exactly one packet per
// chunk, so every packet keeps its chunk's timestamp. The listener moves that timestamp back by
// what the codec and the two resamplers delay the sound (opusDelay), which keeps it in step with
// devices that get PCM.
//
// Only letsgo listeners on another device ask for it (see HelloMsg): the source's own speaker
// keeps bit-exact PCM, and stock snapclients and older letsgo builds get PCM as before.

const (
	opusRate    = 48000
	opusBitrate = 192000
	opusFrames  = opusRate / 50 // 20 ms
	rsTaps      = 48            // per output sample; ~0.5 ms of each resampler's delay
)

// opusMarker opens the codec header, as in Snapcast's own Opus header (then rate, bits, channels).
const opusMarker = 0x4F505553

// opusHeader is the codec header for an Opus stream that decodes to rate/bits/ch PCM.
func opusHeader(rate, bits, ch int) []byte {
	b := make([]byte, 12)
	le.PutUint32(b[0:], opusMarker)
	le.PutUint32(b[4:], uint32(rate))
	le.PutUint16(b[8:], uint16(bits))
	le.PutUint16(b[10:], uint16(ch))
	return b
}

// opusDelay is how much later than its timestamp a chunk's sound comes out of the decoder: the
// encoder's lookahead plus the group delay of the resampler on each side (half its taps, at its
// input rate).
func opusDelay(rate, lookahead int) time.Duration {
	return time.Duration(lookahead)*time.Second/opusRate +
		time.Duration(rsTaps/2)*time.Second/time.Duration(rate) +
		time.Duration(rsTaps/2)*time.Second/opusRate
}

// opusEnc turns 20 ms PCM chunks at the stream rate into Opus packets.
type opusEnc struct {
	enc *gopus.Encoder
	up  *resampler
	in  []int16
	pkt []byte
}

func newOpusEnc(rate, ch int) (*opusEnc, error) {
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: opusRate, Channels: ch, Application: gopus.ApplicationAudio})
	if err != nil {
		return nil, err
	}
	if err := enc.SetBitrate(opusBitrate * ch / 2); err != nil {
		return nil, err
	}
	return &opusEnc{enc: enc, up: newResampler(rate, opusRate, ch), pkt: make([]byte, 4000)}, nil
}

// encode returns the packet for one 20 ms chunk (16-bit little-endian), nil if it is not one.
// A panic in the codec is caught: the chunk is lost and the encoder starts over, but the app, and
// with it the music on every device, keeps going.
func (e *opusEnc) encode(pcm []byte) (pkt []byte) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("opus: encoder failed (%v), starting it over", r)
			func() { defer func() { recover() }(); e.reset() }() // even a reset that fails must not take the app down
			pkt = nil
		}
	}()
	e.in = bytesToPCM(e.in[:0], pcm)
	out := e.up.process(e.in)
	if len(out) != opusFrames*e.up.ch {
		return nil
	}
	n, err := e.enc.EncodeInt16(out, e.pkt)
	if err != nil {
		return nil
	}
	return append([]byte(nil), e.pkt[:n]...)
}

// reset starts a new timeline: nothing of the old one may leak into the first packets.
func (e *opusEnc) reset() { e.enc.Reset(); e.up.reset() }

// opusDec turns the packets back into PCM at the stream rate.
type opusDec struct {
	dec   *gopus.Decoder
	down  *resampler
	pcm   []int16
	out   []byte
	delay time.Duration
}

func newOpusDec(rate, ch int) (*opusDec, error) {
	dec, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(opusRate, ch))
	if err != nil {
		return nil, err
	}
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: opusRate, Channels: ch, Application: gopus.ApplicationAudio})
	if err != nil {
		return nil, err
	}
	return &opusDec{dec: dec, down: newResampler(opusRate, rate, ch), pcm: make([]int16, opusFrames*ch*6),
		delay: opusDelay(rate, enc.Lookahead())}, nil
}

// decode returns the PCM (16-bit little-endian) of one packet; nil if it does not decode. A panic
// in the codec is caught like the encoder's.
func (d *opusDec) decode(pkt []byte) (pcm []byte) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("opus: decoder failed (%v), starting it over", r)
			func() { defer func() { recover() }(); d.reset() }() // even a reset that fails must not take the app down
			pcm = nil
		}
	}()
	n, err := d.dec.DecodeInt16(pkt, d.pcm)
	if err != nil {
		return nil
	}
	out := d.down.process(d.pcm[:n*d.down.ch])
	d.out = d.out[:0]
	for _, v := range out {
		d.out = le.AppendUint16(d.out, uint16(v))
	}
	return d.out
}

func (d *opusDec) reset() { d.dec.Reset(); d.down.reset() }

func bytesToPCM(dst []int16, b []byte) []int16 {
	for i := 0; i+1 < len(b); i += 2 {
		dst = append(dst, int16(le.Uint16(b[i:])))
	}
	return dst
}

// resampler converts between two rates whose ratio is a small fraction (44.1 <-> 48 kHz is 147:160)
// with a Kaiser-windowed sinc, exact phases and no drift. It is streaming: it keeps the last
// rsTaps-1 input frames, and starts primed with that many zeros, so from the very first chunk every
// call returns exactly in*L/M frames (882 <-> 960) and the output lags by rsTaps/2 input frames.
type resampler struct {
	l, m, ch int
	h        [][]float32 // per phase: rsTaps coefficients
	hist     []int16     // interleaved input not yet fully used, starting at input frame `at`
	pos      int64       // next output frame
	at       int64       // input frame index of hist[0]
	out      []int16
}

func newResampler(from, to, ch int) *resampler {
	g := gcd(from, to)
	l, m := to/g, from/g                             // output frame n reads input at n*m/l
	fc := 0.5 * min(1, float64(l)/float64(m)) * 0.95 // cutoff in cycles per input frame: under both Nyquists
	const beta = 8.0
	h := make([][]float32, l)
	for p := range h {
		frac := float64(p) / float64(l)
		h[p] = make([]float32, rsTaps)
		var sum float64
		taps := make([]float64, rsTaps)
		for k := range taps {
			t := float64(k) - (rsTaps/2 - 1) - frac // distance from the point being interpolated
			x := 2 * fc * t
			s := 2 * fc
			if x != 0 {
				s = math.Sin(math.Pi*x) / (math.Pi * t)
			}
			w := t / (rsTaps / 2)
			if w*w < 1 {
				s *= bessel0(beta*math.Sqrt(1-w*w)) / bessel0(beta)
			} else {
				s = 0
			}
			taps[k], sum = s, sum+s
		}
		for k, v := range taps {
			h[p][k] = float32(v / sum) // unity gain at every phase: no ripple on steady tones
		}
	}
	r := &resampler{l: l, m: m, ch: ch, h: h}
	r.reset()
	return r
}

func (r *resampler) reset() {
	r.hist = append(r.hist[:0], make([]int16, (rsTaps-1)*r.ch)...)
	r.pos, r.at = 0, 0
}

// process takes interleaved input frames and returns every output frame they complete. The result
// is reused by the next call.
func (r *resampler) process(in []int16) []int16 {
	r.hist = append(r.hist, in...)
	have := r.at + int64(len(r.hist)/r.ch) // input frames seen, the priming zeros included
	r.out = r.out[:0]
	for {
		i0 := r.pos * int64(r.m) / int64(r.l)
		if i0+rsTaps > have {
			break
		}
		taps := r.h[int(r.pos*int64(r.m)%int64(r.l))]
		base := int(i0-r.at) * r.ch
		for c := 0; c < r.ch; c++ {
			var acc float32
			for k, w := range taps {
				acc += w * float32(r.hist[base+k*r.ch+c])
			}
			r.out = append(r.out, int16(max(-32768, min(32767, math.Round(float64(acc))))))
		}
		r.pos++
	}
	// keep only what the next output still needs
	if keep := r.pos * int64(r.m) / int64(r.l); keep > r.at {
		drop := int(keep-r.at) * r.ch
		r.hist = append(r.hist[:0], r.hist[drop:]...)
		r.at = keep
	}
	if r.pos > 1<<40 { // renormalize long before int64 math could overflow
		k := r.pos / int64(r.l) * int64(r.l)
		r.pos -= k
		r.at -= k * int64(r.m) / int64(r.l)
	}
	return r.out
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// bessel0 is the modified Bessel function I0, for the Kaiser window.
func bessel0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 50; k++ {
		term *= (x / (2 * float64(k))) * (x / (2 * float64(k)))
		sum += term
		if term < 1e-12*sum {
			break
		}
	}
	return sum
}

// ---- listener side: UDP, reordering, concealment ----
//
// TCP turns every lost packet into a stall: nothing behind it is delivered until it has been sent
// again, and on a phone sharing its radio with Bluetooth on a busy channel that came to seconds at a
// time, many times a minute. Measured over the same minute: UDP lost 3 packets of 3000 (never more
// than 2 in a row, never a gap over 150 ms) while the TCP stream stalled 39 times. So a listener that
// takes Opus also asks for every chunk over UDP; the TCP copy still comes, as the backup for what UDP
// loses and the whole path where UDP is blocked. The chunk's sequence number (the header's id) puts
// the two streams back into one: each chunk is decoded once, in order. One that neither path delivers
// in time is concealed by Opus rather than skipped.

// reorder holds the Opus chunks that arrived ahead of one still missing.
type reorder struct {
	started bool              // next is known: from the timeline's notice (see Server.Flush) or its first chunk
	epoch   uint16            // the timeline (see WireChunkMsg)
	next    uint16            // sequence number of the chunk to decode next
	pending map[uint16][]byte // seq -> WireChunk payload, for chunks after next
}

// concealMargin is how long before a missing chunk is due it is given up and concealed.
const concealMargin = 300 * time.Millisecond

// putOpus takes a WireChunk from either path when the stream is Opus; false means it is PCM.
func (c *Client) putOpus(h Header, p []byte) bool {
	c.jmu.Lock()
	defer c.jmu.Unlock()
	if c.opus == nil {
		return false
	}
	j := &c.jit
	if j.pending == nil || int16(h.RefersTo-j.epoch) > 0 { // the first chunk, or a new timeline: start over
		c.opus.reset()
		*j = reorder{epoch: h.RefersTo, pending: map[uint16][]byte{}}
		c.pushChunkEpoch(make([]byte, 12), h.RefersTo) // empty: drops what the old timeline queued
	} else if h.RefersTo != j.epoch {
		return true // an old timeline's straggler
	}
	if len(p) < 12 || le.Uint32(p[8:]) == 0 { // the notice of a new timeline: its id is the first chunk's
		if !j.started {
			j.started, j.next = true, h.ID
		}
		return true
	}
	if !j.started { // joined mid-stream, or the notice is late: start here
		j.started, j.next = true, h.ID
	}
	if int16(h.ID-j.next) < 0 {
		return true // decoded already (the other path's copy)
	}
	if _, dup := j.pending[h.ID]; !dup && len(j.pending) < 1024 {
		j.pending[h.ID] = append([]byte(nil), p...)
	}
	c.drainLocked()
	return true
}

// drainLocked decodes every chunk that is next in line, and conceals the next one if it is missing
// and almost due. Called with jmu held.
func (c *Client) drainLocked() {
	j := &c.jit
	if !j.started {
		return
	}
	for {
		var ts time.Duration
		var pcm []byte
		if p, ok := j.pending[j.next]; ok {
			ts = getTV(p)
			pcm = c.opus.decode(p[12 : 12+min(int(le.Uint32(p[8:])), len(p)-12)])
		} else {
			// Missing. If a later chunk is waiting, this one's time follows from it; once that is
			// close, stop waiting and conceal it.
			first := -1
			for seq, p := range j.pending {
				if d := int(int16(seq - j.next)); first < 0 || d < first {
					first, ts = d, getTV(p)-time.Duration(d)*opusFrameDur
				}
			}
			if first < 0 || !c.dueSoon(ts) {
				return
			}
			pcm = c.opus.decode(nil)
		}
		delete(j.pending, j.next)
		j.next++
		if pcm == nil {
			continue
		}
		out := make([]byte, 12, 12+len(pcm))
		putTV(out, ts-c.opus.delay)
		le.PutUint32(out[8:], uint32(len(pcm)))
		c.pushChunkEpoch(append(out, pcm...), j.epoch)
	}
}

const opusFrameDur = 20 * time.Millisecond

// dueSoon reports whether a chunk stamped ts is about to be played, so waiting longer for it is no use.
func (c *Client) dueSoon(ts time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveOffset {
		return false
	}
	return c.serverNow()+c.outLat > ts+c.bufferDur+c.latencyDur-concealMargin
}

// udpLoop receives the UDP copy of the audio. It also wakes up every 50 ms so a missing chunk is
// concealed in time even if nothing else arrives.
func (c *Client) udpLoop() {
	b := make([]byte, 64<<10)
	for {
		c.udp.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		n, _, err := c.udp.ReadFrom(b)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				c.jmu.Lock()
				if c.opus != nil {
					c.drainLocked()
				}
				c.jmu.Unlock()
				continue
			}
			return // closed
		}
		h, p, err := ReadMsg(bytes.NewReader(b[:n]))
		if err == nil && h.Type == TypeWireChunk {
			c.putOpus(h, p)
		}
	}
}
