#!/usr/bin/env bash
# Build libghostty-vt for the Phase B Go terminal path and print the
# PKG_CONFIG_PATH needed to build the cgo spikes (cmd/vtspike, cmd/ptyspike).
#
# Why this script exists (macOS 26.x / Apple Silicon):
#   libghostty-vt (vendored in the herdr Rust repo) pins Zig 0.15.x. But the
#   macOS 26.5 SDK dropped the plain `arm64-macos` slice from its system .tbd
#   stubs (only `arm64e-macos` remains), and Zig 0.15.2 does NOT fall back
#   arm64 -> arm64e, so its native build-runner fails to link libSystem.
#   Workaround: build a patched copy of the SDK with the `arm64-macos` slice
#   re-injected into every .tbd, and point Zig at it via an `xcrun` shim.
#   (Zig 0.16 handles SDK 26.5 fine, but libghostty-vt requires 0.15.x.)
#
# Idempotent: re-running reuses an existing Zig download / patched SDK.
set -euo pipefail

# --- config -----------------------------------------------------------------
ZIG_VERSION="0.15.2"
ZIG_SHA256="3cc2bab367e185cdfb27501c4b30b1b0653c28d9f73df8dc91488e66ece5fa6b"
# Vendored libghostty-vt source (in the herdr Rust repo). Override with env.
GHOSTTY_VT_DIR="${GHOSTTY_VT_DIR:-$HOME/projs/rust/herdr/vendor/libghostty-vt}"

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLS="$ROOT/.tools"
ZIG_DIR="$TOOLS/zig-aarch64-macos-$ZIG_VERSION"
REAL_SDK="$(xcrun --show-sdk-path)"
FAKE_SDK="$TOOLS/MacOSX-arm64patch.sdk"
SHIM="$TOOLS/shim"

mkdir -p "$TOOLS"

# --- 1. Zig 0.15.2 ----------------------------------------------------------
if [[ ! -x "$ZIG_DIR/zig" ]]; then
  echo ">> downloading Zig $ZIG_VERSION"
  tarball="$TOOLS/zig-$ZIG_VERSION.tar.xz"
  curl -fsSL -o "$tarball" \
    "https://ziglang.org/download/$ZIG_VERSION/zig-aarch64-macos-$ZIG_VERSION.tar.xz"
  got="$(shasum -a 256 "$tarball" | awk '{print $1}')"
  [[ "$got" == "$ZIG_SHA256" ]] || { echo "checksum mismatch: $got" >&2; exit 1; }
  tar -xf "$tarball" -C "$TOOLS"
fi
echo ">> zig: $("$ZIG_DIR/zig" version)"

# --- 2. arm64-patched SDK sysroot ------------------------------------------
if [[ ! -d "$FAKE_SDK" ]]; then
  echo ">> building arm64-patched sysroot from $REAL_SDK"
  mkdir -p "$FAKE_SDK/usr"
  ln -s "$REAL_SDK/usr/include" "$FAKE_SDK/usr/include"
  ln -s "$REAL_SDK/System" "$FAKE_SDK/System"
  cp -R "$REAL_SDK/usr/lib" "$FAKE_SDK/usr/lib"
  [[ -f "$REAL_SDK/SDKSettings.json" ]] && cp "$REAL_SDK/SDKSettings.json" "$FAKE_SDK/" || true
  [[ -f "$REAL_SDK/SDKSettings.plist" ]] && cp "$REAL_SDK/SDKSettings.plist" "$FAKE_SDK/" || true
  n=0
  while IFS= read -r f; do
    perl -0pi -e 's/arm64-macos,\s*arm64e-macos/arm64e-macos/g; s/arm64e-macos/arm64-macos, arm64e-macos/g' "$f"
    n=$((n+1))
  done < <(find "$FAKE_SDK/usr/lib" -type f -name '*.tbd')
  echo ">> patched $n .tbd files (injected arm64-macos slice)"
fi

# --- 3. xcrun shim so Zig's native SDK detection uses the patched sysroot ----
mkdir -p "$SHIM"
cat > "$SHIM/xcrun" <<EOF
#!/bin/bash
for a in "\$@"; do
  case "\$a" in
    --show-sdk-path|--show-sdk-platform-path) echo "$FAKE_SDK"; exit 0;;
    --show-sdk-version) echo "26.5"; exit 0;;
  esac
done
exec /usr/bin/xcrun "\$@"
EOF
chmod +x "$SHIM/xcrun"

# --- 4. build libghostty-vt -------------------------------------------------
echo ">> building libghostty-vt in $GHOSTTY_VT_DIR"
export PATH="$SHIM:$ZIG_DIR:$PATH"
# The xcframework step needs full Xcode (xcodebuild); we only need the static
# lib + headers + pkgconfig, which are produced before it. Tolerate its failure.
( cd "$GHOSTTY_VT_DIR" && zig build -Demit-lib-vt -Doptimize=ReleaseFast ) || \
  echo ">> note: a late build step failed (likely the Apple xcframework); checking outputs..."

PC_DIR="$GHOSTTY_VT_DIR/zig-out/share/pkgconfig"
LIB="$GHOSTTY_VT_DIR/zig-out/lib/libghostty-vt.a"
if [[ -f "$LIB" && -f "$PC_DIR/libghostty-vt-static.pc" ]]; then
  echo
  echo ">> SUCCESS. Build the Go spikes with:"
  echo "   export PKG_CONFIG_PATH=$PC_DIR"
  echo "   go run ./cmd/vtspike"
  echo "   go run ./cmd/ptyspike"
else
  echo ">> FAILED: expected static lib + pkgconfig not found" >&2
  exit 1
fi
