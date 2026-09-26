package app

import (
	"encoding/binary"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"letsgo/snap"
)

// Two devices on Automatic: whichever one plays becomes the source and the other follows
// within about a second, and a pause is heard at once.
func TestAutomaticHandOver(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	dirA, dirB := t.TempDir(), t.TempDir()
	writeRamp(t, dirA, 30)
	writeRamp(t, dirB, 30)
	aSnap, aHTTP, bSnap, bHTTP := freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t)
	a, err := Start([]string{dirA}, "", aSnap, aHTTP, "phone", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	b, err := Start([]string{dirB}, "", bSnap, bHTTP, "laptop", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	// no mDNS in tests: tell each node about the other, and where the other's API is
	a.peerPort, b.peerPort = port(bHTTP), port(aHTTP)
	ap, _ := strconv.Atoi(port(aSnap))
	bp, _ := strconv.Atoi(port(bSnap))
	a.setPeers(snap.Peer{Name: "laptop", IP: "127.0.0.1", Port: bp})
	b.setPeers(snap.Peer{Name: "phone", IP: "127.0.0.1", Port: ap})
	a.SetOutputLatency(0)
	b.SetOutputLatency(0)

	// what each device's speaker is doing: pull audio every 20 ms like an audio device.
	// last is when sound was last heard, at is where in the ramp (the frame number) it was.
	type speaker struct{ last, at atomic.Int64 }
	lastSound := func(n *Node) *speaker {
		var sp speaker
		last := &sp.last
		go func() {
			out := make([]byte, 882*4)
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-n.done:
					return
				case <-tick.C:
				}
				n.Read(out)
				for i := 0; i < 882; i++ {
					l, r := binary.LittleEndian.Uint16(out[i*4:]), binary.LittleEndian.Uint16(out[i*4+2:])
					if l != 0 || r != 0 {
						sp.at.Store(int64(l) + int64(r)<<15 - 1)
						last.Store(time.Now().UnixNano())
						break
					}
				}
			}
		}()
		return &sp
	}
	soundA, soundB := lastSound(a), lastSound(b)
	// how long after `from` the speaker was first heard again
	heardAfter := func(sp *speaker, from time.Time, limit time.Duration) time.Duration {
		for time.Since(from) < limit {
			if l := sp.last.Load(); l > from.UnixNano() {
				return time.Since(from) - time.Since(time.Unix(0, l)) // when it was heard, not when we noticed
			}
			time.Sleep(10 * time.Millisecond)
		}
		return -1
	}
	// how long after `from` the speaker was playing the START of the other device's song (the
	// first 3 s of the ramp), i.e. it follows that device rather than playing what it had before
	hearsNewSong := func(sp *speaker, from time.Time, limit time.Duration) time.Duration {
		for time.Since(from) < limit {
			if l := sp.last.Load(); l > from.UnixNano() && sp.at.Load() < 3*44100 {
				return time.Since(from) - time.Since(time.Unix(0, l))
			}
			time.Sleep(10 * time.Millisecond)
		}
		return -1
	}
	post := func(addr, path, body string) {
		resp, err := http.Post("http://"+addr+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	// 1. the phone starts playing: the laptop (which hears nothing yet) follows
	pressed := time.Now()
	post(aHTTP, "/api/queue", `{"index":0}`)
	d := heardAfter(soundB, pressed, 5*time.Second)
	t.Logf("laptop follows the phone after %v", d.Round(time.Millisecond))
	if d < 0 || d > 1500*time.Millisecond {
		t.Fatalf("laptop took %v to start playing what the phone plays", d)
	}
	if got := heardAfter(soundA, pressed, time.Second); got < 0 || got > 1000*time.Millisecond {
		t.Errorf("the phone itself took %v to be heard", got)
	}

	// 2. a pause is heard at once, on the source and on the listener
	time.Sleep(500 * time.Millisecond)
	paused := time.Now()
	post(aHTTP, "/api/control?cmd=pause", "")
	time.Sleep(500 * time.Millisecond)
	for name, sp := range map[string]*speaker{"phone": soundA, "laptop": soundB} {
		if l := time.Unix(0, sp.last.Load()); l.After(paused.Add(150 * time.Millisecond)) {
			t.Errorf("%s was still audible %v after the pause", name, l.Sub(paused).Round(time.Millisecond))
		}
	}

	// 3. the laptop starts playing: the phone, now idle, follows it
	a.setPeers(snap.Peer{Name: "laptop", IP: "127.0.0.1", Port: bp}) // (peers expire; a real scan renews them)
	pressed = time.Now()
	post(bHTTP, "/api/queue", `{"index":0}`)
	d = heardAfter(soundA, pressed, 5*time.Second)
	t.Logf("phone follows the laptop after %v", d.Round(time.Millisecond))
	if d < 0 || d > 1500*time.Millisecond {
		t.Fatalf("phone took %v to start playing what the laptop plays", d)
	}
	a.mu.Lock()
	from := a.from
	a.mu.Unlock()
	if from != "127.0.0.1:"+port(bSnap) {
		t.Errorf("phone listens to %q, want the laptop", from)
	}

	// 4. and back: pausing the laptop and playing on the phone hands the music over again
	post(bHTTP, "/api/control?cmd=pause", "")
	time.Sleep(700 * time.Millisecond)
	pressed = time.Now()
	post(aHTTP, "/api/queue", `{"index":0}`)
	d = heardAfter(soundB, pressed, 5*time.Second)
	t.Logf("laptop follows the phone again after %v", d.Round(time.Millisecond))
	if d < 0 || d > 1500*time.Millisecond {
		t.Fatalf("laptop took %v to start playing what the phone plays again", d)
	}

	// 5. Shift: the laptop, which is only listening to the phone, starts a song of its own. Nobody
	// pauses the phone and discovery knows nothing (no peers cached): the phone must stop playing
	// its own song and follow the laptop within about a second.
	time.Sleep(4 * time.Second) // the phone is well into its song
	for _, n := range []*Node{a, b} {
		n.mu.Lock()
		n.seen = map[string]seenPeer{}
		n.mu.Unlock()
	}
	pressed = time.Now()
	post(bHTTP, "/api/queue", `{"index":0}`)
	d = hearsNewSong(soundA, pressed, 5*time.Second)
	t.Logf("shift laptop -> phone: the phone follows after %v", d.Round(time.Millisecond))
	if d < 0 || d > 1500*time.Millisecond {
		t.Fatalf("shift: the phone took %v to follow the laptop", d)
	}
	if a.p.State().Playing {
		t.Error("shift: the phone is still playing its own song")
	}

	// 6. and the other way, the case that used to hang: the phone, listening to the laptop, starts
	// its own song. The laptop must stop its song and follow.
	time.Sleep(4 * time.Second)
	for _, n := range []*Node{a, b} {
		n.mu.Lock()
		n.seen = map[string]seenPeer{}
		n.mu.Unlock()
	}
	pressed = time.Now()
	post(aHTTP, "/api/queue", `{"index":0}`)
	d = hearsNewSong(soundB, pressed, 5*time.Second)
	t.Logf("shift phone -> laptop: the laptop follows after %v", d.Round(time.Millisecond))
	if d < 0 || d > 1500*time.Millisecond {
		t.Fatalf("shift: the laptop took %v to follow the phone", d)
	}
	if b.p.State().Playing {
		t.Error("shift: the laptop is still playing its own song")
	}
}
