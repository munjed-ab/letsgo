#!/bin/sh
# Adds "letsgo" to the applications menu (launches dist/desktop from this folder).
# Remove it again with: rm ~/.local/share/applications/letsgo.desktop
set -e
cd "$(dirname "$0")"
[ -x dist/desktop ] || { echo "build first: ./build.sh"; exit 1; }
dir="${XDG_DATA_HOME:-$HOME/.local/share}"
# Chrome names its window differently on Wayland (from the address and the "letsgo" profile) and on X11 (--class)
if [ "$XDG_SESSION_TYPE" = wayland ] || [ -n "$WAYLAND_DISPLAY" ]; then wmclass=chrome-localhost__-letsgo; else wmclass=letsgo; fi
mkdir -p "$dir/applications" "$dir/icons/hicolor/scalable/apps"
cp app/logo.svg "$dir/icons/hicolor/scalable/apps/letsgo.svg"
cat >"$dir/applications/letsgo.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=letsgo
GenericName=Music player
Comment=Play your music on every device on your Wi-Fi, in sync
Exec=$PWD/dist/desktop
Icon=letsgo
Categories=AudioVideo;Audio;Player;
Terminal=false
# the app window's identity in Chrome (see windowArgs in cmd/desktop): this ties the running window to this
# entry, so the dock shows "letsgo" and its logo. Another browser or a -http host other than localhost
# gives another identity.
StartupWMClass=$wmclass
DESKTOP
echo "added letsgo to the applications menu"
