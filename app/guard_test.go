package app

import (
	"net/http"
	"strings"
	"testing"
)

func TestHostAllowed(t *testing.T) {
	AllowedHosts = []string{"music.home"}
	defer func() { AllowedHosts = nil }()
	for host, want := range map[string]bool{
		"127.0.0.1:8080": true, "192.168.0.20:8080": true, "[::1]:8080": true, "localhost:8080": true,
		"pop-os:8080": true, "pop-os.local:8080": true, "music.home:8080": true, "MUSIC.HOME": true,
		"evil.example.com": false, "evil.example.com:8080": false, "127.0.0.1.evil.com": false, "": false,
	} {
		if got := hostAllowed(host); got != want {
			t.Errorf("hostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

// A web page must not be able to drive a device: cross-site requests (including the JSON-as-text/plain
// trick that skips a browser's preflight) and DNS-rebinding names are refused, while the apps,
// which send no Origin, and the device's own page keep working.
func TestGuardRefusesWebPages(t *testing.T) {
	music := t.TempDir()
	writeRamp(t, music, 1)
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
	b.setPeerPort(port(aHTTP))

	do := func(addr, method, path, body string, hdr map[string]string) int {
		req, _ := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		for k, v := range hdr {
			if k == "Host" {
				req.Host = v
			} else {
				req.Header.Set(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, c := range []struct {
		name   string
		method string
		path   string
		body   string
		hdr    map[string]string
		want   int
	}{
		{"the apps send no Origin", "POST", "/api/control?cmd=pause", "", nil, 204},
		{"the device's own page", "POST", "/api/control?cmd=pause", "", map[string]string{"Origin": "http://" + aHTTP}, 204},
		{"another site", "POST", "/api/control?cmd=next", "", map[string]string{"Origin": "https://evil.example"}, 403},
		{"another site, JSON sent as text/plain to skip the preflight", "POST", "/api/sources",
			`{"add":"/"}`, map[string]string{"Origin": "https://evil.example", "Content-Type": "text/plain"}, 403},
		{"a sandboxed or file page (Origin: null)", "POST", "/api/control?cmd=next", "", map[string]string{"Origin": "null"}, 403},
		{"DNS rebinding: the attacker's name in Host", "POST", "/api/control?cmd=next", "", map[string]string{"Host": "evil.example.com:8080"}, 403},
		{"the same, for reading", "GET", "/api/library", "", map[string]string{"Host": "evil.example.com:8080"}, 403},
		{"reading by IP", "GET", "/api/library", "", nil, 200},
	} {
		if got := do(aHTTP, c.method, c.path, c.body, c.hdr); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
	// the desktop window's own page reaches the phone through the laptop: the page's Origin must
	// not follow the request to the phone, which would refuse it
	if got := do(bHTTP, "POST", "/dev/127.0.0.1/api/volume?v=7", "", map[string]string{"Origin": "http://" + bHTTP}); got != 204 {
		t.Errorf("through the laptop's own page: %d, want 204", got)
	}
	if got := do(bHTTP, "POST", "/dev/127.0.0.1/api/volume?v=7", "", map[string]string{"Origin": "https://evil.example"}); got != 403 {
		t.Errorf("another site through the laptop: %d, want 403", got)
	}
}
