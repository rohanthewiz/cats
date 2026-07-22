#!/usr/bin/env bash
# Build the vendored libghostty-vt (third_party/libghostty-vt) for the Go
# terminal path and print the PKG_CONFIG_PATH needed for `-tags ghostty`
# builds. Works on macOS (arm64/x86_64) and Linux (x86_64/aarch64) — the same
# script CI runs.
#
# macOS 26.x / Apple Silicon workaround:
#   libghostty-vt pins Zig 0.15.x. But the macOS 26.5 SDK dropped the plain
#   `arm64-macos` slice from its system .tbd stubs (only `arm64e-macos`
#   remains), and Zig 0.15.2 does NOT fall back arm64 -> arm64e, so its native
#   build-runner fails to link libSystem. Workaround: build a patched copy of
#   the SDK with the `arm64-macos` slice re-injected into every .tbd, and
#   point Zig at it via an `xcrun` shim. The patch is idempotent and harmless
#   on older SDKs that still carry the slice. (Zig 0.16 handles SDK 26.5 fine,
#   but libghostty-vt requires 0.15.x.)
#
# Idempotent: re-running reuses an existing Zig download / patched SDK.
# Lazy Zig package deps (uucode is pre-vendored in zig-pkg/; a few others are
# fetched from deps.files.ghostty.org on first build) land in the Zig global
# cache — CI caches that keyed on build.zig.zon.
set -euo pipefail

# --- config -----------------------------------------------------------------
ZIG_VERSION="0.15.2"

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# Vendored libghostty-vt source. Override with env for an external checkout.
GHOSTTY_VT_DIR="${GHOSTTY_VT_DIR:-$ROOT/third_party/libghostty-vt}"
TOOLS="$ROOT/.tools"

os="$(uname -s)"
arch="$(uname -m)"
case "$os/$arch" in
  Darwin/arm64)  ZIG_TARGET="aarch64-macos"; ZIG_SHA256="3cc2bab367e185cdfb27501c4b30b1b0653c28d9f73df8dc91488e66ece5fa6b" ;;
  Darwin/x86_64) ZIG_TARGET="x86_64-macos";  ZIG_SHA256="375b6909fc1495d16fc2c7db9538f707456bfc3373b14ee83fdd3e22b3d43f7f" ;;
  Linux/x86_64)  ZIG_TARGET="x86_64-linux";  ZIG_SHA256="02aa270f183da276e5b5920b1dac44a63f1a49e55050ebde3aecc9eb82f93239" ;;
  Linux/aarch64) ZIG_TARGET="aarch64-linux"; ZIG_SHA256="958ed7d1e00d0ea76590d27666efbf7a932281b3d7ba0c6b01b0ff26498f667f" ;;
  *) echo "unsupported platform: $os/$arch" >&2; exit 1 ;;
esac
ZIG_DIR="$TOOLS/zig-$ZIG_TARGET-$ZIG_VERSION"

mkdir -p "$TOOLS"

# --- 1. Zig -----------------------------------------------------------------
if [[ ! -x "$ZIG_DIR/zig" ]]; then
  echo ">> downloading Zig $ZIG_VERSION ($ZIG_TARGET)"
  tarball="$TOOLS/zig-$ZIG_TARGET-$ZIG_VERSION.tar.xz"
  curl -fsSL -o "$tarball" \
    "https://ziglang.org/download/$ZIG_VERSION/zig-$ZIG_TARGET-$ZIG_VERSION.tar.xz"
  if command -v shasum >/dev/null; then
    got="$(shasum -a 256 "$tarball" | awk '{print $1}')"
  else
    got="$(sha256sum "$tarball" | awk '{print $1}')"
  fi
  [[ "$got" == "$ZIG_SHA256" ]] || { echo "checksum mismatch: $got" >&2; exit 1; }
  tar -xf "$tarball" -C "$TOOLS"
fi
echo ">> zig: $("$ZIG_DIR/zig" version)"
export PATH="$ZIG_DIR:$PATH"

# --- 2. macOS only: arm64-patched SDK sysroot + xcrun shim -------------------
if [[ "$os" == "Darwin" ]]; then
  REAL_SDK="$(xcrun --show-sdk-path)"
  FAKE_SDK="$TOOLS/MacOSX-arm64patch.sdk"
  SHIM="$TOOLS/shim"
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
  sdk_version="$(xcrun --show-sdk-version)"
  mkdir -p "$SHIM"
  cat > "$SHIM/xcrun" <<EOF
#!/bin/bash
for a in "\$@"; do
  case "\$a" in
    --show-sdk-path|--show-sdk-platform-path) echo "$FAKE_SDK"; exit 0;;
    --show-sdk-version) echo "$sdk_version"; exit 0;;
  esac
done
exec /usr/bin/xcrun "\$@"
EOF
  chmod +x "$SHIM/xcrun"
  export PATH="$SHIM:$PATH"
fi

# --- 3. build libghostty-vt --------------------------------------------------
# -Dversion-string pins ghostty's own version (from its build.zig.zon) so its
# build.zig skips git detection — otherwise it walks up into THIS repo and
# panics when cats has a vX.Y.Z tag checked out (release builds).
echo ">> building libghostty-vt in $GHOSTTY_VT_DIR"
( cd "$GHOSTTY_VT_DIR" && \
  zig build -Demit-lib-vt -Demit-exe=false -Demit-xcframework=false \
    -Dversion-string=1.3.2-dev -Doptimize=ReleaseFast )

PC_DIR="$GHOSTTY_VT_DIR/zig-out/share/pkgconfig"
LIB="$GHOSTTY_VT_DIR/zig-out/lib/libghostty-vt.a"
if [[ -f "$LIB" && -f "$PC_DIR/libghostty-vt-static.pc" ]]; then
  echo
  echo ">> SUCCESS. Build the Go code with -tags ghostty:"
  echo "   export PKG_CONFIG_PATH=$PC_DIR"
  echo "   make test-ghostty   # or: go test -tags ghostty ./..."
else
  echo ">> FAILED: expected static lib + pkgconfig not found" >&2
  exit 1
fi
