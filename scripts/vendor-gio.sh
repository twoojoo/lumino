#!/usr/bin/env bash
# Vendors gioui.org into third_party/gio and applies lumino patches
# (third_party/_gio-patches). go.mod replaces gioui.org with this copy.
#
# Patches (wayland):
#  - track maximized/fullscreen/minimized state set by the compositor
#  - allow dragging maximized windows (compositor restores them)
#  - double-click on a move area toggles maximize
#  - right-click on a move area opens the compositor window menu
# Patches (macOS):
#  - keep the native traffic lights visible on undecorated windows
#  - double-click on a move area zooms the window
# Patches (all platforms):
#  - widget.Clickable shows the hand (pointer) cursor
# Patches (windows):
#  - Direct3D 11: fall back to the WARP software rasterizer without a GPU
set -euo pipefail

GIO_VERSION="${GIO_VERSION:-v0.10.3}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DST="$ROOT/third_party/gio"

SRC="$(GOFLAGS=-mod=mod go mod download -json "gioui.org@$GIO_VERSION" | sed -n 's/.*"Dir": "\(.*\)".*/\1/p')"
rm -rf "$DST"
cp -r "$SRC" "$DST"
chmod -R u+w "$DST"
for p in "$ROOT"/third_party/_gio-patches/*.patch; do
  patch -d "$DST" -p1 < "$p"
done
echo "vendored gioui.org $GIO_VERSION into $DST"
