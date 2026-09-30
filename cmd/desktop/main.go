// Command desktop is the laptop/desktop letsgo app: it runs the node (server +
// discovery + supervisor) and plays the selected audio source through the
// speakers, with the desktop control UI in a Chrome app window.
//
// Symmetric: hit play in the window to cast this machine's library (and hear it);
// when another device is casting and you're not, it auto-plays that instead. The
// window can also browse and control any other device on the network.
// Started a second time it shows the window of the copy that is running, or, if that copy is
// an older build (you rebuilt and reopened the app), quits it and takes over.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"letsgo/app"
	"letsgo/mpris"
	"letsgo/speaker"
)

func main() {
	home, _ := os.UserHomeDir()
	var music dirFlags
	flag.Var(&music, "music", "music folder (repeat for several; default ~/Music)")
	name := flag.String("name", hostname(), "name shown to other devices")
	httpAddr := flag.String("http", ":8080", "web UI address")
	latency := flag.Int("latency", 0, "extra delay ms for this device, e.g. Bluetooth speakers (+ plays later)")
	buffer := flag.Int("buffer", 1000, "sync buffer ms; raise if audio stutters")
	connect := flag.String("connect", "", "listen to this host when not casting, instead of auto-discovery (for networks that block mDNS)")
	noWindow := flag.Bool("no-window", false, "don't open the UI window")
	noDisc := flag.Bool("no-discovery", false, "do not advertise or look for other devices (use -connect / stock Snapcast clients)")
	flag.Parse()
	if *noDisc {
		app.Discovery = false
	}

	logToFile()
	app.Version = buildVersion()
	if !startFresh(*httpAddr, app.Version) {
		if !*noWindow {
			openWindow(localURL(*httpAddr))
		}
		return
	}

	node, err := app.Start(music.or(filepath.Join(home, "Music")), dataDir(), ":1704", *httpAddr, *name, *buffer)
	if err != nil {
		log.Fatal(err)
	}
	defer node.Stop()
	app.Quit = func() { time.Sleep(200 * time.Millisecond); node.Stop(); os.Exit(0) }
	node.SetLatency(*latency)
	node.Pin(*connect)
	log.Printf("letsgo %q: web UI on %s", *name, *httpAddr)

	if err := speaker.Play(nodeReader{node}, node.SetOutputLatency); err != nil {
		log.Fatal(err)
	}

	// Media keys and the desktop's media widget (MPRIS). Not fatal: no session bus, no keys.
	cache, _ := os.UserCacheDir()
	if svc, err := mpris.Start(node, "org.mpris.MediaPlayer2.letsgo", filepath.Join(cache, "letsgo", "art")); err != nil {
		log.Printf("media keys unavailable: %v", err)
	} else {
		defer svc.Close()
	}

	if !*noWindow {
		openWindow(localURL(*httpAddr))
	}
	select {}
}

// logToFile also writes the log to ~/.cache/letsgo/letsgo.log (a menu launch has no terminal to
// read it from). The last run is kept as letsgo.log.old once the file passes 2 MB.
func logToFile() {
	dir, err := os.UserCacheDir()
	if err != nil {
		return
	}
	path := filepath.Join(dir, "letsgo", "letsgo.log")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if fi, err := os.Stat(path); err == nil && fi.Size() > 2<<20 {
		os.Rename(path, path+".old")
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
}

// localURL is where this machine reaches its own UI, whatever the listen address is.
func localURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://localhost" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// buildVersion tells one build of this program from another: the executable's size and time.
func buildVersion() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
}

// startFresh decides what a newly started copy does. It is true when there is nothing to
// show (no letsgo answers on addr), or when an older build was running and has now quit, so
// this copy should start. It is false when a copy of this same build is already running, or an
// old one that cannot be told to quit (a build from before this existed): show its window instead.
func startFresh(addr, version string) bool {
	running, theirs := probe(addr)
	if !running {
		return true
	}
	if theirs == version {
		return false
	}
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Post(localURL(addr)+"/api/quit", "", nil)
	if err != nil {
		return false
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent { // an older build answers every path with its web page
		log.Printf("an older letsgo is still running and cannot be replaced by this one: quit it (pkill -x desktop) and start again")
		return false
	}
	for i := 0; i < 50; i++ { // wait for it to let go of the ports
		if up, _ := probe(addr); !up {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// probe asks the letsgo on addr whether it is up, and which build it is ("" if it does not say).
func probe(addr string) (up bool, version string) {
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get(localURL(addr) + "/api/state")
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, ""
	}
	var s struct {
		Version string `json:"version"`
	}
	json.NewDecoder(resp.Body).Decode(&s)
	return true, s.Version
}

// nodeReader feeds the audio device from the node's currently selected source.
type nodeReader struct{ n *app.Node }

func (r nodeReader) Read(p []byte) (int, error) { return r.n.Read(p) }

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "letsgo-desktop"
}

// windowArgs are the browser flags of the app window.
//
// Identity: letsgo runs it in a profile of its own, named "letsgo", kept in profileDir: apart from your
// browsing, and with a window identity that is only ours. On Wayland Chrome builds the identity (the
// app-id) from the address and the profile name, so it is "chrome-localhost__-letsgo" instead of
// "chrome-localhost__-Default", which every other app window on localhost would share; on X11 --class
// sets it to "letsgo". install-desktop.sh names the one that applies in the launcher (StartupWMClass),
// and the dock then shows "letsgo" with its logo.
//
// Wayland: Chrome's GPU compositing path makes it drop out of full screen 1.5 s after entering it (seen
// on COSMIC with Chrome 154 and an Intel + NVIDIA laptop, for any web page, not only ours; the browser
// itself asks the compositor to leave). Software compositing avoids it and leaves video decoding
// alone, so it is used on Wayland only.
func windowArgs(url, profileDir string, wayland bool) []string {
	args := []string{"--app=" + url, "--window-size=1120,740", "--class=letsgo"}
	if profileDir != "" {
		args = append(args, "--user-data-dir="+profileDir, "--profile-directory=letsgo", "--no-first-run", "--no-default-browser-check")
	}
	if wayland {
		args = append(args, "--disable-gpu-compositing")
	}
	return args
}

// openWindow opens the UI as a chromeless app window if a Chrome-family browser
// is around, else falls back to the default browser.
func openWindow(url string) {
	profile := ""
	if d := dataDir(); d != "" {
		profile = filepath.Join(d, "chrome")
	}
	wayland := os.Getenv("XDG_SESSION_TYPE") == "wayland" || os.Getenv("WAYLAND_DISPLAY") != ""
	for _, b := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "brave-browser", "microsoft-edge"} {
		if path, err := exec.LookPath(b); err == nil {
			exec.Command(path, windowArgs(url, profile, wayland)...).Start()
			return
		}
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	exec.Command(opener, url).Start()
}

// dataDir is where favourites and playlists are saved (~/.config/letsgo).
func dataDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "letsgo")
}

// dirFlags collects every -music given.
type dirFlags []string

func (d *dirFlags) String() string     { return strings.Join(*d, ",") }
func (d *dirFlags) Set(v string) error { *d = append(*d, v); return nil }
func (d dirFlags) or(def string) []string {
	if len(d) == 0 {
		return []string{def}
	}
	return d
}
