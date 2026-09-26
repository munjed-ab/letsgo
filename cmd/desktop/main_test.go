package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalURL(t *testing.T) {
	for in, want := range map[string]string{
		":8080":           "http://localhost:8080",
		"0.0.0.0:8080":    "http://localhost:8080",
		"127.0.0.1:18080": "http://127.0.0.1:18080",
	} {
		if got := localURL(in); got != want {
			t.Errorf("localURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// What a newly started copy does about one that is already running.
func TestStartFresh(t *testing.T) {
	// a running letsgo of some build; quit tells whether it can be told to exit
	serve := func(version string, canQuit bool) (addr string, quitCalled *bool, stop func()) {
		called := false
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/api/state":
				fmt.Fprintf(w, `{"version":%q}`, version)
			case r.URL.Path == "/api/quit" && canQuit:
				called = true
				w.WriteHeader(http.StatusNoContent)
				go srv.Close() // it exits
			default:
				fmt.Fprint(w, "<html>the web page: an old build answers every path with it</html>")
			}
		}))
		return srv.Listener.Addr().String(), &called, srv.Close
	}

	if !startFresh("127.0.0.1:1", "v2") {
		t.Error("nothing running: must start")
	}
	addr, quit, stop := serve("v2", true)
	if startFresh(addr, "v2") || *quit {
		t.Error("the same build is running: show its window, do not touch it")
	}
	stop()
	addr, quit, stop = serve("v1", true)
	defer stop()
	if !startFresh(addr, "v2") || !*quit {
		t.Error("an older build is running: it must be quit and replaced")
	}
	addr, quit, stop2 := serve("", false) // a build from before versions existed
	defer stop2()
	if startFresh(addr, "v2") || *quit {
		t.Error("an old build that cannot quit: show its window, do not start a second copy")
	}
	other := httptest.NewServer(http.NotFoundHandler()) // some other web server on the port
	defer other.Close()
	if !startFresh(other.Listener.Addr().String(), "v2") {
		t.Error("something that is not letsgo is on the port: starting is what reports the clash")
	}
}
