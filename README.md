# herdr-web

A Go + web presentation tier for [herdr](https://herdr.dev). This is **Phase A** of
an incremental migration of herdr off Rust onto a Go stack (rweb / Element / serr /
logger), presented through the browser.

Phase A attaches to an **unmodified, already-running `herdr server`** as a wire-protocol
client, receives herdr's fully-rendered semantic frames, and streams them to a browser
canvas. No Rust build and no Zig toolchain are involved — the installed `herdr` binary
does all terminal emulation and rendering; this gateway is a thin, language-agnostic
client + web renderer.

## Status

| Piece | State |
|-------|-------|
| bincode v2 `standard` codec (`internal/wire`) | ✅ hand-written, validated against live server |
| Wire messages: Hello / Input / Resize / Detach, Welcome / Frame / Shutdown | ✅ |
| Color + modifier decode (`color.go`) | ✅ named / 256-palette / RGB → CSS |
| herdr connection wrapper (`internal/herdrconn`) | ✅ handshake + typed send/recv |
| rweb gateway: page + `/ws` WebSocket bridge (`cmd/gateway`) | ✅ one herdr client per browser tab |
| Browser canvas renderer + keyboard input (`cmd/gateway/web/index.html`) | ✅ renders frames; key→bytes mapping |
| **Frame diffing** (gateway sends only changed cells) | ✅ full frame ~53 KB → steady-state diffs ~100 B |
| **Mouse input** (SGR 1006) gated on server `MouseCapture` | ✅ MouseCapture decodes; browser sends drag/wheel/click |
| **Clipboard**: herdr→browser copy (OSC 52) | ✅ |
| **Paste**: ⌘V text (`InputEvents::Paste`) / Ctrl+V image (`ClipboardImage` → staged file path) | ✅ verified end-to-end into Claude Code |
| **OSC 8 hyperlinks** (click-to-open when mouse not captured) | ✅ |
| **Window title** + **notify toasts** | ✅ |
| Kitty graphics passthrough | ⏳ deferred |
| Headless end-to-end verification (`cmd/wsprobe`, `cmd/smoke`) | ✅ handshake, frame, diffs, mouse-capture confirmed |
| Browser→herdr input/mouse/paste exercised against a live session | ⏳ coded; not injected into the real session (gated) |

The installed herdr 0.7.0 server speaks **protocol 14**; `internal/wire.ProtocolVersion`
matches. Proto 14 inserted `ServerMessage::WindowTitle` at index 7, shifting `MouseCapture`
to 9 — handled in `internal/wire`. The server renders per-client at each client's requested
size, so attaching a web client does not resize other clients' views.

## Run

```bash
# Build
go build ./...

# Smoke-test the protocol directly against a running herdr server (read-only):
go run ./cmd/smoke --socket ~/.config/herdr/herdr-client.sock --frames 2

# Serve herdr in the browser:
go run ./cmd/gateway --addr :8420
# then open http://localhost:8420
```

### Starting the web client (step by step)

1. **Have a `herdr server` running.** The gateway is a thin client — it attaches to an
   already-running herdr session over a Unix socket; it does not start herdr itself.
2. **Start the gateway:**

   ```bash
   go run ./cmd/gateway --addr :8420
   ```

3. **Open `http://localhost:8420`** in your browser.

`--socket` defaults to `~/.config/herdr/herdr-client.sock` (the default session). The
gateway attaches to whatever session that socket belongs to, so the browser controls that
live session — keystrokes reach its focused pane.

> **Note:** `web/index.html` is embedded into the gateway binary at compile time
> (`//go:embed`). After editing it, **restart the gateway** (`go run` recompiles and
> re-embeds) — a browser reload alone will keep serving the old page.

### Headless verification

```bash
# Full browser↔gateway↔herdr frame round-trip, no browser needed (read-only):
go run ./cmd/wsprobe --frames 1
# add --send-input to also exercise the keyboard path (reaches the focused pane!)
```

## Layout

```
internal/wire/        bincode codec, wire messages, framing, color decode
internal/herdrconn/   herdr client connection (handshake, send/recv)
internal/terminal/    Phase B: Go-owned VT emulator (Emulator iface + go-libghostty)
internal/orchestration/  Phase B: Go↔Rust seam (protocol + terminal-backend Host)
cmd/gateway/          rweb web server + WebSocket bridge + embedded canvas UI
cmd/smoke/            direct protocol smoke test (no web)
cmd/wsprobe/          stdlib-only WebSocket client for end-to-end verification
cmd/vtspike/          Phase B spike: drive a go-libghostty terminal in Go, read cells
cmd/ptyspike/         Phase B spike: shell PTY -> internal/terminal.Emulator -> grid
cmd/termhost/         Phase B: terminal-backend daemon (orchestration Host over a socket)
scripts/build-libghostty-vt.sh   build libghostty-vt (Zig 0.15.2 + macOS-26 SDK patch)
```

The CGO terminal backend is behind the `ghostty` build tag, so the Phase A
gateway still builds with a plain `go build ./...` (no Zig toolchain, no
libghostty-vt). Only the Phase B code (`internal/terminal`, the spikes) needs
`-tags ghostty` + `PKG_CONFIG_PATH`.

## Phase B spike (go-libghostty)

A proof-of-concept that Go can own PTY + VT emulation via
[go-libghostty](https://github.com/mitchellh/go-libghostty) is in `cmd/vtspike`
(drive a terminal, read back per-cell glyph + fg/bg) and `cmd/ptyspike` (spawn a
real shell PTY, pump it through the emulator, dump the grid). Both work on Apple
Silicon / macOS 26.5. To build:

```bash
./scripts/build-libghostty-vt.sh          # builds libghostty-vt, prints PKG_CONFIG_PATH
export PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig
go test -tags ghostty ./internal/terminal/   # Emulator round-trip tests
go run  -tags ghostty ./cmd/vtspike
go run  -tags ghostty ./cmd/ptyspike
```

The reusable piece is `internal/terminal`: an `Emulator` interface (`io.Writer`
in, `Snapshot` of cells + cursor out) with a go-libghostty-backed implementation.
The Phase B browser renderer will consume `Snapshot` the way the Phase A gateway
consumes wire `FrameData`.

### Go↔Rust orchestration seam

`internal/orchestration` is the seam where Go becomes the **terminal backend**
and Rust stays the **orchestrator** (workspace/pane tree, layout, detection,
session, compositing). Rust sends commands (`create_pane` / `input` / `resize` /
`close_pane`); Go runs a PTY + `Emulator` per pane and sends events
(`pane_frame` / `pane_exited`). Frames are shaped to drop into Rust's
`wire::FrameData`/`CellData` compositing (packed colors, ratatui modifier bits,
`skip` diffing). The `Host` serves this over any connection; `cmd/termhost` is a
Unix-socket daemon wrapping it. The protocol + frame conversion are pure Go
(tested without the toolchain); the `Host` is behind `-tags ghostty`.

Full design: [`ai_docs/phase-b-orchestration-seam.md`](ai_docs/phase-b-orchestration-seam.md).

```bash
go test ./internal/orchestration/                    # protocol/diff (no toolchain)
go test -tags ghostty ./internal/orchestration/      # + end-to-end Host
go run  -tags ghostty ./cmd/termhost --socket /tmp/herdr-termhost.sock
```

**Toolchain note (the Zig/SDK risk, now resolved):** libghostty-vt pins Zig
0.15.x, but the macOS 26.5 SDK dropped the plain `arm64-macos` slice from its
`.tbd` stubs (only `arm64e-macos` remains) and Zig 0.15.2 doesn't fall back
arm64→arm64e, so its native build fails to link libSystem. The build script
works around this by patching a copy of the SDK to re-add the `arm64-macos`
slice and pointing Zig at it via an `xcrun` shim. Zig itself is downloaded to
`.tools/` (gitignored); no system changes are made.

## What's next (migration roadmap)

- **Phase A polish:** mouse/wheel input, OSC 8 hyperlinks, Kitty graphics passthrough,
  clipboard (OSC 52), frame diffing to cut bandwidth, per-tab isolation.
- **Phase B:** move PTY + VT emulation into Go via `go.mitchellh.com/libghostty`
  (go-libghostty), shrinking the Rust surface. **Spike done** (see above): the
  toolchain builds and both the cell-grid and shell-PTY paths work end-to-end on
  macOS 26.5. Done since: `internal/terminal` (Emulator wrapping go-libghostty)
  and `internal/orchestration` (the Go↔Rust seam + terminal-backend Host /
  `termhost` daemon). Remaining: the Rust side of the seam (drive panes through
  `termhost`, splice Go pane frames into Rust compositing), OSC passthrough
  (title/cwd/clipboard/hyperlinks), scrollback/selection, Go-side input encoding.
- **Phase C:** port herdr's portable logic (app state, BSP layout, agent detection,
  session/workspace) to Go and retire the Rust core.
