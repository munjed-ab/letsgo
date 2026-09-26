// Command play connects to a letsgo server and plays the synced stream through
// the local audio device, printing sync stats. Handy when mDNS discovery is
// blocked or to test one listener at a time.
//
//	play -h <server-ip>[:1704] [-latency ms]
package main

import (
	"flag"
	"log"
	"net"
	"os"
	"time"

	"letsgo/snap"
	"letsgo/speaker"
)

func main() {
	host := flag.String("h", "127.0.0.1", "server host or host:port")
	latency := flag.Int("latency", 0, "extra delay ms for this device (+ plays later)")
	flag.Parse()

	addr := *host
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr += ":1704"
	}
	name, _ := os.Hostname()
	c, err := snap.Connect(addr, name+" (play)", *latency)
	if err != nil {
		log.Fatal(err)
	}
	select {
	case <-c.Ready:
	case <-time.After(10 * time.Second):
		log.Fatal("no codec header from ", addr)
	}
	if err := speaker.Play(c, c.SetOutputLatency); err != nil {
		log.Fatal(err)
	}
	log.Printf("playing %s (%d Hz, %d-bit, %d ch)", addr, c.Rate, c.Bits, c.Channels)
	for c.Live() {
		time.Sleep(2 * time.Second)
		s := c.Stats()
		log.Printf("buffer=%dms syncErr=%.1fms rtt=%.1fms underruns=%d resyncs=%d",
			s.BufferMs, s.SyncErrMs, s.RTTMs, s.Underruns, s.Resyncs)
	}
	log.Print("server went away")
}
