package player

import "sync"

// aheadFrames is how much an ahead source keeps decoded: about 20 s.
const aheadFrames = 20 * 44100

// ahead decodes a source in the background and keeps up to aheadFrames ready, so a decoder that
// blocks for a while does not stop the music. The phone's own decoder (MediaCodec, for videos) did
// exactly that with another app in front: reads that took 3 to 8 s, every ten seconds or so, and
// the whole player waited on each one. All calls into the wrapped source happen on the background
// goroutine (the phone's decoder is used from one thread).
type ahead struct {
	src  source
	rate int
	n    int64 // Frames(), read once at open

	mu      sync.Mutex
	cond    *sync.Cond
	buf     []int16 // decoded, not yet read (interleaved stereo)
	err     error   // what the source returned after the last of buf (EOF at the end)
	seekTo  int64   // -1, or a seek the goroutine has to do
	seekErr chan error
	closing bool
	done    chan struct{}
}

func newAhead(src source) *ahead {
	a := &ahead{src: src, rate: src.Rate(), n: src.Frames(), seekTo: -1, done: make(chan struct{})}
	a.cond = sync.NewCond(&a.mu)
	go a.run()
	return a
}

func (a *ahead) run() {
	defer close(a.done)
	defer a.src.Close()
	chunk := make([]int16, 8192)
	for {
		a.mu.Lock()
		for !a.closing && a.seekTo < 0 && (a.err != nil || len(a.buf) >= aheadFrames*Channels) {
			a.cond.Wait()
		}
		if a.closing {
			a.mu.Unlock()
			return
		}
		if to := a.seekTo; to >= 0 {
			a.seekTo = -1
			a.mu.Unlock()
			err := a.src.SeekFrame(to)
			a.mu.Lock()
			if err == nil {
				a.buf, a.err = a.buf[:0], nil
			}
			a.seekErr <- err
			a.mu.Unlock()
			continue
		}
		a.mu.Unlock()

		n, err := a.src.Read(chunk) // the slow part, with nothing locked
		a.mu.Lock()
		if a.seekTo < 0 { // a seek asked meanwhile makes this read stale
			a.buf = append(a.buf, chunk[:n]...)
			a.err = err
		}
		a.cond.Broadcast()
		a.mu.Unlock()
	}
}

// Read returns what is decoded; it waits only when nothing is (the start of a track, or a decoder
// more than aheadFrames behind).
func (a *ahead) Read(dst []int16) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for len(a.buf) == 0 && a.err == nil && !a.closing {
		a.cond.Wait()
	}
	n := copy(dst, a.buf)
	a.buf = a.buf[n:]
	if len(a.buf) == 0 {
		a.buf = a.buf[:0:0] // let the old backing array go; the goroutine appends to a fresh one
	}
	a.cond.Broadcast() // room for more
	if len(a.buf) == 0 && a.err != nil {
		return n, a.err
	}
	return n, nil
}

func (a *ahead) SeekFrame(frame int64) error {
	ch := make(chan error, 1)
	a.mu.Lock()
	a.seekTo, a.seekErr = frame, ch
	a.cond.Broadcast()
	a.mu.Unlock()
	return <-ch
}

// Close stops the goroutine and waits for it: the next track may open the same decoder.
func (a *ahead) Close() error {
	a.mu.Lock()
	a.closing = true
	a.cond.Broadcast()
	a.mu.Unlock()
	<-a.done
	return nil
}

func (a *ahead) Rate() int     { return a.rate }
func (a *ahead) Frames() int64 { return a.n }
func (a *ahead) NoPicture() bool {
	np, ok := a.src.(interface{ NoPicture() bool })
	return ok && np.NoPicture()
}
