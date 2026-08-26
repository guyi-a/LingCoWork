#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSET_DIR="$(cd "$SCRIPT_DIR/../assets" && pwd)"
SOURCE="${1:-$ASSET_DIR/icon-source.png}"
ICONSET="$ASSET_DIR/icon.iconset"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "iconutil is only available on macOS." >&2
  exit 1
fi

if [[ ! -f "$SOURCE" ]]; then
  echo "Icon source not found: $SOURCE" >&2
  exit 1
fi

rm -rf "$ICONSET"
mkdir -p "$ICONSET"

sips -z 16 16 "$SOURCE" --out "$ICONSET/icon_16x16.png" >/dev/null
sips -z 32 32 "$SOURCE" --out "$ICONSET/icon_16x16@2x.png" >/dev/null
sips -z 32 32 "$SOURCE" --out "$ICONSET/icon_32x32.png" >/dev/null
sips -z 64 64 "$SOURCE" --out "$ICONSET/icon_32x32@2x.png" >/dev/null
sips -z 128 128 "$SOURCE" --out "$ICONSET/icon_128x128.png" >/dev/null
sips -z 256 256 "$SOURCE" --out "$ICONSET/icon_128x128@2x.png" >/dev/null
sips -z 256 256 "$SOURCE" --out "$ICONSET/icon_256x256.png" >/dev/null
sips -z 512 512 "$SOURCE" --out "$ICONSET/icon_256x256@2x.png" >/dev/null
sips -z 512 512 "$SOURCE" --out "$ICONSET/icon_512x512.png" >/dev/null
sips -z 1024 1024 "$SOURCE" --out "$ICONSET/icon_512x512@2x.png" >/dev/null

if ! iconutil -c icns "$ICONSET" -o "$ASSET_DIR/icon.icns"; then
  if ! python3 -c "from PIL import Image; Image.open('$SOURCE').convert('RGBA').save('$ASSET_DIR/icon.icns', format='ICNS')"; then
    echo "iconutil failed. Install Pillow (python3 -m pip install Pillow) and retry." >&2
    exit 1
  fi
fi
rm -rf "$ICONSET"

echo "Generated $ASSET_DIR/icon.icns"
