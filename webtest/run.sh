#!/bin/sh
# End-to-end test of the web UI in headless Chrome against a real letsgo node.
# Needs node and google-chrome; skips (exit 0) when they are missing.
set -e
cd "$(dirname "$0")/.."
if ! command -v node >/dev/null 2>&1 || ! command -v google-chrome >/dev/null 2>&1; then
	echo "web UI test skipped: needs node and google-chrome"
	exit 0
fi
T=$(mktemp -d)
trap 'kill $NODE $PHONE 2>/dev/null; rm -rf "$T"' EXIT
mkdir -p "$T/music/Album" "$T/music/Rock/Live" "$T/extra" "$T/data" "$T/phone" "$T/phonedata"
cp meta/testdata/tagged.mp3 "$T/music/Album/one.mp3"
cp meta/testdata/tagged.flac "$T/music/Album/two.flac"
cp meta/testdata/tagged.mp3 "$T/music/Rock/Live/three.mp3"
cp meta/testdata/plain.mp3 "$T/music/plain.mp3" && cp meta/testdata/plain.mp3 "$T/extra/extra1.mp3"
# a video is only a track where ffmpeg can decode its sound, so its checks run only there
LIB=6
if command -v ffmpeg >/dev/null 2>&1; then cp player/testdata/sweep44100.mp4 "$T/extra/clip.mp4"; LIB=7; fi
cp player/testdata/sweep44100.ogg "$T/music/Rock/sweep.ogg"
cp meta/testdata/plain.mp3 "$T/phone/Phone Song.mp3"
echo '{"tracks":{"Album/one.mp3":{"n":5,"last":100},"gone.mp3":{"n":9,"last":100}}}' >"$T/data/plays.json" # Most Played: one real song, one deleted
go build -o "$T/node" .
# a second node stands in for the phone; the first reaches other devices' API on LETSGO_PEER_PORT
"$T/node" -music "$T/phone" -data "$T/phonedata" -http 127.0.0.1:18082 -snap 127.0.0.1:11705 -name phonetest -no-discovery >"$T/phone.log" 2>&1 &
PHONE=$!
LETSGO_PEER_PORT=18082 "$T/node" -music "$T/music" -data "$T/data" -http 127.0.0.1:18080 -snap 127.0.0.1:11704 -name webtest -no-discovery >"$T/node.log" 2>&1 &
NODE=$!
sleep 2
sed "s|__EXTRA__|$T/extra|g; s|__LIB__|$LIB|g" webtest/test.js >"$T/test.js"
node webtest/run.js app/index.html "$T/test.js" "$T/chrome"
