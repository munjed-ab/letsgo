// Package app is the shared core of a letsgo node: the snapcast server, the web
// UI / HTTP control API, and mDNS discovery. Desktop, CLI and the Android .aar
// all build on this. Audio playback (the client's Read output) is wired up per
// platform: oto on desktop, AudioTrack on Android.
package app

import (
	"cmp"
	_ "embed"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"letsgo/meta"
	"letsgo/player"
	"letsgo/snap"

	"github.com/grandcat/zeroconf"
)

//go:embed index.html
var indexHTML []byte

//go:embed logo.svg
var logoSVG []byte

// Version identifies this build to a copy of the desktop app that is started later, so it
// can tell an older one is still running (see Quit). Empty on the phone and headless builds.
var Version string

// Quit, when set (the desktop app sets it), lets POST /api/quit from this machine shut the
// app down, so that a newer build started later can replace it instead of showing its old window.
var Quit func()

// Discovery turns mDNS on or off: advertising this node and finding others on the
// network. Off it still works with addresses you give it (Pin, or a stock
// Snapcast client). Tests and -no-discovery switch it off; LETSGO_NO_DISCOVERY=1
// does the same from the environment.
var Discovery = os.Getenv("LETSGO_NO_DISCOVERY") == ""

// Node is one running letsgo instance: it serves audio (server) and can play a
// peer's stream (client). Both roles can be active; typically you cast OR listen.
type Node struct {
	srv      *snap.Server
	p        *player.Player
	lists    *Lists
	plays    *Plays
	sources  *Sources
	meta     *meta.Index
	peer     peerNow
	httpSrv  *http.Server
	instance string
	done     chan struct{}
	snapPort int
	pinned   string        // if set, the source to listen to when we are not casting (skips discovery)
	kick     chan struct{} // wakes the supervisor now: something just started playing here
	peerPort string        // other devices' API port; "" = peerHTTPPort (guarded by mu)

	mu        sync.Mutex
	client    *snap.Client
	from      string        // source addr we're listening to, "" if not
	latencyMs int           // per-device audio delay (calibration)
	outLat    time.Duration // how long audio handed to the device takes to be heard
	mdns      *zeroconf.Server
	netHint   *snap.Net           // interface/IP for mDNS where Go cannot find them (Android)
	seen      map[string]seenPeer // peers mDNS found lately, by IP; kept fresh by discover
}

const (
	superviseEvery = 400 * time.Millisecond // how often the source is re-chosen; cheap, so a hand-over is noticed fast
	scanEvery      = 4 * time.Second        // mDNS scans, in the background
	peerTTL        = 15 * time.Second       // a peer that misses this many seconds of scans is gone (scans are flaky)
)

type seenPeer struct {
	snap.Peer
	at time.Time
}

// SetLatency sets the per-device audio delay (calibration knob).
func (n *Node) SetLatency(ms int) {
	n.mu.Lock()
	n.latencyMs = ms
	if n.client != nil {
		n.client.SetLatency(ms)
	}
	n.mu.Unlock()
}

// SetOutputLatency tells the node how long audio it hands to the platform's audio
// device takes to be heard (device queue + hardware). This is what lets a phone
// with a deep AudioTrack buffer and a laptop with a shallow one play in sync.
// Cheap enough to call on every audio write.
func (n *Node) SetOutputLatency(d time.Duration) {
	n.mu.Lock()
	n.outLat = d
	if n.client != nil {
		n.client.SetOutputLatency(d)
	}
	n.mu.Unlock()
}

// Start brings up the snapcast server, the web UI/API, and mDNS advertisement.
// musicDirs are the default music folders (a saved list, changed through the API,
// replaces them); dataDir is where favourites, playlists and that list are saved
// ("" = not saved). Blank
// addrs default to :1704 / :8080; blank instance defaults to "letsgo".
func Start(musicDirs []string, dataDir, snapAddr, httpAddr, instance string, buffer int) (*Node, error) {
	if snapAddr == "" {
		snapAddr = ":1704"
	}
	if httpAddr == "" {
		httpAddr = ":8080"
	}
	if instance == "" {
		instance = "letsgo"
	}
	if buffer <= 0 {
		buffer = 1000
	}

	srv := snap.NewServer(buffer, player.Rate, player.Bits, player.Channels)
	if err := srv.Listen(snapAddr); err != nil {
		return nil, err
	}
	sources := OpenSources(dataFile(dataDir, "sources.json"), musicDirs)
	n := &Node{
		srv:      srv,
		sources:  sources,
		p:        player.New(sources.List(), srv, snap.Now),
		lists:    OpenLists(dataFile(dataDir, "lists.json")),
		plays:    OpenPlays(dataFile(dataDir, "plays.json")),
		meta:     meta.Open(dataFile(dataDir, "meta.json")),
		instance: instance,
		done:     make(chan struct{}),
		kick:     make(chan struct{}, 1),
		seen:     map[string]seenPeer{},
	}

	ln, err := net.Listen("tcp", httpAddr)
	if err != nil {
		n.p.Close()
		srv.Close()
		return nil, err
	}
	n.httpSrv = &http.Server{Handler: guard(n.mux())}
	go n.httpSrv.Serve(ln)

	_, portStr, _ := net.SplitHostPort(snapAddr)
	n.snapPort, _ = strconv.Atoi(portStr)
	if n.snapPort == 0 {
		n.snapPort = 1704
	}
	n.p.SetBuffer(time.Duration(buffer) * time.Millisecond)
	n.p.SetPlayedHook(n.plays.Record)
	n.meta.Scan(n.p.Files())
	n.advertise()
	go n.supervise()
	go n.discover()
	return n, nil
}

