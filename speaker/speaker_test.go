package speaker

import (
	"sync/atomic"
	"testing"
	"time"
)

type counter struct{ bytes atomic.Int64 }

func (c *counter) Read(p []byte) (int, error) {
	c.bytes.Add(int64(len(p)))
	clear(p)
	return len(p), nil
}

// The device must drain exactly what it is given: a wrong consumption rate means
// the stream plays slow/fast and glitches. Needs a real audio device.
func TestConsumptionRate(t *testing.T) {
	if testing.Short() {
		t.Skip("plays silence on the real audio device for 10 s")
	}
	c := &counter{}
	if err := Play(c, func(time.Duration) {}); err != nil {
		t.Skipf("no audio device: %v", err)
	}
	time.Sleep(2 * time.Second) // startup fill
	b0, t0 := c.bytes.Load(), time.Now()
	time.Sleep(8 * time.Second)
	frames := float64(c.bytes.Load()-b0) / 4 / time.Since(t0).Seconds()
	if frames < 44100*0.999 || frames > 44100*1.001 {
		t.Errorf("device consumed %.0f frames/s, want 44100 +-0.1%%", frames)
	}
}
