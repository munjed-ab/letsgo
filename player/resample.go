package player

import "io"

// resampler converts any source rate to the stream rate with linear interpolation.
// Why linear: it's ~4 multiplies per sample, basically free on a phone.
// Quality is fine for 48k->44.1k. If you ever want better, this is the one
// place to swap in a windowed-sinc filter.
type resampler struct {
	src  source
	step float64 // source frames advanced per output frame
	buf  []int16 // window of source frames (stereo interleaved)
	pos  float64 // fractional read position inside buf, in frames
	eof  bool
}

func newResampler(src source, outRate int) source {
	if src.Rate() == outRate {
		return src // no work at all for matching files
	}
	return &resampler{src: src, step: float64(src.Rate()) / float64(outRate)}
}

func (r *resampler) Rate() int    { return int(float64(r.src.Rate()) / r.step) }
func (r *resampler) Close() error { return r.src.Close() }

// Seek and Frames are in output frames; the source counts its own.
func (r *resampler) SeekFrame(frame int64) error {
	if err := r.src.SeekFrame(int64(float64(frame) * r.step)); err != nil {
		return err
	}
	r.buf, r.pos, r.eof = r.buf[:0], 0, false
	return nil
}

func (r *resampler) Frames() int64 {
	if f := r.src.Frames(); f >= 0 {
		return int64(float64(f) / r.step)
	}
	return -1
}

func (r *resampler) Read(dst []int16) (int, error) {
	n := 0
	for n < len(dst) {
		i := int(r.pos)
		if i+1 >= len(r.buf)/2 { // need frame i and i+1, refill
			if r.eof {
				return n, io.EOF
			}
			// keep the frames we still need, drop the rest
			if r.buf == nil {
				r.buf = make([]int16, 0, 4096)
			}
			k := copy(r.buf[:cap(r.buf)], r.buf[min(i*2, len(r.buf)):]) // slide leftovers to front
			got, err := r.src.Read(r.buf[k:cap(r.buf)])
			r.buf = r.buf[:k+got]
			r.pos -= float64(i)
			if err != nil || got == 0 {
				r.eof = true
			}
			continue
		}
		f := r.pos - float64(i)
		a, b := r.buf[i*2:i*2+2], r.buf[i*2+2:i*2+4]
		dst[n] = int16(float64(a[0]) + (float64(b[0])-float64(a[0]))*f)
		dst[n+1] = int16(float64(a[1]) + (float64(b[1])-float64(a[1]))*f)
		n += 2
		r.pos += r.step
	}
	return n, nil
}
