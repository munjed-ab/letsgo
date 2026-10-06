# Privacy and security

letsgo is built for a network you trust, such as your home Wi-Fi. This page says exactly what it
does with your data and your network, and, just as important, what it does **not** protect. Read
"Known weaknesses" before you run it anywhere else.

## Your data and privacy

**No accounts, no cloud, no telemetry.** letsgo has no login, no analytics, no crash reporting and no
update check. Its source contains one URL that points off your machine or network: the link to this
repository behind the GitHub badge in *Devices → About*. It is only opened when you tap or click it, in
your browser; letsgo itself never contacts it. (You can check:
`grep -rnoE "https?://[^ \"]+" --include=*.go --include=*.kt --include=*.html .` finds that link,
`localhost` and `127.0.0.1`, addresses of your own devices assembled at run time, and a made-up
`evil.example` that a test uses to check that other sites are refused). Your music is never uploaded
anywhere. Only the audio you are playing moves, from the playing device to the devices on your network.

**What is stored, and where.**

| Where | What |
|---|---|
| Desktop `~/.config/letsgo/` | `lists.json` (favourites, playlists), `plays.json` (play counts), `sources.json` (music folders), `meta.json` (cached song titles, artists, albums, cover-art hashes), `chrome/` (the browser profile of the app window: its cache, no logins) |
| Desktop `~/.cache/letsgo/` | `art/` (cover art for the media widget), `remux/` (seekable copies of fragmented mp4 videos, see how-it-works; delete any time), `letsgo.log` |
| Phone | the same files in the app's private storage, which other apps cannot read |

These files describe your library and listening habits. They stay on the device. Nothing reads them
except the app itself, and the network API described below (which serves your library's file names,
tags and cover art to anyone who can reach the device).

**The log.** `~/.cache/letsgo/letsgo.log` records source changes, reconnects and network stalls, and it
includes the **file names** of the songs played and the **IP addresses** of your other devices. It is
kept to about 2 MB (the previous one is `letsgo.log.old`). Delete it any time. Check it before you
attach it to a bug report.

**What other devices can see.** A device that runs letsgo announces itself on the local network
(mDNS) under its name: the phone's model (for example `Infinix X6731B`) or the computer's hostname.
Change the desktop name with `-name`. Anyone on the same network can see that name and that the
device is running letsgo.

