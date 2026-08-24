#!/usr/bin/env bash
# Assemble a macOS .app bundle for cats. The bundle is adhoc-signed (personal
# use — no cert chain, so Gatekeeper on another Mac still needs a one-time
# right-click -> Open).
#
# Two variants, selected by the first argument:
#   self   — self-contained: catapp + catway + cathost + catctl. Runs fully
#            local (make macapp). Requires the ghostty binaries in bin/ (make
#            binaries), which this script does NOT build — it copies them.
#   client — thin client: catapp only, baked to remote mode (make macapp-client).
#            No backend binaries, so no ghostty/Zig toolchain needed to produce it.
#
# Usage: build-macapp.sh <self|client> <AppName> <bundle-id> <version>
#
# Design notes:
#   - catapp is built here (plain `go build`, cgo on for webview, no -tags
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
echo "  building catapp (mode=$MODE)"
( cd "$ROOT" && go build -trimpath \
    -ldflags "-X main.defaultMode=${MODE}" \
    -o "$MACOS/catapp" ./cmd/catapp )

# Self-contained variant also carries the three static daemons. They must already
# be built (make binaries -> bin/); we only copy so this script needs no ghostty
# toolchain of its own.
if [ "$VARIANT" = "self" ]; then
  for bin in catway cathost catctl; do
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

# Info.plist. Adhoc-signed personal build: macOS is lenient about CFBundleVersion,
# so the git-describe VERSION is fine for both keys. NSHighResolutionCapable gives
# a crisp Retina window.
#
# NSMicrophoneUsageDescription is load-bearing, not boilerplate. TCC attributes a
# privacy-guarded request from any process in a pane (a shell, an agent's voice
# recorder) to its "responsible process" — this app — and renders the permission
# prompt with the usage string from THIS bundle's Info.plist. With the key absent
# macOS cannot show a prompt at all and silently denies: AVFoundation capture
# then delivers all-zero audio frames, which presents as "voice input records
# but transcribes to nothing" rather than any visible error. Ghostty.app ships
# the same key (plus a dozen siblings — camera, calendar, …) for exactly this
# reason; add those the same way if a pane workload ever needs them.
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
  <key>CFBundleExecutable</key><string>catapp</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>LSMinimumSystemVersion</key><string>10.15</string>
  <key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
  <key>NSMicrophoneUsageDescription</key>
  <string>A program running in a ${APP_NAME} terminal pane would like to use the microphone.</string>
${ICON_KEY}
</dict>
</plist>
PLIST

# Adhoc-sign the assembled bundle. The Go binaries are already linker-signed
# (each individually valid), but the BUNDLE carries no signature, so its
# identity is the main binary's default identifier "a.out" with Info.plist
# unsealed — and "a.out" is what TCC would record a microphone grant against.
# Signing the bundle derives the identifier from CFBundleIdentifier and seals
# Info.plist, so privacy grants land on a name that means something. Adhoc has
# no cert chain, so TCC pins the grant to this build's cdhash: a rebuild means
# one re-prompt, which is the honest ceiling for a personal unsigned app.
echo "  signing (adhoc)"
codesign --force --sign - "$APP"

echo "==> built $APP"
