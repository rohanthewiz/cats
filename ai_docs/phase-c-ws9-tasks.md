# Phase C — WS9 actionable task list (browser-facing protocol)

**Date:** 2026-07-03. Companion to `ai_docs/fbl_go_port_feasibility_analysis.md` (Part 2, WS9
at §234-241) and `ai_docs/phase-b-orchestration-seam.md` (the existing seam contract).
All work is Go-side, `~/projs/go/herdr-web`. Line anchors mapped 2026-07-03 — re-confirm
before editing.

**Goal:** one versioned WebSocket protocol between the Go server and the browser — layout
tree + per-pane grid diffs + chrome state down; structured key/mouse/paste/resize/command
events up — consolidating today's three non-interoperating protocols:

- **α** browser JSON (`cmd/gateway/main.go:133-140` cmds, `:176-230` pump, `:264-335`
  frame/diff encode; JS in `cmd/gateway/web/index.html:177-193,252-290`) — single composited
  grid sourced from the *Rust* server via bincode; no pane identity, no layout, chrome
  reverse-engineered from cell colors.
- **β** orchestration seam (`internal/orchestration/protocol.go`) — per-pane `pane_frame`
  (`:407-419`) + chrome events `pane_cwd`/`pane_agent`/`pane_title`/`pane_modes`/
  `pane_clipboard` (`:235-345`) that already carry most of WS9's data but never reach the
  browser. Go↔Go after WS0 — free to evolve.
- **γ** bincode wire (`internal/wire`, `internal/herdrconn`) — the Rust-server client.
  **Deletion target**; nothing ports except color→CSS knowledge (`internal/wire/color.go`).

## Decisions (locked provisionally 2026-07-03 — Rohan may override; re-open before Stage 2)

