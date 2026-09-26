package player

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures (testdata/sweep*) are 3 s of stereo tone whose pitch doubles every
// second: 440 Hz, 880 Hz, 1760 Hz. Where a decoder lands after a seek shows up
// as the pitch, so seeking is checked by ear-equivalent, not just by "no error".

func writeSweepWAV(t *testing.T, path string, rate int) {
	frames := rate * 3
	b := make([]byte, 44+frames*4)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+frames*4))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 2)
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*4))
	binary.LittleEndian.PutUint16(b[32:], 4)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(frames*4))
	for k := 0; k < frames; k++ {
		tt := float64(k) / float64(rate)
		v := int16(0.5 * 32767 * math.Sin(2*math.Pi*tt*440*math.Pow(2, math.Floor(tt))))
		binary.LittleEndian.PutUint16(b[44+k*4:], uint16(v))
		binary.LittleEndian.PutUint16(b[44+k*4+2:], uint16(v))
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// freq estimates the pitch of the left channel from its zero crossings.
func freq(pcm []int16) float64 {
	cross, prev := 0, pcm[0]
	for i := 2; i < len(pcm); i += 2 {
		if (prev < 0) != (pcm[i] < 0) {
			cross++
		}
		prev = pcm[i]
	}
	return float64(cross) / 2 / (float64(len(pcm)/2) / Rate)
}

func readFrames(t *testing.T, s source, n int) []int16 {
	buf := make([]int16, n*2)
	got := 0
	for got < len(buf) {
		k, err := s.Read(buf[got:])
		got += k
		if err != nil {
			break
		}
	}
	return buf[:got]
}

func TestSeekAllFormats(t *testing.T) {
	dir := t.TempDir()
	wav44, wav48 := filepath.Join(dir, "s44.wav"), filepath.Join(dir, "s48.wav")
	writeSweepWAV(t, wav44, 44100)
	writeSweepWAV(t, wav48, 48000)
	files := []string{wav44, wav48, "testdata/sweep44100.mp3", "testdata/sweep48000.mp3", "testdata/sweep44100.ogg", "testdata/sweep44100.flac"}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := open(path)
			if err != nil {
				t.Fatal(err)
			}
			s := newResampler(raw, Rate) // 48 kHz files go through the resampler, as in the app
			defer s.Close()

			if f := s.Frames(); math.Abs(float64(f)-3*Rate) > 0.03*3*Rate {
				t.Errorf("Frames() = %d, want about %d", f, 3*Rate)
			}
			for _, c := range []struct {
				at, hz float64
			}{{1.5, 880}, {0.3, 440}, {2.5, 1760}, {1.2, 880}} { // forward, backward, forward, backward
				if err := s.SeekFrame(int64(c.at * Rate)); err != nil {
					t.Fatalf("seek %.1f: %v", c.at, err)
				}
				pcm := readFrames(t, s, Rate/10)
				if len(pcm) < Rate/10*2 {
					t.Fatalf("seek %.1f: only %d frames after seeking", c.at, len(pcm)/2)
				}
				if got := freq(pcm); math.Abs(got-c.hz) > 0.05*c.hz {
					t.Errorf("after seeking to %.1fs the pitch is %.0f Hz, want %.0f Hz", c.at, got, c.hz)
				}
			}
			// Seeking past the end must end the track cleanly, not hang or panic.
			if err := s.SeekFrame(100 * Rate); err == nil {
				if pcm := readFrames(t, s, Rate); len(pcm) > Rate*2/2 { // at most a little tail
					t.Errorf("read %d frames past the end", len(pcm)/2)
				}
			}
		})
	}
}

// Lossless formats must seek to the exact sample, not just the right neighbourhood.
func TestSeekIsSampleExact(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "s.wav")
	writeSweepWAV(t, wav, 44100)
	for _, path := range []string{wav, "testdata/sweep44100.flac"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			a, _ := open(path)
			whole := readFrames(t, a, 3*Rate)
			a.Close()

			b, _ := open(path)
			defer b.Close()
			for _, at := range []int{0, 1, 4095, 4096, 44100 + 17, 100000, 3*Rate - 2000} {
				if err := b.SeekFrame(int64(at)); err != nil {
					t.Fatalf("seek to frame %d: %v", at, err)
				}
				got := readFrames(t, b, 500)
				want := whole[at*2 : min(len(whole), (at+500)*2)]
				if len(got) != len(want) {
					t.Fatalf("at %d: got %d values, want %d", at, len(got), len(want))
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("at frame %d: sample %d differs (%d vs %d)", at, i/2, got[i], want[i])
					}
				}
			}
		})
	}
}
