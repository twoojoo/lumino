#!/usr/bin/env bash
# Builds the macOS release on a Mac (Gio needs cgo + the Apple SDK): arm64 and
# amd64 binaries merged into a universal Kubeon.app, packed as .dmg and .zip
# in dist/.
#
# Signing: ad-hoc by default (runs after "Open" in System Settings or
# `xattr -dr com.apple.quarantine Kubeon.app`). With a Developer ID:
#   MACOS_SIGN_IDENTITY="Developer ID Application: …"   sign with hardened runtime
#   MACOS_NOTARY_PROFILE=<keychain profile>              also notarize + staple
#     (xcrun notarytool store-credentials <profile> …)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
[[ "$(uname -s)" == Darwin ]] || { echo "package-macos.sh must run on macOS" >&2; exit 1; }
# shellcheck source=scripts/version.sh
source "$ROOT/scripts/version.sh"
VERSION="$(kubeon_version)"
DIST="${DIST:-dist}"
OUT="$DIST/macos"
APP="$OUT/Kubeon.app"
export MACOSX_DEPLOYMENT_TARGET=12.0 # Go 1.25 needs macOS 12+

rm -rf "$OUT"
mkdir -p "$OUT"
"$ROOT/scripts/notices.sh" "$DIST/THIRD_PARTY_NOTICES.txt"
for arch in arm64 amd64; do
  carch=$arch; [[ $arch == amd64 ]] && carch=x86_64
  echo "==> go build darwin/$arch ($VERSION)"
  GOOS=darwin GOARCH=$arch CGO_ENABLED=1 CC="clang -arch $carch" \
    CGO_CFLAGS="-mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET" \
    CGO_LDFLAGS="-mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET" \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$OUT/kubeon-$arch" ./cmd/kubeon
done

echo "==> Kubeon.app (universal)"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
lipo -create -output "$APP/Contents/MacOS/kubeon" "$OUT/kubeon-arm64" "$OUT/kubeon-amd64"
rm "$OUT/kubeon-arm64" "$OUT/kubeon-amd64"
lipo -info "$APP/Contents/MacOS/kubeon"
sed -e "s/@VERSION@/$(num_version "$VERSION")/g" -e "s/@DISPLAY_VERSION@/$VERSION/g" packaging/macos/Info.plist > "$APP/Contents/Info.plist"
plutil -lint "$APP/Contents/Info.plist"
cp packaging/icons/kubeon.icns "$APP/Contents/Resources/kubeon.icns"
cp "$DIST/THIRD_PARTY_NOTICES.txt" "$APP/Contents/Resources/THIRD_PARTY_NOTICES.txt"
"$APP/Contents/MacOS/kubeon" --version

if [[ -n "${MACOS_SIGN_IDENTITY:-}" ]]; then
  echo "==> codesign ($MACOS_SIGN_IDENTITY)"
  codesign --force --options runtime --timestamp -s "$MACOS_SIGN_IDENTITY" "$APP"
else
  echo "==> codesign (ad-hoc)"
  codesign --force -s - "$APP"
fi
codesign --verify --verbose "$APP"

echo "==> dmg / zip"
STAGE="$OUT/dmg"
mkdir -p "$STAGE"
cp -R "$APP" "$STAGE/"
ln -s /Applications "$STAGE/Applications"
DMG="$DIST/kubeon-$VERSION-macos-universal.dmg"
rm -f "$DMG"
hdiutil create -volname "Kubeon $VERSION" -srcfolder "$STAGE" -fs HFS+ -format UDZO -ov "$DMG"
rm -rf "$STAGE"
if [[ -n "${MACOS_SIGN_IDENTITY:-}" ]]; then
  codesign --force --timestamp -s "$MACOS_SIGN_IDENTITY" "$DMG"
fi
if [[ -n "${MACOS_SIGN_IDENTITY:-}" && -n "${MACOS_NOTARY_PROFILE:-}" ]]; then
  echo "==> notarize"
  xcrun notarytool submit "$DMG" --keychain-profile "$MACOS_NOTARY_PROFILE" --wait
  xcrun stapler staple "$DMG"
  xcrun stapler staple "$APP"
fi
(cd "$OUT" && ditto -c -k --keepParent Kubeon.app "../kubeon-$VERSION-macos-universal.zip")
ls -la "$DIST"
