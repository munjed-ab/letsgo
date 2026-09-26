package snap

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"
)

// Server accepts snapclients on :1704 and fans audio out to all of them.
//
// "Never halt" rule: nothing a client does can block the audio loop.
// Each client has its own queue; if it's full (slow phone, weak Wi-Fi),
// that client's chunks get dropped and everyone else keeps playing.
type Server struct {
	BufferMs int
	codecHdr []byte

	mu       sync.Mutex
	ln       net.Listener
	sessions map[*session]struct{}
	volume   int
	muted    bool

	// recent holds the chunks still due to be heard (the last BufferMs of the stream).
	// A device that joins mid-song gets them at once and is audible immediately,
	// in step with everyone else, instead of waiting a whole buffer for fresh chunks.
	recent    []recentChunk
	lastChunk time.Time // wall clock of the last Broadcast, to tell a live stream from a stale one
	epoch     uint16    // bumped by Flush; stamped on every chunk (see WireChunkMsg)
}

type recentChunk struct {
	ts  time.Duration
	msg []byte // never written to a socket itself: sessions get copies
}

type session struct {
	conn  net.Conn
	out   chan []byte
	name  string
	ready bool // true after Hello, only then do we send audio
	drops int  // chunks dropped because this client's queue was full
}

type ClientInfo struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
}

func NewServer(bufferMs, rate, bits, ch int) *Server {
	return &Server{
		BufferMs: bufferMs,
		codecHdr: CodecHeaderMsg("pcm", WavHeader(rate, bits, ch)),
		sessions: map[*session]struct{}{},
		volume:   100,
	}
}

func (s *Server) Listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				time.Sleep(100 * time.Millisecond) // e.g. fd limit, back off, don't spin
				continue
			}
			go s.handle(c)
		}
	}()
	return nil
}

// Close stops listening and disconnects every client, freeing the port so a
// new Server can bind it (Android keeps the process alive across service restarts).
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		s.ln.Close()
	}
	for ss := range s.sessions {
		ss.conn.Close()
	}
}

func (s *Server) settingsMsg(refersTo uint16) []byte {
	j, _ := json.Marshal(map[string]any{
		"bufferMs": s.BufferMs, "latency": 0, "muted": s.muted, "volume": s.volume,
	})
	return ServerSettingsMsg(j, refersTo)
}

func (s *Server) handle(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(true) // small time pings must not wait for Nagle
	}
	// 256 x 20ms chunks = ~5s of slack per client before we start dropping
	ss := &session{conn: c, out: make(chan []byte, 256), name: c.RemoteAddr().String()}
	s.mu.Lock()
	s.sessions[ss] = struct{}{}
	s.mu.Unlock()
	log.Printf("connect %s", ss.name)

	done := make(chan struct{})
	go ss.writer(done)
	defer func() {
		s.mu.Lock()
		delete(s.sessions, ss)
		s.mu.Unlock()
		close(done)
		c.Close()
		log.Printf("disconnect %s", ss.name)
	}()

	for {
		// clients ping every second; 15s of silence = dead (phone left the area)
		c.SetReadDeadline(time.Now().Add(15 * time.Second))
		h, p, err := ReadMsg(c)
		if err != nil {
			return
		}
		switch h.Type {
		case TypeTime:
			ss.send(TimeReply(h))
		case TypeHello:
			var hello struct{ HostName, ClientName string }
			if len(p) > 4 {
				json.Unmarshal(p[4:], &hello)
			}
			s.mu.Lock()
			if hello.HostName != "" {
				ss.name = hello.HostName
			}
			ss.send(s.settingsMsg(h.ID))
			ss.send(append([]byte(nil), s.codecHdr...))                               // copy: writer stamps the header in place
			if time.Since(s.lastChunk) < time.Duration(s.BufferMs)*time.Millisecond { // still streaming
				for _, c := range s.recent {
					ss.send(append([]byte(nil), c.msg...))
				}
			}
			ss.ready = true
			s.mu.Unlock()
			log.Printf("hello %s (%s)", ss.name, c.RemoteAddr())
		}
		// ClientInfo (client-side volume) and anything unknown: ignored
	}
}

// send never blocks: full queue = drop. Returns false if it dropped.
func (ss *session) send(b []byte) bool {
	select {
	case ss.out <- b:
		return true
	default:
		return false
	}
}

func (ss *session) writer(done chan struct{}) {
	for {
		select {
		case <-done:
			return
		case b := <-ss.out:
			stampSent(b)
			ss.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
			if _, err := ss.conn.Write(b); err != nil {
				ss.conn.Close() // reader loop sees it and cleans up
				return
			}
		}
	}
}

// Broadcast one audio chunk to every ready client.
// The chunk bytes are shared, stampSent rewrites the header per-write,
// so each session gets its own copy of the header.
func (s *Server) Broadcast(ts time.Duration, pcm []byte) {
	s.mu.Lock()
	msg := WireChunkMsg(ts, pcm, s.epoch)
	s.lastChunk = time.Now()
	s.recent = append(s.recent, recentChunk{ts, msg})
	for len(s.recent) > 1 && s.recent[0].ts < ts-time.Duration(s.BufferMs)*time.Millisecond {
		s.recent = s.recent[1:]
	}
	for ss := range s.sessions {
		if ss.ready {
			cp := make([]byte, len(msg))
			copy(cp, msg)
			ss.send(cp)
		}
	}
	s.mu.Unlock()
}

// Flush starts a new timeline: every listener drops the audio it has queued and
// waits for the chunks that follow. The player calls it when it pauses, resumes,
// jumps or seeks, so those take effect now rather than a buffer from now.
func (s *Server) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	s.recent = nil
	msg := WireChunkMsg(Now(), nil, s.epoch) // an empty chunk just carries the new epoch
	for ss := range s.sessions {
		if ss.ready {
			ss.send(append([]byte(nil), msg...))
		}
	}
}

// SetVolume pushes a master volume (0..100) to all clients.
func (s *Server) SetVolume(v int, muted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volume, s.muted = max(0, min(100, v)), muted
	m := s.settingsMsg(0)
	for ss := range s.sessions {
		ss.send(append([]byte(nil), m...))
	}
}

func (s *Server) Volume() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.volume, s.muted
}

// Clients lists the connected listeners by device name. The server's own
// device (a loopback connection) is marked, and identical names get the
// address appended so every entry is tellable apart.
func (s *Server) Clients() []ClientInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ClientInfo{}
	count := map[string]int{}
	for ss := range s.sessions {
		if ss.ready {
			ap, _ := netip.ParseAddrPort(ss.conn.RemoteAddr().String())
			ci := ClientInfo{ss.name, ss.conn.RemoteAddr().String()}
			if ap.Addr().IsLoopback() {
				ci.Name += " (this device)"
			}
			count[ci.Name]++
			out = append(out, ci)
		}
	}
	for i, ci := range out {
		if count[ci.Name] > 1 {
			out[i].Name += " (" + ci.Addr + ")"
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
