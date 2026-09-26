# Contributing

Thanks for looking. letsgo is small on purpose, and changes that keep it that way are the easiest to
accept: fix the cause, add a test that fails without the fix, and say why in the commit message.

## Build and test

```sh
./build.sh                # go vet, all tests, then dist/: APK, desktop app, headless binaries
SKIP_TESTS=1 ./build.sh   # just build
```

Prerequisites and versions are in [docs/troubleshooting.md](docs/troubleshooting.md#building-from-source).
For the Go code alone you need only Go: `go vet ./... && go test -race ./app ./snap ./player ./meta ./cmd/...`.

Two packages touch real hardware and are **not** in that line on purpose. `speaker` plays 10 seconds of
silence on your sound card and `mpris` registers a name on your D-Bus session bus. Both skip themselves
when the device or bus is missing, and `./build.sh` runs them. Run them when you change those packages.

The web UI test (`webtest/run.sh`) needs `node` and `google-chrome`; it skips itself without them. It
uses ports 18080 to 18082 on 127.0.0.1.

## Rules the tests keep

- **Tests never touch your network or your devices.** Anything that starts a node must switch
  discovery off (`app.Discovery = false`, `-no-discovery`, or `LETSGO_NO_DISCOVERY=1`). A test node that
  advertised itself would show up on, and could control, the developer's real phone and laptop.
- **No test writes a package-level variable while a node is running.** Nodes have background goroutines;
  use a per-node setting (see `setPeerPort`). Run new real-time tests under load to check them:
  `go test -race -count=10 -run YourTest ./app/` while every core is busy.
- The sync engine is tested in virtual time (`snap/client_test.go`) so timing tests do not depend on the
  machine; end-to-end tests over loopback (`snap/loopback_test.go`, `app/handover_test.go`) check the real
  thing in real time.
- Tests use small synthetic audio (`player/testdata`, `meta/testdata`: tones and sweeps made with ffmpeg).
  Do not add copyrighted music.

## Style

- `gofmt`, `go vet`. Comments explain *why* (a constraint, a surprising choice), not what the code does.
- Keep the layout in the README's code map. New behaviour that a user can see belongs in the README and,
  if it touches the network or stored data, in [docs/privacy-and-security.md](docs/privacy-and-security.md).
- If you change the wire protocol, keep stock Snapcast clients working and update
  [docs/how-it-works.md](docs/how-it-works.md).
- The Android UI is Jetpack Compose in `android-app/`; the Go node runs inside it through `./mobile`.

## Commits and changes

One logical change per commit, imperative subject, the reason in the body. Do not commit build output,
`local.properties`, logs, playlists or anything from your own music library (`.gitignore` covers most
of it; check `git status` before you commit).

## How this project is made

Much of the code was written by the maintainer working with an AI coding assistant (Claude Code, from
Anthropic). Those commits carry a `Co-Authored-By` line. The assistant's work is held to the same
standard as anyone's: it needs tests, and a claim in the docs needs to be true. If you use an AI
assistant for a contribution, that is fine; say so in the pull request and check what it wrote.