If multicast does not get through (a phone's hotspot often drops it), a device also looks for others by
address: while it knows of no other device it sends `GET /api/state` on the letsgo port (8080) to every
other address of its own /24 subnet, once every 12 s, and it asks the devices it has seen before the same
way when a scan misses them. That is all it sends, and only inside your own network. `-no-discovery`
switches it off together with mDNS.

**Android backups.** The app allows Android backup (`allowBackup`), so its private files
(favourites, playlists, play counts) can be included in a device backup. If you do not want that,
turn the manifest attribute off before you build.

## What letsgo listens on

| Port | Protocol | What it is for | Bound to |
|---|---|---|---|
| TCP 1704 | Snapcast stream | the audio stream to listeners | all interfaces |
| TCP 8080 | HTTP | the web UI and the control API | all interfaces (`-http` changes it) |
| UDP 5353 | mDNS | announcing this device and finding others | multicast on the local network |

Other devices reach a node on these ports, which is how listening, control and Shift work. If you
run a firewall, allow them from your local network only.

## Android permissions

| Permission | Why |
|---|---|
| `INTERNET`, `ACCESS_NETWORK_STATE`, `ACCESS_WIFI_STATE` | the stream and the API travel over the network; the app picks the Wi-Fi interface |
| `CHANGE_WIFI_MULTICAST_STATE` | needed to hear mDNS announcements |
| `WAKE_LOCK`, `FOREGROUND_SERVICE`, `FOREGROUND_SERVICE_MEDIA_PLAYBACK` | keep playing and stay in sync with the screen off (a media notification is shown) |
| `POST_NOTIFICATIONS` | that media notification, on Android 13 and later |
| `MODIFY_AUDIO_SETTINGS` | audio focus, so other apps pause while you play |
| `READ_MEDIA_AUDIO`, `READ_EXTERNAL_STORAGE` (Android 12 and older) | read your music |
| `MANAGE_EXTERNAL_STORAGE` ("All files access") | the music server is Go code that opens files **by path**, so it cannot use Android's media APIs; this lets it read folders you add (SD card, Downloads). It reads only the folders you have chosen |

The app allows plain HTTP (`usesCleartextTraffic`) because its own web UI and the other devices are
plain HTTP on your LAN.

## Known weaknesses

**There is no authentication and no encryption.** Anyone who can reach port 8080 or 1704 on a device
can:

- control it: play, pause, skip, change the volume and the sync offset, make it follow them (Shift)
  or listen to another device;
- read your library's file names, tags and cover art (`/api/library`, `/api/meta`, `/api/art`), and
  **download the video files in it** (`/api/video`, the file itself, not just its sound);
- **add any folder on that device as a music folder** (`POST /api/sources`). The node then indexes and
  serves every audio and video file under it, so a stranger on the network could list and stream audio
  files, and download video files, from anywhere the app can read;
- join the audio stream on port 1704 and record what is playing. The stream is not encrypted.

They cannot read or write arbitrary files: playing, and `/api/video`, only work for tracks in the
library, and `/api/video` only hands out video files. This is
by design for a home network: there is no pairing step to get wrong. It is **not safe on a public or
shared Wi-Fi** (café, hotel, campus, guest network). Do not run letsgo there, or block ports 1704 and
8080 with a firewall.

**Web pages are refused, other programs are not.** A browser lets any web page send requests to
`http://localhost:8080` or to a device's address, and to point its own domain name at a device (DNS
rebinding). letsgo refuses both: every request whose `Origin` is not the device itself is rejected
(this includes the trick of sending JSON as `text/plain` to skip the browser's preflight), and so is
any request whose `Host` is not an IP address, `localhost`, a single-word or `*.local` name. A site you
visit therefore cannot press buttons or add folders on your devices. If you reach a device by a
name of your own (`music.home`), list it in the `LETSGO_ALLOWED_HOSTS` environment variable
(comma separated). This protects against web pages only. A program on the network that is not a
browser sends no `Origin` and is not affected: **there is still no login.**

**The desktop window relays to other devices.** So the window can browse the phone, the laptop's
node forwards `/dev/<ip>/api/...` to another device's API. It only forwards to addresses on your
local network (private, loopback and link-local ranges) and only to letsgo's port, so it cannot be
used to reach the internet or other services. But it means anyone who can reach the laptop's port 8080
can also reach the phone's API through it.

**Shutting down.** `POST /api/quit` (used so that a newer desktop build can replace an older one)
only works from the same machine and only on the desktop app.

## Running it more privately

- Use it on your own network only, and keep the router's client isolation off for the devices that
  should hear each other.
- `-no-discovery` stops the device advertising and looking for others; then give addresses by hand
  (`-connect`, or *Devices → Listen to*).
- `-http 127.0.0.1:8080` limits the web UI and API to the machine itself. That also stops other
  devices controlling it and stops Shift from reaching it, but streaming to it still works.
- A firewall that allows TCP 1704 and 8080 and UDP 5353 only from your LAN subnet is the simplest hardening.
- Stock Snapcast clients can connect to a letsgo device; they have the same lack of authentication.

## Supply chain

- Go dependencies are listed in `go.mod` with hashes in `go.sum`; `THIRD_PARTY.md` lists every
  library that ends up in the binaries with its licence. There is no vendored code.
- The release APK is signed with the **Android debug key**, so it installs and upgrades in place. That
  is a convenience, not a security statement: anyone can sign an APK with that key. If you
  distribute the app, build it and sign it with your own key.
- Builds are not byte-for-byte reproducible.

## Reporting a problem

See `SECURITY.md`.
