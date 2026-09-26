#!/bin/sh
# Test, then build everything into dist/:
#   letsgo-linux-amd64, letsgo-android-arm64   headless server binaries (Termux etc.)
#   desktop, play                              laptop app and headless listener
#   letsgo.apk                                 Android app (needs gomobile + Android SDK/NDK)
# SKIP_TESTS=1 skips the tests (they take ~1 min; one plays silence on the audio device, and the web UI
# test needs node + google-chrome and is skipped without them).
set -e
cd "$(dirname "$0")"
mkdir -p dist

if [ -z "$SKIP_TESTS" ]; then
	echo "== tests"
	go vet ./...
	go test -race -count=1 ./...
	./webtest/run.sh
fi

echo "== binaries"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/letsgo-android-arm64 .
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/letsgo-linux-amd64 .
go build -o dist/desktop ./cmd/desktop
go build -o dist/play ./cmd/play

echo "== apk"
if command -v gomobile >/dev/null 2>&1 && [ -n "$ANDROID_HOME" ] && [ -n "$ANDROID_NDK_HOME" ]; then
	gomobile bind -target=android/arm64 -androidapi 21 -o android-app/app/libs/letsgo.aar ./mobile
	(cd android-app && ${GRADLE:-gradle} :app:assembleRelease --no-daemon -q)
	cp android-app/app/build/outputs/apk/release/app-release.apk dist/letsgo.apk
else
	echo "skipped: needs gomobile, ANDROID_HOME and ANDROID_NDK_HOME (see README)"
fi
ls -lh dist
