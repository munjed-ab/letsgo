package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
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
