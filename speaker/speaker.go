// Package speaker plays a PCM stream (44.1 kHz, 16-bit, stereo) on the desktop
// through oto (PulseAudio/PipeWire, ALSA fallback), and reports how long audio
// takes to be heard so the sync client can compensate.
package speaker

import (
	"io"
	"time"

	"letsgo/player"

	"github.com/ebitengine/oto/v3"
)

const (
	// oto reads ahead of the device by this much. Do not go small: at 20 ms oto
	// read only 94% of what the device drained (the rest played as silence, i.e.
	// crackle and a stream that runs 6% slow); 60 ms lost 0.3%. 100 ms is exact.
	// TestConsumptionRate guards this.
	playerBuf = 100 * time.Millisecond

	// Everything behind oto's buffer: the Pulse/PipeWire stream (oto asks for
	// 100 ms by default), the sound server's mixing quantum and the DAC. The
	// first part is a request, the rest a guess, so this is calibrated by ear/mic
	// against the phone, whose output delay Android reports exactly. Still off on
	// your machine? Use -latency.
	downstream = 130 * time.Millisecond
)

var keep *oto.Player // oto stops a player that gets garbage collected

// Play starts playing src and returns once the device is open. Before every
// read it calls setLatency with how long the audio it is about to return takes
// to be heard: what oto still has queued plus the fixed downstream delay.
func Play(src io.Reader, setLatency func(time.Duration)) error {
	ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:   player.Rate,
		ChannelCount: player.Channels,
		Format:       oto.FormatSignedInt16LE,
	})
	if err != nil {
		return err
	}
	<-ready
	const frameBytes = player.Channels * player.Bits / 8
	keep = ctx.NewPlayer(readerFunc(func(b []byte) (int, error) {
		queued := time.Duration(keep.BufferedSize()/frameBytes) * time.Second / player.Rate
		setLatency(queued + downstream)
		return src.Read(b)
	}))
	keep.SetBufferSize(frameBytes * player.Rate * int(playerBuf/time.Millisecond) / 1000)
	keep.Play()
	return nil
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(b []byte) (int, error) { return f(b) }
