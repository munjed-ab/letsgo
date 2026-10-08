package snap

import (
	"net"
	"testing"
	"time"
)

// After a Wi-Fi stall a listener's queue holds audio whose time has passed. The server must skip it
// and go straight to audio that can still be heard, not spend the link on chunks that get thrown away.
func TestStaleAudioIsNotSent(t *testing.T) {
	srv := NewServer(100, 44100, 16, 2)
	a, b := net.Pipe() // synchronous: nothing moves until the listener reads, like a stalled link
	defer b.Close()
	go srv.handle(a)
	go b.Write(HelloMsg(1, "slow", "slow", "", 0))
	for { // settings, then the codec header
		h, _, err := ReadMsg(b)
		if err != nil {
			t.Fatal(err)
		}
		if h.Type == TypeCodecHeader {
			break
		}
	}
	for !func() bool { srv.mu.Lock(); defer srv.mu.Unlock(); return len(srv.sessions) == 1 && anyReady(srv) }() {
		time.Sleep(time.Millisecond)
	}
	pcm := make([]byte, 4)
	for i := 0; i < 5; i++ {
		srv.Broadcast(Now()-10*time.Second, pcm) // long past: no listener can play it
	}
	fresh := Now()
	srv.Broadcast(fresh, pcm)

	b.SetReadDeadline(time.Now().Add(2 * time.Second))
	h, p, err := ReadMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if d := getTV(p) - fresh; h.Type != TypeWireChunk || d < -time.Millisecond || d > time.Millisecond { // the wire keeps microseconds
		t.Fatalf("first audio sent is stamped %v, want the fresh chunk %v (stale ones must be skipped)", getTV(p), fresh)
	}
}

func anyReady(s *Server) bool {
	for ss := range s.sessions {
		if ss.ready {
			return true
		}
	}
	return false
}