func dataFile(dataDir, name string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, name)
}

// advertise (re)announces this node over mDNS. Best-effort: the server works
// without it, peers just need to be given our address.
func (n *Node) advertise() {
	if !Discovery {
		return
	}
	n.mu.Lock()
	hint, old := n.netHint, n.mdns
	n.mu.Unlock()
	if old != nil {
		old.Shutdown()
	}
	m, err := snap.Advertise(n.instance, n.snapPort, hint)
	if err != nil {
		log.Printf("mdns advertise: %v", err)
	} else if hint != nil {
		log.Printf("mdns advertising %q on %s (%s)", n.instance, hint.Iface.Name, hint.IP)
	}
	n.mu.Lock()
	n.mdns = m
	n.mu.Unlock()
}

// SetNetwork tells the node which interface/IP to use for mDNS, for platforms
// where Go cannot enumerate them (Android). Call again whenever the network
// changes; identical calls are free.
func (n *Node) SetNetwork(name string, index int, ip string) {
	n.mu.Lock()
	same := n.netHint != nil && n.netHint.Iface.Name == name && n.netHint.Iface.Index == index && n.netHint.IP == ip
	if !same {
		n.netHint = &snap.Net{
			Iface: net.Interface{Index: index, Name: name, Flags: net.FlagUp | net.FlagMulticast | net.FlagBroadcast},
			IP:    ip,
		}
	}
	n.mu.Unlock()
	if !same {
		n.advertise()
	}
}

func (n *Node) pinnedAddr() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.pinned
}

// Pin makes this node listen to addr ("ip" or "ip:port") whenever it is not
// casting itself, instead of discovering peers. For networks that block mDNS.
func (n *Node) Pin(addr string) {
	if _, _, err := net.SplitHostPort(addr); err != nil && addr != "" {
		addr += ":1704"
	}
	n.mu.Lock()
	n.pinned = addr
	n.mu.Unlock()
	n.kickSupervisor()
}

// supervise keeps the audio source pointed at the right place so every device
// hears something sensible with no manual wiring:
//
//  1. if WE are casting (our player is playing), play our own stream via
//     loopback — the DJ hears their own music, in sync with everyone else;
//  2. else if a peer is casting, play that peer;
//  3. else stay where we are (silence costs nothing, and the next play is heard at once).
//
// It looks every superviseEvery, and at once when kickSupervisor says something started here.
func (n *Node) supervise() {
	cur := ""
	wasPlaying := false
	for {
		select {
		case <-n.done:
			return
		case <-n.kick:
		case <-time.After(superviseEvery):
		}
		if playing := n.p.State().Playing; playing && !wasPlaying {
			go n.shift() // we just started playing: whoever else was the source now follows us (see shift.go)
			wasPlaying = true
		} else if !playing {
			wasPlaying = false
		}
		want := n.decideSource()
		select {
		case <-n.done: // stopped while deciding: don't reconnect
			return
		default:
		}
		n.mu.Lock()
		dead := n.client != nil && !n.client.Live()
		latency := n.latencyMs
		n.mu.Unlock()
		if want == cur && !dead {
			continue
		}
		if want == "" {
			n.Disconnect()
		} else if _, err := n.Connect(want, latency); err != nil {
			cur = "" // retry next tick
			continue
		}
		log.Printf("source: %q -> %q (dead=%v)", cur, want, dead)
		cur = want
	}
}

// kickSupervisor makes the supervisor look now instead of at its next tick.
func (n *Node) kickSupervisor() {
	select {
	case n.kick <- struct{}{}:
	default:
	}
}

