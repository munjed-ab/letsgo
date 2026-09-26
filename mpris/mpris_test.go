//go:build linux

package mpris

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"letsgo/app"
)

type fake struct {
	mu   sync.Mutex
	now  app.Now
	did  []string
	args []float64
}

func (f *fake) Now() app.Now { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fake) Do(cmd string, arg float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.did, f.args = append(f.did, cmd), append(f.args, arg)
	return nil
}
func (f *fake) ArtFile(now app.Now, dir string) string { return "" }
func (f *fake) set(n app.Now)                          { f.mu.Lock(); f.now = n; f.mu.Unlock() }
func (f *fake) last() (string, float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.did) == 0 {
		return "", 0
	}
	return f.did[len(f.did)-1], f.args[len(f.args)-1]
}

// Talks to the service over the real session bus like playerctl or the desktop's
// media widget would.
func TestMPRIS(t *testing.T) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Skip("no D-Bus session bus")
	}
	src := &fake{now: app.Now{Track: "a/b.mp3", Title: "Song", Artist: "Artist", Album: "Album", Playing: true, Elapsed: 10, Duration: 200}}
	name := fmt.Sprintf("org.mpris.MediaPlayer2.letsgo.test%d", os.Getpid())
	svc, err := Start(src, name, t.TempDir())
	if err != nil {
		t.Skipf("cannot register on the session bus: %v", err)
	}
	defer svc.Close()

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	obj := conn.Object(name, objPath)
	call := func(method string, args ...any) *dbus.Call { return obj.Call(playerIface+"."+method, 0, args...) }

	// what the widget shows
	var all map[string]dbus.Variant
	if err := obj.Call(propsIface+".GetAll", 0, playerIface).Store(&all); err != nil {
		t.Fatal(err)
	}
	if all["PlaybackStatus"].Value() != "Playing" || !all["CanSeek"].Value().(bool) || !all["CanControl"].Value().(bool) {
		t.Errorf("status/caps: %v", all)
	}
	meta := all["Metadata"].Value().(map[string]dbus.Variant)
	if meta["xesam:title"].Value() != "Song" || meta["xesam:album"].Value() != "Album" ||
		meta["xesam:artist"].Value().([]string)[0] != "Artist" || meta["mpris:length"].Value().(int64) != 200_000_000 {
		t.Errorf("metadata: %v", meta)
	}
	if _, ok := meta["mpris:trackid"].Value().(dbus.ObjectPath); !ok {
		t.Errorf("trackid is not an object path: %v", meta["mpris:trackid"])
	}
	if pos := all["Position"].Value().(int64); pos < 10_000_000 || pos > 11_000_000 {
		t.Errorf("position %d µs, want ~10 s", pos)
	}

	// what the media keys do
	for method, want := range map[string]string{"PlayPause": "toggle", "Next": "next", "Previous": "prev", "Pause": "pause", "Play": "play", "Stop": "pause"} {
		if err := call(method).Err; err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if got, _ := src.last(); got != want {
			t.Errorf("%s -> %q, want %q", method, got, want)
		}
	}
	call("SetPosition", dbus.ObjectPath("/org/letsgo/track/x"), int64(42_500_000))
	if cmd, arg := src.last(); cmd != "seek" || arg != 42.5 {
		t.Errorf("SetPosition -> %s %.2f", cmd, arg)
	}
	call("Seek", int64(5_000_000)) // relative: about 10 s + 5 s
	if cmd, arg := src.last(); cmd != "seek" || arg < 14.9 || arg > 16 {
		t.Errorf("Seek +5s -> %s %.2f", cmd, arg)
	}
	var wrote dbus.Variant
	if err := obj.Call(propsIface+".Set", 0, playerIface, "Volume", dbus.MakeVariant(0.5)).Store(&wrote); err == nil {
		t.Error("Set on a read-only property succeeded")
	}

	// the widget must be told when the track or state changes
	sigs := make(chan *dbus.Signal, 16)
	conn.Signal(sigs)
	conn.AddMatchSignal(dbus.WithMatchObjectPath(objPath), dbus.WithMatchInterface(propsIface))
	src.set(app.Now{Track: "c/d.mp3", Title: "Other song", Playing: false, Elapsed: 0, Duration: 100})
	deadline := time.After(3 * time.Second)
	gotStatus, gotTitle := false, false
	for !(gotStatus && gotTitle) {
		select {
		case s := <-sigs:
			if len(s.Body) < 2 {
				continue
			}
			ch, _ := s.Body[1].(map[string]dbus.Variant)
			if st, ok := ch["PlaybackStatus"]; ok && st.Value() == "Paused" {
				gotStatus = true
			}
			if m, ok := ch["Metadata"]; ok && m.Value().(map[string]dbus.Variant)["xesam:title"].Value() == "Other song" {
				gotTitle = true
			}
		case <-deadline:
			t.Fatalf("no PropertiesChanged for the new track (status %v, title %v)", gotStatus, gotTitle)
		}
	}

	// nothing playing at all: Stopped, empty metadata
	src.set(app.Now{})
	time.Sleep(800 * time.Millisecond)
	var status dbus.Variant
	obj.Call(propsIface+".Get", 0, playerIface, "PlaybackStatus").Store(&status)
	if status.Value() != "Stopped" {
		t.Errorf("idle status %v", status)
	}
}
