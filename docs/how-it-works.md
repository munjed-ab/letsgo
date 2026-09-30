# How letsgo works

This is the technical description: architecture, the wire protocol (including what letsgo adds to
it), the sync algorithm, how devices choose who to listen to, and every timing constant. The code
is the source of truth; file names are given so you can check any claim here.

## Every device is the same program

A *node* is one running letsgo. It is three things at once:

```
 music files ─► player ─► snap server :1704 ─► listeners (other nodes, or stock Snapcast clients)
                 (decode,      │
                  20 ms chunks)│ loopback: a device that is playing hears itself the same way
                               ▼
 speakers ◄── client (jitter buffer, clock sync, playout)   ◄── whichever server the supervisor picked
 phone / laptop / browser ─► HTTP API + web UI :8080 ─► player, lists, supervisor
```

- **player** (`player/`) decodes mp3, flac, ogg and wav (and the sound of videos, see below), resamples
  to one stream format (44.1 kHz, 16-bit, stereo) and cuts it into 20 ms chunks, each stamped with the
  server clock.
- **snap server** (`snap/server.go`) sends every chunk to every connected listener. A slow
  listener only loses its own chunks; it can never hold up the others.
- **client** (`snap/client.go`) keeps a jitter buffer, keeps its clock in step with the server's and
  hands the audio device exactly what is due to be heard now.
- **supervisor** (`app/app.go`) decides which server the client listens to (see below).
- **HTTP API** (`app/`) is what the apps, the web UI and other devices use to control a node.

Nothing needs a central server. A device that is only listening holds no music.

## The stream

The wire format is the [Snapcast](https://github.com/badaix/snapcast) binary protocol, version 2
(`snap/proto.go`), so stock Snapcast clients such as `snapclient` and Snapdroid can connect to any
letsgo device. letsgo is an independent implementation and is not affiliated with the Snapcast
project.

Every message is a 26-byte header (`type, id, refersTo, sent, received, size`, little-endian) plus a
payload. The messages used are `Hello`, `ServerSettings` (buffer, volume, mute), `CodecHeader`
(`pcm` plus a WAV header), `WireChunk` (audio) and `Time` (clock sync). The audio is raw PCM,
about 1.4 Mbit/s per listener; there is no compression.

## Keeping devices in step

**One clock.** The server stamps every chunk with its own monotonic clock (`snap/clock.go`; wall
clock jumps cannot affect it). Each listener estimates its offset to that clock with NTP-style
pings: a burst of 8 pings 100 ms apart on connect, then one per second. From the last 16 samples it
takes the 4 with the lowest round-trip time and uses their median offset.

**One rule for playout.** A chunk stamped `ts` must be *heard* at `ts + buffer + latency`, where
`buffer` is the server's sync buffer (default 1000 ms) and `latency` is the per-device sync offset
you can set. Audio handed to the device is heard `outLat` later, so the client hands over each chunk
`outLat` early. `outLat` is what makes a phone and a laptop agree:

- Android reports it from `AudioTrack.getTimestamp` (about 300 ms on the phone this was built on),
  falling back to queued frames plus a guess.
- The desktop uses oto's queue plus a calibrated constant (`speaker/speaker.go`). The constant is a
  measured guess; that is what *Sync offset* is for.

**Correcting drift.** The client compares where it is with where it should be. Under 3 ms it does
nothing; from 3 ms it plays 0.2 % faster or slower (inaudible) until the smoothed error is back under
1 ms; beyond 100 ms it skips or pads silence. The error is smoothed with a 4 s time constant because
audio callbacks are jittery.

**Buffers.** The jitter buffer holds up to 5 s. The server queues up to 256 chunks (about 5 s) per
listener and drops that listener's chunks, never anyone else's, if it falls further behind. A gap
of up to 2 s in the timestamps is filled with silence, an overlap is trimmed, anything bigger
restarts the timeline.

## What letsgo adds to the protocol

These are compatible with stock clients (they ignore them) and are only used between letsgo devices.

**Fast commands (epochs).** With a plain Snapcast buffer, pressing next is heard one buffer
(1 s) later, because every listener already holds a second of audio. letsgo instead:

1. Starts a fresh timeline on play, resume, next, previous and seek: the first chunk is stamped
   `startLead` (350 ms) from now instead of a whole buffer, and the audio the buffer would have
   covered is sent at once as a burst. Listeners schedule by the stamps, so they still start
   together.
2. Bumps an *epoch* counter (`Server.Flush`). Every chunk carries it in the header's `refersTo`
   field, which stock clients ignore for chunks. A letsgo client that sees it change throws away
   what it had queued.
3. On pause, sends one empty chunk with a new epoch, which silences every listener at once.

A phone cannot recall the last ~300 ms already in its `AudioTrack`, so it stops that much later than
a laptop. Against a stock client, or a letsgo build from before this, the burst chunks look like an
overlap and are dropped until they catch up: it behaves as it always did, one buffer late.

**Backlog on join.** The server keeps the chunks that are still due to be heard (the last `buffer`)
and sends them to a device that connects mid-song, so it is audible immediately and in step
instead of one buffer after it connects. A stale stream (nothing sent for a buffer) is not
replayed.

## Who listens to whom

Every node runs a supervisor (`supervise` in `app/app.go`), every 400 ms and at once when something
starts playing here:

1. If **this** device is playing, listen to our own server over loopback. The player hears itself
   through the same path as everyone else, so it is in step with them.
2. Else if a peer is set (*Devices → Listen to*, or `-connect`), listen to it.
3. Else if a peer is **casting** (it answers `GET /api/now?local=1` with `playing: true`), listen to
   it. The peer we are already attached to keeps priority while it casts.
4. Else stay attached to what we have. Silence is free, and the next play, here or there, is heard
   at once.

Peers come from mDNS (`_snapcast._tcp`, UDP 5353), scanned every 4 s in the background and kept for
15 s, so one missed scan cannot cut the music. Probes to peers run in parallel with a 500 ms
timeout.

### Shift

When a device that was listening (or idle) starts playing, it becomes the source and the rest must
follow. The supervisor notices this rising edge and calls `shift` (`app/shift.go`):

- It sends `POST /api/shift?port=<snap port>` to every device it knows: those mDNS found, the one it
  was listening to, and a pinned one. The last two do not depend on discovery.
- A receiver takes the caller's address from the connection, records it as a peer (keeping the
  name mDNS gave it), pauses if it was playing, and wakes its supervisor, which connects to the
  caller.
