package mobile

import (
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"letsgo/player"
)

// fakeDecoder plays "frame k = k" in chunks of 3 frames, so what the player gets can be traced to a position.
type fakeDecoder struct {
	next, total int
	seekedUs    int64
	closed      bool
	openErr     error
}

func (f *fakeDecoder) Open(path string) (int64, error) {
	if f.openErr != nil {
		return 0, f.openErr
	}
	return int64(f.total) * 1_000_000 / 8000, nil
}
func (f *fakeDecoder) Rate() int { return 8000 }
func (f *fakeDecoder) Read() ([]byte, error) {
	if f.next >= f.total {
		return nil, nil
	}
	b := make([]byte, 0, 12)
	for i := 0; i < 3 && f.next < f.total; i++ {
		b = binary.LittleEndian.AppendUint16(b, uint16(f.next))
		b = binary.LittleEndian.AppendUint16(b, uint16(f.next)|0x8000)
		f.next++
	}
	return b, nil
}
func (f *fakeDecoder) SeekUs(us int64) { f.seekedUs = us; f.next = int(us * 8000 / 1_000_000) }
func (f *fakeDecoder) Close()          { f.closed = true }

func TestVideoDecoderBridge(t *testing.T) {
	old := player.VideoDecoder
	defer func() { player.VideoDecoder = old }()
	d := &fakeDecoder{total: 100}
	SetVideoDecoder(d)

	src, err := player.VideoDecoder("clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if src.Rate() != 8000 || src.Frames() != 100 {
		t.Fatalf("rate %d, frames %d; want 8000, 100", src.Rate(), src.Frames())
	}
	// reads of any size (here 7 frames, not a multiple of the decoder's 3) lose and repeat nothing
	pcm, frame := make([]int16, 14), 0
	for {
		n, err := src.Read(pcm)
		for i := 0; i < n; i += 2 {
			if pcm[i] != int16(frame) || pcm[i+1] != int16(uint16(frame)|0x8000) {
				t.Fatalf("frame %d came out as %d,%d", frame, pcm[i], pcm[i+1])
			}
			frame++
		}
		if err != nil {
			if err != io.EOF {
				t.Fatal(err)
			}
			break
		}
	}
	if frame != 100 {
		t.Errorf("read %d frames, want 100", frame)
	}
	// a seek drops what was buffered and asks the decoder for the position in microseconds
	if err := src.SeekFrame(4000); err != nil || d.seekedUs != 500_000 {
		t.Errorf("seek: err %v, decoder asked for %d us, want 500000", err, d.seekedUs)
	}
	src.Close()
	if !d.closed {
		t.Error("Close did not reach the decoder")
	}

	d.openErr = errors.New("no audio track")
	if _, err := player.VideoDecoder("clip.mp4"); err == nil {
		t.Error("a file the decoder cannot open must fail, so the player skips it")
	}
}
