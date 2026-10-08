package snap

import (
	"bytes"
	"io"
	"math"
	"net"
	"testing"
	"time"
)

// tone is a stereo test signal: a few sines per side, so a wrong delay cannot line up by accident.
func tone(t float64) (l, r float64) {
	l = 6000*math.Sin(2*math.Pi*220*t) + 3000*math.Sin(2*math.Pi*660*t) + 1500*math.Sin(2*math.Pi*1500*t)
	r = 6000*math.Sin(2*math.Pi*330*t) + 3000*math.Sin(2*math.Pi*990*t)
	return
}

func toneChunk(i int) []int16 {
	out := make([]int16, simFrames*2)
	for j := 0; j < simFrames; j++ {
		l, r := tone(float64(i*simFrames+j) / simRate)
		out[j*2], out[j*2+1] = int16(l), int16(r)
	}
	return out
}

// snrAt compares got (interleaved, at simRate, from the start of the signal) with the tone delayed
// by d, skipping the first second while everything settles.
func snrAt(got []int16, d time.Duration) float64 {
	var s, e float64
	for i := simRate; i < len(got)/2; i++ {
		l, r := tone(float64(i)/simRate - d.Seconds())
		el, er := float64(got[i*2])-l, float64(got[i*2+1])-r
		s += l*l + r*r
		e += el*el + er*er
	}
	return 10 * math.Log10(s/e)
}

// Each 20 ms chunk becomes exactly one 48 kHz packet's worth and back, from the first chunk on, and
// the round trip is clean and late by exactly the resamplers' stated delay.
func TestResamplerRoundTrip(t *testing.T) {
	up, down := newResampler(simRate, opusRate, 2), newResampler(opusRate, simRate, 2)
	var got []int16
	for i := 0; i < 100; i++ {
		mid := up.process(toneChunk(i))
		if len(mid) != opusFrames*2 {
			t.Fatalf("chunk %d: %d frames at 48 kHz, want %d", i, len(mid)/2, opusFrames)
		}
		back := down.process(mid)
		if len(back) != simFrames*2 {
			t.Fatalf("chunk %d: %d frames back, want %d", i, len(back)/2, simFrames)
		}
		got = append(got, back...)
	}
	d := time.Duration(rsTaps/2)*time.Second/simRate + time.Duration(rsTaps/2)*time.Second/opusRate
	if snr := snrAt(got, d); snr < 60 {
		t.Errorf("round trip is %.1f dB above its error, want 60+", snr)
	}
}

// Through Opus the sound comes out opusDelay late, to a tenth of a millisecond: that is what the
// listener stamps back, so an Opus device plays in step with a PCM one.
func TestOpusDelayIsExact(t *testing.T) {
	enc, err := newOpusEnc(simRate, 2)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := newOpusDec(simRate, 2)
	if err != nil {
		t.Fatal(err)
	}
	var got []int16
	bytes := 0
	for i := 0; i < 150; i++ {
		var b []byte
		for _, v := range toneChunk(i) {
			b = le.AppendUint16(b, uint16(v))
		}
		pkt := enc.encode(b)
		if pkt == nil {
			t.Fatalf("chunk %d did not encode", i)
		}
		bytes += len(pkt)
		pcm := dec.decode(pkt)
		if len(pcm) != simFrames*4 {
			t.Fatalf("chunk %d decoded to %d bytes, want %d", i, len(pcm), simFrames*4)
		}
		got = bytesToPCM(got, pcm)
	}
	if kbps := bytes * 8 / 3 / 1000; kbps > opusBitrate/1000*5/4 {
		t.Errorf("%d kbit/s on the wire, want about %d", kbps, opusBitrate/1000)
	}
	best, bestD := -math.MaxFloat64, time.Duration(0)
	for d := dec.delay - time.Millisecond; d <= dec.delay+time.Millisecond; d += 10 * time.Microsecond {
		if snr := snrAt(got, d); snr > best {
			best, bestD = snr, d
		}
	}
	if off := bestD - dec.delay; off < -100*time.Microsecond || off > 100*time.Microsecond {
		t.Errorf("sound comes out %v late, opusDelay says %v", bestD, dec.delay)
	}
	if best < 20 {
		t.Errorf("decoded tone is only %.1f dB above its error", best)
	}
}

