package snap

import (
	"net"
	"sync"
	"testing"
	"time"
)

// Pause and play must reach a listener at once, not a buffer later: after Flush it goes
// silent immediately even though it holds most of a buffer of audio, and a restart stamped
// startLead ahead is audible about then.
func TestFlushStopsAndRestartsAtOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	const buffer = 800 * time.Millisecond
	const startLead = 350 * time.Millisecond
	srv := NewServer(int(buffer/time.Millisecond), simRate, 16, 2)
	if err := srv.Listen(addr); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	chunk := func(i int) []byte {
		data := make([]byte, simFrames*4)
		for j := 0; j < simFrames; j++ {
			v := i*simFrames + j + 1
			le.PutUint16(data[j*4:], uint16(v&0x7fff))
			le.PutUint16(data[j*4+2:], uint16(v>>15))
		}
		return data
	}
	realtime := func(t0 time.Duration, from, n int) { // n chunks on a timeline, sent as a live player would
		for i := from; i < from+n; i++ {
			ts := t0 + time.Duration(i-from)*20*time.Millisecond
			time.Sleep(max(0, ts-simLead-Now()))
			srv.Broadcast(ts, chunk(i))
		}
	}

	c, err := Connect(addr, "listener", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	type sample struct {
		at    time.Time
		sound bool
	}
	var mu sync.Mutex
	var samples []sample
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		out := make([]byte, simFrames*4)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			c.Read(out)
			sound := false
			for _, b := range out {
				if b != 0 {
					sound = true
					break
				}
			}
			mu.Lock()
			samples = append(samples, sample{time.Now(), sound})
			mu.Unlock()
		}
	}()

	realtime(Now()+100*time.Millisecond, 0, 100) // 2 s of music, then...
	pausedAt := time.Now()
	srv.Flush() // ...pause: the listener holds ~800 ms of audio that must not be played out
	time.Sleep(700 * time.Millisecond)

	resumedAt := time.Now()
	srv.Flush()
	t0 := Now() - buffer + startLead // heard startLead from now
	for i := 0; t0+time.Duration(i)*20*time.Millisecond <= Now()+simLead; i++ {
		srv.Broadcast(t0+time.Duration(i)*20*time.Millisecond, chunk(1000+i)) // the burst
	}
	burst := 0
	for ; t0+time.Duration(burst)*20*time.Millisecond <= Now()+simLead; burst++ {
	}
	realtime(t0+time.Duration(burst)*20*time.Millisecond, 1000+burst, 60)

	mu.Lock()
	defer mu.Unlock()
	var silentBy, soundAgain time.Duration
	for _, s := range samples {
		if s.at.After(pausedAt) && s.at.Before(resumedAt) {
			if s.sound {
				silentBy = s.at.Sub(pausedAt) // the last moment sound was still heard
			}
		}
		if s.at.After(resumedAt) && s.sound && soundAgain == 0 {
			soundAgain = s.at.Sub(resumedAt)
		}
	}
	t.Logf("sound stopped %v after the pause, came back %v after the resume", silentBy.Round(time.Millisecond), soundAgain.Round(time.Millisecond))
	if silentBy > 100*time.Millisecond {
		t.Errorf("still heard %v after the pause (the listener held %v of audio)", silentBy, buffer)
	}
	if soundAgain < startLead-150*time.Millisecond || soundAgain > startLead+250*time.Millisecond {
		t.Errorf("sound came back %v after the resume, want ~%v", soundAgain, startLead)
	}
	if st := c.Stats(); st.Underruns > 1 { // the one at the pause is expected
		t.Errorf("underruns %d", st.Underruns)
	}
}
