#!/usr/bin/env bash
# Cross-compiles the Windows release (pure Go, no cgo: Gio uses Direct3D 11
# through syscalls) for amd64 and arm64, embeds icon/manifest/version
# resources and packs portable zips in dist/. Runs on any OS.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
# shellcheck source=scripts/version.sh
source "$ROOT/scripts/version.sh"
VERSION="$(kubeon_version)"
DIST="${DIST:-dist}"
ARCHES="${ARCHES:-amd64 arm64}"
WINRES="github.com/tc-hib/go-winres@v0.3.3"

# Windows version resources need a numeric a.b.c.d version
NUMVER="$(num_version "$VERSION")"

"$ROOT/scripts/notices.sh" "$DIST/THIRD_PARTY_NOTICES.txt"

echo "==> resources ($VERSION / $NUMVER)"
trap 'rm -f cmd/kubeon/rsrc_windows_*.syso' EXIT
GOFLAGS=-mod=mod go run "$WINRES" make \
  --in packaging/windows/winres.json \
  --out cmd/kubeon/rsrc \
  --arch "$(echo "$ARCHES" | tr ' ' ',')" \
  --product-version "$NUMVER" --file-version "$NUMVER"

for arch in $ARCHES; do
  OUT="$DIST/windows-$arch"
  rm -rf "$OUT"
  mkdir -p "$OUT"
  echo "==> go build windows/$arch"
  GOOS=windows GOARCH="$arch" CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -H windowsgui -X main.version=$VERSION" \
    -o "$OUT/kubeon.exe" ./cmd/kubeon
  cp "$DIST/THIRD_PARTY_NOTICES.txt" "$OUT/THIRD_PARTY_NOTICES.txt"
  ZIP="$ROOT/$DIST/kubeon-$VERSION-windows-$arch.zip"
  rm -f "$ZIP"
  if command -v zip >/dev/null; then
    (cd "$OUT" && zip -q -9 "$ZIP" kubeon.exe THIRD_PARTY_NOTICES.txt)
  else
    (cd "$OUT" && python3 -m zipfile -c "$ZIP" kubeon.exe THIRD_PARTY_NOTICES.txt)
  fi
done
ls -la "$DIST"/*.zip
