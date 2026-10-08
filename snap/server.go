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
	opusHdr  []byte

	encMu sync.Mutex     // the encoder is used by Broadcast and Flush, outside mu: encoding takes ~0.5 ms on a phone
	enc   *opusEnc       // nil if it could not be made: everyone gets PCM
	udp   net.PacketConn // sends the UDP copy of the Opus chunks (see opus.go); nil if none could be opened
	seq   uint16         // sequence number of the next chunk, in the header's id

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
	ts       time.Duration
	msg, opu []byte // PCM and Opus; never written to a socket themselves: sessions get copies
}

type session struct {
	conn  net.Conn
	out   chan outMsg
	name  string
	ready bool         // true after Hello, only then do we send audio
	opus  bool         // it asked for Opus (see HelloMsg)
	udp   *net.UDPAddr // where it wants the UDP copy, nil for none
	drops int          // chunks dropped because this client's queue was full
}

// outMsg is one message queued for a listener. due, for audio, is the server time after which no
// listener can still play it (see Server.due): later than that it is not sent at all.
type outMsg struct {
	b   []byte
	due time.Duration // 0 = always send
}

type ClientInfo struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
}

func NewServer(bufferMs, rate, bits, ch int) *Server {
	s := &Server{
		BufferMs: bufferMs,
		codecHdr: CodecHeaderMsg("pcm", WavHeader(rate, bits, ch)),
		opusHdr:  CodecHeaderMsg("opus", opusHeader(rate, bits, ch)),
		sessions: map[*session]struct{}{},
		volume:   100,
	}
	if bits == 16 {
		if enc, err := newOpusEnc(rate, ch); err == nil {
			s.enc = enc
		} else {
			log.Printf("opus: %v (sending PCM to everyone)", err)
		}
	}
	return s
}

func (s *Server) Listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	u, err := net.ListenPacket("udp", ":0")
	if err != nil {
		log.Printf("udp: %v (audio goes over TCP only)", err)
	}
	s.mu.Lock()
	s.ln, s.udp = ln, u
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
	if s.udp != nil {
		s.udp.Close()
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
	ss := &session{conn: c, out: make(chan outMsg, 256), name: c.RemoteAddr().String()}
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
			var hello struct {
				HostName, ClientName, Codecs string
				Udp                          int
			}
			if len(p) > 4 {
				json.Unmarshal(p[4:], &hello)
			}
			s.mu.Lock()
			if hello.HostName != "" {
				ss.name = hello.HostName
			}
			ss.opus = s.enc != nil && hello.Codecs == "opus"
			if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil && ss.opus && s.udp != nil && hello.Udp > 0 && hello.Udp < 65536 {
				ss.udp = net.UDPAddrFromAddrPort(netip.AddrPortFrom(ap.Addr(), uint16(hello.Udp)))
			}
			ss.send(s.settingsMsg(h.ID))
			hdr := s.codecHdr
			if ss.opus {
				hdr = s.opusHdr
			}
			ss.send(append([]byte(nil), hdr...))                                      // copy: writer stamps the header in place
			if time.Since(s.lastChunk) < time.Duration(s.BufferMs)*time.Millisecond { // still streaming
				for _, c := range s.recent {
					if m := c.pick(ss.opus); m != nil {
						ss.sendAudio(append([]byte(nil), m...), s.due(c.ts))
						s.sendUDP(ss, m)
					}
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
func (ss *session) send(b []byte) bool { return ss.sendAudio(b, 0) }

func (ss *session) sendAudio(b []byte, due time.Duration) bool {
	select {
	case ss.out <- outMsg{b, due}:
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
		case m := <-ss.out:
			// After a Wi-Fi stall the queue is full of audio whose time has passed. Sending it would
			// only keep the link busy while the listener drops it: skip to what can still be heard.
			if m.due != 0 && Now() > m.due {
				continue
			}
			stampSent(m.b)
			// A stall of a few seconds on a busy Wi-Fi is ridden out: hanging up costs a reconnect
			// and a new clock lock on top. Only a link that is gone (the 15 s read deadline) ends it.
			ss.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := ss.conn.Write(m.b); err != nil {
				ss.conn.Close() // reader loop sees it and cleans up
				return
			}
		}
	}
}

// Broadcast one audio chunk to every ready client.
// The chunk bytes are shared, stampSent rewrites the header per-write,
// so each session gets its own copy of the header.
//
// It is always encoded to Opus too, listeners or not: a device that joins needs the recent audio
// in Opus to be audible at once (see recent).
func (s *Server) Broadcast(ts time.Duration, pcm []byte) {
	var pkt []byte
	if s.enc != nil {
		s.encMu.Lock()
		pkt = s.enc.encode(pcm)
		s.encMu.Unlock()
	}
	s.mu.Lock()
	c := recentChunk{ts: ts, msg: WireChunkMsg(ts, pcm, s.epoch)}
	if pkt != nil {
		c.opu = WireChunkMsg(ts, pkt, s.epoch)
		le.PutUint16(c.opu[2:], s.seq) // the listener puts the UDP and TCP copies back in order by it
	}
	s.seq++
	s.lastChunk = time.Now()
	s.recent = append(s.recent, c)
	for len(s.recent) > 1 && s.recent[0].ts < ts-time.Duration(s.BufferMs)*time.Millisecond {
		s.recent = s.recent[1:]
	}
	for ss := range s.sessions {
		if m := c.pick(ss.opus); ss.ready && m != nil {
			ss.sendAudio(append([]byte(nil), m...), s.due(ts))
			s.sendUDP(ss, m)
		}
	}
	s.mu.Unlock()
}

// sendUDP sends the UDP copy of an Opus chunk to a listener that asked for one. A datagram never
// blocks for long, and one that is lost is the TCP copy's job (called with mu held).
func (s *Server) sendUDP(ss *session, msg []byte) {
	if ss.udp != nil {
		s.udp.WriteTo(msg, ss.udp)
	}
}

// pick is the chunk in the listener's codec (nil: there is none, the encoder failed on it).
func (c recentChunk) pick(opus bool) []byte {
	if opus {
		return c.opu
	}
	return c.msg
}

// due is the latest a chunk stamped ts can still be heard by a listener: one buffer after it, plus
// the most a device's own sync offset can delay it (the app allows up to 2 s).
func (s *Server) due(ts time.Duration) time.Duration {
	return ts + time.Duration(s.BufferMs)*time.Millisecond + 2*time.Second
}

// Flush starts a new timeline: every listener drops the audio it has queued and
// waits for the chunks that follow. The player calls it when it pauses, resumes,
// jumps or seeks, so those take effect now rather than a buffer from now.
func (s *Server) Flush() {
	if s.enc != nil {
		s.encMu.Lock()
		s.enc.reset()
		s.encMu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	s.recent = nil
	msg := WireChunkMsg(Now(), nil, s.epoch) // an empty chunk just carries the new epoch,
	le.PutUint16(msg[2:], s.seq)             // and the sequence number its first chunk will have
	for ss := range s.sessions {
		if ss.ready {
			ss.send(append([]byte(nil), msg...))
			s.sendUDP(ss, msg)
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