- **D1 Diff scheme: sparse-index.** Browser gets only changed cells as `{i, cell}` pairs
  (α's existing `fdiff` shape, now per-pane). Gateway/orchestrator translates from β's
  skip-flag diffs (`FrameFromSnapshot`, `protocol.go:503-560`). β itself is NOT changed.
- **D2 Colors: packed u32 on the wire** (β's `0x02_RR_GG_BB`, `packRGB` `protocol.go:439`);
  a small JS shim resolves to CSS. `wire.ColorToCSS` logic moves client-side; enables
  browser-side theming. (α's server-side CSS strings retire.)
- **D3 Layout payload: computed rects, not the tree.** Browser receives `[]PaneInfo`-shaped
  rects (`internal/layout/layout.go:56`, computed by `TileLayout.Panes(area)` `:151`) plus
  `SplitBorder`s (`:70`) for drag-resize. The BSP `Node` tree stays server-side; the
  tagged-union `Node` DTO is deferred to WS3 (persistence), NOT built here.
- **D4 Structured input lands in WS9** (schema AND server-side VT encoding), retiring the
  browser-JS key encoding (`index.html:252-275`) and — once WS2 wires it through — the Rust
  InputMirror's known kitty bits-2/8 degradation and the XTMODKEYS Enter special-case.

---

## Stage 1 — Protocol spec doc — DONE (`ai_docs/phase-c-ws9-protocol.md`, 2026-07-03)

- [x] **1.1** Write `ai_docs/phase-c-ws9-protocol.md`: envelope (`t` discriminator, protocol
  version, per-message `pane` addressing), full message inventory both directions, framing
  (text JSON now; binary cell encoding explicitly deferred with a version-bump path).
- [x] **1.2** Down messages: `hello/welcome` (version + initial full state), `layout`
  (workspace/tab list, focus, pane rects + borders — D3), `pane_frame` full, `pane_diff`
  (D1/D2), `pane_scroll`, and chrome: `pane_title`, `pane_cwd`, `pane_agent` (identity +
  idle|working|blocked state from β `PaneAgent` `protocol.go:249-256`), `pane_modes` (subset
  the browser needs: mouse capture/encoding, bracketed paste, kitty flags — from β
  `PaneModes` `:332-345`), `clipboard` (OSC 52), `notify`, `pane_exited`, `error`,
  `shutdown`.
- [x] **1.3** Up messages: `init` (cols/rows/dpr), structured `key` (D4: code, text,
  mods, repeat — NOT pre-encoded VT bytes), `mouse` (cell coords + button/mods/kind; server
  applies the pane's reported encoding), `paste` (text), `image` (b64 + ext), `resize`
  (window → server recomputes layout), and commands: `focus_pane`, `split`, `close_pane`,
  `resize_split` (border drag), `switch_tab`, `switch_workspace`, `scroll` — command set
  cross-checked against WS2's `AppEvent` inventory so WS9 informs WS2, not vice versa.
- [x] **1.4** Record the α/β/γ disposition table: what each existing message maps to, what
  dies. β's open questions (`phase-b-orchestration-seam.md:137-151`) get their answers
  linked here.

## Stage 2 — Go message layer: `internal/browserproto` — DONE (2026-07-11)

- [x] **2.1** New package `internal/browserproto`: typed structs for every Stage-1 message,
  JSON tags, encode/decode helpers, `ProtocolVersion` const. Table-driven round-trip tests
  (mirror the discipline of β's `protocol_test.go`). → `proto.go`/`down.go`/`up.go`/`cmd.go`;
  `TestRoundTrip` (25 message types) + `TestWireShapes` pins exact JSON against the spec.
  Cmd `id` pinned as a string (spec updated).
- [x] **2.2** Layout DTOs (D3): `PaneRectInfo` (from `layout.PaneInfo` — id, rect, inner rect,
  focused), `BorderInfo` (from `layout.SplitBorder` — pos, direction, ratio, path), workspace/
  tab envelope from `internal/workspace`. → `layout.go`: `BuildLayout` (zoomed tab = focused
  pane full-area, no borders), `BorderID`/`BorderPath` (stateless opaque path encoding "r01…",
  round-trip pinned by `TestBorderIDDrivesResize`). One accessor added:
  `workspace.PublicPaneID` (`workspace.go:311`) — accessor only, no behavior change.
- [x] **2.3** Frame translation: β `Frame` (skip-flag) → browser `pane_frame`/`pane_diff`
  (sparse-index, D1; packed u32 colors pass through, D2). → `frame.go`: `FrameTranslator`
  (per pane per connection; full on β-full/first-frame/`Reset()`/diff >60%, def_fg/def_bg =
  dominant colors of the full frame, held fixed across diffs), `ModesFrom` (β modes → the §3
  display subset). Property test `TestReplayReconstruction`: 60-step seeded replay through
  the real `FrameFromSnapshot`, browser-side reconstruction == β-side fold every step
  (surfaced β's link-removal-without-content-change non-propagation — resolveCell's skip
  compare ignores Link; the test models it, β unchanged per out-of-scope rule).

## Stage 3 — Structured input encoding (D4)

- [ ] **3.1** Spike first: confirm whether go-libghostty exposes the key-event VT encoder
  (kitty protocol + legacy + modifyOtherKeys). If yes, wrap it. If no, port the **pure Rust
  encoders** from herdr `src/pane/input_mirror.rs` + `src/input` — they are spec-grade
  (pinned by the Stage-B differential test: 45 combos × key/mouse matrix; fixes recorded in
  `phase-c-ws0-ws1-tasks.md` B2).
- [ ] **3.2** `key`/`mouse`/`paste` events → bytes, driven by the pane's live mode state
  (β `pane_modes` / `terminal.InputModes`, `internal/terminal/terminal.go:131`): kitty
  flags **including bits 2/8** (report-event-types/report-all-keys — the degradation WS9
  exists to retire), DECCKM, bracketed paste, mouse encodings (X10/UTF-8/SGR + alternate
  scroll). Table tests keyed to the same combo matrix as the Rust differential test.
- [ ] **3.3** Keep a `raw` input message in the protocol as an escape hatch during
  transition (α's current behavior); mark deprecated in the spec.

## Stage 4 — Proof harness (the WS2/WS8 build-target)

- [ ] **4.1** `cmd/gateway` spike mode (flag or new `cmd/gateway2`): speak the new protocol
  to the browser and **source panes directly from the termhost daemon** via the existing
  `internal/orchestration` client — no WS2 orchestrator yet: one hard-coded workspace/tab,
  a fixed two-pane split from `internal/layout`, static `layout` message.
- [ ] **4.2** Minimal JS: per-pane canvases positioned from the `layout` rects; packed-u32
  color resolve (D2); structured key/mouse senders (D4); chrome rendered as plain HTML text
  (title, cwd, agent state) — proving chrome-as-data, not styled UI (that's WS8).
- [ ] **4.3** Acceptance: two live shell panes in one page; focus switch routes input
  correctly; a TUI app (e.g. `htop`) gets correct mouse + kitty-negotiated keys through the
  server-side encoder; `pane_agent` state changes visibly (run a fake agent from the herdr
  test fixtures); daemon restart survival still works (reconnect + resync full frames).
- [ ] **4.4** `internal/wire`/`internal/herdrconn` untouched but confirmed unreferenced by
  the new path (they die in WS11; the old gateway mode keeps working against Rust herdr
  during transition).

## Out of scope (flagged so they don't creep in)

- Auth/TLS/origin checks → WS10. Styled chrome/HTML UI → WS8. Tagged-union `Node`
  serialization → WS3. Binary cell frames → post-WS9 version bump. Changing β itself
  (daemon seam) → not needed; WS9 only *consumes* it.

## Sequencing note

Stages 1→2 are strictly ordered; Stage 3 can proceed in parallel with 2 after 1.3 fixes the
event schema. Stage 4 needs all three. Exit criterion: WS2 (orchestrator) and WS8 (web UI)
can both start against `internal/browserproto` + the Stage-4 harness without touching α/γ.
