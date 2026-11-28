#!/usr/bin/env bash
# Builds dist/HopDesk.app (Apple Silicon + Intel) and a .dmg. Run on a Mac
# with Go and the Xcode command line tools.
#
# Optional signing and notarization (needs a paid Apple Developer account):
#   MACOS_SIGN_IDENTITY="Developer ID Application: Your Name (TEAMID)"
#   APPLE_ID=you@example.com APPLE_TEAM_ID=TEAMID APPLE_APP_PASSWORD=xxxx-xxxx-xxxx-xxxx
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo dev)}"
NUMVER="$(echo "${VERSION#v}" | grep -Eo '^[0-9]+(\.[0-9]+){0,2}' || true)"
NUMVER="${NUMVER:-0.0.0}"
OUT=dist
APP="$OUT/HopDesk.app"
DMG="$OUT/HopDesk-$VERSION-macOS.dmg"

echo "Building HopDesk $VERSION for macOS"
rm -rf "$APP" "$OUT/dmg" "$OUT/HopDesk.iconset" "$OUT"/HopDesk-*-macOS.dmg   # keep only the newest .dmg
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

export CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=13.0
LDFLAGS="-s -w -X main.version=$VERSION"
for arch in arm64 amd64; do
  carch=$arch; [ "$arch" = amd64 ] && carch=x86_64
  GOOS=darwin GOARCH=$arch CC="clang -arch $carch" \
    CGO_CFLAGS="-mmacosx-version-min=13.0" CGO_LDFLAGS="-mmacosx-version-min=13.0" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/hopdesk-$arch" ./cmd/agent
done
lipo -create -output "$APP/Contents/MacOS/HopDesk" "$OUT/hopdesk-arm64" "$OUT/hopdesk-amd64"
rm "$OUT/hopdesk-arm64" "$OUT/hopdesk-amd64"
sed "s/__VERSION__/$NUMVER/g" packaging/macos/Info.plist > "$APP/Contents/Info.plist"

# App icon
ICONSET="$OUT/HopDesk.iconset"
mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  sips -z $s $s packaging/icon.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
  sips -z $((s * 2)) $((s * 2)) packaging/icon.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/HopDesk.icns"
rm -rf "$ICONSET"

# Sign: Developer ID if available, otherwise ad-hoc (fine for personal use).
if [ -n "${MACOS_SIGN_IDENTITY:-}" ]; then
  codesign --force --options runtime --timestamp --sign "$MACOS_SIGN_IDENTITY" "$APP"
else
  codesign --force --sign - "$APP"
fi
codesign --verify --verbose "$APP"

# Disk image with a shortcut to Applications
mkdir -p "$OUT/dmg"
cp -R "$APP" "$OUT/dmg/"
ln -s /Applications "$OUT/dmg/Applications"
hdiutil create -volname "HopDesk" -srcfolder "$OUT/dmg" -ov -format UDZO "$DMG" >/dev/null
rm -rf "$OUT/dmg"

if [ -n "${MACOS_SIGN_IDENTITY:-}" ] && [ -n "${APPLE_ID:-}" ]; then
  codesign --force --sign "$MACOS_SIGN_IDENTITY" "$DMG"
  xcrun notarytool submit "$DMG" --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" --password "$APPLE_APP_PASSWORD" --wait
  xcrun stapler staple "$DMG"
fi

echo "Done: $DMG"
