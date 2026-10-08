// letsgo: one tiny binary that turns a machine into a synced multi-room music
// server. Control from any browser at the web UI; listeners use any Snapcast
// client (or another letsgo node).
//
//	music files --> player (decode + timestamp) --> snap server :1704 --> clients
//	                     ^
//	      web UI / HTTP API :8080 (control from any browser)
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"

	"letsgo/app"
)

func main() {
	var music dirFlags
	flag.Var(&music, "music", "music folder (repeat for several; default /sdcard/Music)")
	data := flag.String("data", defaultDataDir(), "where favourites and playlists are saved")
	snapAddr := flag.String("snap", ":1704", "snapcast stream listen address")
	httpAddr := flag.String("http", ":8080", "web UI / API listen address")
	name := flag.String("name", "letsgo", "name shown to other devices (mDNS)")
	buffer := flag.Int("buffer", 4000, "sync buffer ms: a Wi-Fi stall shorter than this is not heard")
	noDisc := flag.Bool("no-discovery", false, "do not advertise or look for other devices (use -connect / stock Snapcast clients)")
	flag.Parse()
	if *noDisc {
		app.Discovery = false
	}

	n, err := app.Start(music.or("/sdcard/Music"), *data, *snapAddr, *httpAddr, *name, *buffer)
	if err != nil {
		log.Fatal(err)
	}
	defer n.Stop()
	log.Printf("stream on %s, web UI on %s, discoverable as %q", *snapAddr, *httpAddr, *name)
	select {} // serve forever
}

func defaultDataDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return "" // nowhere to save: lists live in memory
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
