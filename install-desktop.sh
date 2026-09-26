#!/bin/sh
# Adds "letsgo" to the applications menu (launches dist/desktop from this folder).
# Remove it again with: rm ~/.local/share/applications/letsgo.desktop
set -e
cd "$(dirname "$0")"
[ -x dist/desktop ] || { echo "build first: ./build.sh"; exit 1; }
dir="${XDG_DATA_HOME:-$HOME/.local/share}"
mkdir -p "$dir/applications" "$dir/icons/hicolor/scalable/apps"
cp app/logo.svg "$dir/icons/hicolor/scalable/apps/letsgo.svg"
cat >"$dir/applications/letsgo.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=letsgo
Comment=Play your music on every device on your Wi-Fi, in sync
Exec=$PWD/dist/desktop
Icon=letsgo
Categories=AudioVideo;Audio;Player;
Terminal=false
DESKTOP
echo "added letsgo to the applications menu"
