#!/usr/bin/env bash
# Regenerate scripts/AppIcon.icns from scripts/icon/cats-icon.svg. The bundler
# (build-macapp.sh) copies AppIcon.icns into the .app if present, so this is a
# one-time (or on-design-change) step, not part of every build.
#
# Requires: rsvg-convert (SVG raster) + iconutil (macOS, .iconset -> .icns).
# Falls back to sips + magick if rsvg-convert is unavailable.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SVG="$ROOT/scripts/icon/cats-icon.svg"
OUT="$ROOT/scripts/AppIcon.icns"
SET="$(mktemp -d)/Cats.iconset"
mkdir -p "$SET"

[ -f "$SVG" ] || { echo "gen-icon: missing $SVG" >&2; exit 1; }

# Render one PNG at the given pixel size into the iconset under the given name.
render() { # size name
  local size="$1"
  local name="$2"
  local dst="$SET/$name"
  if command -v rsvg-convert >/dev/null; then
    rsvg-convert -w "$size" -h "$size" "$SVG" -o "$dst"
  elif command -v magick >/dev/null; then
    magick -background none -density 512 "$SVG" -resize "${size}x${size}" "$dst"
  else
    echo "gen-icon: need rsvg-convert or magick" >&2; exit 1
  fi
}

# The ten slices Apple's .icns expects (1x + @2x for each logical size).
render 16   icon_16x16.png
render 32   icon_16x16@2x.png
render 32   icon_32x32.png
render 64   icon_32x32@2x.png
render 128  icon_128x128.png
render 256  icon_128x128@2x.png
render 256  icon_256x256.png
render 512  icon_256x256@2x.png
render 512  icon_512x512.png
render 1024 icon_512x512@2x.png

iconutil -c icns "$SET" -o "$OUT"
echo "==> wrote $OUT"
