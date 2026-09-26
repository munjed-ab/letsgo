// Package mpris exposes a letsgo node on the desktop's D-Bus session bus as a
// media player (the MPRIS standard), so keyboard media keys, the desktop's media
// widget and lock screen, and tools like playerctl can show and control what is
// playing. Linux only; elsewhere Start reports that it is unsupported.
package mpris

import "letsgo/app"

// Source is what MPRIS needs from a node (satisfied by *app.Node).
type Source interface {
	Now() app.Now
	Do(cmd string, arg float64) error
	// ArtFile makes now's cover art available as a file under dir and returns its path ("" = none).
	ArtFile(now app.Now, dir string) string
}
