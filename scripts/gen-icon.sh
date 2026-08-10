#!/usr/bin/env bash
# Regenerate scripts/AppIcon.icns from the artwork in scripts/icon/. The bundler
# (build-macapp.sh) copies AppIcon.icns into the .app if present, so this is a
# one-time (or on-design-change) step, not part of every build.
#
# Two source drawings, not one. cats-icon.svg is the real mark: a fine-line
# tracing whose head outline is about 2% of the head's width. That is correct
# from 64px up and hopeless below it — at 16px the stroke lands near a fifth of
# a pixel and renders as a smudge. cats-icon-small.svg is the same silhouette
# with the outline thickened, and it is used for the 16 and 32px slices only.
# An .icns is a family, so carrying different art per size is the intended fix
# rather than a workaround.
#
# Requires: iconutil (macOS, .iconset -> .icns) plus something that can raster
# an SVG. Tries rsvg-convert, then magick, then QuickLook — the last needs
# nothing installed, which is why it exists: neither of the other two ships with
# macOS, so without it a clean machine cannot regenerate the icon at all.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SVG_MAIN="$ROOT/scripts/icon/cats-icon.svg"
SVG_SMALL="$ROOT/scripts/icon/cats-icon-small.svg"
OUT="$ROOT/scripts/AppIcon.icns"
SET="$(mktemp -d)/Cats.iconset"
mkdir -p "$SET"

for f in "$SVG_MAIN" "$SVG_SMALL"; do
  [ -f "$f" ] || { echo "gen-icon: missing $f" >&2; exit 1; }
done

# Below this pixel size the bold reduction is used instead of the real mark.
SMALL_MAX=32

# QuickLook fallback state: a 1024 master per source drawing, rendered on first
# use and reused for every slice. Only prepared when the better rasterisers are
# both absent, since resampling a bitmap is softer at 16px than rendering the
# vector directly.
QLDIR=""
if ! command -v rsvg-convert >/dev/null && ! command -v magick >/dev/null; then
  command -v qlmanage >/dev/null || {
    echo "gen-icon: need rsvg-convert, magick, or qlmanage" >&2; exit 1; }
  QLDIR="$(mktemp -d)"
fi

# master_for prints the path to a 1024px raster of the given SVG, making it once.
master_for() { # svg
  local svg="$1"
  local dst="$QLDIR/$(basename "$svg").png"
  if [ ! -f "$dst" ]; then
    qlmanage -t -s 1024 -o "$QLDIR" "$svg" >/dev/null 2>&1 || true
    [ -f "$dst" ] || { echo "gen-icon: qlmanage could not rasterise $svg" >&2; exit 1; }
  fi
  printf '%s' "$dst"
}

# Render one PNG at the given pixel size into the iconset under the given name.
render() { # size name
  local size="$1"
  local name="$2"
  local dst="$SET/$name"
  local svg="$SVG_MAIN"
  [ "$size" -le "$SMALL_MAX" ] && svg="$SVG_SMALL"

  if command -v rsvg-convert >/dev/null; then
    rsvg-convert -w "$size" -h "$size" "$svg" -o "$dst"
  elif command -v magick >/dev/null; then
    magick -background none -density 512 "$svg" -resize "${size}x${size}" "$dst"
  else
    sips -z "$size" "$size" "$(master_for "$svg")" --out "$dst" >/dev/null
  fi
}

# The ten slices Apple's .icns expects (1x + @2x for each logical size). Note
# that @2x names are chosen by their PIXEL size, so icon_16x16@2x is a 32px
# render and takes the small art, while icon_32x32@2x is 64px and takes the real
# mark — the split follows pixels, not the logical label.
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
