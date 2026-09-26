package snap

import (
	"context"
	"log"
	"net"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

// mDNS service type stock Snapcast servers advertise, so Snapdroid and other
// letsgo peers find us with no typed IP.
const mdnsService = "_snapcast._tcp"

// Peer is a letsgo/snapcast server found on the local network.
type Peer struct {
	Name string
	IP   string
	Port int
}

// Net pins the network interface and IP that mDNS uses. Needed where Go cannot
// enumerate interfaces itself (Android 11+ hides them from apps, which used to
// silently break discovery there). nil means "find them yourself".
type Net struct {
	Iface net.Interface
	IP    string
}

// Advertise announces this server on the LAN. Keep the returned handle alive for
// as long as you want to be discoverable; call Shutdown to stop. On Android a
// held WifiManager.MulticastLock is required for this to reach other devices.
func Advertise(instance string, port int, n *Net) (*zeroconf.Server, error) {
	if n == nil {
		return zeroconf.Register(instance, mdnsService, "local.", port, nil, nil)
	}
	host := strings.Map(func(r rune) rune {
		if r == ' ' || r == '.' {
			return '-'
		}
		return r
	}, instance)
	return zeroconf.RegisterProxy(instance, mdnsService, "local.", port, host, []string{n.IP}, nil, []net.Interface{n.Iface})
}

// Browse scans for peers for the given duration and returns those seen. Entries
// whose instance name equals self are skipped so we don't discover ourselves.
func Browse(d time.Duration, self string, n *Net) []Peer {
	var opts []zeroconf.ClientOption
	if n != nil {
		opts = append(opts, zeroconf.SelectIfaces([]net.Interface{n.Iface}))
	}
	resolver, err := zeroconf.NewResolver(opts...)
	if err != nil {
		log.Printf("mdns resolver: %v", err)
		return nil
	}
	entries := make(chan *zeroconf.ServiceEntry)
	seen := map[string]Peer{}
	done := make(chan struct{})
	go func() {
		for e := range entries {
			// zeroconf escapes spaces in instance names ("My\ Phone")
			if strings.ReplaceAll(e.Instance, `\ `, " ") == self || len(e.AddrIPv4) == 0 {
				continue
			}
			seen[e.Instance] = Peer{Name: e.Instance, IP: e.AddrIPv4[0].String(), Port: e.Port}
		}
		close(done)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := resolver.Browse(ctx, mdnsService, "local.", entries); err != nil {
		log.Printf("mdns browse: %v", err)
		return nil
	}
	<-ctx.Done()
	<-done
	out := make([]Peer, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	return out
}
