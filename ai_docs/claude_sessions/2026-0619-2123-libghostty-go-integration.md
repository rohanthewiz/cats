# herdr-web — Phase B: go-libghostty integration (spike + terminal runtime)

**Date:** 2026-0619-2123
**Session ID:** `0bb3c185-84f8-4860-8885-3f63a2fac339`
**Project:** `~/projs/go/herdr-web` · references `~/projs/rust/herdr` (Rust source + vendored libghostty-vt)
**Branch:** `roh/phase-b` · 2 commits this session, nothing pushed

> Supersedes the earlier `2026-0619-2111-libghostty-go-integration.md` snapshot
> (same session). This one also covers the `internal/terminal` package built after
> the spike.

---

## Goal

Execute **Phase B** of the herdr → Go/web migration: move the terminal runtime
(PTY + VT emulation) off the Rust server and into Go via **go-libghostty**. Two
stages happened this session:

1. **Spike** — prove the toolchain builds and Go can drive a terminal end to end.
2. **Phase B proper (first slice)** — a reusable `internal/terminal` package
   wrapping go-libghostty behind a stable interface, with the CGO confined so the
   Phase A gateway still builds toolchain-free.

**Both done.** Build/vet/test green in both modes; the full shell-PTY → Go
emulator → cell-grid path works on Apple Silicon / macOS 26.5.

---

## The toolchain risk — found, diagnosed, resolved

**Mechanism:** macOS 26.5 (Tahoe) **dropped the plain `arm64-macos` slice** from
its system `.tbd` linker stubs — they now list only `arm64e-macos` (+ x86_64).
Zig 0.16 falls back arm64→arm64e, but **libghostty-vt pins Zig 0.15.x**
(`build.zig.zon` `minimum_zig_version = "0.15.2"`; `requireZig` enforces an exact
major.minor match). Zig 0.15.2 does **not** fall back, so its native build-runner
can't resolve a single libSystem symbol — even a hello-world fails to link
(`undefined symbol: __availability_version_check`, `_abort`, `_malloc`, …).

Diagnosis path: Zig 0.16 fails libghostty-vt at the build.zig *compile* stage
(`std.Io.Dir.readFileAlloc` arity changed 0.15→0.16) → can't use it; Zig 0.15.2
native hello-world fails to link → not a libghostty problem; `SDKROOT` env is
ignored (Zig shells out to `xcrun`); `-target aarch64-macos` (explicit → Zig's
**bundled** tbd, which has `arm64-macos`) works → confirms the missing slice is
root cause, but `-Dtarget=` only affects the *output* lib, not the build-runner.

**Fix (no system changes; all under gitignored `.tools/`):**
1. Download Zig **0.15.2** (sha256-verified) to `.tools/zig-aarch64-macos-0.15.2/`.
2. Build an **arm64-patched SDK** at `.tools/MacOSX-arm64patch.sdk`: symlink
   `usr/include` + `System`, copy `usr/lib`, inject `arm64-macos` into every
   `.tbd` (perl, idempotent). ~395 tbds patched.
3. An **`xcrun` shim** (`.tools/shim/xcrun`) returning the patched SDK path for
   `--show-sdk-path` (SDKROOT was ignored; Zig shells out to xcrun).
4. `PATH="$SHIM:$ZIG_DIR:$PATH" zig build -Demit-lib-vt -Doptimize=ReleaseFast`.

All in **`scripts/build-libghostty-vt.sh`** (idempotent). Build = 73/75 steps;
the only failure is the Apple `xcframework` step (needs full Xcode, irrelevant to
CGO). Produces `zig-out/lib/libghostty-vt.a` + headers + `share/pkgconfig/*.pc`.

---

## go-libghostty integration notes (non-obvious)

- Module `go.mitchellh.com/libghostty`, pinned `v0.0.0-20260528200934-790a3ff6e9f6`
  (commit `790a3ff6e9f6`). **No API stability promised** → confined to one file.
- Links via **pkg-config**, **static by default** (`#cgo pkg-config: --static
  libghostty-vt-static` + `-DGHOSTTY_STATIC`). `export PKG_CONFIG_PATH=<vt>/zig-out/share/pkgconfig`.
  Go's link step uses **system clang** (handles SDK 26.5 fine) — the Zig SDK
  problem is confined to building the `.a`.
- Cell-grid readback: `NewTerminal(WithSize)` → `term.Write`/`VTWrite` →
  `rs.Update(term)` → `rs.RowIterator(ri)` → `for ri.Next(){ ri.Cells(rc);
  for rc.Next(){ rc.AppendGraphemes(buf[:0]); rc.StyleInto(&style) } }`.
  `StyleInto` fills fg/bg + has-flags + bold/faint/italic/underline/strike/inverse
  in one call. Cursor: `rs.CursorViewportX/Y`, `CursorVisible`, `CursorVisualStyle`.
  Defaults: `rs.Colors()`. Dimensions: `rs.Cols()/Rows()`.

