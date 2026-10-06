package app

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"letsgo/player"
)

// A video sits in the library like a song, plays through the same player, and its file is served
// (with Range, which is how a screen seeks in it) to whoever shows the picture. Nothing else is:
// not a song, not an id that is not in the library.
func TestVideoTrack(t *testing.T) {
	if !player.VideoSupported() {
		t.Skip("no ffmpeg: videos are not listed on this machine")
	}
	music := t.TempDir()
	clip, err := os.ReadFile("../player/testdata/sweep44100.mp4")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(music, "clip.mp4"), clip, 0o644)
	writeRampNamed(t, music, "ramp.wav", 1)
	httpAddr := freeAddr(t)
	n, err := Start([]string{music}, "", freeAddr(t), httpAddr, "video", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Stop()

	fetch := func(path, rng string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", "http://"+httpAddr+path, nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}

	var lib []string
	resp, body := fetch("/api/library", "")
	json.Unmarshal(body, &lib)
	if resp.StatusCode != 200 || len(lib) != 2 || lib[0] != "clip.mp4" {
		t.Fatalf("library = %v, want the video next to the song", lib)
	}

	resp, body = fetch("/api/video?t=clip.mp4", "")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "video/mp4" || !bytes.Equal(body, clip) {
		t.Errorf("whole file: status %d, type %q, %d bytes (want %d)", resp.StatusCode, resp.Header.Get("Content-Type"), len(body), len(clip))
	}
	resp, body = fetch("/api/video?t=clip.mp4", "bytes=100-199")
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(body, clip[100:200]) {
		t.Errorf("Range: status %d, %d bytes, want 206 and the same 100 bytes as the file", resp.StatusCode, len(body))
	}
	for _, id := range []string{"ramp.wav", "nope.mp4", "../clip.mp4", ""} {
		if resp, _ := fetch("/api/video?t="+id, ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("/api/video?t=%q: status %d, want 404", id, resp.StatusCode)
		}
	}

	// it plays: the sound of the video is what the node casts
	if resp, err := http.Post("http://"+httpAddr+"/api/play?i=0", "", nil); err != nil || resp.StatusCode != 204 {
		t.Fatalf("play: %v %v", resp, err)
	}
	var st struct {
		Player struct {
			Playing  bool
			Track    string
			Duration float64
		}
	}
	for i := 0; i < 50; i++ {
		_, body := fetch("/api/state", "")
		json.Unmarshal(body, &st)
		if st.Player.Playing && st.Player.Duration > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !st.Player.Playing || st.Player.Track != "clip.mp4" || st.Player.Duration < 2.9 || st.Player.Duration > 3.1 {
		t.Errorf("playing the video: %+v, want clip.mp4 playing, about 3 s long", st.Player)
	}
}

// A fragmented MP4 has no length in its header and Android will not seek in it, so the picture is
// served from an ordinary copy; a file that is already ordinary is served as it is.
func TestFragmentedVideoIsServedSeekable(t *testing.T) {
	if !player.VideoSupported() || player.FFmpeg() == "" {
		t.Skip("no ffmpeg")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // the copy goes in the cache: not the developer's own
	music := t.TempDir()
	plain, err := os.ReadFile("../player/testdata/sweep44100.mp4")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(music, "plain.mp4"), plain, 0o644)
	frag := filepath.Join(music, "frag.mp4")
	if b, err := exec.Command(player.FFmpeg(), "-nostdin", "-v", "error", "-i", filepath.Join(music, "plain.mp4"), "-c", "copy", "-movflags", "frag_keyframe+empty_moov", frag).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	if !fragmentedMP4(frag) || fragmentedMP4(filepath.Join(music, "plain.mp4")) {
		t.Fatal("fragmentedMP4 should say yes for the fragmented file and no for the plain one")
	}
	httpAddr := freeAddr(t)
	n, err := Start([]string{music}, "", freeAddr(t), httpAddr, "video", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Stop()
	get := func(track string) []byte {
		resp, err := http.Get("http://" + httpAddr + "/api/video?t=" + track)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return b
	}
	if got := get("plain.mp4"); !bytes.Equal(got, plain) {
		t.Error("a plain mp4 must be served as it is")
	}
	got := get("frag.mp4")
	if len(got) == 0 || fragmentedBytes(got) {
		t.Fatalf("the fragmented file must be served as an ordinary mp4 (%d bytes, fragmented=%v)", len(got), fragmentedBytes(got))
	}
	if i := bytes.Index(got, []byte("mvhd")); i < 0 || binary.BigEndian.Uint32(got[i+16:]) == 0 || binary.BigEndian.Uint32(got[i+20:]) == 0 {
		t.Error("the copy has no length in its header")
	}
}

func fragmentedBytes(b []byte) bool {
	return bytes.Contains(b, []byte("mvex")) || bytes.Contains(b, []byte("moof"))
}

// A video extension does not make a picture: an audio-only webm (a song saved from YouTube) is a song for the
// screens, which then show no video button and no black picture.
func TestSoundOnlyVideoHasNoPicture(t *testing.T) {
	if !player.VideoSupported() || player.FFmpeg() == "" {
		t.Skip("no ffmpeg")
	}
	music := t.TempDir()
	clip, err := os.ReadFile("../player/testdata/sweep44100.mp4")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(music, "a-clip.mp4"), clip, 0o644)
	if b, err := exec.Command(player.FFmpeg(), "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=d=3", "-c:a", "libvorbis", filepath.Join(music, "b-voice.webm")).CombinedOutput(); err != nil {
		if b, err = exec.Command(player.FFmpeg(), "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=d=3", "-c:a", "aac", filepath.Join(music, "b-voice.mkv")).CombinedOutput(); err != nil {
			t.Skipf("ffmpeg cannot make a sound-only file here: %v %s", err, b)
		}
		os.Rename(filepath.Join(music, "b-voice.mkv"), filepath.Join(music, "b-voice.webm")) // the name is all the app looks at
	}
	n, err := Start([]string{music}, "", freeAddr(t), freeAddr(t), "video", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Stop()
	for i, want := range []bool{false, true} { // library order: a-clip.mp4, b-voice.webm
		n.p.PlayIndex(i)
		time.Sleep(300 * time.Millisecond)
		if now := n.Now(); now.NoPicture != want {
			t.Errorf("%s: NoPicture = %v, want %v", now.Track, now.NoPicture, want)
		}
		n.p.Pause()
	}
}
