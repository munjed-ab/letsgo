# Third-party software

letsgo itself is under the MIT licence (`LICENSE`). It is built from the open-source libraries below, all
under permissive licences that allow this use. Each library's licence text is in its own repository; if
you redistribute a build, keep those notices with it.

The list was made from what the Go toolchain says is linked into the binaries:

```sh
go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}{{end}}{{end}}' . ./cmd/... ./mobile | sort -u
```

and by reading each module's `LICENSE` file. Re-run it when you change `go.mod`.

## Go libraries (desktop, headless server and the Android node)

| Library | Version | Licence | Used for |
|---|---|---|---|
| [ebitengine/oto](https://github.com/ebitengine/oto) | v3.5.1 | Apache-2.0 | audio output on the desktop |
| [ebitengine/purego](https://github.com/ebitengine/purego) | v0.11.0 | Apache-2.0 | calling system audio libraries without cgo |
| [hajimehoshi/go-mp3](https://github.com/hajimehoshi/go-mp3) | v0.3.4 | Apache-2.0 | mp3 decoding |
| [mewkiz/flac](https://github.com/mewkiz/flac) | v1.0.14 | Unlicense | flac decoding |
| [jfreymuth/oggvorbis](https://github.com/jfreymuth/oggvorbis), [vorbis](https://github.com/jfreymuth/vorbis) | v1.0.5, v1.0.2 | MIT | ogg vorbis decoding |
| [dhowden/tag](https://github.com/dhowden/tag) | 2024-04-17 | BSD-2-Clause | song tags and cover art |
| [godbus/dbus](https://github.com/godbus/dbus) | v5.2.2 | BSD-2-Clause | Linux media keys and widget (MPRIS) |
| [grandcat/zeroconf](https://github.com/grandcat/zeroconf) | v1.0.0 | MIT | finding other devices (mDNS) |
| [miekg/dns](https://github.com/miekg/dns) | v1.1.27 | BSD-3-Clause | DNS messages for mDNS |
| [cenkalti/backoff](https://github.com/cenkalti/backoff) | v2.2.1 | MIT | retries inside zeroconf |
| icza/bitio, jfreymuth/pulse, mewkiz/pkg, mewpkg/term | see `go.mod` | Apache-2.0, MIT, Unlicense, Unlicense | helpers of the libraries above |
| [golang.org/x/net](https://pkg.go.dev/golang.org/x/net), [x/crypto](https://pkg.go.dev/golang.org/x/crypto), [x/sys](https://pkg.go.dev/golang.org/x/sys) | see `go.mod` | BSD-3-Clause | networking and system calls |
| the Go standard library and runtime | Go 1.26 or newer | BSD-3-Clause | everything else |

`golang.org/x/mobile` (BSD-3-Clause) is used at build time by `gomobile bind` to package the Go node for
Android.

## Android app (Kotlin)

All of these are Apache-2.0 (Google, JetBrains):

- AndroidX: core-ktx 1.13.1, appcompat 1.7.0, activity-compose 1.9.0, lifecycle-runtime-ktx 2.8.2
- Jetpack Compose (BOM 2024.06.00): ui, foundation, material3, material-icons-extended
- Kotlin standard library (Kotlin 1.9.24)

The release build shrinks these to what the app uses.

## Artwork

- The GitHub mark in *Devices → About* is the `mark-github` icon of [Octicons](https://github.com/primer/octicons)
  (MIT, GitHub Inc.), drawn as a vector in the app and the web page. It is a link to this repository; use of
  the mark follows [GitHub's logo guidelines](https://github.com/logos).

## Programs used but not shipped

- **ffmpeg**, on laptops: if it is installed, letsgo runs it as a separate program to decode the sound
  of video files. It is not bundled, linked or downloaded by letsgo, and nothing changes without it
  except that video files are not listed.
- **Android's media codecs** (MediaExtractor, MediaCodec, MediaPlayer), part of the operating system: the
  phone uses them for the sound and the picture of videos.

## Not from a third party

- **Snapcast.** letsgo speaks the Snapcast wire protocol so that stock Snapcast clients can connect. It is
  an independent implementation written from the protocol, and is not affiliated with the Snapcast
  project.
- **Test audio** in `player/testdata` and `meta/testdata` is synthetic (tones and sweeps made with ffmpeg;
  `sweep44100.mp4` is the same sweep with a plain blue picture).
- **The logo and launcher icons** (`app/logo.svg`, `app/logo.png`, `android-app/app/src/main/res`) were
  made for this project. No third-party artwork or fonts are used.
