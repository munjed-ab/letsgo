package player

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hajimehoshi/go-mp3"
	"github.com/jfreymuth/oggvorbis"
	"github.com/mewkiz/flac"
)

// source = any decoder, normalized to interleaved stereo int16 at its native rate.
// Read returns how many int16 values it wrote (always even: L,R,L,R...).
type source interface {
	Rate() int
	Read(dst []int16) (int, error)
	Close() error
	SeekFrame(frame int64) error // jump to a frame counted at the source's own rate
	Frames() int64               // length in frames at the source's own rate, -1 if unknown
}

// seekBuf is a buffered io.ReadSeeker. Decoders read in tiny pieces, so they
// need buffering, and seeking needs the buffer to be dropped (bufio.Reader can't seek).
type seekBuf struct {
	f  io.ReadSeeker
	br *bufio.Reader
}

func newSeekBuf(f io.ReadSeeker) *seekBuf { return &seekBuf{f, bufio.NewReaderSize(f, 64<<10)} }

func (s *seekBuf) Read(p []byte) (int, error) { return s.br.Read(p) }

func (s *seekBuf) Seek(off int64, whence int) (int64, error) {
	if whence == io.SeekCurrent {
		off -= int64(s.br.Buffered()) // the file is ahead of the reader by what is buffered
	}
	n, err := s.f.Seek(off, whence)
	s.br.Reset(s.f)
	return n, err
}

var Exts = map[string]bool{".mp3": true, ".flac": true, ".ogg": true, ".wav": true}

