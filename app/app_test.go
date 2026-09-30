package app

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var handedOut = map[string]bool{}

// freeAddr returns a loopback address nothing is listening on. The probe socket
// is closed at once, so it also remembers what it returned: two calls in a row
// must never give the same port.
// Tests must never advertise on, or join, the real network: a test node would show
// up on (and could control) the developer's actual devices.
func TestMain(m *testing.M) {
	Discovery = false
	os.Exit(m.Run())
}

func freeAddr(t *testing.T) string {
	for {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		if !handedOut[addr] {
			handedOut[addr] = true
			return addr
		}
	}
}

// ramp.wav: 44.1k stereo 16-bit where frame k is L=(k+1)&0x7fff, R=(k+1)>>15, so a
// played sample can be traced back to its position in the file.
func writeRamp(t *testing.T, dir string, seconds int) { writeRampNamed(t, dir, "ramp.wav", seconds) }

func writeRampNamed(t *testing.T, dir, name string, seconds int) {
	frames := 44100 * seconds
	b := make([]byte, 44+frames*4)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+frames*4))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 2)
	binary.LittleEndian.PutUint32(b[24:], 44100)
	binary.LittleEndian.PutUint32(b[28:], 44100*4)
	binary.LittleEndian.PutUint16(b[32:], 4)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(frames*4))
	for k := 0; k < frames; k++ {
		binary.LittleEndian.PutUint16(b[44+k*4:], uint16((k+1)&0x7fff))
		binary.LittleEndian.PutUint16(b[44+k*4+2:], uint16((k+1)>>15))
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func port(addr string) string { _, p, _ := net.SplitHostPort(addr); return p }

// The full product path on loopback: file -> decoder -> player -> server -> TCP ->
// another node's client -> Node.Read (what the speaker pulls). Two nodes, as on
// a phone and a laptop.
func TestTwoNodesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	dir := t.TempDir()
	writeRamp(t, dir, 30)
	aSnap, aHTTP := freeAddr(t), freeAddr(t)
	a, err := Start([]string{dir}, "", aSnap, aHTTP, "phone", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	b, err := Start([]string{t.TempDir()}, "", freeAddr(t), freeAddr(t), "laptop", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	b.Pin(aSnap)
	b.SetOutputLatency(0)
	a.p.PlayIndex(0)

	out := make([]byte, 882*4)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	prevLast, bad, audio := -1, 0, 0
	start := time.Now()
	for time.Since(start) < 9*time.Second {
		<-tick.C
		b.Read(out)
		first, last := -1, -1
		for i := 0; i < 882; i++ {
			l, r := binary.LittleEndian.Uint16(out[i*4:]), binary.LittleEndian.Uint16(out[i*4+2:])
			if l == 0 && r == 0 {
				continue
			}
			k := int(l) + int(r)<<15 - 1
			if first < 0 {
				first = k
			}
			last = k
		}
		if first < 0 {
			continue
		}
		audio++
		// A read consumes 880..884 frames (exact, or 0.2% slewing).
		if last-first < 875 || last-first > 889 || (prevLast >= 0 && (first-prevLast < -2 || first-prevLast > 5)) {
			if time.Since(start) > 3*time.Second { // ignore the initial lock
				bad++
			}
		}
		prevLast = last
	}
	if audio < 200 {
		t.Fatalf("only %d reads carried audio in 9 s", audio)
	}
	if bad != 0 {
		t.Errorf("%d discontinuities in the played stream", bad)
	}
	b.mu.Lock()
	c := b.client
	b.mu.Unlock()
	if c == nil {
		t.Fatal("laptop never connected to the pinned phone")
	}
	if s := c.Stats(); s.Underruns != 0 || s.Resyncs != 0 || s.SyncErrMs > 10 || s.SyncErrMs < -10 {
		t.Errorf("stats %+v", s)
	}

	// The listener list must tell devices apart.
	names := ""
	for _, ci := range a.srv.Clients() {
		names += ci.Name + "|"
	}
	if !strings.Contains(names, "phone") || !strings.Contains(names, "laptop") {
		t.Errorf("listener names %q, want both devices by name", names)
	}
}

// Android keeps the process alive when the service restarts: Stop must free the
// ports and goroutines so Start works again in the same process.
func TestStopThenStartAgain(t *testing.T) {
	snapAddr, httpAddr := freeAddr(t), freeAddr(t)
	for i := 0; i < 3; i++ {
		n, err := Start([]string{t.TempDir()}, "", snapAddr, httpAddr, "node", 500)
		if err != nil {
			t.Fatalf("start #%d: %v", i, err)
		}
		conn, err := net.DialTimeout("tcp", snapAddr, time.Second)
		if err != nil {
			t.Fatalf("start #%d: stream port not serving: %v", i, err)
		}
		conn.Close()
		n.Stop()
		if _, err := net.DialTimeout("tcp", snapAddr, 300*time.Millisecond); err == nil {
			t.Fatalf("stop #%d: stream port still open", i)
		}
	}
}

// The web UI's sync-offset box: POST /api/latency sets this device's offset,
// /api/state reports it, and absurd values are clamped.
func TestLatencyEndpoint(t *testing.T) {
	httpAddr := freeAddr(t)
	n, err := Start([]string{t.TempDir()}, "", freeAddr(t), httpAddr, "node", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Stop()
	post := func(q string) {
		resp, err := http.Post("http://"+httpAddr+"/api/latency?"+q, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	get := func() int {
		resp, err := http.Get("http://" + httpAddr + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var s struct{ LatencyMs int }
		json.NewDecoder(resp.Body).Decode(&s)
		return s.LatencyMs
	}
	post("ms=-40")
	if got := get(); got != -40 {
		t.Errorf("latency = %d, want -40", got)
	}
	post("ms=999999")
	if got := get(); got != 2000 {
		t.Errorf("latency = %d, want clamp to 2000", got)
	}
}

// Favourites, playlists, folders and queues through the real HTTP API, and that
// they survive a restart.
func TestListsAPIAndPersistence(t *testing.T) {
	music, data := t.TempDir(), t.TempDir()
	for _, f := range []string{"a.wav", "b.wav", "c.wav"} {
		writeRampNamed(t, music, f, 20)
	}
	httpAddr := freeAddr(t)
	n, err := Start([]string{music}, data, freeAddr(t), httpAddr, "node", 500)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, "http://"+httpAddr+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}
	ok := func(method, path, body string) string {
		code, out := call(method, path, body)
		if code != 200 && code != 204 {
			t.Fatalf("%s %s %s -> %d %s", method, path, body, code, out)
		}
		return out
	}

	ok("POST", "/api/folder", `{"name":"Trips"}`)
	var created struct{ ID string }
	json.Unmarshal([]byte(ok("POST", "/api/playlist", `{"name":"Road","folder":"Trips"}`)), &created)
	ok("POST", "/api/playlist/add", `{"id":"`+created.ID+`","tracks":["c.wav","a.wav","b.wav"]}`)
	ok("POST", "/api/fav", `{"track":"b.wav","on":true}`)

	// play the playlist starting at its 2nd track
	ok("POST", "/api/queue", `{"tracks":["c.wav","a.wav","b.wav"],"index":1,"context":"Road"}`)
	var st struct {
		Player struct {
			Track   string
			Context string
			Count   int
		}
	}
	json.Unmarshal([]byte(ok("GET", "/api/state", "")), &st)
	if st.Player.Track != "a.wav" || st.Player.Context != "Road" || st.Player.Count != 3 {
		t.Errorf("state after /api/queue: %+v", st.Player)
	}

	// bad input is rejected, not ignored
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/queue", `{"tracks":[]}`},                       // explicitly empty
		{"POST", "/api/queue", `{"tracks":["nope.wav"]}`},             // nothing playable
		{"POST", "/api/playlist", `{"name":"  "}`},                    // blank name
		{"POST", "/api/playlist/add", `{"id":"missing","tracks":[]}`}, // unknown playlist
		{"POST", "/api/fav", `not json`},
		{"GET", "/api/fav", ``},
	} {
		if code, _ := call(tc.method, tc.path, tc.body); code < 400 {
			t.Errorf("%s %s %s -> %d, want an error", tc.method, tc.path, tc.body, code)
		}
	}

	// no tracks = whole library
	ok("POST", "/api/queue", `{"index":2}`)
	json.Unmarshal([]byte(ok("GET", "/api/state", "")), &st)
	if st.Player.Track != "c.wav" || st.Player.Context != "" || st.Player.Count != 3 {
		t.Errorf("library queue: %+v", st.Player)
	}

	want := ok("GET", "/api/lists", "")
	n.Stop()

	// a fresh node on the same data dir sees the same lists
	httpAddr = freeAddr(t)
	n2, err := Start([]string{music}, data, freeAddr(t), httpAddr, "node", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Stop()
	if got := ok("GET", "/api/lists", ""); got != want {
		t.Errorf("after restart:\n got %s\nwant %s", got, want)
	}
	if !strings.Contains(want, `"favorites":["b.wav"]`) || !strings.Contains(want, `"name":"Road"`) || !strings.Contains(want, `"Trips"`) {
		t.Errorf("lists missing expected content: %s", want)
	}
}

// Bulk actions and multiple music folders through the real API.
func TestBulkAndSourcesAPI(t *testing.T) {
	music, extra := t.TempDir(), filepath.Join(t.TempDir(), "Downloads")
	os.MkdirAll(extra, 0o755)
	writeRampNamed(t, music, "a.wav", 3)
	writeRampNamed(t, music, "b.wav", 3)
	writeRampNamed(t, extra, "c.wav", 3)
	httpAddr := freeAddr(t)
	n, err := Start([]string{music}, "", freeAddr(t), httpAddr, "node", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Stop()
	call := func(path, body string) (int, string) {
		method := "POST"
		if body == "" {
			method = "GET"
		}
		req, _ := http.NewRequest(method, "http://"+httpAddr+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}
	library := func() string { _, b := call("/api/library", ""); return b }

	if got := library(); got != `["a.wav","b.wav"]` {
		t.Fatalf("library %s", got)
	}
	// second folder: its tracks appear under its own name; the first folder's ids stay put
	if code, out := call("/api/sources", `{"add":"`+extra+`"}`); code != 200 || !strings.Contains(out, extra) {
		t.Fatalf("add source: %d %s", code, out)
	}
	if got := library(); got != `["Downloads/c.wav","a.wav","b.wav"]` {
		t.Errorf("library after add %s", got)
	}
	if code, _ := call("/api/sources", `{"add":"/definitely/not/here"}`); code != 400 {
		t.Errorf("bad folder -> %d, want 400", code)
	}
	if code, _ := call("/api/sources", `{"add":"`+music+`"}`); code != 400 {
		t.Errorf("duplicate folder -> %d, want 400", code)
	}

	// bulk favourite / unfavourite
	if code, out := call("/api/fav", `{"tracks":["a.wav","Downloads/c.wav","b.wav"],"on":true}`); code != 204 {
		t.Fatalf("bulk fav: %d %s", code, out)
	}
	if _, out := call("/api/lists", ""); !strings.Contains(out, `"favorites":["a.wav","Downloads/c.wav","b.wav"]`) {
		t.Errorf("favorites: %s", out)
	}
	call("/api/fav", `{"tracks":["a.wav","b.wav"],"on":false}`)
	if _, out := call("/api/lists", ""); !strings.Contains(out, `"favorites":["Downloads/c.wav"]`) {
		t.Errorf("favorites after bulk remove: %s", out)
	}
	if code, _ := call("/api/fav", `{"tracks":[],"on":true}`); code != 400 {
		t.Errorf("empty selection -> %d, want 400", code)
	}

	// queue a track from the extra folder: it must resolve and play
	if code, out := call("/api/queue", `{"tracks":["Downloads/c.wav","a.wav"],"index":0,"context":"Mix"}`); code != 204 {
		t.Fatalf("queue: %d %s", code, out)
	}
	if _, out := call("/api/state", ""); !strings.Contains(out, `"track":"Downloads/c.wav"`) {
		t.Errorf("state: %s", out)
	}

	// removing a folder drops its tracks
	call("/api/sources", `{"remove":"`+extra+`"}`)
	if got := library(); got != `["a.wav","b.wav"]` {
		t.Errorf("library after remove %s", got)
	}
}

// Now/control: what is playing, its tags and cover art, seeking, and that a
// device which is only HEARING another can control it (its notification's pause
// button pauses the laptop that is actually playing).
func TestNowMetaArtAndRemoteControl(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	music := t.TempDir()
	writeRampNamed(t, music, "ramp.wav", 30)
	tagged, _ := os.ReadFile("../meta/testdata/tagged.mp3")
	os.WriteFile(filepath.Join(music, "tagged.mp3"), tagged, 0o644)

	aSnap, aHTTP := freeAddr(t), freeAddr(t)
	a, err := Start([]string{music}, "", aSnap, aHTTP, "phone", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	bHTTP := freeAddr(t)
	b, err := Start([]string{t.TempDir()}, "", freeAddr(t), bHTTP, "laptop", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	b.setPeerPort(port(aHTTP)) // the laptop reaches the phone's API on its test port
	b.Pin(aSnap)

	get := func(addr, path string, out any) int {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}
	post := func(addr, path string) int {
		resp, err := http.Post("http://"+addr+path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// tags and art are read in the background
	var meta struct {
		Done, Total int
		Tracks      map[string]struct{ T, A, Al, Art string }
	}
	for i := 0; i < 100; i++ {
		get(aHTTP, "/api/meta", &meta)
		if meta.Total > 0 && meta.Done == meta.Total {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	tr := meta.Tracks["tagged.mp3"]
	if tr.T != "Tagged Song" || tr.A != "The Artist" || tr.Art == "" {
		t.Fatalf("meta for tagged.mp3: %+v (done %d/%d)", tr, meta.Done, meta.Total)
	}
	if _, has := meta.Tracks["ramp.wav"]; has {
		t.Error("untagged wav should not be listed with tags")
	}
	resp, err := http.Get("http://" + aHTTP + "/api/art/" + tr.Art + "?s=64")
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("cover art request: %v %v", err, resp)
	}
	if code := get(aHTTP, "/api/art/nosuchhash", nil); code != 404 {
		t.Errorf("unknown art -> %d, want 404", code)
	}

	// the phone plays ramp.wav; its Now falls back to the file name for the title
	a.p.SetQueue([]string{"ramp.wav"}, 0, "Road trip")
	time.Sleep(2500 * time.Millisecond) // the laptop connects (1.5 s supervisor tick) and audio flows
	var now Now
	get(aHTTP, "/api/now", &now)
	if now.Track != "ramp.wav" || now.Title != "ramp" || !now.Playing || now.Context != "Road trip" || now.Remote || now.Duration < 29 {
		t.Errorf("phone's Now: %+v", now)
	}

	// the laptop is not casting but hears the phone: its Now is the phone's track,
	// and its controls act on the phone
	var bn Now
	get(bHTTP, "/api/now", &bn)
	if !bn.Remote || bn.Track != "ramp.wav" || !bn.Playing || bn.Source != aHTTP {
		t.Fatalf("laptop's Now: %+v", bn)
	}
	if code := post(bHTTP, "/api/control?cmd=pause"); code != 204 {
		t.Fatalf("pause via laptop -> %d", code)
	}
	if a.p.State().Playing {
		t.Error("pausing from the laptop did not pause the phone")
	}
	if code := post(bHTTP, "/api/control?cmd=seek&t=12.5"); code != 204 {
		t.Fatalf("seek via laptop -> %d", code)
	}
	if e := a.p.State().Elapsed; e < 12.4 || e > 12.7 {
		t.Errorf("seek via laptop: phone is at %.2f s, want 12.5", e)
	}
	post(bHTTP, "/api/control?cmd=play")
	if !a.p.State().Playing {
		t.Error("play via laptop did not resume the phone")
	}
	if code := post(bHTTP, "/api/control?cmd=explode"); code != 400 {
		t.Errorf("unknown command -> %d, want 400", code)
	}
	// A paused phone is still what the laptop controls (it has nothing of its own
	// queued), so pressing play on the laptop can bring it back.
	post(aHTTP, "/api/control?cmd=pause")
	time.Sleep(1100 * time.Millisecond) // the peer's answer is cached for 1 s
	get(bHTTP, "/api/now", &bn)
	if !bn.Remote || bn.Playing || bn.Track != "ramp.wav" {
		t.Errorf("laptop's Now while the phone is paused: %+v", bn)
	}
	// /api/listen pins and unpins
	var st struct{ Pinned string }
	post(bHTTP, "/api/control?cmd=toggle") // harmless local toggle on an empty queue
	req, _ := http.NewRequest("POST", "http://"+bHTTP+"/api/listen", strings.NewReader(`{"addr":""}`))
	http.DefaultClient.Do(req)
	get(bHTTP, "/api/state", &st)
	if st.Pinned != "" {
		t.Errorf("pinned = %q after unpin", st.Pinned)
	}
}

// A device that has a music library but has not played anything yet must follow a
// paused peer (media keys resume the peer); once it plays its own music, it is its own.
func TestFreshDeviceFollowsPausedPeer(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	phoneMusic, laptopMusic := t.TempDir(), t.TempDir()
	writeRampNamed(t, phoneMusic, "phone.wav", 30)
	writeRampNamed(t, laptopMusic, "laptop.wav", 30)
	aSnap, aHTTP := freeAddr(t), freeAddr(t)
	a, err := Start([]string{phoneMusic}, "", aSnap, aHTTP, "phone", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	b, err := Start([]string{laptopMusic}, "", freeAddr(t), freeAddr(t), "laptop", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	b.setPeerPort(port(aHTTP))
	b.Pin(aSnap)

	if n := b.Now(); n.Track != "" || n.Remote {
		t.Fatalf("a device that has played nothing reported %+v", n)
	}
	a.p.PlayIndex(0)
	time.Sleep(2500 * time.Millisecond)
	if n := b.Now(); !n.Remote || n.Track != "phone.wav" {
		t.Fatalf("laptop should follow the playing phone: %+v", n)
	}
	a.p.Pause()
	time.Sleep(1200 * time.Millisecond)
	if n := b.Now(); !n.Remote || n.Playing || n.Track != "phone.wav" {
		t.Fatalf("laptop should still follow the PAUSED phone: %+v", n)
	}
	if err := b.Do("play", 0); err != nil || !a.p.State().Playing {
		t.Fatalf("laptop's play must resume the phone (err %v, phone playing %v)", err, a.p.State().Playing)
	}
	if b.p.State().Started {
		t.Error("the laptop started its own music instead")
	}
	// now the laptop plays its own music: its controls are its own from then on
	b.p.PlayIndex(0)
	a.p.Pause()
	time.Sleep(1200 * time.Millisecond)
	if n := b.Now(); n.Remote || n.Track != "laptop.wav" {
		t.Errorf("after playing its own song the laptop should control itself: %+v", n)
	}
}

// Pausing the phone from the laptop (a click on the video, the pause button) must keep showing the
// phone's track, even though the laptop played a song of its own earlier: the picture and the play
// button that resumes it stay, instead of the laptop's old song coming back.
func TestPausingPeerKeepsShowingIt(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time test")
	}
	phoneMusic, laptopMusic := t.TempDir(), t.TempDir()
	writeRampNamed(t, phoneMusic, "phone.wav", 30)
	writeRampNamed(t, laptopMusic, "laptop.wav", 30)
	aSnap, aHTTP := freeAddr(t), freeAddr(t)
	a, err := Start([]string{phoneMusic}, "", aSnap, aHTTP, "phone", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	b, err := Start([]string{laptopMusic}, "", freeAddr(t), freeAddr(t), "laptop", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	b.setPeerPort(port(aHTTP))
	b.Pin(aSnap)

	b.p.PlayIndex(0) // the laptop has played something of its own
	b.p.Pause()
	a.p.PlayIndex(0)
	time.Sleep(2500 * time.Millisecond)
	if n := b.Now(); !n.Remote || n.Track != "phone.wav" || !n.Playing {
		t.Fatalf("laptop should follow the playing phone: %+v", n)
	}
	if err := b.Do("toggle", 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	if n := b.Now(); !n.Remote || n.Playing || n.Track != "phone.wav" {
		t.Fatalf("laptop dropped the phone it paused: %+v", n)
	}
	if err := b.Do("toggle", 0); err != nil || !a.p.State().Playing {
		t.Fatalf("laptop's play must resume the phone (err %v, phone playing %v)", err, a.p.State().Playing)
	}
}
