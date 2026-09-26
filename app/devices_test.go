package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The desktop UI browses and controls another device through this node: what it
// sends to /dev/<ip>/api/... must arrive at that device, and nothing else may be reachable.
func TestDeviceProxy(t *testing.T) {
	music := t.TempDir()
	writeRamp(t, music, 5)
	aHTTP, bHTTP := freeAddr(t), freeAddr(t)
	a, err := Start([]string{music}, "", freeAddr(t), aHTTP, "phone", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	b, err := Start([]string{t.TempDir()}, "", freeAddr(t), bHTTP, "laptop", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	b.setPeerPort(port(aHTTP)) // the laptop reaches the phone's API on its test port

	do := func(method, addr, path, body string) (int, string) {
		req, _ := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}

	if code, out := do("GET", bHTTP, "/api/library", ""); code != 200 || strings.Contains(out, "ramp.wav") {
		t.Fatalf("laptop's own library must not hold the phone's songs: %d %s", code, out)
	}
	if code, out := do("GET", bHTTP, "/dev/127.0.0.1/api/library", ""); code != 200 || !strings.Contains(out, "ramp.wav") {
		t.Fatalf("phone library through the laptop: %d %s", code, out)
	}
	if code, _ := do("POST", bHTTP, "/dev/127.0.0.1/api/queue", `{"tracks":["ramp.wav"],"index":0,"context":"from laptop"}`); code != 204 {
		t.Fatalf("queue through the laptop: %d", code)
	}
	var st struct {
		Player struct {
			Playing bool
			Context string
		}
		Volume int
	}
	_, out := do("GET", aHTTP, "/api/state", "")
	json.Unmarshal([]byte(out), &st)
	if !st.Player.Playing || st.Player.Context != "from laptop" {
		t.Fatalf("the phone did not start playing what the laptop asked: %s", out)
	}
	if code, _ := do("POST", bHTTP, "/dev/127.0.0.1/api/volume?v=42", ""); code != 204 {
		t.Fatalf("volume through the laptop: %d", code)
	}
	_, out = do("GET", aHTTP, "/api/state", "")
	json.Unmarshal([]byte(out), &st)
	if st.Volume != 42 {
		t.Fatalf("query string lost on the way: volume %d", st.Volume)
	}

	for _, bad := range []string{"8.8.8.8", "example.com", "0.0.0.0", "127.0.0.1%2F..%2F"} {
		if code, _ := do("GET", bHTTP, "/dev/"+bad+"/api/library", ""); code != http.StatusForbidden && code != http.StatusNotFound {
			t.Errorf("%s must not be reachable through the proxy, got %d", bad, code)
		}
	}
	if code, out := do("GET", bHTTP, "/dev/127.0.0.2/api/state", ""); code != http.StatusBadGateway {
		t.Errorf("a device that is gone must give 502, got %d %s", code, out)
	}
}

// The desktop app can be told to quit by a newer copy of itself (only from this machine, and
// only if the build supports it), and reports which build it is.
func TestQuitAndVersion(t *testing.T) {
	addr := freeAddr(t)
	n, err := Start([]string{t.TempDir()}, "", freeAddr(t), addr, "node", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Stop()
	oldV, oldQ := Version, Quit
	defer func() { Version, Quit = oldV, oldQ }()

	post := func() int {
		resp, err := http.Post("http://"+addr+"/api/quit", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	Quit = nil
	if code := post(); code != http.StatusForbidden {
		t.Errorf("a build that cannot quit answered %d, want 403", code)
	}
	quit := make(chan struct{}, 1)
	Version, Quit = "build-7", func() { quit <- struct{}{} }
	if code := post(); code != http.StatusNoContent {
		t.Errorf("quit from this machine answered %d", code)
	}
	select {
	case <-quit:
	case <-time.After(time.Second):
		t.Error("Quit was not called")
	}
	resp, _ := http.Get("http://" + addr + "/api/state")
	var st struct{ Version string }
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st.Version != "build-7" {
		t.Errorf("state reports version %q", st.Version)
	}
}
