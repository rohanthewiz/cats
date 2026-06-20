# Phase B: the Go↔Rust orchestration seam

Status: **interim design + Go side implemented** (`internal/orchestration`).
The Rust side is not built yet; this doc is the contract it will implement.

## Goal

In Phase B, Go takes over **PTY + VT emulation per pane**. Rust keeps everything
else: the workspace/pane tree, BSP layout, agent detection, session persistence,
and **compositing** the whole herdr UI (sidebar, tab bar, pane content) into the
semantic cell grid it already ships to clients over the Phase A wire protocol.

So the seam is narrow: **Rust is the orchestrator, Go is the terminal backend.**
Rust tells Go "run this pane at this size / here's input / resize / close"; Go
tells Rust "here is this pane's cell grid / the pane exited."

```
            ┌────────────────────── Rust server ──────────────────────┐
 clients ◀──┤ wire protocol (Phase A, unchanged)                       │
            │ workspace · pane tree · BSP layout · detection · session │
            │ COMPOSITES pane grids + chrome → FrameData               │
            └───────────────▲───────────────────┬─────────────────────┘
                            │ PaneFrame          │ CreatePane/Input/Resize/Close
                            │ (cells+cursor)     ▼
            ┌───────────────┴──────────── Go terminal host ────────────┐
            │ internal/orchestration.Host                              │
            │   pane = PTY (creack/pty) + Emulator (go-libghostty)     │
            │   read pump: PTY → Emulator → dirty                      │
            │   query responses: Emulator → PTY (WritePTY callback)    │
            └─────────────────────────────────────────────────────────┘
```

This replaces, in the Rust server, the per-pane work in `src/pty`, `src/ghostty`,
`src/terminal` and the read callback at `src/pane.rs` (`process_pty_bytes`). Rust's
client-facing wire protocol is untouched — only the *source* of pane cells moves.

## Why Rust still composites (interim)

The Phase A finding is that the Rust server renders the **entire** UI into one
cell grid. Re-pointing only the pane-content cells at Go is a contained change;
moving compositing to Go would touch the whole `src/ui` tree. So for the interim
seam Rust composites, splicing Go-reported pane grids into its frame. Compositing
moves to Go in Phase C when the Rust core is retired.

## Transport

- **Channel:** a dedicated Unix domain socket (separate from the client wire
  socket), or any `net.Conn` / `io.ReadWriteCloser`. The Go `Host.Serve(conn)`
  is transport-agnostic.
- **Framing:** `[u32-LE length][payload]`, max 8 MiB per frame — matches the
  existing wire convention (`internal/wire`), with a larger cap than the 2 MiB
  client frames because a host-side frame is a single pane (smaller than a full
  composited UI) but we leave headroom for large grids.
- **Payload:** **JSON**, one object per frame, tagged by a `"type"` field.
  Rationale: this channel is new and internal to the migration, local (same host),
  and benefits from being trivially debuggable and implementable on both sides.
  The bandwidth-sensitive path (pane frames) is diffed (see `skip`), and can be
  swapped for a binary cell encoding later without changing the message shapes.

## Messages

All messages are JSON objects with a `"type"` discriminator. Pane IDs are `u32`
(matching Rust `PaneId(u32)`). Sizes are cells; pixel cell size is carried for
parity with Rust resize (it only matters for pixel-based reports today).

### Rust → Go (commands)

| type | fields | meaning |
|---|---|---|
| `hello` | `protocol_version` | handshake; Go replies `welcome` |
| `create_pane` | `pane_id, cols, rows, cell_width_px, cell_height_px, cwd, command, args, env{}` | spawn PTY + emulator; `command` empty ⇒ default shell |
| `input` | `pane_id, data` (base64 bytes) | write raw bytes to the pane's PTY (Rust still encodes keys/mouse for now) |
| `resize` | `pane_id, cols, rows, cell_width_px, cell_height_px` | resize PTY + emulator |
| `close_pane` | `pane_id` | terminate child, free the pane |

### Go → Rust (events)

