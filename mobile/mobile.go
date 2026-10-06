// Package mobile is the gomobile-bindable surface for the Android app. Android
// 10+ forbids exec'ing a shipped binary, so the whole node runs in-process here.
// The node's supervisor picks the audio source (own cast, or a casting peer);
// Kotlin just runs an AudioTrack loop pulling PCM from Read and reporting how
// long the device takes to play it. Stream is always 44100 Hz / 16-bit / stereo.
package mobile

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"time"

	"letsgo/app"
	"letsgo/player"
)

var node *app.Node

// Start brings up the server, web UI (http://127.0.0.1:8080) and mDNS. name is
// what other devices see (musicDir is the default folder; more can be added
// later through the API); buffer is the sync buffer in ms; dataDir is where
// favourites and playlists are saved. Call Stop before calling Start again.
func Start(musicDir, dataDir, name string, buffer int) error {
	if node != nil {
		return nil
	}
	n, err := app.Start([]string{musicDir}, dataDir, ":1704", ":8080", name, buffer)
	if err != nil {
		return err
	}
	node = n
	return nil
}

// Stop shuts the node down.
func Stop() {
	if node != nil {
		node.Stop()
		node = nil
	}
}

// Read fills buf with the PCM due to play now (signed 16-bit LE, interleaved
// stereo at 44100 Hz; silence when nothing is casting). Source for an AudioTrack
// write loop. Always returns len(buf).
func Read(buf []byte) (int, error) {
	if node == nil {
		for i := range buf {
			buf[i] = 0
		}
		return len(buf), nil
	}
	return node.Read(buf)
}

// SetOutputLatencyUs reports how long audio handed to AudioTrack now takes to be
// heard, in microseconds (queued frames + hardware). Call it before each Read.
func SetOutputLatencyUs(us int64) {
	if node != nil {
		node.SetOutputLatency(time.Duration(us) * time.Microsecond)
	}
}

// SetLatency adjusts this device's extra audio delay live (calibration knob).
func SetLatency(ms int) {
	if node != nil {
		node.SetLatency(ms)
	}
}

// SetNetwork tells the node which network interface/IP to use for peer
// discovery. Android hides interfaces from Go, so Kotlin reads them and passes
// them here (call at start and whenever the network changes).
func SetNetwork(name string, index int, ip string) {
	if node != nil {
		node.SetNetwork(name, index, ip)
	}
}

// NowJSON describes what is playing (this phone's track, or the one being heard
// from another device) as JSON: track, title, artist, album, art, playing,
// elapsed, duration, remote, source. For the notification and lock screen.
func NowJSON() string {
	if node == nil {
		return "{}"
	}
	b, _ := json.Marshal(node.Now())
	return string(b)
}

// Control sends a media command (play, pause, toggle, next, prev, seek with arg
// in seconds) to whichever device NowJSON describes.
func Control(cmd string, arg float64) error {
	if node == nil {
		return nil
	}
	return node.Do(cmd, arg)
}

// VideoDecoder is implemented by the app on top of Android's MediaExtractor and MediaCodec: it
// decodes the sound of a video file (the pure-Go decoders cannot; a laptop uses ffmpeg). One
// file is open at a time; Open closes the previous one.
type VideoDecoder interface {
	// Open starts decoding the first audio track of the file. It returns the length in
	// microseconds (0 if unknown) and fails if there is no audio to decode.
	Open(path string) (int64, error)
	// Rate is the sample rate of what Read returns (valid after Open).
	Rate() int
	// NoPicture reports that the file opened last has no video track, only sound (valid after Open).
	NoPicture() bool
	// Read returns the next decoded audio: 16-bit little-endian stereo, whole frames. Empty means the end.
	Read() ([]byte, error)
	// SeekUs moves to a position in microseconds; the next Read starts there.
	SeekUs(us int64)
	Close()
}

// SetVideoDecoder makes videos playable on this phone. Call it before Start: the library is
// scanned then, and videos are left out of it when nothing can decode them.
func SetVideoDecoder(d VideoDecoder) {
	player.VideoDecoder = func(path string) (player.Source, error) {
		us, err := d.Open(path)
		if err != nil {
			return nil, err
		}
		v := &videoSource{d: d, rate: d.Rate(), frames: -1, noPicture: d.NoPicture()}
		if us > 0 {
			v.frames = us * int64(v.rate) / 1_000_000
		}
		return v, nil
	}
}

// videoSource is a player.Source over a VideoDecoder.
type videoSource struct {
	d         VideoDecoder
	rate      int
	frames    int64
	noPicture bool
	buf       []byte // decoded audio not handed out yet
}

func (v *videoSource) Rate() int       { return v.rate }
func (v *videoSource) NoPicture() bool { return v.noPicture }
func (v *videoSource) Frames() int64   { return v.frames }
func (v *videoSource) Close() error    { v.d.Close(); return nil }

func (v *videoSource) SeekFrame(frame int64) error {
	v.buf = nil
	v.d.SeekUs(frame * 1_000_000 / int64(v.rate))
	return nil
}

func (v *videoSource) Read(dst []int16) (int, error) {
	n := 0
	for n < len(dst) {
		if len(v.buf) < 4 {
			b, err := v.d.Read()
			if err != nil {
				return n, err
			}
			if len(b) < 4 {
				return n, io.EOF
			}
			v.buf = b
		}
		k := min(len(dst)-n, len(v.buf)/4*2) // whole stereo frames
		for i := 0; i < k; i++ {
			dst[n+i] = int16(binary.LittleEndian.Uint16(v.buf[i*2:]))
		}
		n += k
		v.buf = v.buf[k*2:]
	}
	return n, nil
}
