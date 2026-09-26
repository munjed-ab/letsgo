//go:build linux

package mpris

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const (
	objPath     = "/org/mpris/MediaPlayer2"
	rootIface   = "org.mpris.MediaPlayer2"
	playerIface = "org.mpris.MediaPlayer2.Player"
	propsIface  = "org.freedesktop.DBus.Properties"
)

// Service is a running MPRIS registration.
type Service struct {
	conn   *dbus.Conn
	src    Source
	artDir string
	stop   chan struct{}
	once   sync.Once

	mu  sync.Mutex
	cur snap
}

// snap is what the desktop was last told; changes against it become signals.
type snap struct {
	status  string
	meta    map[string]dbus.Variant
	canSeek bool
	elapsed float64
	at      time.Time
	track   string
}

// Start registers busName (e.g. "org.mpris.MediaPlayer2.letsgo") on the session
// bus and keeps it in step with src. Cover art is cached as files under artDir.
func Start(src Source, busName, artDir string) (*Service, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	s := &Service{conn: conn, src: src, artDir: artDir, stop: make(chan struct{})}
	s.cur = s.take() // before anything is exported: requests can arrive the moment we are
	p, r, pr := player{s}, root{}, props{s}
	for _, e := range []struct {
		o     any
		iface string
		names map[string]string
	}{{r, rootIface, nil}, {p, playerIface, map[string]string{"SeekBy": "Seek"}}, {pr, propsIface, nil}} {
		if err := conn.ExportWithMap(e.o, e.names, objPath, e.iface); err != nil {
			conn.Close()
			return nil, err
		}
	}
	playerMethods := introspect.Methods(p)
	for i := range playerMethods {
		if playerMethods[i].Name == "SeekBy" {
			playerMethods[i].Name = "Seek"
		}
	}
	err = conn.Export(introspect.NewIntrospectable(&introspect.Node{
		Name: objPath,
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			{Name: propsIface, Methods: introspect.Methods(pr), Signals: []introspect.Signal{{Name: "PropertiesChanged", Args: []introspect.Arg{
				{Name: "interface_name", Type: "s"}, {Name: "changed_properties", Type: "a{sv}"}, {Name: "invalidated_properties", Type: "as"}}}}},
			{Name: rootIface, Methods: introspect.Methods(r), Properties: []introspect.Property{
				ro("CanQuit", "b"), ro("CanRaise", "b"), ro("HasTrackList", "b"), ro("Identity", "s"),
				ro("SupportedUriSchemes", "as"), ro("SupportedMimeTypes", "as")}},
			{Name: playerIface, Methods: playerMethods, Signals: []introspect.Signal{{Name: "Seeked", Args: []introspect.Arg{{Name: "Position", Type: "x"}}}}, Properties: []introspect.Property{
				ro("PlaybackStatus", "s"), ro("LoopStatus", "s"), ro("Rate", "d"), ro("Shuffle", "b"), ro("Metadata", "a{sv}"),
				ro("Volume", "d"), ro("Position", "x"), ro("MinimumRate", "d"), ro("MaximumRate", "d"), ro("CanGoNext", "b"),
				ro("CanGoPrevious", "b"), ro("CanPlay", "b"), ro("CanPause", "b"), ro("CanSeek", "b"), ro("CanControl", "b")}},
		},
	}), objPath, "org.freedesktop.DBus.Introspectable")
	if err != nil {
		conn.Close()
		return nil, err
	}
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		conn.Close()
		return nil, errors.New("could not own " + busName + " on the session bus (already running?)")
	}
	go s.loop()
	return s, nil
}

func ro(name, typ string) introspect.Property {
	return introspect.Property{Name: name, Type: typ, Access: "read"}
}

// Close unregisters from the bus.
func (s *Service) Close() {
	s.once.Do(func() {
		close(s.stop)
		s.conn.Close()
	})
}

func v(x any) dbus.Variant { return dbus.MakeVariant(x) }

// take reads the node and turns it into what MPRIS clients expect.
func (s *Service) take() snap {
	now := s.src.Now()
	sn := snap{status: "Stopped", meta: map[string]dbus.Variant{}, canSeek: now.Duration > 0, elapsed: now.Elapsed, at: time.Now(), track: now.Track}
	if now.Track == "" {
		return sn
	}
	sn.status = "Paused"
	if now.Playing {
		sn.status = "Playing"
	}
	sum := sha1.Sum([]byte(now.Track))
	sn.meta["mpris:trackid"] = v(dbus.ObjectPath("/org/letsgo/track/" + hex.EncodeToString(sum[:8])))
	sn.meta["xesam:title"] = v(now.Title)
	artists := []string{}
	if now.Artist != "" {
		artists = append(artists, now.Artist)
	}
	sn.meta["xesam:artist"] = v(artists)
	if now.Album != "" {
		sn.meta["xesam:album"] = v(now.Album)
	}
	if now.Duration > 0 {
		sn.meta["mpris:length"] = v(int64(now.Duration * 1e6))
	}
	if f := s.src.ArtFile(now, s.artDir); f != "" {
		sn.meta["mpris:artUrl"] = v("file://" + f)
	}
	return sn
}

