package app

import (
	"cmp"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"letsgo/snap"
)

// Finding devices without multicast. mDNS is how devices normally find each other, but multicast is the
// first thing a phone's hotspot or a busy Wi-Fi loses, and then two devices sit next to each other and
// do not see one another until something resets. So devices are also found by asking their API:
//
//   - a device seen before that the last scan missed is asked directly (keepKnown), so one lost packet
//     never drops it;
//   - while no device is known at all, every address of our own /24 is asked (sweep).
//
// Only the letsgo API port on the subnet we are on is touched, and only while nothing else works.

const (
	sweepEvery       = 12 * time.Second // how often to sweep while no device is known
	readvertiseEvery = 30 * time.Second // how often to announce ourselves again while no device is known
)

// probe asks the device at ip who it is; ok is false if no letsgo answers there.
func (n *Node) probe(ip string) (p snap.Peer, ok bool) {
	c := http.Client{Timeout: 700 * time.Millisecond} // its answer includes what it hears, which can take 400 ms
	resp, err := c.Get("http://" + net.JoinHostPort(ip, n.peerPortStr()) + "/api/state")
	if err != nil {
		return p, false
	}
	defer resp.Body.Close()
	var s struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		SnapPort int             `json:"snapPort"` // absent from older builds: the default
		Player   json.RawMessage `json:"player"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&s) != nil || s.Name == "" || s.Player == nil {
		return p, false // something else listens on that port
	}
	if s.ID == n.id {
		return p, false // that is us, under an address we did not know was ours (see shift.go)
	}
	return snap.Peer{Name: s.Name, IP: ip, Port: cmp.Or(s.SnapPort, 1704)}, true
}

// keepKnown asks the devices that scans showed before but did not show lately, and counts the ones
// that answer as seen.
func (n *Node) keepKnown() {
	n.mu.Lock()
	var stale []string
	for ip, p := range n.seen {
		if time.Since(p.at) > scanEvery {
			stale = append(stale, ip)
		}
	}
	n.mu.Unlock()
	var wg sync.WaitGroup
	for _, ip := range stale {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := n.probe(ip); !ok {
				return
			}
			n.mu.Lock()
			if p, ok := n.seen[ip]; ok {
				p.at = time.Now()
				n.seen[ip] = p
			}
			n.mu.Unlock()
		}()
	}
	wg.Wait()
}

// ownSubnet is the /24 this device is on: the one the phone says it uses, else the one its default
// route leaves by (a UDP "connect" sends nothing, the system only picks the address). ponytail: always
// a /24, which is what a hotspot and nearly every home router hands out; a wider network is covered by mDNS.
func (n *Node) ownSubnet() (netip.Prefix, bool) {
	n.mu.Lock()
	hint := n.netHint
	n.mu.Unlock()
	ip := ""
	if hint != nil {
		ip = hint.IP
	} else {
		ip = routeIP()
	}
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is4() || !a.IsPrivate() {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(a, 24).Masked(), true
}

// routeIP is the address the default route leaves by (a UDP "connect" sends nothing, the system only
// picks the address), "" if there is none.
func routeIP() string {
	c, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}

// sweep asks every other address of our own subnet whether a letsgo device answers, and reports whether
// there was a subnet to ask.
func (n *Node) sweep() bool {
	sub, ok := n.ownSubnet()
	if !ok {
		return false
	}
	return n.sweepPrefix(sub)
}

func (n *Node) sweepPrefix(sub netip.Prefix) bool {
	n.mu.Lock()
	hint := n.netHint
	n.mu.Unlock()
	self := map[string]bool{routeIP(): true} // on mobile data there is no hint and Android hides InterfaceAddrs: without this the phone found itself and every Shift paused it
	if hint != nil {
		self[hint.IP] = true
	}
	if as, err := net.InterfaceAddrs(); err == nil { // not on Android 11+, where the hint above stands in
		for _, a := range as {
			if ipn, ok := a.(*net.IPNet); ok {
				self[ipn.IP.String()] = true
			}
		}
	}
	sem := make(chan struct{}, 64)
	var wg sync.WaitGroup
	for a := sub.Addr().Next(); sub.Contains(a) && sub.Contains(a.Next()); a = a.Next() { // not the network or broadcast address
		if self[a.String()] {
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if p, ok := n.probe(a.String()); ok {
				n.setPeers(p)
			}
		}()
	}
	wg.Wait()
	return true
}
