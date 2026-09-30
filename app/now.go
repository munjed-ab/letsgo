package app

import (
	"cmp"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Now is what this device is playing right now, for mini-players, the phone's
// notification and the desktop's media keys. If this device is not casting but
// is hearing another one, it describes THAT device's track, and Do acts on that
// device: pause on your phone's notification pauses the laptop that is playing.
type Now struct {
	Track    string  `json:"track"`
	Title    string  `json:"title"` // from the tags, else the file name
	Artist   string  `json:"artist"`
	Album    string  `json:"album"`
	Art      string  `json:"art"` // cover art hash; GET /api/art/<hash> on Source (or here if Source is empty)
	Context  string  `json:"context"`
	Playing  bool    `json:"playing"`
	Elapsed  float64 `json:"elapsed"`
	Duration float64 `json:"duration"`
	Remote   bool    `json:"remote"`
	Source   string  `json:"source"` // host:port of the device being heard, when Remote
}

// peerHTTPPort is the web/API port on other devices; every letsgo device uses the
// same default. A variable so tests can run several nodes on one machine
// (LETSGO_PEER_PORT does the same for separate processes).
var peerHTTPPort = cmp.Or(os.Getenv("LETSGO_PEER_PORT"), "8080")

// peerNow caches the answer from the device we are hearing, so a UI polling
// every second doesn't turn into a request per poll.
type peerNow struct {
	mu   sync.Mutex
	addr string
	at   time.Time
	now  Now
	ok   bool
}

func (n *Node) localNow() Now {
	st := n.p.State()
	if !st.Playing && !st.Started {
		// An idle queue is not "something playing": without this a device that
		// merely has a music library would never follow a paused peer.
		return Now{}
	}
	i := n.meta.Get(st.Track)
	title := i.Title
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(st.Track), filepath.Ext(st.Track))
	}
	return Now{
		Track: st.Track, Title: title, Artist: i.Artist, Album: i.Album, Art: i.Art,
		Context: st.Context, Playing: st.Playing, Elapsed: st.Elapsed, Duration: st.Duration,
	}
}

// httpAddr of the device we listen to, "" if we are not listening to another device.
func (n *Node) hearing() string {
	n.mu.Lock()
	from, c := n.from, n.client
	n.mu.Unlock()
	if c == nil || !c.Live() || from == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(from)
	if err != nil || from == net.JoinHostPort("127.0.0.1", strconv.Itoa(n.snapPort)) { // our own stream
		return ""
	}
	return net.JoinHostPort(host, n.peerPortStr())
}

// Now returns what is playing here, or on the device we are hearing when that
// one is playing (or paused, for a device with nothing of its own) and we are not.
func (n *Node) Now() Now {
	local := n.localNow()
	if local.Playing {
		return local
	}
	addr := n.hearing()
	if addr == "" {
		return local
	}
	n.peer.mu.Lock()
	defer n.peer.mu.Unlock()
	if n.peer.addr != addr || time.Since(n.peer.at) > time.Second {
		n.peer.addr, n.peer.at = addr, time.Now()
		n.peer.now, n.peer.ok = Now{}, false
		c := http.Client{Timeout: 400 * time.Millisecond}
		if resp, err := c.Get("http://" + addr + "/api/now?local=1"); err == nil {
			n.peer.ok = json.NewDecoder(resp.Body).Decode(&n.peer.now) == nil
			resp.Body.Close()
		}
	}
	// Follow the peer while it plays. Once it is paused keep following it if we
	// have nothing of our own queued, so "play" on this device's controls resumes
	// it instead of doing nothing.
	if n.peer.ok && (n.peer.now.Playing || (n.peer.now.Track != "" && local.Track == "")) {
		r := n.peer.now
		r.Remote, r.Source = true, addr
		if r.Playing { // the answer may be up to a second old (see above): move it on, or a video shown here would lag
			r.Elapsed += time.Since(n.peer.at).Seconds()
			if r.Duration > 0 {
				r.Elapsed = min(r.Elapsed, r.Duration)
			}
		}
		return r
	}
	return local
}

// Do sends a media command to whichever device Now describes: "play", "pause",
// "toggle", "next", "prev", or "seek" (arg = seconds).
func (n *Node) Do(cmd string, arg float64) error {
	if now := n.Now(); now.Remote {
		q := url.Values{"cmd": {cmd}, "t": {strconv.FormatFloat(arg, 'f', 3, 64)}, "local": {"1"}}
		c := http.Client{Timeout: 2 * time.Second}
		resp, err := c.Post("http://"+now.Source+"/api/control?"+q.Encode(), "", nil)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return errBad
		}
		n.peer.mu.Lock()
		n.peer.at = time.Time{} // re-read the peer's state on the next Now
		n.peer.mu.Unlock()
		return nil
	}
	return n.doLocal(cmd, arg)
}

func (n *Node) doLocal(cmd string, arg float64) error {
	switch cmd {
	case "play":
		n.p.Resume()
	case "pause":
		n.p.Pause()
	case "toggle":
		n.p.Toggle()
	case "next":
		n.p.Next()
	case "prev":
		n.p.Prev()
	case "seek":
		n.p.Seek(arg)
	default:
		return errBad
	}
	n.kickSupervisor() // a play here must be heard at once, not at the supervisor's next look
	return nil
}

// ArtFile makes now's cover art available as a file under dir (desktop widgets
// only take file:// URLs) and returns its path, or "" if the track has none.
func (n *Node) ArtFile(now Now, dir string) string {
	if now.Art == "" {
		return ""
	}
	path := filepath.Join(dir, now.Art+".jpg")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	var data []byte
	if now.Remote {
		c := http.Client{Timeout: 2 * time.Second}
		resp, err := c.Get("http://" + now.Source + "/api/art/" + url.PathEscape(now.Art) + "?s=512")
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return ""
		}
		data, _ = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	} else {
		data, _, _ = n.meta.Art(now.Art, 512)
	}
	if len(data) == 0 {
		return ""
	}
	os.MkdirAll(dir, 0o755)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) != nil || os.Rename(tmp, path) != nil {
		return ""
	}
	return path
}