---

## What was built (branch `roh/phase-b`)

### Commit 1 — `feat: Phase B spike — Go-owned terminals via go-libghostty` (f34eaf6)
```
cmd/vtspike/main.go              drive a terminal in Go; read plain text + per-cell glyph/fg/bg
cmd/ptyspike/main.go             spawn /bin/sh PTY (creack/pty), io.Copy into the emulator
scripts/build-libghostty-vt.sh   reproducible libghostty-vt build w/ the SDK-26 workaround
README.md, .gitignore (.tools/), go.mod (+libghostty, +creack/pty)
```

### Commit 2 — `feat: internal/terminal Emulator (Phase B terminal runtime)` (6d44a58)
```
internal/terminal/terminal.go    Emulator interface + Snapshot/Cell/Cursor/Color (pure Go, untagged)
internal/terminal/ghostty.go     go-libghostty-backed impl (//go:build ghostty)
internal/terminal/ghostty_test.go  round-trip tests (//go:build ghostty)
cmd/vtspike, cmd/ptyspike        now //go:build ghostty; ptyspike refactored onto Emulator
README.md, scripts/...           document the -tags ghostty requirement
```

**Key design decision:** **all CGO is behind the `ghostty` build tag.** Default
`go build ./...` stays toolchain-free for Phase A (no Zig, no libghostty-vt);
Phase B builds with `-tags ghostty` + `PKG_CONFIG_PATH`. The `Emulator` interface
+ `Snapshot` value types are pure Go and always compile.

`Snapshot` carries: `Cols/Rows`, `[][]Cell` (each `Cell` = grapheme + `*Color`
fg/bg + bold/faint/italic/underline/strikethrough/inverse), `Cursor`
(X/Y/Visible/Style), and `DefaultFg/DefaultBg`. It's the Phase B analogue of wire
`FrameData` — the browser renderer will consume it the way the Phase A gateway
consumes `FrameData`.

### Verified
- Default build/vet **toolchain-free**: `go build ./...` + `go vet ./...` clean
  with `PKG_CONFIG_PATH` unset (confirmed a fresh cgo build *does* fail without it,
  hence the tag).
- `-tags ghostty`: build, vet, and `go test ./internal/terminal/` all pass
  (7 tests: dimensions, content, fg/bg, style flags, cursor, resize).
- `ptyspike`: real shell → Emulator → Snapshot; red `#cc6666`, blue `#81a2be`,
  cursor (0,4). `vtspike`: raw-API readback unchanged.
- gofmt clean.

---

## How to reproduce

```bash
cd ~/projs/go/herdr-web
./scripts/build-libghostty-vt.sh    # downloads Zig 0.15.2, patches SDK, builds the .a
export PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig
go test -tags ghostty ./internal/terminal/
go run  -tags ghostty ./cmd/ptyspike
# Phase A stays toolchain-free:
go build ./...
```
`GHOSTTY_VT_DIR` env overrides the vendored libghostty-vt source location.

---

## State at end of session

- 8 tasks completed (4 spike + 4 terminal-runtime). Build/vet/test/gofmt clean.
- 2 commits on `roh/phase-b`; **nothing pushed**.
- libghostty-vt vendored at **v1.3.2, commit `0f7cd84b`** (per
  `vendor/libghostty-vt.vendor.json`). go-libghostty pinned in go.mod.
- Rust repo untouched except `zig build` writing `zig-out/`+`zig-pkg/` under its
  vendored libghostty-vt (artifacts, not source).

## Next steps (rest of Phase B — needs design, not just code)

- **Go↔Rust orchestration seam** (the big one): Rust keeps workspace/pane-tree,
  layout, detection, session; Go runs the `Emulator` per pane and reports
  `Snapshot`s, receiving layout + input routing. Design this protocol next.
- **Browser renderer adapter:** map `Snapshot` → the gateway's existing JSON cell
  format so the Phase A canvas renderer can draw Go-emulated panes.
- **Coverage parity:** OSC-8 hyperlinks, scrollback, cursor-shape, wide/grapheme
  edge cases — diff Go-rendered cells vs the Rust path for the same input stream
  (the plan's Phase B verification).
- **Input path:** key/mouse encoding into the PTY (go-libghostty has
  key_encoder/mouse_encoder; or encode in Go and write to the PTY).
- **CI:** cache the libghostty-vt `.a` (build is ~1 min after deps) so
  `-tags ghostty` tests run in CI.
- Consider a project memory for the macOS-26.5 / Zig-0.15 tbd finding if it keeps
  recurring (currently documented in the build script + README).
```
