package snap

import (
	"net"
	"testing"
	"time"
)

// End to end over a real TCP socket in real time: server stamps chunks, client
// syncs its clock via pings, an audio-device-like loop pulls 20 ms reads. Runs
// longer than the old 3.5 s reseed failure and checks continuity, sync and that
// volume changes reach the client.
func TestLoopbackEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	const buffer = 400
	srv := NewServer(buffer, simRate, 16, 2)
	if err := srv.Listen(addr); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	defer close(stop)
	t0 := Now() + 200*time.Millisecond
	go func() { // the player loop: stamp 20 ms chunks, send 60 ms ahead
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

	c, err := Connect(addr, "testpc", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case <-c.Ready:
	case <-time.After(3 * time.Second):
		t.Fatal("no codec header")
	}
	time.Sleep(700 * time.Millisecond) // let the ping burst lock the clock

	out := make([]byte, simFrames*4)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var errs []time.Duration
	prevLast, glitches, audio := -2, 0, 0
	end := time.Now().Add(6 * time.Second)
	for time.Now().Before(end) {
		<-tick.C
		callAt := Now()
		rs := c.Stats().Resyncs
		c.Read(out)
		exact := c.slew == 0 && c.Stats().Resyncs == rs // only exact-speed reads are bit-for-bit

		first, last := -1, -1
		for i := 0; i < simFrames; i++ {
			l, r := le.Uint16(out[i*4:]), le.Uint16(out[i*4+2:])
			if l == 0 && r == 0 {
				continue
			}
			k := int(l) + int(r)<<15 - 1
			if first < 0 {
				first = k
				// outLat is 0, so frame i is heard when it is handed over
				hear := callAt + time.Duration(int64(i)*int64(time.Second)/simRate)
				want := t0 + time.Duration(int64(k)*int64(time.Second)/simRate) + buffer*time.Millisecond
				errs = append(errs, hear-want)
			} else if exact && k != last+1 {
				glitches++
			}
			last = k
		}
		if first < 0 {
			continue
		}
		audio++
		if exact && prevLast >= 0 && first != prevLast+1 {
			glitches++
		}
		prevLast = -2
		if exact {
			prevLast = last
		}
	}

	if audio < 250 {
		t.Fatalf("only %d of ~300 reads carried audio", audio)
	}
	if glitches != 0 {
		t.Errorf("glitches = %d", glitches)
	}
	s := c.Stats()
	if s.Underruns != 0 || s.Resyncs != 0 {
		t.Errorf("underruns=%d resyncs=%d, want 0/0 (%+v)", s.Underruns, s.Resyncs, s)
	}
	c.mu.Lock()
	off := c.offset
	c.mu.Unlock()
	if abs(off) > 2*time.Millisecond { // same process, same clock: the true offset is 0
		t.Errorf("clock offset %v, want ~0", off)
	}
	if n := len(errs); n < 100 {
		t.Fatalf("only %d measurements", n)
	} else if m := abs(errs[n-1]); m > 6*time.Millisecond {
		t.Errorf("sync error at the end %v, want <= 6ms", errs[n-1])
	}

	// Volume set on the server must reach the client through the settings message.
	srv.SetVolume(80, false)
	for i := 0; i < 50; i++ {
		c.mu.Lock()
		v := c.vol
		c.mu.Unlock()
		if v == 80 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("volume 80 never reached the client")
}

// Each device must show up under its own name; the server's own device is marked
// and identical names get told apart.
func TestClientNames(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	srv := NewServer(400, simRate, 16, 2)
	if err := srv.Listen(addr); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	for _, name := range []string{"Pixel \"9\"", "laptop", "laptop"} { // quotes must survive JSON
		c, err := Connect(addr, name, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
	}
	var got []string
	for i := 0; i < 50 && len(got) < 3; i++ {
		time.Sleep(20 * time.Millisecond)
		got = got[:0]
		for _, ci := range srv.Clients() {
			got = append(got, ci.Name)
		}
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("duplicate entry %q in %q", n, got)
		}
		seen[n] = true
	}
	if len(got) != 3 || !seen[`Pixel "9" (this device)`] {
		t.Errorf("names = %q", got)
	}
}
