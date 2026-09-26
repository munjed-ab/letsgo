# Troubleshooting

Where to look first: the laptop app writes `~/.cache/letsgo/letsgo.log` (source changes, reconnects,
network stalls, `shift:` lines). On Android, `adb logcat | grep GoLog` shows the same lines. Both
include song file names and IP addresses; look before you share them.

## Devices do not find each other

1. Are they on the **same Wi-Fi network** (same router, same subnet)? Check both devices' addresses:
   they normally start with the same three numbers, like `192.168.0.x`.
2. Some routers isolate clients ("AP isolation", "client isolation") and most **guest networks** do.
   Turn it off, or use a normal network.
3. A **VPN** on either device can send local traffic the wrong way. Pause it.
4. A **firewall** must allow TCP 1704 and 8080 and UDP 5353 from the local network. For example with
   `ufw`: `sudo ufw allow from 192.168.0.0/24 to any port 1704,8080 proto tcp` and
   `sudo ufw allow from 192.168.0.0/24 to any port 5353 proto udp` (use your own subnet).
5. Automatic discovery uses mDNS, which some networks block, and a computer with both Ethernet and
   Wi-Fi may announce on the wrong one. **Give the address by hand:** on the phone *Devices → Listen to
   → Enter an address…*, on the laptop `./dist/desktop -connect <phone-ip>`, or in the desktop window
   *Music from → Add by address…*.

Then look at *Devices*: a device that is found but shows "Not reachable" can be seen but not
contacted, which points at a firewall or isolation.

## "403 forbidden" in a browser

letsgo refuses requests that look like a web page acting for someone else (see
[privacy-and-security.md](privacy-and-security.md)). A device reached by an IP address, `localhost`, a
single-word name or a `*.local` name is fine. If you use another name for it (a name from your
router such as `music.home`), start it with `LETSGO_ALLOWED_HOSTS=music.home`.

## No sound, or crackle, on the laptop

- The laptop plays through PulseAudio or PipeWire (with an ALSA fallback). Check that the
  right output device is selected in your system sound settings, and that something else is not
  holding it exclusively.
- The log says `source: ... -> ...` when the app connects to a device. No such line means nothing is
  playing yet: start a song on any device.
- Crackle or a stream that runs slow means the audio callback is starved. Note the time and look for
  `audio gap` or `player fell behind` lines in the log.

## Stutters or drops out

- Wi-Fi is the usual cause, especially the 2.4 GHz band in a crowded place. Move closer or use 5 GHz.
- Raise the sync buffer on the device that is **playing**: `-buffer 1500` (laptop app or headless
  server). The buffer belongs to the source; the phone app uses 1000 ms.
- A phone in battery saver can pause Wi-Fi. See the next section.

## The phone stops when the screen turns off

The app is a foreground service with a media notification, and it holds Wi-Fi, multicast and CPU
wake locks. Some Android versions still stop apps to save battery (Infinix, Xiaomi, Samsung, Oppo and
others are known for it).

- Set the app's battery use to **Unrestricted** (or "Don't optimise").
- Allow **notifications** for the app (Android 13 and later); without the notification the service can
  be stopped.
- Do not swipe the notification away while playing.

## Songs are missing on the phone

- Allow **All files access** when the app asks: the music server reads folders by path.
- The default folder is the phone's `Music` folder. Add others (SD card, Downloads) in *Devices → Music
  folders*, then *Rescan*.
- Only mp3, flac, ogg and wav are played.

## Two devices are a little apart

Bluetooth speakers add 100 to 250 ms. Use *Devices → Sync offset* on the slow device: a positive number
plays that device later, a negative one earlier. Wired speakers rarely need it. The laptop's speaker
delay is an estimate (see [how-it-works.md](how-it-works.md)), so a few tens of milliseconds of offset
against the phone is normal to tune by ear.

## Shift is slow, or a device keeps its own song

Shift, the fast play/next/seek and the instant join need the **same build of letsgo on both
devices**. An older device still works with a newer one, but falls back to the old, slower behaviour.
After you rebuild, install the new APK on the phone and start the new desktop app. Look in the log for
`shift: now playing here, asking [...]` on the device you pressed play on and `shift: <ip> started
playing, following it` on the other.

## The desktop window looks old after an update

Closing the window does not quit the app (music keeps playing). Starting a new build normally
replaces the copy that is running, and you get the new version. Builds from before that was added cannot
be replaced: the log says `an older letsgo is still running and cannot be replaced`. Quit that one once
with `pkill -x desktop` and start again. No Chrome-family browser? The window opens in your
default browser; `-no-window` starts the app without opening anything.

## Building from source

| Tool | Version used | Notes |
|---|---|---|
| Go | 1.26 or newer (built with 1.27) | the `go` line in `go.mod` |
| gomobile | from `go install golang.org/x/mobile/cmd/gomobile@latest`, then `gomobile init` | Android build only |
| JDK | 17 | Android build only |
| Android SDK | platform 34, build-tools 34, NDK 27.1.12297006 | set `ANDROID_HOME` and `ANDROID_NDK_HOME` |
| Gradle | 8.9 | not bundled: put it on `PATH` or set `GRADLE` |
| Node and Chrome | any recent | only for the web UI test |

- `go: missing go.sum entry` or a module error: run `go mod download`. Do **not** run `go mod tidy`
  without reading the comment at the top of `go.mod` (it removes something the Android build needs).
- `gomobile: missing golang.org/x/mobile dependency`: `go get -tool golang.org/x/mobile/cmd/gobind`.
- Gradle cannot find `letsgo.aar`: it is built by `./build.sh` (from `./mobile`), not stored in git. Run
  the script rather than Gradle alone.
- `SDK location not found`: export `ANDROID_HOME`; `android-app/local.properties` is machine-specific
  and not in the repository.
- The Android build only produces an **arm64** APK (64-bit ARM phones, which is nearly every phone
  since 2017). 32-bit and x86 devices are not supported.
