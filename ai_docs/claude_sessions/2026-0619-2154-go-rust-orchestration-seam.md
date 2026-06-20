# herdr-web — Phase B: Go↔Rust orchestration seam

**Date:** 2026-0619-2154
**Session ID:** `0bb3c185-84f8-4860-8885-3f63a2fac339`
**Project:** `~/projs/go/herdr-web` · references `~/projs/rust/herdr` (Rust source + vendored libghostty-vt)
**Branch:** `roh/phase-b`

> Continues `2026-0619-2123-libghostty-go-integration.md` (same session). That doc
> covers the Phase B spike + the `internal/terminal` Emulator. This one covers the
> **orchestration seam** built after it. Non-overlapping; read 2123 first for the
> toolchain/Emulator context.

---

## Goal

Build the **Go↔Rust orchestration seam** — the next Phase B step. Decision (from
the migration plan): **Rust stays the orchestrator** (workspace/pane tree, BSP
layout, agent detection, session, and compositing the whole UI into the cell grid
it already ships to clients); **Go becomes the terminal backend** (PTY + VT
emulation per pane). Rust sends commands; Go runs a PTY + `Emulator` per pane and
reports cell-grid frames. This keeps the Phase A client wire protocol untouched —
only the *source* of pane-content cells moves to Go.

**Done.** Protocol + Host + daemon implemented and tested (both build modes,
`-race`). The Rust side is intentionally untouched; this session delivers the Go
side + the contract doc the Rust side will implement.

---

## Grounding (Rust architecture, via Explore agent)

Mapped the real Rust pane/PTY model so the protocol matches reality (file:line in
`~/projs/rust/herdr`):

- **Pane ID** = `PaneId(u32)`, global atomic counter (`layout.rs:8`).
- **Sizing** = `(rows u16, cols u16, cell_width_px u32, cell_height_px u32)`
  (`pane.rs:2152` resize). PTY spawned via `portable-pty`, `PtySize{rows,cols,0,0}`
  (`pty/backend/unix.rs:12`), env `TERM=xterm-256color` `COLORTERM=truecolor` + HERDR vars.
- **Cell grid Rust consumes** (`wire.rs:407` `CellData`): `{symbol:String, fg:u32,
  bg:u32, modifier:u16 (ratatui bits), skip:bool, hyperlink:Option<u32>}`; colors
  packed `0x02_RR_GG_BB` (`wire.rs:651` `color_to_u32`); `CursorState{x,y,visible,shape(DECSCUSR)}`.
- **The seam** = the PTY read callback `on_read` → `terminal.process_pty_bytes`
  (`pane.rs:1689`). Today it feeds the Rust emulator; in Phase B Go owns the PTY
  read loop and returns cells. Terminal query-responses (DA/DSR) currently flow
  back to the PTY (`terminal_responses`) — in the Go design they stay inside Go.
- **IPC convention**: length-prefixed `[u32-LE len][payload]`, 2 MiB cap, bincode
  (`wire.rs:768`). The new seam reuses the length-prefix convention.

---

## Design (the contract: `ai_docs/phase-b-orchestration-seam.md`)

- **Transport**: a dedicated Unix socket; `Host.Serve(conn)` is transport-agnostic.
- **Framing**: `[u32-LE length][JSON payload]`, 8 MiB cap. **JSON** chosen for the
  seam (new, internal, local, debuggable; the bandwidth path is diffed and can go
  binary later without changing message shapes).
- **Commands (Rust→Go)**: `hello`, `create_pane{pane_id,cols,rows,cell_px,cwd,command,args,env}`,
  `input{pane_id,data(base64)}`, `resize{...}`, `close_pane{pane_id}`.
- **Events (Go→Rust)**: `welcome`, `pane_frame{pane_id,frame}`, `pane_exited{pane_id,exit_code}`, `error`.
- **Frame** mirrors Rust `FrameData`/`CellData` so Rust splices pane grids straight
  into its compositor: packed `0x02_RR_GG_BB` colors (nil fg/bg resolved to
  snapshot defaults → concrete RGB), ratatui modifier bits
  (`BOLD=1,DIM=2,ITALIC=4,UNDERLINED=8,REVERSED=64,CROSSED_OUT=256`), `skip` for
  unchanged cells (diff), DECSCUSR cursor shape (block=2, underline=4, bar=6).
- **Deferred** (documented): input-encoding ownership (Rust still encodes keys →
  raw bytes for now), OSC passthrough (title/cwd/clipboard/hyperlinks), Kitty
  graphics, scrollback/selection, binary cell encoding.