func open(path string) (source, error) {
	if byPlatform(path) {
		s, err := openVideo(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		return s, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r := newSeekBuf(f)
	var s source
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		s, err = newMP3(r, f)
	case ".flac":
		s, err = newFLAC(r, f)
	case ".ogg":
		s, err = newOgg(r, f)
	case ".wav":
		s, err = newWAV(r, f)
	default:
		err = errors.New("unsupported")
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return s, nil
}

// ---- mp3: go-mp3 already gives 16-bit LE stereo bytes ----

type mp3src struct {
	d   *mp3.Decoder
	c   io.Closer
	raw []byte
	eof bool // sought to the end
}

func newMP3(r io.ReadSeeker, c io.Closer) (source, error) {
	d, err := mp3.NewDecoder(r)
	if err != nil {
		return nil, err
	}
	return &mp3src{d: d, c: c}, nil
}
func (m *mp3src) Rate() int    { return m.d.SampleRate() }
func (m *mp3src) Close() error { return m.c.Close() }
func (m *mp3src) Frames() int64 {
	if l := m.d.Length(); l >= 0 {
		return l / 4 // 16-bit stereo
	}
	return -1
}

// SeekFrame jumps to a frame. MP3 frames depend on the ones before them (the bit
// reservoir and overlap), so the first frames after a raw seek decode as noise:
// start a couple of frames early and throw those away.
func (m *mp3src) SeekFrame(frame int64) error {
	m.eof = false
	if l := m.d.Length(); l < 0 || frame*4 >= l { // go-mp3 panics on a seek past the end
		m.eof = l >= 0
		if m.eof {
			return nil
		}
	}
	const preroll = 3 * 1152
	pre := min(frame, preroll)
	if _, err := m.d.Seek((frame-pre)*4, io.SeekStart); err != nil {
		return err
	}
	scratch := make([]int16, 2*1152)
	for pre > 0 {
		n, err := m.Read(scratch[:min(int64(len(scratch)), pre*2)])
		pre -= int64(n / 2)
		if err != nil || n == 0 {
			break
		}
	}
	return nil
}
func (m *mp3src) Read(dst []int16) (int, error) {
	if m.eof {
		return 0, io.EOF
	}
	if cap(m.raw) < len(dst)*2 {
		m.raw = make([]byte, len(dst)*2)
	}
	b := m.raw[:len(dst)*2]
	n, err := io.ReadFull(m.d, b)
	n &^= 3 // whole stereo frames only
	for i := 0; i < n/2; i++ {
		dst[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	return n / 2, err
}

// ---- flac: frames of per-channel int32 samples at any bit depth ----

type flacsrc struct {
	s        *flac.Stream
	c        io.Closer
	pending  []int16 // decoded but not yet handed out
	back     []int16
	shift    int
	channels int
}

func newFLAC(r io.ReadSeeker, c io.Closer) (source, error) {
	s, err := flac.NewSeek(r)
	if err != nil {
		return nil, err
	}
	return &flacsrc{s: s, c: c, shift: int(s.Info.BitsPerSample) - 16, channels: int(s.Info.NChannels)}, nil
}
func (f *flacsrc) Rate() int    { return int(f.s.Info.SampleRate) }
func (f *flacsrc) Close() error { return f.c.Close() }
func (f *flacsrc) Frames() int64 {
	if n := f.s.Info.NSamples; n > 0 {
		return int64(n)
	}
	return -1
}

// Seek lands on the frame boundary at or before the target, then decodes and
// drops the few samples in between so the position is exact.
func (f *flacsrc) SeekFrame(frame int64) error {
	got, err := f.s.Seek(uint64(frame))
	// The library reports EOF when the target is inside the last block; land
	// earlier and decode forward instead.
	for back := int64(16384); err == io.EOF && back < 1<<20; back *= 2 {
		got, err = f.s.Seek(uint64(max(0, frame-back)))
	}
	if err != nil {
		return err
	}
	f.pending = f.pending[:0]
	scratch := make([]int16, 2*1024)
	for skip := frame - int64(got); skip > 0; {
		n, err := f.Read(scratch[:min(int64(len(scratch)), skip*2)])
		skip -= int64(n / 2)
		if err != nil || n == 0 {
			return nil // ran into the end: fine, the next Read reports EOF
		}
	}
	return nil
}
func (f *flacsrc) Read(dst []int16) (int, error) {
	n := 0
	for n < len(dst) {
		if len(f.pending) == 0 {
			fr, err := f.s.ParseNext()
			if err != nil {
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					return n, io.EOF
				}
				return n, err
			}
			f.pending = f.back[:0] // reuse the same backing array every frame, no garbage
			for i := 0; i < int(fr.BlockSize); i++ {
				l := fr.Subframes[0].Samples[i]
				r := l
				if f.channels > 1 {
					r = fr.Subframes[1].Samples[i] // >2 channels: keep front L/R
				}
				f.pending = append(f.pending, f.to16(l), f.to16(r))
			}
			f.back = f.pending
		}
		c := copy(dst[n:], f.pending)
		n += c
		f.pending = f.pending[c:]
	}
	return n, nil
}
func (f *flacsrc) to16(v int32) int16 {
	if f.shift > 0 {
		return int16(v >> f.shift)
	}
	return int16(v << -f.shift)
}

// ---- ogg vorbis: float32 interleaved ----

type oggsrc struct {
	r   *oggvorbis.Reader
	c   io.Closer
	buf []float32
}

func newOgg(r io.ReadSeeker, c io.Closer) (source, error) {
	o, err := oggvorbis.NewReader(r)
	if err != nil {
		return nil, err
	}
	return &oggsrc{r: o, c: c}, nil
}
func (o *oggsrc) Rate() int    { return o.r.SampleRate() }
func (o *oggsrc) Close() error { return o.c.Close() }
func (o *oggsrc) Frames() int64 {
	if l := o.r.Length(); l > 0 {
		return l
	}
	return -1
}
func (o *oggsrc) SeekFrame(frame int64) error { return o.r.SetPosition(frame) }
func (o *oggsrc) Read(dst []int16) (int, error) {
	ch := o.r.Channels()
	frames := len(dst) / 2
	if cap(o.buf) < frames*ch {
		o.buf = make([]float32, frames*ch)
	}
	got, err := o.r.Read(o.buf[:frames*ch])
	got /= ch
	for i := 0; i < got; i++ {
		l := o.buf[i*ch]
		r := l
		if ch > 1 {
			r = o.buf[i*ch+1]
		}
		dst[i*2], dst[i*2+1] = f2i(l), f2i(r)
	}
	return got * 2, err
}
func f2i(v float32) int16 {
	v *= 32767
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// ---- wav: hand-rolled, 16-bit PCM mono/stereo only (covers 99% of wavs) ----

type wavsrc struct {
	r    io.Reader
	rs   io.ReadSeeker
	c    io.Closer
	rate int
	ch   int
	raw  []byte
	data int64 // file offset of the first sample
	size int64 // bytes of sample data
}

func newWAV(r io.ReadSeeker, c io.Closer) (source, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, errors.New("not a wav")
	}
	w := &wavsrc{c: c}
	for { // walk chunks until "data"
		var ck [8]byte
		if _, err := io.ReadFull(r, ck[:]); err != nil {
			return nil, err
		}
		size := int64(binary.LittleEndian.Uint32(ck[4:]))
		switch string(ck[:4]) {
		case "fmt ":
			b := make([]byte, size)
			if _, err := io.ReadFull(r, b); err != nil {
				return nil, err
			}
			if binary.LittleEndian.Uint16(b[0:]) != 1 || binary.LittleEndian.Uint16(b[14:]) != 16 {
				return nil, errors.New("only 16-bit PCM wav")
			}
			w.ch = int(binary.LittleEndian.Uint16(b[2:]))
			w.rate = int(binary.LittleEndian.Uint32(b[4:]))
		case "data":
			if w.rate == 0 {
				return nil, errors.New("no fmt chunk")
			}
			w.rs, w.size = r, size
			w.data, _ = r.Seek(0, io.SeekCurrent)
			w.r = io.LimitReader(r, size)
			return w, nil
		default:
			if _, err := io.CopyN(io.Discard, r, size+size&1); err != nil {
				return nil, err
			}
		}
	}
}
func (w *wavsrc) Rate() int     { return w.rate }
func (w *wavsrc) Close() error  { return w.c.Close() }
func (w *wavsrc) Frames() int64 { return w.size / int64(w.ch*2) }
func (w *wavsrc) SeekFrame(frame int64) error {
	off := min(max(frame, 0)*int64(w.ch*2), w.size)
	if _, err := w.rs.Seek(w.data+off, io.SeekStart); err != nil {
		return err
	}
	w.r = io.LimitReader(w.rs, w.size-off)
	return nil
}
func (w *wavsrc) Read(dst []int16) (int, error) {
	frames := len(dst) / 2
	need := frames * w.ch * 2
	if cap(w.raw) < need {
		w.raw = make([]byte, need)
	}
	n, err := io.ReadFull(w.r, w.raw[:need])
	got := n / (w.ch * 2)
	for i := 0; i < got; i++ {
		l := int16(binary.LittleEndian.Uint16(w.raw[i*w.ch*2:]))
		r := l
		if w.ch > 1 {
			r = int16(binary.LittleEndian.Uint16(w.raw[i*w.ch*2+2:]))
		}
		dst[i*2], dst[i*2+1] = l, r
	}
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	return got * 2, err
}
