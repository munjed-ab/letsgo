package app

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"
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