func (s *Service) loop() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.refresh()
		}
	}
}

// refresh announces what changed since the last look.
func (s *Service) refresh() {
	next := s.take()
	s.mu.Lock()
	old := s.cur
	s.cur = next
	s.mu.Unlock()

	changed := map[string]dbus.Variant{}
	if next.status != old.status {
		changed["PlaybackStatus"] = v(next.status)
	}
	if !reflect.DeepEqual(next.meta, old.meta) {
		changed["Metadata"] = v(next.meta)
	}
	if next.canSeek != old.canSeek {
		changed["CanSeek"] = v(next.canSeek)
	}
	if len(changed) > 0 {
		s.conn.Emit(objPath, propsIface+".PropertiesChanged", playerIface, changed, []string{})
	}
	// A position that moved by more than time explains is a seek: tell the widget.
	if next.track == old.track && next.track != "" {
		expected := old.elapsed
		if old.status == "Playing" {
			expected += next.at.Sub(old.at).Seconds()
		}
		if math.Abs(next.elapsed-expected) > 1.5 {
			s.conn.Emit(objPath, playerIface+".Seeked", int64(next.elapsed*1e6))
		}
	}
}

// position is the current position in microseconds, extrapolated between polls.
func (s *Service) position() int64 {
	s.mu.Lock()
	c := s.cur
	s.mu.Unlock()
	e := c.elapsed
	if c.status == "Playing" {
		e += time.Since(c.at).Seconds()
	}
	return int64(e * 1e6)
}

func (s *Service) do(cmd string, arg float64) *dbus.Error {
	if err := s.src.Do(cmd, arg); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

// ---- org.mpris.MediaPlayer2 ----

type root struct{}

func (root) Raise() *dbus.Error { return nil }
func (root) Quit() *dbus.Error  { return nil }

// ---- org.mpris.MediaPlayer2.Player ----

type player struct{ s *Service }

func (p player) Next() *dbus.Error      { return p.s.do("next", 0) }
func (p player) Previous() *dbus.Error  { return p.s.do("prev", 0) }
func (p player) Pause() *dbus.Error     { return p.s.do("pause", 0) }
func (p player) Play() *dbus.Error      { return p.s.do("play", 0) }
func (p player) PlayPause() *dbus.Error { return p.s.do("toggle", 0) }
func (p player) Stop() *dbus.Error      { return p.s.do("pause", 0) }

// SeekBy is exported on the bus as "Seek" (go vet reserves that Go name for io.Seeker).
func (p player) SeekBy(offset int64) *dbus.Error {
	return p.s.do("seek", math.Max(0, float64(p.s.position()+offset)/1e6))
}
func (p player) SetPosition(_ dbus.ObjectPath, pos int64) *dbus.Error {
	return p.s.do("seek", math.Max(0, float64(pos)/1e6))
}
func (p player) OpenUri(string) *dbus.Error { return nil }

// ---- org.freedesktop.DBus.Properties ----

type props struct{ s *Service }

func (p props) all(iface string) (map[string]dbus.Variant, bool) {
	p.s.mu.Lock()
	c := p.s.cur
	p.s.mu.Unlock()
	switch iface {
	case rootIface:
		return map[string]dbus.Variant{
			"CanQuit": v(false), "CanRaise": v(false), "HasTrackList": v(false), "Identity": v("letsgo"),
			"SupportedUriSchemes": v([]string{}), "SupportedMimeTypes": v([]string{}),
		}, true
	case playerIface:
		return map[string]dbus.Variant{
			"PlaybackStatus": v(c.status), "LoopStatus": v("Playlist"), "Rate": v(1.0), "Shuffle": v(false),
			"Metadata": v(c.meta), "Volume": v(1.0), "Position": v(p.s.position()),
			"MinimumRate": v(1.0), "MaximumRate": v(1.0), "CanGoNext": v(true), "CanGoPrevious": v(true),
			"CanPlay": v(true), "CanPause": v(true), "CanSeek": v(c.canSeek), "CanControl": v(true),
		}, true
	}
	return nil, false
}

func (p props) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	m, ok := p.all(iface)
	if !ok {
		return dbus.Variant{}, dbus.MakeFailedError(errors.New("unknown interface " + iface))
	}
	val, ok := m[name]
	if !ok {
		return dbus.Variant{}, dbus.MakeFailedError(errors.New("unknown property " + name))
	}
	return val, nil
}

func (p props) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	m, ok := p.all(iface)
	if !ok {
		return nil, dbus.MakeFailedError(errors.New("unknown interface " + iface))
	}
	return m, nil
}

func (p props) Set(string, string, dbus.Variant) *dbus.Error {
	return dbus.MakeFailedError(errors.New("read-only"))
}
