package app

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"letsgo/player"
)

// A fragmented MP4 (what yt-dlp and many downloaders save) carries no length in its header, and
// Android's MediaPlayer refuses to seek in a file without one: a phone could never put its picture at
// the right place, and said "cannot show this video". ffmpeg copies the streams into an ordinary MP4
// (nothing is re-encoded, about a second per 100 MB) and that copy is served instead of the file.
// Only a device with ffmpeg can do it, which is the laptop; the file itself is never touched.

var remuxMu sync.Mutex // one copy at a time; the second request for the same file then finds it done

// fragmentedMP4 reports whether the file's header says it is fragmented with no length (a moov box
// with an mvex but no mehd). Files that are not mp4-like, or are unreadable, are "no".
func fragmentedMP4(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".mov":
	default:
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var pos int64
	for range 64 { // the boxes at the top level; moov is near the start or, in a plain file, not what we look for
		var h [16]byte
		if _, err := f.ReadAt(h[:8], pos); err != nil {
			return false
		}
		size, kind, hdr := int64(binary.BigEndian.Uint32(h[:4])), string(h[4:8]), int64(8)
		if size == 1 {
			if _, err := f.ReadAt(h[8:16], pos+8); err != nil {
				return false
			}
			size, hdr = int64(binary.BigEndian.Uint64(h[8:])), 16
		}
		if kind == "moof" || kind == "mdat" {
			return false // no moov before the media: a plain file with moov at the end, or not ours
		}
		if kind == "moov" {
			b, _ := io.ReadAll(io.NewSectionReader(f, pos+hdr, min(size-hdr, 4<<20)))
			s := string(b)
			return strings.Contains(s, "mvex") && !strings.Contains(s, "mehd")
		}
		if size < hdr {
			return false
		}
		pos += size
	}
	return false
}

// seekableVideo is the file to serve for a video: the file itself, or a seekable copy of it in the
// cache when it is a fragmented MP4 and ffmpeg is here (a failed copy falls back to the file).
func seekableVideo(path string) string {
	if player.FFmpeg() == "" || !fragmentedMP4(path) {
		return path
	}
	st, err := os.Stat(path)
	dir, err2 := os.UserCacheDir()
	if err != nil || err2 != nil {
		return path
	}
	dir = filepath.Join(dir, "letsgo", "remux")
	out := filepath.Join(dir, fmt.Sprintf("%x.mp4", sha1.Sum([]byte(fmt.Sprint(path, st.Size(), st.ModTime().UnixNano())))))
	remuxMu.Lock()
	defer remuxMu.Unlock()
	if _, err := os.Stat(out); err == nil {
		return out
	}
	os.MkdirAll(dir, 0o755)
	tmp := out + ".tmp"
	if b, err := exec.Command(player.FFmpeg(), "-nostdin", "-v", "error", "-y", "-i", path, "-c", "copy", "-movflags", "+faststart", "-f", "mp4", tmp).CombinedOutput(); err != nil {
		os.Remove(tmp)
		log.Printf("video: could not make a seekable copy of %s: %v %s", filepath.Base(path), err, b)
		return path
	}
	if os.Rename(tmp, out) != nil {
		return path
	}
	log.Printf("video: %s is fragmented without a length; serving a seekable copy (%s)", filepath.Base(path), out)
	return out
}
