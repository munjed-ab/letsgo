package app

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// AllowedHosts are extra host names (comma separated, from LETSGO_ALLOWED_HOSTS) a browser may
// use to reach this device, for example a name you gave it in your router: "music.home,nas.lan".
var AllowedHosts = strings.Split(os.Getenv("LETSGO_ALLOWED_HOSTS"), ",")

// guard refuses requests a web page could make on someone's behalf, which is the one attack a
// browser makes possible against a device with no login:
//
//   - cross-site requests: a page on another site posting to http://<this device>:8080. A browser
//     always sends an Origin header on those, so any Origin that is not this device itself is refused;
//   - DNS rebinding: a page that points its own name at this device's address. The Host header then
//     carries that name, so only IP addresses, "localhost", single-word and *.local names (and
//     AllowedHosts) are accepted.
//
// The apps and other devices are not browsers: they send no Origin and use an IP address, so they
// pass. This does nothing against someone who is not using a browser; there is still no login.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) || !sameOrigin(r) {
			http.Error(w, "forbidden: not a request from this device's own page", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	switch {
	case host == "":
		return false
	case net.ParseIP(host) != nil, host == "localhost", !strings.Contains(host, "."), strings.HasSuffix(host, ".local"):
		return true
	}
	for _, h := range AllowedHosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" && h == host {
			return true
		}
	}
	return false
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // not a browser making a cross-site request
	}
	u, err := url.Parse(o)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}