func (n *Node) decideSource() string {
	loopback := net.JoinHostPort("127.0.0.1", strconv.Itoa(n.snapPort))
	if n.p.State().Playing {
		return loopback // self-loopback: hear what we cast
	}
	n.mu.Lock()
	cur, pinned := n.from, n.pinned
	n.mu.Unlock()
	if pinned != "" {
		return pinned
	}
	// Ask everyone we might follow, all at once, whether they are casting.
	peers := n.knownPeers()
	hosts := make([]string, 0, len(peers)+1)
	if cur != "" && cur != loopback {
		hosts = append(hosts, hostOf(cur))
	}
	for _, p := range peers {
		if !slices.Contains(hosts, p.IP) {
			hosts = append(hosts, p.IP)
		}
	}
	casting := n.castingNow(hosts)
	// Stick with the peer we have while it is casting: a missed mDNS scan can never cut the music.
	if cur != "" && cur != loopback && casting[hostOf(cur)] {
		return cur
	}
	for _, p := range peers {
		if casting[p.IP] {
			return net.JoinHostPort(p.IP, strconv.Itoa(cmp.Or(p.Port, 1704)))
		}
	}
	// Nobody is casting. Stay attached to whatever we have (our own stream or the last
	// peer): silence costs nothing and the next play, here or there, is heard at once.
	// (If that peer vanished, the supervisor notices the dead connection and looks again.)
	return cur
}

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// castingNow asks each host's API whether it is playing, all at once.
func (n *Node) castingNow(hosts []string) map[string]bool {
	out := make(map[string]bool, len(hosts))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := n.peerCasting(h)
			mu.Lock()
			out[h] = c
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// peerCasting asks a peer's HTTP API whether it is actively playing its own music.
func (n *Node) peerCasting(addr string) bool {
	c := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := c.Get("http://" + net.JoinHostPort(hostOf(addr), n.peerPortStr()) + "/api/now?local=1")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var s struct {
		Playing bool `json:"playing"`
	}
	return json.NewDecoder(resp.Body).Decode(&s) == nil && s.Playing
}

// peerPortStr is the API port of other devices.
func (n *Node) peerPortStr() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.peerPort != "" {
		return n.peerPort
	}
	return peerHTTPPort
}

// setPeerPort overrides the API port of other devices for this node only. Tests use it to run
// several nodes on one machine (nothing writes the package-level default while nodes are running).
func (n *Node) setPeerPort(p string) {
	n.mu.Lock()
	n.peerPort = p
	n.mu.Unlock()
}

// discover keeps the list of peers fresh in the background. A scan takes over a second and
// must never hold up the choice of source.
func (n *Node) discover() {
	if !Discovery {
		return
	}
	var lastSweep time.Time
	lastAdvert := time.Now() // Start has just announced us
	for {
		n.mu.Lock()
		hint := n.netHint
		n.mu.Unlock()
		n.setPeers(snap.Browse(1200*time.Millisecond, n.instance, hint)...)
		n.keepKnown()
		if len(n.knownPeers()) == 0 { // multicast may not be getting through (a hotspot): ask by address, and announce again
			if time.Since(lastSweep) > sweepEvery && n.sweep() {
				lastSweep = time.Now()
			}
			if time.Since(lastAdvert) > readvertiseEvery { // a network that went down and up leaves the old announcement deaf
				lastAdvert = time.Now()
				n.advertise()
			}
		}
		select {
		case <-n.done:
			return
		case <-time.After(scanEvery):
		}
	}
}

func (n *Node) setPeers(ps ...snap.Peer) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, p := range ps {
		n.seen[p.IP] = seenPeer{p, time.Now()}
	}
}

// knownPeers are the other devices seen in the last peerTTL, in a stable order (excluding us).
func (n *Node) knownPeers() []snap.Peer {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []snap.Peer
	for ip, p := range n.seen {
		switch {
		case time.Since(p.at) > peerTTL:
			delete(n.seen, ip)
		case n.netHint != nil && ip == n.netHint.IP: // that is us
		default:
			out = append(out, p.Peer)
		}
	}
	slices.SortFunc(out, func(a, b snap.Peer) int { return cmp.Compare(a.IP, b.IP) })
	return out
}

// Read fills buf with the PCM the current source says is due now (silence if no
// source). This is what the platform audio device (oto / AudioTrack) pulls.
func (n *Node) Read(buf []byte) (int, error) {
	n.mu.Lock()
	c := n.client
	n.mu.Unlock()
	if c == nil || !c.Live() {
		for i := range buf {
			buf[i] = 0
		}
		return len(buf), nil
	}
	return c.Read(buf)
}