- An older node has no `/api/shift` and answers with its web page, so the sender falls back to
  `POST /api/control?cmd=pause&local=1`.

Measured on two nodes on one machine, the follower is audible about 330 ms after the press.

## The player

- **Queue.** Playing a list makes it the queue. `tracks` that are not in the library are dropped, so
  a caller cannot make the node open arbitrary files.
- **Shuffle.** The queue is dealt like a deck (`dealLocked`): every track once per round. A new round
  holds the last few tracks played (the smaller of a third of the queue and 8) back to the end, so a
  track never returns right after the round that ended. *Previous* walks back through what was played.
- **Position.** The timeline shows what is being *heard* (chunk timestamp plus buffer), not what is
  being produced.
- **Most Played.** A track counts as played after 30 s of it has been produced (or half of it if it
  is shorter than a minute). Seeking does not add to it; playing it again does.

## Videos

A video is a song with a picture: the same queue, the same stream, the same controls. Only the sound
goes through the player and out to every device; the picture never enters the stream.

- **Which files.** `mp4`, `m4v`, `mov`, `mkv` and `webm` (`player.VideoExts`), by extension. They are
  in the library only when this device can decode their sound (`player.VideoSupported`), so a track that
  could not play is never listed. A file with no audio track fails to open and is skipped like any broken file.
- **The sound.** The pure-Go decoders cannot read AAC in an MP4, so the platform does it. A laptop runs
  one `ffmpeg` per play position (`player/video.go`): it writes raw 44.1 kHz stereo to a pipe, and a
  seek starts a new process at the new position. The phone registers `VideoAudio.kt` (MediaExtractor
  and MediaCodec) through `mobile.SetVideoDecoder` before the node starts; it decodes one block ahead
  when a file is opened, because HE-AAC only reveals its true sample rate once it has produced sound,
  and folds 5.1 down to stereo. Either way the player sees an ordinary source.
- **The picture.** `GET /api/video?t=<track>` serves the file itself, with `Range` support, but only
  for library tracks that are videos. A screen that shows the picture takes it from the device that is
  playing (its own node, or the source it hears; the web page goes through `/dev/<ip>/`) and plays it
  muted. It never opens by itself.
