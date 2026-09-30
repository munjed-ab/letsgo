package player

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Videos play as songs: only their sound goes through the player (and out to every device);
// the picture is served as the file itself (see the app's /api/video) and each screen shows
// it on its own, muted and kept in step with the sound.
//
// None of the pure-Go decoders reads the audio in a video (AAC in an MP4 needs a codec the
// project does not carry), so the platform does it: ffmpeg where it is installed (laptops,
// Termux), or whatever the app registers in VideoDecoder (the phone gives Android's own
// MediaCodec). With neither, video files are left out of the library instead of listed as
// tracks that cannot play.

// VideoExts are the video containers that count as tracks.
var VideoExts = map[string]bool{".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".webm": true}

// Source is one decoded track: interleaved stereo int16 at Rate(), what VideoDecoder returns.
type Source = source

// VideoDecoder, when set (before the library is scanned), opens the audio of a video file
// instead of ffmpeg.
var VideoDecoder func(path string) (Source, error)

// IsVideo reports whether path is a video file (by extension).
func IsVideo(path string) bool { return VideoExts[strings.ToLower(filepath.Ext(path))] }

var ffmpegPath = sync.OnceValue(func() string {
	p, _ := exec.LookPath("ffmpeg")
	return p
})

// VideoSupported reports whether this device can play the sound of a video file.
func VideoSupported() bool { return VideoDecoder != nil || ffmpegPath() != "" }

func openVideo(path string) (source, error) {
	if VideoDecoder != nil {
		return VideoDecoder(path)
	}
	if ffmpegPath() == "" {
		return nil, errors.New("ffmpeg is not installed")
	}
	return openFFmpeg(path)
}

// ffsrc decodes with one ffmpeg process per play position: it writes raw stereo PCM at the
// stream rate to a pipe, and a seek is a new process started at the new position.
type ffsrc struct {
	path   string
	frames int64 // length in frames, -1 = unknown
	start  float64
	cmd    *exec.Cmd
	out    *bufio.Reader
	raw    []byte
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

var durationRE = regexp.MustCompile(`Duration: (\d+):(\d\d):(\d\d(?:\.\d+)?)`)

// openFFmpeg asks ffmpeg what the file holds (the run "fails" because there is no output file,
// which is fine: the listing is all that is wanted) and does not start decoding yet.
func openFFmpeg(path string) (source, error) {
	info, _ := exec.Command(ffmpegPath(), "-hide_banner", "-nostdin", "-i", path).CombinedOutput()
	if !strings.Contains(string(info), "Audio:") {
		return nil, errors.New("no audio track")
	}
	s := &ffsrc{path: path, frames: -1}
	if m := durationRE.FindStringSubmatch(string(info)); m != nil {
		h, mins := atoi(m[1]), atoi(m[2])
		sec, _ := strconv.ParseFloat(m[3], 64)
		s.frames = int64((float64(h*3600+mins*60) + sec) * Rate)
	}
	return s, nil
}

func (s *ffsrc) Rate() int     { return Rate }
func (s *ffsrc) Frames() int64 { return s.frames }

func (s *ffsrc) run() error {
	cmd := exec.Command(ffmpegPath(), "-nostdin", "-v", "quiet",
		"-ss", strconv.FormatFloat(s.start, 'f', 3, 64), "-i", s.path,
		"-map", "0:a:0", "-vn", "-sn", "-dn", "-f", "s16le", "-ac", strconv.Itoa(Channels), "-ar", strconv.Itoa(Rate), "-")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd, s.out = cmd, bufio.NewReaderSize(out, 64<<10)
	return nil
}

func (s *ffsrc) stop() {
	if s.cmd != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
		s.cmd, s.out = nil, nil
	}
}

func (s *ffsrc) Close() error { s.stop(); return nil }

func (s *ffsrc) SeekFrame(frame int64) error {
	s.stop()
	s.start = float64(max(frame, 0)) / Rate
	return nil // the next Read starts ffmpeg there
}

func (s *ffsrc) Read(dst []int16) (int, error) {
	if s.cmd == nil {
		if err := s.run(); err != nil {
			return 0, fmt.Errorf("ffmpeg: %w", err)
		}
	}
	if cap(s.raw) < len(dst)*2 {
		s.raw = make([]byte, len(dst)*2)
	}
	b := s.raw[:len(dst)*2]
	n, err := io.ReadFull(s.out, b)
	n &^= 3 // whole stereo frames only
	for i := 0; i < n/2; i++ {
		dst[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	return n / 2, err
}
