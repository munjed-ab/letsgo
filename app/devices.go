package app

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"time"
)

// devProxy talks to other devices for the UI. Timeouts keep a phone that left the
// Wi-Fi from hanging the window.
var devProxy = &http.Transport{
	DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
	ResponseHeaderTimeout: 6 * time.Second,
	MaxIdleConnsPerHost:   4,
}

// devRoutes lets the desktop UI browse and control ANY device, not just this one:
//
//	GET/POST /dev/<ip>/api/...   is forwarded to that device's own /api/... (same path and body)
//
// It goes through this node so the page never makes cross-origin requests (the
// phone needs no changes). Only addresses on the local network are reachable, and
// only their API on the letsgo port, so it cannot be used to poke at anything else.
func (n *Node) devRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/dev/{ip}/api/{path...}", func(w http.ResponseWriter, r *http.Request) {
		ip, err := netip.ParseAddr(r.PathValue("ip"))
		if err != nil || !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
			http.Error(w, "not a device on this network", http.StatusForbidden)
			return
		}
		host := net.JoinHostPort(ip.String(), n.peerPortStr())
		(&httputil.ReverseProxy{
			Transport: devProxy,
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL.Scheme, pr.Out.URL.Host = "http", host
				pr.Out.URL.Path, pr.Out.URL.RawPath = "/api/"+r.PathValue("path"), ""
				pr.Out.Host = host
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				http.Error(w, "device unreachable", http.StatusBadGateway)
			},
		}).ServeHTTP(w, r)
	})
}