- **Keeping the picture in step.** The node reports the position being *heard* (`Now.Elapsed`); for a
  device that hears another one that answer can be up to a second old, so the node moves it on by its
  age before handing it out. The screen aims at that position minus its own sync offset (its speakers
  are that late), and: the web page seeks when it is 0.4 s off (0.05 s while paused) and otherwise
  nudges the playback rate by up to 10 % to close the gap in about two seconds, checked once a second;
  the phone seeks when it is 0.35 s off (0.1 s while paused), checked every 300 ms. Measured in Chrome
  against a running node the picture stayed within 30 ms of the reported position, through a seek and
  a pause.
- **When a jump cannot be decoded.** Some files cannot be started from certain places (a keyframe the
  decoder cannot begin at: in one test file, Chrome fails on every seek between 62.7 s and 65.3 s), and
  the player then stops with an error. The screen does not treat that as "cannot show this video": it
  opens the file again 3 s before the place that failed (6 s, then 9 s if it fails again), which plays
  through, and catches up by playing up to 4x faster without jumping. Only a format it cannot decode at
  all, or three failures in a row, shows the message. The phone takes the file's length from the node,
  because asking `MediaPlayer` for it fails on some files and that failure is reported as an error.
- **Pausing what you hear.** A device that hears another one keeps showing that device's track while
  it is paused, once it has shown it, so pause (or stop) from its controls leaves the video and the
  play button that resumes it in place. It goes back to its own last song only when it plays one.
- **Full screen.** On the web page it is the browser's Fullscreen API on the video box alone (the keys
  and a click on the picture still drive playback); only closing the video view or leaving full screen
  yourself ends it. On the phone it is an overlay (`VideoFullScreen` in `VideoPane.kt`) that hides the
  system bars and asks for landscape when the video is wider than tall, portrait otherwise, then restores
  both. A tap shows or hides the controls over the picture (title, exit, timeline, previous, play/pause,
  next); they hide after 3.5 s while playing. A song in the queue does not end full screen: the box shows
  its cover, and the next video fills it again. The phone's picture is a new player on the same URL, so it
  takes a moment to come back after entering or leaving.

## Files a node writes

| File (desktop: `~/.config/letsgo/`, phone: the app's private storage) | Content |
|---|---|
| `lists.json` | favourites, playlists, playlist folders |
| `plays.json` | play counts and last-played time per track |
| `sources.json` | music folders you chose (until you change them, the defaults apply) |
| `meta.json` | cached tags and cover-art hashes per track |

The desktop also writes `~/.cache/letsgo/art/` (cover art for the media widget),
`~/.cache/letsgo/letsgo.log`, and `~/.config/letsgo/chrome/` (the browser profile of its app window: cache
and settings of that window only, nothing of yours; safe to delete). Each file is written atomically (temporary file, then rename). If
`lists.json` cannot be parsed it is kept as `lists.json.bad` instead of being overwritten, so nothing
you made is lost silently. The other files simply start again (`meta.json` is only a cache;
`plays.json` would restart from zero, `sources.json` from the defaults).

## Constants

| Name | Value | Where |
|---|---|---|
| Stream format | 44.1 kHz, 16-bit, stereo | `player/player.go` |
| Chunk | 20 ms (882 frames) | `player/player.go` |
| Produce ahead of now | 60 ms | `player/player.go` |
| Start lead for commands | 350 ms | `player/player.go` |
| Sync buffer | 1000 ms default (`-buffer`) | `main.go`, `cmd/desktop` |
| Play counts after | 30 s (or half a short track) | `player/player.go` |
| Hard resync / slew on / slew off | 100 ms / 3 ms / 1 ms | `snap/client.go` |
| Slew rate, error smoothing | 0.2 %, 4 s | `snap/client.go` |
| Clock samples kept / used | 16 / best 4 | `snap/client.go` |
| Jitter buffer | 5 s | `snap/client.go` |
| Per-listener queue | 256 chunks | `snap/server.go` |
| Server write deadline / read deadline | 3 s / 15 s | `snap/server.go` |
| Supervisor tick, mDNS scan, peer lifetime | 400 ms, 4 s, 15 s | `app/app.go` |
| Video picture: jump when off by (playing / paused) | web 0.4 s / 0.05 s, phone 0.35 s / 0.1 s | `app/index.html`, `VideoPane.kt` |
| Video picture: catch-up rate limit (web only) | ±10 % | `app/index.html` |