| type | fields | meaning |
|---|---|---|
| `welcome` | `protocol_version, error?` | handshake reply |
| `pane_frame` | `pane_id, frame` | the pane's grid (full or diff); see Frame |
| `pane_exited` | `pane_id, exit_code` | child process ended; pane removed |
| `error` | `pane_id?, message` | non-fatal error for a command |

### Frame (the pane cell grid)

Shaped to drop straight into Rust's `wire::FrameData`/`CellData` compositing:

```jsonc
{
  "cols": 80, "rows": 24,
  "full": true,                  // false ⇒ diff (unchanged cells have skip=true)
  "cursor": { "x": 3, "y": 0, "visible": true, "shape": 2 },
  "cells": [                     // row-major, len == cols*rows
    { "symbol": "h", "fg": 33554431, "bg": 33554431, "modifier": 1, "skip": false, "hyperlink": null }
  ]
}
```

- **Colors** are packed `u32` exactly like `wire.rs::color_to_u32`:
  `0x02_RR_GG_BB` for RGB. The Go emulator resolves every cell to concrete RGB
  (nil fg/bg ⇒ the snapshot's default fg/bg), so Rust receives explicit colors.
- **modifier** is the ratatui `Modifier` bitmask: `BOLD=1, DIM=2, ITALIC=4,
  UNDERLINED=8, REVERSED=64, CROSSED_OUT=256` (mapped from the emulator's per-cell
  bold/faint/italic/underline/inverse/strikethrough).
- **skip** marks cells unchanged since the last frame for this pane (diff). On a
  full frame all `skip=false`; a resize forces a full frame.
- **cursor.shape** is the DECSCUSR param (`2` steady block, `4` underline,
  `6` bar; block-hollow ⇒ `2`).
- **hyperlink** is reserved for OSC 8 (not yet populated; see open questions).

## Pane lifecycle (Go side)

1. `create_pane` → open a PTY (`creack/pty`) sized `cols×rows`, spawn `command`
   (or the default shell) with `TERM=xterm-256color`, `cwd`, and `env`. Create an
   `Emulator` and wire its **WritePTY** callback to the PTY master, so terminal
   query responses (DA1/DSR/etc.) are written back inside Go.
2. A per-pane **read pump** copies PTY output into the emulator and flags the pane
   dirty.
3. A coalescing **flusher** (~60 Hz) snapshots dirty panes, diffs against the last
   sent snapshot, and emits `pane_frame`. This mirrors the Phase A rAF coalescing.
4. `input` writes bytes to the PTY; `resize` resizes both PTY and emulator (forces
   a full frame next flush); `close_pane` kills the child and frees resources.
5. When the child exits / the PTY hits EOF, emit `pane_exited` and drop the pane.

## Mapping to the Rust seam

| Rust today | Phase B |
|---|---|
| `PaneRuntime::spawn(rows, cols, cwd, …)` (`pane.rs:1315`) | send `create_pane` |
| `on_read` → `terminal.process_pty_bytes` (`pane.rs:1689`) | Go owns the PTY read loop; Rust receives `pane_frame` |
| `terminal.resize(rows, cols, …)` (`pane.rs:2152`) | send `resize` |
| `PtyIoActorHandle::write_user_input` | send `input` |
| `terminal_responses` written back to PTY | handled inside Go (WritePTY callback) |
| `FrameData`/`CellData` (`wire.rs:407`) | `pane_frame.frame` matches this shape |

## Open questions / deferred

- **Input encoding ownership.** Interim: Rust keeps key/mouse encoding and sends
  raw bytes via `input`. go-libghostty has key/mouse encoders; moving encoding to
  Go (sending structured events instead) is a later simplification.
- **OSC passthrough.** Title (OSC 0/2), cwd (OSC 7), clipboard (OSC 52), and
  hyperlinks (OSC 8) matter for Rust chrome/detection. go-libghostty exposes
  effect callbacks for some; these become extra Go→Rust events (`pane_title`,
  `pane_cwd`, `pane_clipboard`). Not in the first cut.
- **Kitty graphics.** Deferred (as in Phase A).
- **Scrollback / selection.** The emulator owns scrollback; exposing it to Rust
  for selection/search is future work.
- **Binary cell encoding.** If JSON frame bulk shows up in profiling, switch the
  `cells` payload to a packed binary block behind the same message types.
```
