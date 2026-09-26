// Package snap speaks the Snapcast binary protocol (v2), server side only.
// Stock snapclient / Snapdroid connect to us and think we're snapserver.
//
// Every message = 26-byte header + payload, all little-endian:
//
//	u16 type | u16 id | u16 refersTo | tv sent | tv received | u32 size
//
// where tv = i32 sec + i32 usec.
package snap

import (
	"encoding/binary"
	"io"
	"time"
)

const (
	TypeCodecHeader    = 1
	TypeWireChunk      = 2
	TypeServerSettings = 3
	TypeTime           = 4
	TypeHello          = 5
	TypeClientInfo     = 7

	headerSize = 26
	maxPayload = 1 << 20 // refuse anything bigger, a client can't make us allocate GBs
)

var le = binary.LittleEndian

// Header is the fixed part of every message.
type Header struct {
	Type, ID, RefersTo uint16
	Sent, Received     time.Duration // on the server clock (see clock.go)
	Size               uint32
}

func putTV(b []byte, d time.Duration) {
	le.PutUint32(b[0:], uint32(int32(d/time.Second)))
	le.PutUint32(b[4:], uint32(int32((d%time.Second)/time.Microsecond)))
}

func getTV(b []byte) time.Duration {
	return time.Duration(int32(le.Uint32(b[0:])))*time.Second +
		time.Duration(int32(le.Uint32(b[4:])))*time.Microsecond
}

// ReadMsg reads one message. Received is stamped the moment the header lands,
// that stamp is what makes time sync accurate.
func ReadMsg(r io.Reader) (Header, []byte, error) {
	var hb [headerSize]byte
	if _, err := io.ReadFull(r, hb[:]); err != nil {
		return Header{}, nil, err
	}
	h := Header{
		Type:     le.Uint16(hb[0:]),
		ID:       le.Uint16(hb[2:]),
		RefersTo: le.Uint16(hb[4:]),
		Sent:     getTV(hb[6:]),
		Received: Now(),
		Size:     le.Uint32(hb[22:]),
	}
	if h.Size > maxPayload {
		return h, nil, io.ErrShortBuffer
	}
	p := make([]byte, h.Size)
	_, err := io.ReadFull(r, p)
	return h, p, err
}

// ReadMsgInto is like ReadMsg but reads the payload into buf (grown only if too
// small), so a hot receive loop reuses one buffer instead of allocating per
// message. The returned slice aliases buf; copy out anything you keep past the
// next call. Cuts the garbage that causes GC pauses (which starve audio).
func ReadMsgInto(r io.Reader, buf []byte) (Header, []byte, error) {
	var hb [headerSize]byte
	if _, err := io.ReadFull(r, hb[:]); err != nil {
		return Header{}, buf, err
	}
	h := Header{
		Type:     le.Uint16(hb[0:]),
		ID:       le.Uint16(hb[2:]),
		RefersTo: le.Uint16(hb[4:]),
		Sent:     getTV(hb[6:]),
		Received: Now(),
		Size:     le.Uint32(hb[22:]),
	}
	if h.Size > maxPayload {
		return h, buf, io.ErrShortBuffer
	}
	if uint32(cap(buf)) < h.Size {
		buf = make([]byte, h.Size)
	}
	buf = buf[:h.Size]
	_, err := io.ReadFull(r, buf)
	return h, buf, err
}

// frame builds header+payload into one slice so it goes out in a single write.
func frame(typ, refersTo uint16, payload []byte) []byte {
	b := make([]byte, headerSize+len(payload))
	le.PutUint16(b[0:], typ)
	le.PutUint16(b[4:], refersTo)
	// sent is stamped at write time in session.writer, received stays 0
	le.PutUint32(b[22:], uint32(len(payload)))
	copy(b[headerSize:], payload)
	return b
}

// stampSent writes "now" into the sent field right before the bytes hit the socket.
func stampSent(b []byte) { putTV(b[6:], Now()) }

func lenPrefixed(s []byte) []byte {
	b := make([]byte, 4+len(s))
	le.PutUint32(b, uint32(len(s)))
	copy(b[4:], s)
	return b
}

// ServerSettingsMsg: JSON string with buffer, volume, mute.
// refersTo = the Hello's id when answering a Hello; the client waits for that exact reply.
func ServerSettingsMsg(json []byte, refersTo uint16) []byte {
	return frame(TypeServerSettings, refersTo, lenPrefixed(json))
}

// CodecHeaderMsg: codec name + codec-specific header. For "pcm" it's a WAV header,
// that's how the client learns sample rate / bits / channels.
func CodecHeaderMsg(codec string, hdr []byte) []byte {
	p := append(lenPrefixed([]byte(codec)), lenPrefixed(hdr)...)
	return frame(TypeCodecHeader, 0, p)
}

// WireChunkMsg: one slice of audio + the server time its first sample belongs to.
// Clients play it at ts + bufferMs, all on the same (synced) clock. That's the sync.
//
// epoch counts timeline restarts (pause, play, jump, seek). It rides in the header's
// refersTo, which stock Snapcast clients ignore for chunks. A letsgo client that sees
// it change drops everything it has queued, which is what makes those commands take
// effect at once instead of after a whole buffer.
func WireChunkMsg(ts time.Duration, pcm []byte, epoch uint16) []byte {
	p := make([]byte, 12+len(pcm))
	putTV(p, ts)
	le.PutUint32(p[8:], uint32(len(pcm)))
	copy(p[12:], pcm)
	return frame(TypeWireChunk, epoch, p)
}

// TimeReply answers a client's Time ping. latency = how long the ping took
// client->server (their clock vs ours). Client measures the return leg itself,
// then halves the difference to get the offset between clocks.
func TimeReply(req Header) []byte {
	p := make([]byte, 8)
	putTV(p, req.Received-req.Sent)
	return frame(TypeTime, req.ID, p)
}

// WavHeader is the 44-byte RIFF header snapclient expects for codec "pcm".
func WavHeader(rate, bits, ch int) []byte {
	b := make([]byte, 44)
	copy(b[0:], "RIFF")
	le.PutUint32(b[4:], 36)
	copy(b[8:], "WAVEfmt ")
	le.PutUint32(b[16:], 16)
	le.PutUint16(b[20:], 1) // PCM
	le.PutUint16(b[22:], uint16(ch))
	le.PutUint32(b[24:], uint32(rate))
	le.PutUint32(b[28:], uint32(rate*ch*bits/8))
	le.PutUint16(b[32:], uint16(ch*bits/8))
	le.PutUint16(b[34:], uint16(bits))
	copy(b[36:], "data")
	return b
}
