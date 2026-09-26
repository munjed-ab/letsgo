// Package mobile is the gomobile-bindable surface for the Android app. Android
// 10+ forbids exec'ing a shipped binary, so the whole node runs in-process here.
// The node's supervisor picks the audio source (own cast, or a casting peer);
// Kotlin just runs an AudioTrack loop pulling PCM from Read and reporting how
// long the device takes to play it. Stream is always 44100 Hz / 16-bit / stereo.
package mobile

import (
	"encoding/json"
	"time"

	"letsgo/app"
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
