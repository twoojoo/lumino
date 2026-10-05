#!/usr/bin/env bash
# Builds the Linux release (full Gio build: Wayland + X11 + Vulkan) inside the
# packaging/linux Docker image and packs it as AppImage, .deb, .rpm and
# .tar.gz in dist/. Architecture = host architecture (amd64 or arm64).
#
#   scripts/package-linux.sh            # docker build + package
#   VERSION=1.2.3 scripts/package-linux.sh
#   scripts/package-linux.sh --inside   # already in the build env (CI)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
# shellcheck source=scripts/version.sh
source "$ROOT/scripts/version.sh"
VERSION="$(kubeon_version)"
DIST="${DIST:-dist}"
IMAGE="kubeon-build-linux"

if [[ "${1:-}" != "--inside" ]]; then
  # needs network for license metadata: generated on the host
  "$ROOT/scripts/notices.sh" "$DIST/THIRD_PARTY_NOTICES.txt"
  docker build -t "$IMAGE" packaging/linux
  # create the bind-mount sources first, or Docker makes them root-owned
  GOMOD="$(go env GOMODCACHE)"
  mkdir -p "$DIST" .dev/docker-cache/go-build "$GOMOD"
  exec docker run --rm \
    --user "$(id -u):$(id -g)" \
    -e HOME=/tmp -e VERSION="$VERSION" -e DIST="$DIST" \
    -e GOCACHE=/cache/go-build -e GOMODCACHE=/gomod \
    -v "$ROOT:/src" -w /src \
    -v "$ROOT/.dev/docker-cache:/cache" \
    -v "$GOMOD:/gomod" \
    "$IMAGE" scripts/package-linux.sh --inside
fi

case "$(uname -m)" in
  x86_64) ARCH=amd64 AARCH=x86_64 ;;
  aarch64 | arm64) ARCH=arm64 AARCH=aarch64 ;;
  *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;;
esac
OUT="$DIST/linux-$ARCH"
rm -rf "$OUT"
mkdir -p "$OUT"

echo "==> go build linux/$ARCH ($VERSION)"
CGO_ENABLED=1 go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" \
  -o "$OUT/kubeon" ./cmd/kubeon
"$OUT/kubeon" --version

echo "==> AppImage"
APPDIR="$OUT/AppDir"
mkdir -p "$APPDIR/usr/bin" "$APPDIR/usr/share/applications" "$APPDIR/usr/share/metainfo" "$APPDIR/usr/share/doc/kubeon"
cp "$DIST/THIRD_PARTY_NOTICES.txt" "$APPDIR/usr/share/doc/kubeon/"
cp "$OUT/kubeon" "$APPDIR/usr/bin/kubeon"
cp packaging/linux/kubeon.desktop "$APPDIR/usr/share/applications/"
cp packaging/linux/kubeon.desktop "$APPDIR/kubeon.desktop"
cp packaging/linux/kubeon.metainfo.xml "$APPDIR/usr/share/metainfo/io.github.twoojoo.kubeon.appdata.xml"
for s in 16 24 32 48 64 128 256 512; do
  mkdir -p "$APPDIR/usr/share/icons/hicolor/${s}x${s}/apps"
  cp "packaging/icons/kubeon-$s.png" "$APPDIR/usr/share/icons/hicolor/${s}x${s}/apps/kubeon.png"
done
cp packaging/icons/kubeon-256.png "$APPDIR/kubeon.png"
ln -sf kubeon.png "$APPDIR/.DirIcon"
cat > "$APPDIR/AppRun" <<'RUN'
#!/bin/sh
HERE="$(dirname "$(readlink -f "$0")")"
exec "$HERE/usr/bin/kubeon" "$@"
RUN
chmod +x "$APPDIR/AppRun"
ARCH="$AARCH" appimagetool --no-appstream "$APPDIR" "$DIST/kubeon-$VERSION-$AARCH.AppImage"
rm -rf "$APPDIR"

echo "==> deb / rpm"
# nfpm doesn't expand variables in contents paths: render the config
sed -e "s|\${VERSION}|$(pkg_version "$VERSION")|g" -e "s|\${ARCH}|$ARCH|g" -e "s|\${DIST}|$DIST|g" \
  packaging/linux/nfpm.yaml > "$OUT/nfpm.yaml"
nfpm package --config "$OUT/nfpm.yaml" --packager deb --target "$DIST/"
nfpm package --config "$OUT/nfpm.yaml" --packager rpm --target "$DIST/"
rm "$OUT/nfpm.yaml"
# "~" (prerelease ordering) belongs inside the package only: GitHub rewrites
# it in asset names, which would no longer match SHA256SUMS.txt
for f in "$DIST"/*'~'*; do
  if [[ -e "$f" ]]; then mv "$f" "${f//\~/-}"; fi
done

echo "==> tar.gz"
TAR="kubeon-$VERSION-linux-$ARCH"
mkdir -p "$OUT/$TAR"
cp "$OUT/kubeon" packaging/linux/kubeon.desktop packaging/icons/kubeon-256.png "$OUT/$TAR/"
cp "$DIST/THIRD_PARTY_NOTICES.txt" "$OUT/$TAR/"
tar -C "$OUT" -czf "$DIST/$TAR.tar.gz" "$TAR"
rm -rf "${OUT:?}/${TAR:?}"

ls -la "$DIST"
