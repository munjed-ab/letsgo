package app

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"letsgo/snap"
)

var videoMime = map[string]string{".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".mkv": "video/x-matroska", ".webm": "video/webm"}

// mediaRoutes wires seek, "what is playing" and media control, tags and cover
// art, and choosing which device to listen to.
//
//	GET  /api/now[?local=1]                     what is playing (local=1: this device only)
//	POST /api/control?cmd=play|pause|toggle|next|prev|seek[&t=SEC][&local=1]
//	POST /api/seek?t=SEC                        seek this device's own player
//	GET  /api/meta                              {done,total,tracks:{id:{t,a,al,art}}}
//	GET  /api/art/{hash}[?s=PIXELS]             cover art, scaled to fit
//	GET  /api/video?t=TRACK                     the file of a video track, with Range (screens show it muted, in step with the sound)
//	GET  /api/peers                             devices found on the network, and whether they cast
//	POST /api/listen {addr}                     listen to that device ("" = automatic)
func (n *Node) mediaRoutes(mux *http.ServeMux) {
	num := func(r *http.Request, k string) float64 {
		f, _ := strconv.ParseFloat(r.URL.Query().Get(k), 64)
		return f
	}
	mux.HandleFunc("/api/now", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("local") == "1" {
			json.NewEncoder(w).Encode(n.localNow())
			return
		}
		json.NewEncoder(w).Encode(n.Now())
	})
	mux.HandleFunc("/api/control", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var err error
		if r.URL.Query().Get("local") == "1" {
			err = n.doLocal(r.URL.Query().Get("cmd"), num(r, "t"))
		} else {
			err = n.Do(r.URL.Query().Get("cmd"), num(r, "t"))
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/seek", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		n.p.Seek(num(r, "t"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/meta", func(w http.ResponseWriter, r *http.Request) {
		tracks, done, total := n.meta.All()
		json.NewEncoder(w).Encode(map[string]any{"done": done, "total": total, "tracks": tracks})
	})
	mux.HandleFunc("/api/art/{hash}", func(w http.ResponseWriter, r *http.Request) {
		data, mime, ok := n.meta.Art(r.PathValue("hash"), int(num(r, "s")))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "public, max-age=86400") // a hash never changes its picture
		w.Write(data)
	})
	mux.HandleFunc("/api/video", func(w http.ResponseWriter, r *http.Request) {
		path, ok := n.p.VideoFile(r.URL.Query().Get("t"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		if mime, ok := videoMime[strings.ToLower(filepath.Ext(path))]; ok { // Go's own table has none of these
			w.Header().Set("Content-Type", mime)
		}
		http.ServeFile(w, r, seekableVideo(path)) // answers Range requests, which is how a screen seeks
	})
	mux.HandleFunc("/api/peers", func(w http.ResponseWriter, r *http.Request) {
		type peer struct {
			snap.Peer
			Casting bool `json:"casting"`
		}
		found := n.Peers(1500 * time.Millisecond)
		out := make([]peer, len(found))
		done := make(chan struct{}, len(found))
		for i, p := range found {
			go func() {
				out[i] = peer{p, n.peerCasting(p.IP)}
				done <- struct{}{}
			}()
		}
		for range found {
			<-done
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/listen", func(w http.ResponseWriter, r *http.Request) {
		var q struct{ Addr string }
		if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&q) != nil {
			http.Error(w, "POST {\"addr\":...}", http.StatusBadRequest)
			return
		}
		n.Pin(q.Addr)
		w.WriteHeader(http.StatusNoContent)
	})
}
