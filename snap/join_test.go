package snap

import (
	"net"
	"testing"
	"time"
)

// A device that joins a stream that is already playing must be audible almost at
// once, in step with everyone else, not a whole buffer later. (The server hands a
// newcomer the chunks that are still due to be heard.)
func TestJoinMidStreamIsAudibleAtOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	const buffer = 800
	srv := NewServer(buffer, simRate, 16, 2)
	if err := srv.Listen(addr); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	stop := make(chan struct{})
	defer close(stop)
	t0 := Now() + 100*time.Millisecond
	go func() { // the player loop, as in the end-to-end test
		for i := 0; ; i++ {
			ts := t0 + time.Duration(i)*20*time.Millisecond
			select {
			case <-stop:
				return
			case <-time.After(max(0, ts-simLead-Now())):
			}
			data := make([]byte, simFrames*4)
			for j := 0; j < simFrames; j++ {
				v := i*simFrames + j + 1
				le.PutUint16(data[j*4:], uint16(v&0x7fff))
				le.PutUint16(data[j*4+2:], uint16(v>>15))
			}
			srv.Broadcast(ts, data)
		}
	}()
	time.Sleep(2 * time.Second) // the stream has been playing for a while

	joined := time.Now()
	c, err := Connect(addr, "latecomer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	out := make([]byte, simFrames*4)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var firstAudio time.Duration
	var lastSample, glitches, reads int
	lastSample = -1
	for time.Since(joined) < 3*time.Second {
		<-tick.C
		c.Read(out)
		first, last := -1, -1
		for i := 0; i < simFrames; i++ {
			l, r := le.Uint16(out[i*4:]), le.Uint16(out[i*4+2:])
			if l == 0 && r == 0 {
				continue
			}
			k := int(l) + int(r)<<15 - 1
			if first < 0 {
				first = k
			}
			last = k
		}
		if first < 0 {
			continue
		}
		if firstAudio == 0 {
			firstAudio = time.Since(joined)
		}
		reads++
		if lastSample >= 0 && time.Since(joined) > firstAudio+500*time.Millisecond && (first-lastSample < -2 || first-lastSample > 5) {
			glitches++ // after the first half second the stream must be continuous
		}
		lastSample = last
	}
	t.Logf("first audio %v after joining (buffer %d ms)", firstAudio.Round(time.Millisecond), buffer)
	if firstAudio == 0 || firstAudio > 300*time.Millisecond {
		t.Errorf("first audio %v after joining, want < 300ms (the buffer is %d ms)", firstAudio, buffer)
	}
	if reads < 100 || glitches != 0 {
		t.Errorf("reads with audio %d, glitches %d", reads, glitches)
	}
}
