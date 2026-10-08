package player

import (
	"io"
	"testing"
	"time"
)

// rampSrc counts samples (sample i has value i mod 30000) and blocks on some reads, like the phone's
// decoder with another app in front.
type rampSrc struct {
	pos, total int64
	reads      int
	stallEvery int
	stall      time.Duration
}

func (r *rampSrc) Rate() int     { return Rate }
func (r *rampSrc) Frames() int64 { return r.total / Channels }
func (r *rampSrc) Close() error  { return nil }
func (r *rampSrc) SeekFrame(f int64) error {
	r.pos = f * Channels
	return nil
}
func (r *rampSrc) Read(dst []int16) (int, error) {
	if r.reads++; r.stallEvery > 0 && r.reads%r.stallEvery == 0 {
		time.Sleep(r.stall)
	}
	n := 0
	for ; n < len(dst) && r.pos < r.total; n++ {
		dst[n] = int16(r.pos % 30000)
		r.pos++
	}
	if r.pos >= r.total {
		return n, io.EOF
	}
	return n, nil
}

// A decoder that stalls for 1.5 s now and then is not heard once the read-ahead is primed: every
// read is answered at once, the samples come out exactly, a seek lands where it should, and the
// end of the track still arrives.
func TestAheadRidesOutDecoderStalls(t *testing.T) {
	src := &rampSrc{total: 30 * Rate * Channels, stallEvery: 40, stall: 1500 * time.Millisecond}
	a := newAhead(src)
	defer a.Close()
	time.Sleep(2500 * time.Millisecond) // primed (and one stall already behind it)

	buf := make([]int16, chunkFrames*Channels)
	want := int64(0)
	check := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if buf[i] != int16(want%30000) {
				t.Fatalf("sample %d is %d, want %d", want, buf[i], want%30000)
			}
			want++
		}
	}
	for i := 0; i < 200; i++ { // 4 s of 20 ms reads, in real time
		start := time.Now()
		n, err := a.Read(buf)
		if err != nil || n != len(buf) {
			t.Fatalf("read %d: %d samples, %v", i, n, err)
		}
		if d := time.Since(start); d > 50*time.Millisecond {
			t.Fatalf("read %d waited %v for the decoder", i, d)
		}
		check(n)
		time.Sleep(20 * time.Millisecond)
	}

	if err := a.SeekFrame(25 * Rate); err != nil {
		t.Fatal(err)
	}
	want = 25 * Rate * Channels
	var err error
	for err == nil {
		var n int
		n, err = a.Read(buf)
		check(n)
	}
	if err != io.EOF || want != src.total {
		t.Fatalf("ended with %v after sample %d, want EOF after %d", err, want, src.total)
	}
}
