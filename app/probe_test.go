package app

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"letsgo/snap"
)

// Devices must be found by their address when multicast does not get through: a sweep of the subnet finds a
// letsgo device (and only that), and a known device the scans missed is kept by asking it.
func TestSweepAndKeepKnown(t *testing.T) {
	aSnap, aHTTP := freeAddr(t), "127.0.0.2:"+port(freeAddr(t)) // the "phone" lives at another address of the same /24
	a, err := Start([]string{t.TempDir()}, "", aSnap, aHTTP, "phone", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	// something else that answers on the same port at 127.0.0.3: not a letsgo device
	l, err := net.Listen("tcp", "127.0.0.3:"+port(aHTTP))
	if err != nil {
		t.Fatal(err)
	}
	other := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"name":"router"}`)) })}
	go other.Serve(l)
	defer other.Close()

	b, err := Start([]string{t.TempDir()}, "", freeAddr(t), freeAddr(t), "laptop", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	b.setPeerPort(port(aHTTP))

	if !b.sweepPrefix(netip.MustParsePrefix("127.0.0.0/24")) {
		t.Fatal("sweep did not run")
	}
	ps := b.knownPeers()
	if len(ps) != 1 || ps[0].IP != "127.0.0.2" || ps[0].Name != "phone" || strconv.Itoa(ps[0].Port) != port(aSnap) {
		t.Fatalf("sweep found %+v, want only the phone at 127.0.0.2 with its stream port %s", ps, port(aSnap))
	}

	// the scans missed it (and a device that is gone): only the one that answers is kept
	old := time.Now().Add(-10 * time.Second)
	b.mu.Lock()
	b.seen["127.0.0.2"] = seenPeer{ps[0], old}
	b.seen["127.0.0.9"] = seenPeer{ps[0], old}
	b.mu.Unlock()
	b.keepKnown()
	b.mu.Lock()
	alive, gone := b.seen["127.0.0.2"].at, b.seen["127.0.0.9"].at
	b.mu.Unlock()
	if time.Since(alive) > time.Second || !gone.Equal(old) {
		t.Errorf("keepKnown: answering device refreshed %v ago (want just now), silent one %v (want untouched)", time.Since(alive), gone)
	}
}

// Whatever puts this device's own address into its list of devices (a sweep on mobile data once
// did), it never becomes a device to follow: asked by address it is recognised as itself, and a
// Shift it sends itself is ignored, so playing here keeps playing. That loop once left a phone
// unable to play anything until the network changed.
func TestNeverFollowsItself(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	dir := t.TempDir()
	writeRamp(t, dir, 30)
	aSnap, aHTTP := freeAddr(t), freeAddr(t)
	a, err := Start([]string{dir}, "", aSnap, aHTTP, "phone", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	a.setPeerPort(port(aHTTP)) // other devices' API is where ours is: every probe and Shift reaches us
	if _, ok := a.probe("127.0.0.1"); ok {
		t.Fatal("asked at its own address, it took itself for another device")
	}
	sp, _ := strconv.Atoi(port(aSnap))
	a.setPeers(snap.Peer{Name: "phone", IP: "127.0.0.1", Port: sp}) // in the list anyway
	resp, err := http.Post("http://"+aHTTP+"/api/queue", "application/json", strings.NewReader(`{"index":0}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	time.Sleep(2 * time.Second) // the supervisor sees it playing and Shifts everyone it knows
	if !a.p.State().Playing {
		t.Fatal("it Shifted to itself and paused its own music")
	}
}
