# herdr-web — Phase B spike (go-libghostty integration)

**Date:** 2026-0619-2111
**Session ID:** `0bb3c185-84f8-4860-8885-3f63a2fac339`
**Project:** `~/projs/go/herdr-web` · references `~/projs/rust/herdr` (Rust source + vendored libghostty-vt)
**Branch:** `roh/phase-b`

---

## Goal

Start the **Phase B spike** from the migration roadmap: prove that Go can own the
terminal runtime (PTY + VT emulation) via **go-libghostty**, the cornerstone that lets
Phase B retire the Rust server's `src/pty`, `src/pane`, `src/ghostty`, `src/terminal`.
The known unknown going in was the **Zig / macOS-SDK-26.5 toolchain risk** the user
flagged in the Phase A session.

**Outcome: spike succeeds end-to-end on Apple Silicon / macOS 26.5.** Pure-Go VT
emulation works, per-cell grid readout works, and a real shell PTY pumped through the
emulator renders correctly. The toolchain risk was real, pinned to an exact mechanism,
and worked around.

---

## The toolchain risk — found, diagnosed, resolved

**Mechanism:** macOS 26.5 (Tahoe) **dropped the plain `arm64-macos` slice** from its
system `.tbd` linker stubs — they now list only `arm64e-macos` (+ x86_64). Zig 0.16
falls back arm64→arm64e, but **libghostty-vt pins Zig 0.15.x** (its `build.zig.zon`
`minimum_zig_version = "0.15.2"`, and `requireZig` enforces an exact major.minor match).
Zig 0.15.2 does **not** do the arm64→arm64e fallback, so its **native build-runner
binary** can't resolve a single libSystem symbol — even a hello-world fails to link
(`undefined symbol: __availability_version_check`, `_abort`, `_malloc`, …).

Diagnosis path (each step ruled something out):
- Zig 0.16.0 (brew) fails libghostty-vt at the *build.zig compile* stage
  (`std.Io.Dir.readFileAlloc` changed arity 0.15→0.16) → can't use 0.16.
- Zig 0.15.2 native hello-world fails to link libc → not a libghostty problem.
- `SDKROOT` env is ignored by Zig 0.15.2 native detection (it shells out to `xcrun`).
- `zig build-exe hello.zig -target aarch64-macos` (explicit target → Zig's **bundled**
  `libSystem.tbd`, which *has* `arm64-macos`) → **works**. Confirms the missing slice is
  the root cause. But `-Dtarget=` only affects the *output* lib, not the build-runner.
- Compared the two tbds: system `libSystem.tbd` targets = `[x86_64-macos, …, arm64e-macos,
  arm64e-maccatalyst]` (no plain arm64); Zig's bundled one includes `arm64-macos`.

**Fix (no system modification; all under gitignored `.tools/`):**
1. Download Zig **0.15.2** (verified sha256) to `.tools/zig-aarch64-macos-0.15.2/`.
2. Build a **patched copy of the SDK** at `.tools/MacOSX-arm64patch.sdk`: symlink
   `usr/include` + `System`, copy `usr/lib`, then inject `arm64-macos` into every `.tbd`
   (perl, idempotent: `s/arm64-macos,\s*arm64e-macos/arm64e-macos/g;
   s/arm64e-macos/arm64-macos, arm64e-macos/g`). ~395 tbds patched.
3. An **`xcrun` shim** (`.tools/shim/xcrun`) that returns the patched SDK path for
   `--show-sdk-path` (Zig native detection shells out to xcrun; SDKROOT was ignored).
4. Build: `PATH="$SHIM:$ZIG_DIR:$PATH" zig build -Demit-lib-vt -Doptimize=ReleaseFast`.

All captured idempotently in **`scripts/build-libghostty-vt.sh`**.

**Build result:** 73/75 steps succeed. Only failure is the Apple `xcframework` step
(`xcodebuild -create-xcframework`) — needs full Xcode (we have CommandLineTools only) and
is **irrelevant to CGO**. The static lib + headers + pkgconfig are produced before it:
- `zig-out/lib/libghostty-vt.a` (+ `.dylib`)
- `zig-out/include/ghostty/vt/*.h`
- `zig-out/share/pkgconfig/libghostty-vt-static.pc` (+ non-static)