func (n *Node) mux() *http.ServeMux {
	mux := http.NewServeMux()
	cmd := func(f func(r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", http.StatusMethodNotAllowed)
				return
			}
			f(r)
			n.kickSupervisor()
			w.WriteHeader(http.StatusNoContent)
		}
	}
	num := func(r *http.Request, k string) int { v, _ := strconv.Atoi(r.URL.Query().Get(k)); return v }

	mux.HandleFunc("/logo.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(logoSVG)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		vol, muted := n.srv.Volume()
		n.mu.Lock()
		from, c, lat := n.from, n.client, n.latencyMs
		n.mu.Unlock()
		st := map[string]any{
			"name": n.instance, "snapPort": n.snapPort, "version": Version, "player": n.p.State(), "volume": vol, "muted": muted,
			"clients": n.srv.Clients(), "listeningTo": from, "latencyMs": lat, "pinned": n.pinnedAddr(), "now": n.Now(),
		}
		if c != nil {
			st["audio"] = c.Stats() // what THIS device is playing: buffer, sync error, glitches
		}
		json.NewEncoder(w).Encode(st)
	})
	mux.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if r.Method != http.MethodPost || Quit == nil || !net.ParseIP(host).IsLoopback() {
			http.Error(w, "not available", http.StatusForbidden) // only this machine may stop the app, not the network
			return
		}
		w.WriteHeader(http.StatusNoContent)
		go Quit()
	})
	n.listRoutes(mux)
	n.mediaRoutes(mux)
	n.devRoutes(mux)
	n.shiftRoutes(mux)
	mux.HandleFunc("/api/library", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(n.p.Library())
	})
	mux.HandleFunc("/api/play", cmd(func(r *http.Request) { n.p.PlayIndex(num(r, "i")) }))
	mux.HandleFunc("/api/toggle", cmd(func(*http.Request) { n.p.Toggle() }))
	mux.HandleFunc("/api/next", cmd(func(*http.Request) { n.p.Next() }))
	mux.HandleFunc("/api/prev", cmd(func(*http.Request) { n.p.Prev() }))
	mux.HandleFunc("/api/shuffle", cmd(func(r *http.Request) { n.p.SetShuffle(r.URL.Query().Get("on") == "1") }))
	// This device's own sync offset: + plays later, - earlier. Fine-tunes one speaker
	// (Bluetooth adds 100-250 ms) without touching the others.
	mux.HandleFunc("/api/latency", cmd(func(r *http.Request) { n.SetLatency(max(-2000, min(2000, num(r, "ms")))) }))
	mux.HandleFunc("/api/rescan", cmd(func(*http.Request) { n.p.Rescan(); n.meta.Scan(n.p.Files()) }))
	mux.HandleFunc("/api/volume", cmd(func(r *http.Request) {
		n.srv.SetVolume(num(r, "v"), r.URL.Query().Get("mute") == "1")
	}))
	return mux
}

// Peers lists the other letsgo devices on the network (excludes self). The background
// scan keeps it fresh; before its first result it scans for up to d.
func (n *Node) Peers(d time.Duration) []snap.Peer {
	if !Discovery {
		return nil
	}
	if p := n.knownPeers(); len(p) > 0 {
		return p
	}
	n.mu.Lock()
	hint := n.netHint
	n.mu.Unlock()
	found := snap.Browse(d, n.instance, hint)
	n.setPeers(found...)
	return found
}

// Connect starts (or replaces) the audio client listening to addr ("ip" or
// "ip:port"). The returned client's Read is the PCM source for the audio device.
func (n *Node) Connect(addr string, latencyMs int) (*snap.Client, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr += ":1704"
	}
	c, err := snap.Connect(addr, n.instance, latencyMs)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	if n.client != nil {
		n.client.Close()
	}
	c.SetOutputLatency(n.outLat)
	n.client, n.from = c, addr
	n.mu.Unlock()
	return c, nil
}

// Disconnect stops listening to a peer.
func (n *Node) Disconnect() {
	n.mu.Lock()
	if n.client != nil {
		n.client.Close()
		n.client = nil
	}
	n.from = ""
	n.mu.Unlock()
}

// Stop shuts everything down.
func (n *Node) Stop() {
	close(n.done)
	n.Disconnect()
	n.p.Close()
	n.srv.Close()
	n.mu.Lock()
	m := n.mdns
	n.mu.Unlock()
	if m != nil {
		m.Shutdown()
	}
	if n.httpSrv != nil {
		n.httpSrv.Close()
	}
}
