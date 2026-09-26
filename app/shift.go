package app

import (
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"time"

	"letsgo/snap"
)

// Shift is what happens when a device that is only listening (or idle) starts playing a song
// of its own while other devices are connected: it becomes the source, and everyone else
// pauses and follows it. Whichever device plays last is talking; the others listen.
//
// The device that starts playing tells the others (POST /api/shift on each), so it does not
// depend on them noticing by themselves. The message also carries who is now the source
// (the caller's address), so a device that mDNS has not found yet still follows.

// shift is called when this device has just started playing: everyone we know about is asked
// to pause and follow us. That is the devices mDNS found, the one we are listening to (the
// previous source, known for certain even if discovery is not working) and a pinned one.
func (n *Node) shift() {
	n.mu.Lock()
	from, pinned := n.from, n.pinned
	n.mu.Unlock()
	var hosts []string
	add := func(addr string) {
		if h := hostOf(addr); h != "" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	for _, p := range n.knownPeers() {
		add(p.IP)
	}
	if from != net.JoinHostPort("127.0.0.1", strconv.Itoa(n.snapPort)) { // not our own stream
		add(from)
	}
	add(pinned)
	if len(hosts) == 0 {
		return
	}
	log.Printf("shift: now playing here, asking %v to follow", hosts)
	for _, h := range hosts {
		go n.tellShift(h)
	}
}

func (n *Node) tellShift(host string) {
	c := http.Client{Timeout: 800 * time.Millisecond}
	base := "http://" + net.JoinHostPort(host, n.peerPortStr())
	var resp *http.Response
	var err error
	for try := 0; try < 2; try++ { // one retry: Wi-Fi in power save sometimes drops the first packet
		if resp, err = c.Post(base+"/api/shift?port="+strconv.Itoa(n.snapPort), "", nil); err == nil {
			break
		}
	}
	if err != nil {
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent { // an older node has no /api/shift (it answers with its web page): just pause it
		if r, err := c.Post(base+"/api/control?cmd=pause&local=1", "", nil); err == nil {
			r.Body.Close()
		}
	}
}

// shiftRoutes handles the other end:
//
//	POST /api/shift[?port=SNAPPORT]   the caller has started playing: pause here, follow it
func (n *Node) shiftRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/shift", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "unknown caller", http.StatusBadRequest)
			return
		}
		port, _ := strconv.Atoi(r.URL.Query().Get("port"))
		if port <= 0 || port > 65535 {
			port = 1704
		}
		n.mu.Lock()
		name := host
		if known, ok := n.seen[host]; ok {
			name = known.Name // keep the name mDNS gave it
		}
		n.mu.Unlock()
		n.setPeers(snap.Peer{Name: name, IP: host, Port: port}) // it is the source now, whether or not mDNS shows it
		n.p.Pause()
		n.kickSupervisor()
		log.Printf("shift: %s started playing, following it", host)
		w.WriteHeader(http.StatusNoContent)
	})
}