---

## What was built — commit `c22bfdc`

```
internal/orchestration/protocol.go       (pure Go) message types, JSON codec
                                         (u32-LE, 8 MiB), Snapshot->Frame + diff
internal/orchestration/protocol_test.go  (pure Go) codec round-trip, color pack,
                                         modifier bits, full/diff frame
internal/orchestration/host.go           (-tags ghostty) Host: pane mgmt + serve loop
internal/orchestration/host_test.go      (-tags ghostty) e2e over net.Pipe
cmd/termhost/main.go                     (-tags ghostty) Unix-socket daemon
internal/terminal/ghostty.go             + WithWritePTY option
ai_docs/phase-b-orchestration-seam.md    full design (the Rust-side contract)
README.md, scripts/build-libghostty-vt.sh  updated
```

### Host design (non-obvious bits)
- A **pane** = PTY (`creack/pty`) + `terminal.Emulator` + child `*exec.Cmd`.
- **Read pump** (per pane): `ptmx.Read` → `emu.Write` → mark dirty. On EOF/EIO
  (child exit) → emit a final `pane_frame` then `pane_exited`.
- **Flusher** (~60 Hz ticker, mirrors Phase A rAF coalescing): snapshots dirty
  panes, diffs vs the last sent snapshot, emits `pane_frame`.
- **WritePTY callback**: `terminal.WithWritePTY` wires the emulator's query
  responses back to the pane's PTY — so DA/DSR/etc. are handled entirely in Go.
- **Concurrency** (passes `-race`): `emuMu` serializes all emulator access (Write,
  Snapshot, Resize, Close) — the emulator is not concurrency-safe — and guards
  `prev`/`closed`; `ptyMu` serializes PTY writes (user input + WritePTY callback);
  the panes map is mutex-guarded; outbound events go through a single writer
  goroutine via a buffered channel + `done` signal.

### Key design choice (consistent with prior session)
- **All CGO behind `-tags ghostty`.** `protocol.go` + `Snapshot→Frame` conversion
  are pure Go and tested **without** the toolchain (CI-friendly). The `Host`,
  daemon, and host test are tagged.

### Verified
- Default (no toolchain): `go build ./...`, `go vet ./...`,
  `go test ./internal/orchestration/` (protocol/diff/codec) all pass.
- `-tags ghostty`: build, vet, and `go test -race ./internal/...` pass — including
  the e2e Host test (run `printf` command and see the frame; `cat` input echo;
  close → `pane_exited`).
- `cmd/termhost` smoke: listens on the socket, serves, cleans up on SIGTERM.
- gofmt clean.

---

## How to run

```bash
cd ~/projs/go/herdr-web
export PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig
go test ./internal/orchestration/                 # protocol/diff (no toolchain)
go test -tags ghostty -race ./internal/...        # + end-to-end Host
go run  -tags ghostty ./cmd/termhost --socket /tmp/herdr-termhost.sock
# Phase A stays toolchain-free:
go build ./...
```

---

## State at end of session

- 12 tasks completed across the session (spike 4, terminal runtime 4, seam 4).
- Branch `roh/phase-b`, **4 commits, nothing pushed**:
  `f34eaf6` spike · `6d44a58` terminal runtime · `8c2e577` docs consolidation ·
  `c22bfdc` orchestration seam.
- Rust repo untouched except `zig build` artifacts under its vendored libghostty-vt.

## Next steps (the Rust side — outside this repo)

- Implement the seam in the Rust server: connect to `termhost`, send
  `create_pane`/`input`/`resize`/`close_pane`, and **splice `pane_frame` cells
  into the compositor** in place of `process_pty_bytes` per-pane rendering
  (`pane.rs:1689`). Retire `src/pty`/`src/ghostty`/`src/terminal` for that path.
- **OSC passthrough** as new Go→Rust events (`pane_title`/`pane_cwd`/
  `pane_clipboard`) — needed for chrome + agent detection.
- **Input encoding** could move to Go (go-libghostty key/mouse encoders) so Rust
  sends structured events instead of raw bytes.
- **Scrollback/selection** exposure; **Kitty graphics**; **binary cell encoding**
  if JSON frame bulk shows up in profiling.
- **CI**: cache the libghostty-vt `.a` so `-tags ghostty` tests run in CI.
- A project memory for the macOS-26.5/Zig-0.15 tbd finding if it recurs (currently
  documented in the build script + README + 2123 session notes).
```
