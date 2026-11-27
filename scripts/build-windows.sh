#!/usr/bin/env bash
# Builds dist/HopDesk-<version>-Windows-Setup.exe. Runs on macOS or Linux:
#   macOS: brew install makensis      Linux: sudo apt-get install nsis
# Set WITH_FFMPEG=0 to leave out ffmpeg (smaller installer, JPEG video mode).
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo dev)}"
NUMVER="$(echo "${VERSION#v}" | grep -Eo '^[0-9]+(\.[0-9]+){0,2}' || true)"
NUMVER="${NUMVER:-0.0.0}"
while [ "$(echo "$NUMVER" | tr -cd . | wc -c)" -lt 2 ]; do NUMVER="$NUMVER.0"; done
WITH_FFMPEG="${WITH_FFMPEG:-1}"
OUT="$PWD/dist/windows"
SETUP="$PWD/dist/HopDesk-$VERSION-Windows-Setup.exe"
mkdir -p "$OUT"

echo "Building HopDesk $VERSION for Windows"

# Icon, version info and manifest embedded in the .exe
command -v go-winres >/dev/null || go install github.com/tc-hib/go-winres@latest
WINRES="$(command -v go-winres || echo "$(go env GOPATH)/bin/go-winres")"
(cd cmd/agent && "$WINRES" simply --arch amd64 --manifest gui --icon ../../packaging/icon.png \
  --product-name HopDesk --file-description "HopDesk remote desktop" --original-filename HopDesk.exe \
  --file-version "$NUMVER.0" --product-version "$NUMVER.0")
trap 'rm -f cmd/agent/rsrc_windows_*.syso' EXIT

# -H=windowsgui: no console window; logs go to %APPDATA%\hopdesk\hopdesk.log
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -H=windowsgui -X main.version=$VERSION" -o "$OUT/HopDesk.exe" ./cmd/agent

NSIS_ARGS=()
if [ "$WITH_FFMPEG" = 1 ]; then
  if [ ! -f "$OUT/ffmpeg.exe" ]; then
    # LGPL build: includes the Windows Media Foundation H.264 encoder (h264_mf).
    curl -fsSL -o "$OUT/ffmpeg.zip" https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-lgpl.zip
    unzip -j -o -q "$OUT/ffmpeg.zip" '*/bin/ffmpeg.exe' -d "$OUT"
    unzip -p "$OUT/ffmpeg.zip" '*/LICENSE.txt' > "$OUT/ffmpeg-LICENSE.txt"
    rm "$OUT/ffmpeg.zip"
  fi
  NSIS_ARGS+=(-DWITH_FFMPEG)
fi

makensis -V2 -DVERSION="$NUMVER" -DBUILDDIR="$OUT" -DOUTFILE="$SETUP" ${NSIS_ARGS[@]+"${NSIS_ARGS[@]}"} packaging/windows/hopdesk.nsi
echo "Done: $SETUP"
