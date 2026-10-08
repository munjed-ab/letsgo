module letsgo

go 1.27.0

// The two replace lines fetch golang.org/x/sys and golang.org/x/text from their official GitHub
// mirrors at pinned versions. They date from the project's first build environment and are known
// to work. A trial build without them also compiled and passed the tests, but they have not been
// removed.
//
// golang.org/x/mobile is required below although no Go file imports it: `gomobile bind` (the
// Android build) refuses to run without it, and `go mod tidy` deletes it. After a tidy, put it back
// with: go get -tool golang.org/x/mobile/cmd/gobind
replace golang.org/x/sys => github.com/golang/sys v0.30.0

replace golang.org/x/text => github.com/golang/text v0.22.0

require (
	github.com/dhowden/tag v0.0.0-20240417053706-3d75831295e8
	github.com/ebitengine/oto/v3 v3.5.1
	github.com/godbus/dbus/v5 v5.2.2
	github.com/grandcat/zeroconf v1.0.0
	github.com/hajimehoshi/go-mp3 v0.3.4
	github.com/jfreymuth/oggvorbis v1.0.5
	github.com/mewkiz/flac v1.0.14
	github.com/thesyncim/gopus v0.2.2
)

require (
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/ebitengine/purego v0.11.0 // indirect
	github.com/icza/bitio v1.1.0 // indirect
	github.com/jfreymuth/pulse v0.1.3 // indirect
	github.com/jfreymuth/vorbis v1.0.2 // indirect
	github.com/mewkiz/pkg v0.0.0-20250417130911-3f050ff8c56d // indirect
	github.com/mewpkg/term v0.0.0-20241026122259-37a80af23985 // indirect
	github.com/miekg/dns v1.1.27 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/mobile v0.0.0-20260908204917-8b95e45f8d3e // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/tools v0.50.0 // indirect
)
