# herdr-web (Go) — Phase B: selection passthrough on the termhost seam

**Date:** 2026-0624-1408
**Session ID:** loaded from `2026-0622-0008-termhost-osc-detection.md` + `2026-0621-0855-e2e-herdr-termhost-test.md`
**Repo:** `~/projs/go/herdr-web` (Go terminal backend) · paired with `~/projs/rust/herdr` (Rust orchestrator)
**Branch (Go):** `roh/phase-b` · **Branch (Rust):** `roh/phase-b-termhost-client`

> Continues the Go↔Rust seam. The immediately prior work shipped OSC 7/52/9/0/2
> passthrough, agent detection (Stages A–C.2), OSC 8 hyperlinks, and scrollback
> (`2026-0622-0008-termhost-osc-detection.md`). This session implements the
> **selection** request/response that was flagged as the next step there.

---

## Goal

Termhost panes keep an **unfed local emulator** (slice-1 from the e2e session):
display/IO/cursor come from the Go backend, but the ~40 `PaneRuntime` methods that
read the local emulator return empty. Selection extraction was one of those —
drag-select copy on a termhost pane produced nothing. Close that gap with the
decided model: **Go request/response**. Rust owns selection state + mouse/key
handling; Go owns the fed emulator that can resolve coordinates to text.

## What shipped

### Go side — commit `67f84f1` (selection passthrough)
- **`protocol.go`**: `RequestSelection { pane_id, anchor, cursor, rectangle }`
  command + `SelectionPoint { row, col }`, and `PaneSelection { pane_id, text }`
  reply event. `SelectionPoint` is **screen-buffer (absolute) coordinates** (row
  from the top of scrollback, stable across scroll) — mirrors herdr's `Selection`
  endpoints. New message types `MsgRequestSelection` / `MsgPaneSelection`.
- **`terminal.go`**: `Emulator.FormatSelection(anchor, cursor, rectangle)` + the
  pure `SelectionEndpoint { Row, Col }` type.
- **`ghostty.go`**: the impl **mirrors herdr's `read_text_screen`** exactly: order
  the two endpoints top-left → bottom-right (same rule as Rust `Selection::ordered`),
  resolve each via a `PointTagScreen` `GridRef`, build a `libghostty.Selection`,
  then `SelectionFormatString(WithSelectionFormat(Plain), WithSelectionUnwrap(true),
  WithSelectionTrim(true))`. The two grid refs are **borrowed views** of terminal
  internals, so they're built and consumed back-to-back with no intervening
  mutation — the Host holds `emuMu` across the whole call.
- **`host.go`**: dispatch `request_selection` → `requestSelection` extracts under
  `emuMu` and **always** replies with `pane_selection` (definite response; `""` =
  no selectable content).
- Tests: codec round-trip for both messages + `-tags ghostty` Host integration
  `TestHostReportsPaneSelection` (child prints `HELLO WORLD`, request screen cols
  0..4 inclusive → `pane_selection` text `"HELLO"`).

### Rust side — commit herdr `ef489d3` (consumer)
The selection reply rides the **same async signal path** OSC 52 clipboard already
uses (`PaneSignal::Clipboard` → `AppEvent::ClipboardWrite`), so no blocking on the
UI thread — important because `extract_selection` is a synchronous `&self` call but
the seam is async.
- **`proto.rs`**: `Command::RequestSelection {…}` + `SelectionPoint { row, col }`
  (`rectangle` `skip_serializing_if` when false, matching Go's `omitempty`) and
  `Event::PaneSelection { pane_id, text }`. 5 new tests.
- **`client.rs`**: `PaneSignal::Selection(String)`; handle the `pane_selection`
  event into the pane's sink; `TermhostPane::request_selection(...)` sends the cmd.
- **`pane.rs`**: the signal sink turns `PaneSignal::Selection(text)` into the same
  `AppEvent::ClipboardWrite { content: text.into_bytes() }` the in-process drag-copy
  produces (skips empty). `PaneRuntime::request_termhost_selection(sel)` reads
  `sel.ordered_cells()` and fires the request; returns `false` for in-process panes.
- **`terminal/runtime.rs`**: delegate `request_termhost_selection` (the call site
  uses the `TerminalRuntime` wrapper, not `PaneRuntime` — the one compile fix).
- **`app/actions.rs`** `copy_selection`: try `request_termhost_selection` first
  (async reply → clipboard); in-process panes fall back to synchronous extract+copy.

## Key facts for future me

- **Selection coordinates are screen-buffer/absolute** (row `u32`, col `u16`) on
  both sides. Go resolves them via `PointTagScreen` `GridRef`. The Go side orders
  the endpoints, so the Rust side may send anchor/cursor in any order.
- **`rectangle` is always false today** — herdr's `Selection` has no rectangle
  field. The seam carries the flag for future block selection.
- **Selection extraction is request/response, async on the wire.** The reply
  becomes an `AppEvent::ClipboardWrite`, exactly like OSC 52. No UI-thread block.
- **Scope ported = drag-select copy only.** Double-click word copy
  (`try_double_click_copy`) and URL-at-cell (`url_at_pane_cell`) read a row's text
  *synchronously* to compute word/URL bounds, then extract again — they can't ride
  the async reply as-is, so they remain degraded for termhost panes. **This is the
  in-progress follow-up (next).**
- **Build/run env unchanged:** Go ghostty `PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig`;
  Rust `ZIG=~/projs/go/herdr-web/.tools/zig-wrapped`.
- **Seam now — commands (Rust→Go):** `hello`, `create_pane`, `input`, `resize`,
  `close_pane`, `scroll_viewport`, **`request_selection`**.
- **Seam now — events (Go→Rust):** `welcome`, `pane_frame`, `pane_cwd`,
  `pane_agent`, `pane_clipboard`, `pane_title`, **`pane_selection`**, `pane_exited`,
  `error`.

## Verification (all green)

- Go: default `go build ./...` + `-tags ghostty` build; `go test ./internal/...`
  (pure) + `-tags ghostty ./internal/...` (incl. `TestHostReportsPaneSelection`);
  gofmt/vet clean both modes.
- Rust: `cargo build` (feature off) clean; `--features termhost` build + clippy
  clean (the one `actions.rs:2726` `build_toast` / `TerminalTitleReported` warning
  is pre-existing). `cargo test --bin herdr` = **1892 passed**; `--features
  termhost` = **1908 passed** (+16 termhost proto tests).

## Commits

```
Go   (roh/phase-b):                67f84f1 feat: selection passthrough on the termhost seam (Go side)
Rust (roh/phase-b-termhost-client): ef489d3 feat: consume selection passthrough on the termhost seam (Rust side)
```

## Next steps

- **In progress this session:** double-click word copy + URL-at-cell for termhost
  panes. They need a *synchronous* selection round-trip (read row text → compute
  bounds → extract) — unlike drag-copy, which is fire-and-forget. Options: a
  blocking request/response over the seam (oneshot fulfilled on the reader thread)
  or a Go-side word/URL bounds helper. Decide and wire.
- Remaining termhost degradations: kitty graphics; scroll-lock/pinning (output
  snaps to bottom); native-TUI hyperlink click resolver.
- Daemon lifecycle: have the Rust server spawn/supervise `cmd/termhost`.
- Eventually: make termhost the default and retire the Rust in-process PTY/detect path.
</content>
</invoke>