// A letsgo listener on another device gets Opus and plays it; one on the server's own device gets PCM.
func TestOpusListener(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	srv := NewServer(400, simRate, 16, 2)
	if err := srv.Listen(addr); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	stop := make(chan struct{})
	defer close(stop)
	t0 := Now() + 200*time.Millisecond
	go func() {
		for i := 0; ; i++ {
			ts := t0 + time.Duration(i)*20*time.Millisecond
			select {
			case <-stop:
				return
			case <-time.After(max(0, ts-simLead-Now())):
			}
			var b []byte
			for _, v := range toneChunk(i) {
				b = le.AppendUint16(b, uint16(v))
			}
			srv.Broadcast(ts, b)
		}
	}()

	local, err := Connect(addr, "local", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	OpusOnLoopback = true
	remote, err := Connect(addr, "remote", 0)
	OpusOnLoopback = false
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	for _, c := range []*Client{local, remote} {
		select {
		case <-c.Ready:
		case <-time.After(3 * time.Second):
			t.Fatal("no codec header")
		}
	}
	if local.opus != nil || remote.opus == nil {
		t.Fatalf("codecs: local opus=%v remote opus=%v, want PCM locally and Opus remotely", local.opus != nil, remote.opus != nil)
	}
	time.Sleep(time.Second) // clock lock, buffer fills
	// Both read at the same moment, like two devices side by side: they must play the same sound.
	a, b := make([]byte, simFrames*4), make([]byte, simFrames*4)
	var loud, diff float64
	for i := 0; i < 50; i++ {
		local.Read(a)
		remote.Read(b)
		for j := 0; j < len(a); j += 2 {
			x, y := float64(int16(le.Uint16(a[j:]))), float64(int16(le.Uint16(b[j:])))
			loud += x * x
			diff += (x - y) * (x - y)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rms := math.Sqrt(loud / float64(50*len(a)/2)); rms < 2000 {
		t.Fatalf("the PCM listener plays at RMS %.0f, want the tone (~4000)", rms)
	}
	if snr := 10 * math.Log10(loud/diff); snr < 15 {
		t.Errorf("Opus and PCM listeners differ: %.1f dB apart, want 15+ (a delay off by the codec's 7 ms is below 0)", snr)
	}
}

// opusChunks encodes n chunks of the tone as the server would send them: WireChunk payloads, with
// their sequence numbers.
func opusChunks(t *testing.T, n int, t0 time.Duration) [][]byte {
	enc, err := newOpusEnc(simRate, 2)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for i := 0; i < n; i++ {
		var b []byte
		for _, v := range toneChunk(i) {
			b = le.AppendUint16(b, uint16(v))
		}
		msg := WireChunkMsg(t0+time.Duration(i)*opusFrameDur, enc.encode(b), 7)
		out = append(out, msg[headerSize:])
	}
	return out
}

// opusTestClient is a listener with its clock locked, fed by hand.
func opusTestClient(t *testing.T, now *time.Duration) *Client {
	d, err := newOpusDec(simRate, 2)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{clock: func() time.Duration { return *now }, haveOffset: true, bufferDur: time.Second,
		Rate: simRate, Bits: 16, Channels: 2, frameBytes: 4, opus: d, vol: 100}
	return c
}

func buffered(c *Client) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf[c.r:c.w]...)
}

// The UDP and TCP copies arrive in any order and twice: every chunk is still decoded exactly once, in
// order, so the listener gets the very same audio as from one perfect stream.
func TestReorderDecodesOnceInOrder(t *testing.T) {
	now := time.Duration(0)
	chunks := opusChunks(t, 60, time.Hour)         // stamped far ahead: nothing is due, nothing concealed
	notice := WireChunkMsg(0, nil, 7)[headerSize:] // what Server.Flush sends first: the first chunk is 0
	want := opusTestClient(t, &now)
	want.putOpus(Header{Type: TypeWireChunk, ID: 0, RefersTo: 7}, notice)
	for i, p := range chunks {
		want.putOpus(Header{Type: TypeWireChunk, ID: uint16(i), RefersTo: 7}, p)
	}
	got := opusTestClient(t, &now)
	got.putOpus(Header{Type: TypeWireChunk, ID: 0, RefersTo: 7}, notice)
	order := []int{}
	for i := 0; i < 60; i += 3 { // each group of three arrives backwards, and every chunk twice
		order = append(order, i+2, i+1, i, i+1, i+2)
	}
	order = append(order, 5, 0, 30) // late copies of chunks long decoded
	for _, i := range order {
		got.putOpus(Header{Type: TypeWireChunk, ID: uint16(i), RefersTo: 7}, chunks[i])
	}
	if !bytes.Equal(buffered(got), buffered(want)) || len(buffered(want)) != 60*simFrames*4 {
		t.Fatalf("reordered stream gave %d bytes, in-order %d (want %d, and the same)", len(buffered(got)), len(buffered(want)), 60*simFrames*4)
	}
	// a straggler from an older timeline changes nothing
	got.putOpus(Header{Type: TypeWireChunk, ID: 61, RefersTo: 6}, chunks[0])
	if len(buffered(got)) != 60*simFrames*4 {
		t.Fatal("a chunk of an older timeline was played")
	}
}

// A chunk lost on both paths is waited for while there is time, then concealed: the timeline has no
// hole and the chunks after it play.
func TestMissingChunkIsConcealedWhenDue(t *testing.T) {
	t0 := 10 * time.Second
	now := t0 - time.Second // the first chunk is heard at t0 + buffer
	chunks := opusChunks(t, 20, t0)
	c := opusTestClient(t, &now)
	for i, p := range chunks {
		if i != 5 {
			c.putOpus(Header{Type: TypeWireChunk, ID: uint16(i), RefersTo: 7}, p)
		}
	}
	if n := len(buffered(c)) / (simFrames * 4); n != 5 {
		t.Fatalf("%d chunks playable while chunk 5 can still come, want 5", n)
	}
	now = t0 + 5*opusFrameDur + time.Second - concealMargin + time.Millisecond // chunk 5 is nearly due
	c.jmu.Lock()
	c.drainLocked()
	c.jmu.Unlock()
	if n := len(buffered(c)) / (simFrames * 4); n != 20 {
		t.Fatalf("%d chunks playable after chunk 5 was due, want all 20 (5 concealed)", n)
	}
	c.mu.Lock()
	end := c.endTS()
	c.mu.Unlock()
	if want := t0 + 20*opusFrameDur - c.opus.delay; end-want > time.Millisecond || want-end > time.Millisecond {
		t.Errorf("buffer ends at %v, want %v: the concealed chunk left a hole or overlap", end, want)
	}
}

// stallProxy forwards a TCP connection, but the server-to-listener direction freezes for `stall`
// every `every`, the way the phone's TCP stream did on a busy Wi-Fi.
func stallProxy(t *testing.T, server string, stall, every time.Duration) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			a, err := ln.Accept()
			if err != nil {
				return
			}
			b, err := net.Dial("tcp", server)
			if err != nil {
				a.Close()
				return
			}
			t.Cleanup(func() { a.Close(); b.Close() })
			go io.Copy(b, a)
			go func() {
				start := time.Now()
				buf := make([]byte, 4096)
				for {
					if since := time.Since(start) % every; since < stall {
						time.Sleep(stall - since)
					}
					n, err := b.Read(buf)
					if err != nil {
						return
					}
					a.Write(buf[:n])
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// With the TCP stream freezing for 1.5 s every 3 s, an Opus listener keeps playing without a single
// underrun: the UDP copy carries the audio.
func TestUDPRidesOutTCPStalls(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	srv := NewServer(1000, simRate, 16, 2)
	if err := srv.Listen(addr); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	stop := make(chan struct{})
	defer close(stop)
	t0 := Now() + 200*time.Millisecond
	go func() {
		for i := 0; ; i++ {
			ts := t0 + time.Duration(i)*20*time.Millisecond
			select {
			case <-stop:
				return
			case <-time.After(max(0, ts-simLead-Now())):
			}
			var b []byte
			for _, v := range toneChunk(i) {
				b = le.AppendUint16(b, uint16(v))
			}
			srv.Broadcast(ts, b)
		}
	}()
	OpusOnLoopback = true
	c, err := Connect(stallProxy(t, addr, 1500*time.Millisecond, 3*time.Second), "remote", 0)
	OpusOnLoopback = false
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case <-c.Ready:
	case <-time.After(5 * time.Second):
		t.Fatal("no codec header")
	}
	time.Sleep(2 * time.Second) // clock lock (pings ride the stalling TCP too), buffer fills
	before := c.Stats()
	out := make([]byte, simFrames*4)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for end := time.Now().Add(7 * time.Second); time.Now().Before(end); {
		<-tick.C
		c.Read(out)
	}
	after := c.Stats()
	if after.Underruns != before.Underruns || after.Resyncs != before.Resyncs || !after.Aligned {
		t.Errorf("over TCP stalls: underruns %d -> %d, resyncs %d -> %d, aligned %v", before.Underruns, after.Underruns, before.Resyncs, after.Resyncs, after.Aligned)
	}
}