Full build takes ~1 min wall (`~210s user`, parallel) after deps are fetched.

---

## go-libghostty integration (non-obvious bits)

- Module: `go.mitchellh.com/libghostty` (mirror: github.com/mitchellh/go-libghostty;
  source of truth tangled.org). Pinned version this session:
  `v0.0.0-20260528200934-790a3ff6e9f6`. **No API stability promised.**
- Links via **pkg-config**, **static by default** (`cgo_static.go`:
  `#cgo pkg-config: --static libghostty-vt-static` + `-DGHOSTTY_STATIC`). Build with
  `export PKG_CONFIG_PATH=<vt>/zig-out/share/pkgconfig`. Go's link step uses **system
  clang** (handles SDK 26.5 fine) — the Zig SDK problem is confined to building the `.a`.
  `-tags dynamic` switches to the shared lib if ever needed.
- Cell-grid readout API (iterator-based; canonical pattern from the package's own tests):
  `NewTerminal(WithSize(c,r))` → `term.VTWrite([]byte)` (also satisfies `io.Writer` via
  `Write`) → `rs := NewRenderState(); rs.Update(term)` → `rs.RowIterator(ri)` →
  `for ri.Next() { ri.Cells(rc); for rc.Next() { rc.AppendGraphemes(buf[:0]);
  rc.FgColor(); rc.BgColor() } }`. `FgColor`/`BgColor` return `*ColorRGB` (nil = use
  terminal default). Plain/VT/HTML readback via `NewFormatter` + `FormatString`.

---

## What was built (branch `roh/phase-b`, NOT committed)

```
cmd/vtspike/main.go              drive a go-libghostty terminal in Go; read back plain
                                 text AND per-cell glyph+fg/bg (proves Phase B cell path)
cmd/ptyspike/main.go             spawn a real /bin/sh PTY (creack/pty), io.Copy its
                                 output into the emulator, dump the grid via formatter
scripts/build-libghostty-vt.sh   reproducible libghostty-vt build w/ the SDK-26 workaround
README.md                        added Phase B spike section + toolchain note; roadmap
.gitignore                       added .tools/
go.mod                           + go.mitchellh.com/libghostty, + github.com/creack/pty
```

### Verified output
- `vtspike`: plain readback (`Hello, world!` / `plain line two` / `RED text`) +
  per-cell colors — `world` = `#b5bd68` (green), `RED` = `#cc6666` (red), default palette.
- `ptyspike`: 80×24 grid after a real shell; `tput cols` returned `80` (confirms a real
  TTY with correct winsize); grid read back correctly.
- `CGO_ENABLED=1 go build ./...`, `go vet ./...`, `gofmt` all clean.

---

## How to reproduce

```bash
cd ~/projs/go/herdr-web
./scripts/build-libghostty-vt.sh    # downloads Zig 0.15.2, patches SDK, builds the .a
export PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig
go run ./cmd/vtspike
go run ./cmd/ptyspike
```

`GHOSTTY_VT_DIR` env overrides the vendored libghostty-vt source location.

---

## State at end of session

- All four spike tasks completed; build/vet/gofmt clean.
- Nothing committed (waiting on the user); Rust repo untouched except `zig build` writing
  `zig-out/` + `zig-pkg/` under its vendored libghostty-vt (build artifacts, not source).
- libghostty-vt vendored at **v1.3.2, commit `0f7cd84b`** (per
  `vendor/libghostty-vt.vendor.json`) — pin this when wrapping it.

## Next steps (Phase B proper)

- A Go **terminal-runtime package** wrapping go-libghostty behind an interface, with the
  upstream **commit pinned** (mirror the existing `vendor/libghostty-vt.vendor.json` style).
- Coverage: resize, scrollback, OSC-8 hyperlinks, cursor shape — diff Go-rendered cells
  against the Rust path for the same input stream (the plan's Phase B verification).
- Define the **Go↔Rust orchestration seam**: Rust keeps workspace/pane tree, layout,
  detection, session; Go reports pane cell grids and receives layout + input routing.
- Consider committing the libghostty-vt build into CI as a cached artifact (the SDK patch
  + Zig download make a clean build ~1 min after deps cache).
