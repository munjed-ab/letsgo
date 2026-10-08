package snap

import (
	"encoding/json"
	"time"
)

// Client-side message builders. The server half lives in proto.go; here we build
// the two messages a client sends: Hello (once) and Time pings (every second).

// HelloMsg identifies us to the server. The server reads the JSON after the
// 4-byte length prefix and uses HostName as the client's display name. codecs
// "opus" asks a letsgo server for Opus instead of PCM, and udpPort (if not 0) for
// a copy of every chunk over UDP to that port (others ignore both).
func HelloMsg(id uint16, host, name, codecs string, udpPort int) []byte {
	j, _ := json.Marshal(map[string]any{"HostName": host, "ClientName": name, "Codecs": codecs, "Udp": udpPort})
	b := frame(TypeHello, 0, lenPrefixed(j))
	le.PutUint16(b[2:], id)
	return b
}

// TimePing is a clock-sync request. Payload is unused by the server (it only
// needs our Sent stamp and the id to echo), so 8 zero bytes. Sent is written at
// send time by the writer via stampSent — that stamp is our clock at send.
func TimePing(id uint16) []byte {
	b := frame(TypeTime, 0, make([]byte, 8))
	le.PutUint16(b[2:], id)
	return b
}

// ParseTimeReply pulls the "server received - our sent" latency the server put
// in the reply payload. Combined with the reply's own Sent stamp and the moment
// we read it, this gives the client<->server clock offset (see client.go).
func ParseTimeReply(p []byte) time.Duration {
	if len(p) < 8 {
		return 0
	}
	return getTV(p)
}

// StampSent exposes the write-time Sent stamping for the client writer.
func StampSent(b []byte) { stampSent(b) }

// ParseCodecHeader reads a CodecHeader payload: len-prefixed codec name, then a
// len-prefixed codec header (a 44-byte WAV header for "pcm", see opusHeader for "opus").
// Returns the rate/bits/ch of the PCM it stands for.
func ParseCodecHeader(p []byte) (codec string, rate, bits, ch int, ok bool) {
	if len(p) < 4 {
		return
	}
	n := int(le.Uint32(p))
	if len(p) < 4+n+4 {
		return
	}
	codec = string(p[4 : 4+n])
	hdr := p[4+n:]
	m := int(le.Uint32(hdr))
	hdr = hdr[4:]
	if codec == "opus" && len(hdr) >= m && m >= 12 && le.Uint32(hdr) == opusMarker { // see opusHeader
		return codec, int(le.Uint32(hdr[4:])), int(le.Uint16(hdr[8:])), int(le.Uint16(hdr[10:])), true
	}
	if len(hdr) < m || m < 44 {
		return codec, 0, 0, 0, false
	}
	// WAV: ch @22 (u16), rate @24 (u32), bits @34 (u16)
	ch = int(le.Uint16(hdr[22:]))
	rate = int(le.Uint32(hdr[24:]))
	bits = int(le.Uint16(hdr[34:]))
	return codec, rate, bits, ch, true
}
