#!/usr/bin/env bash
# Assemble a macOS .app bundle for herdr. The bundle is unsigned (personal use);
# on another Mac it needs a one-time right-click -> Open to clear Gatekeeper.
#
# Two variants, selected by the first argument:
#   self   — self-contained: herdrapp + gateway + termhost + herdrctl. Runs fully
#            local (make macapp). Requires the ghostty binaries in bin/ (make
#            binaries), which this script does NOT build — it copies them.
#   client — thin client: herdrapp only, baked to remote mode (make macapp-client).
#            No backend binaries, so no ghostty/Zig toolchain needed to produce it.
#
# Usage: build-macapp.sh <self|client> <AppName> <bundle-id> <version>
#
# Design notes:
#   - herdrapp is built here (plain `go build`, cgo on for webview, no -tags
#     ghostty). The three ghostty daemons are static (otool -L shows only system
#     frameworks), so there are no dylibs to copy and no @rpath fixups.
#   - The launcher finds its sibling daemons via os.Executable() -> same dir, so
#     everything lives together in Contents/MacOS.
set -euo pipefail

VARIANT="${1:?usage: build-macapp.sh <self|client> <AppName> <bundle-id> <version>}"
APP_NAME="${2:?missing app name}"
BUNDLE_ID="${3:?missing bundle id}"
VERSION="${4:-dev}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
APP="$DIST/${APP_NAME}.app"
MACOS="$APP/Contents/MacOS"
RES="$APP/Contents/Resources"

case "$VARIANT" in
  self)   MODE=local  ;;
  client) MODE=remote ;;
  *) echo "build-macapp: unknown variant '$VARIANT' (want self|client)" >&2; exit 2 ;;
esac

echo "==> assembling ${APP_NAME}.app (variant=$VARIANT, mode=$MODE, version=$VERSION)"

# Start clean so a rebuild never leaves a stale binary behind.
rm -rf "$APP"
mkdir -p "$MACOS" "$RES"

# The launcher. cgo is required (WebKit); do not pass -tags ghostty. The baked
# defaultMode decides local-vs-remote on first run (before any app.json exists).
echo "  building herdrapp (mode=$MODE)"
( cd "$ROOT" && go build -trimpath \
    -ldflags "-X main.defaultMode=${MODE}" \
    -o "$MACOS/herdrapp" ./cmd/herdrapp )

# Self-contained variant also carries the three static daemons. They must already
# be built (make binaries -> bin/); we only copy so this script needs no ghostty
# toolchain of its own.
if [ "$VARIANT" = "self" ]; then
  for bin in gateway termhost herdrctl; do
    if [ ! -x "$ROOT/bin/$bin" ]; then
      echo "build-macapp: bin/$bin missing — run 'make binaries' first" >&2
      exit 1
    fi
    cp "$ROOT/bin/$bin" "$MACOS/$bin"
  done
fi

# Optional icon: drop an AppIcon.icns at scripts/AppIcon.icns to have it bundled.
ICON_KEY=""
if [ -f "$ROOT/scripts/AppIcon.icns" ]; then
  cp "$ROOT/scripts/AppIcon.icns" "$RES/AppIcon.icns"
  ICON_KEY='  <key>CFBundleIconFile</key><string>AppIcon</string>'
fi

# Info.plist. Unsigned personal build: macOS is lenient about CFBundleVersion, so
# the git-describe VERSION is fine for both keys. NSHighResolutionCapable gives a
# crisp Retina window.
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>${APP_NAME}</string>
  <key>CFBundleDisplayName</key><string>${APP_NAME}</string>
  <key>CFBundleIdentifier</key><string>${BUNDLE_ID}</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundleExecutable</key><string>herdrapp</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>LSMinimumSystemVersion</key><string>10.15</string>
  <key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
${ICON_KEY}
</dict>
</plist>
PLIST

echo "==> built $APP"
