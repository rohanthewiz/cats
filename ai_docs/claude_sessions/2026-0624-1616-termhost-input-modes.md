# herdr-web (Go) — Phase B: report terminal input modes over the seam (retire-Rust #1)

**Date:** 2026-0624-1616
**Repo:** `~/projs/go/herdr-web` (Go terminal backend) · paired with `~/projs/rust/herdr` (Rust orchestrator)
**Branch (Go):** `roh/phase-b` · **Branch (Rust):** `roh/phase-b-termhost-client`

> First step toward retiring the Rust in-process terminal backend (Go as the single
> backend). The endgame's blocking gap was **input correctness**: termhost panes
> encoded keys/mouse and routed input using their *unfed* local emulator's modes.

---

## Decision: report modes, don't move encoding (the cleaner boundary)

The plan said "move input encoding to Go," but investigating the code changed the
recommendation (confirmed with the user via AskUserQuestion → "report modes"):

**Rust needs the modes for its own UI control flow, regardless of where bytes are
encoded.** `forward_pane_mouse_button` only forwards a mouse event to the program
when mouse reporting is *enabled* — otherwise the mouse drives herdr's selection /
scroll. Same for `wheel_routing` (alt-scroll vs mouse-report vs local), `send_paste`
(bracketed?), `try_send_focus_event` (focus reporting?). That "is this event for the
program or for my UI" decision is inherently a Rust-UI concern and will always live
in Rust. So moving byte-encoding to Go would *still* need the modes reported back —
strictly more work for no extra correctness.

**The fix that's necessary and sufficient:** Go reports the modes; Rust mirrors them
onto its (unfed) local emulator. The encoders (`encode_mouse_button`,
`encode_terminal_key`) and the query methods (`input_state`, `wheel_routing`,
`keyboard_protocol`, `synchronized_output_active`) **all read that local emulator**,
so one mirror fixes everything with zero call-site or encoder changes. Go owns the
terminal+modes; Rust owns the UI and uses the modes.

## What shipped

### Go — commit `2b540bd`
- **`terminal`**: `Emulator.InputModes()` + `InputModes`/`MouseMode`/`MouseEncoding`
  types. ghostty impl queries libghostty `ModeGet` (mouse 9/1000/1002/1003, SGR/UTF8,
  bracketed paste, focus, DECCKM, alt-scroll, sync-output), `ActiveScreen`,
  `KittyKeyboardFlags`.
- **`protocol`**: `PaneModes { pane_id, alternate_screen, application_cursor,
  bracketed_paste, focus_reporting, mouse_mode, mouse_encoding, mouse_alternate_scroll,
  synchronized_output, kitty_keyboard_flags }`.
- **`host`**: per-pane `lastModes`; the **flusher re-checks modes after a dirty
  frame** (modes only change via program output) and emits `pane_modes` on change.
- Tests: codec round-trip + Host integration (child `printf '\033[?2004h\033[?1003h
  \033[?1006h'` → `pane_modes` with bracketed paste + SGR any-motion mouse).

### Rust — commit herdr `87d0abf`
- **`proto`**: `Event::PaneModes`. **`client`**: `PaneSignal::Modes(PaneInputModes)`.
- **`pane/terminal.rs`**: `GhosttyPaneTerminal::apply_input_modes` — idempotent
  mirror. Key subtleties (see below): mouse tracking + encoding fed as the program's
  own `CSI ? Pn h/l` sequences; alt-screen switched **both** directions; kitty flags
  set **absolutely** (`CSI = flags ; 1 u`).
- **`pane.rs`**: the signal sink applies modes onto the local terminal + updates the
  kitty-flags atomic. (Sink captures `Arc<PaneTerminal>` + the `AtomicU16`.)
- Tests: proto decode + a terminal test proving the mirror makes **both** `input_state`
  and `encode_mouse_button` (SGR) correct, and that clearing tracking disables both.

## Gotchas worth remembering

- **`mode_set` is NOT enough for the mouse encoder.** `input_state`'s `mode_get` sees
  a mouse mode set via `mode_set`, but libghostty's `MouseEncoder::set_from_terminal`
  reads tracking state from the **escape-sequence path**, so `encode_mouse_button`
  returned empty until `apply_input_modes` was switched to feed `\x1b[?1003h` etc.
  (app-cursor/bracketed/focus/alt-scroll/sync via `mode_set` are fine — the existing
  handoff test proves app-cursor→`\x1bOA` works via `mode_set`.)
- **Kitty flags must be set, not pushed.** `seed_keyboard_protocol_flags` uses
  `CSI > Pn u` (push) — fine once at handoff, but repeated applies would grow the
  kitty stack. `apply_input_modes` uses `CSI = flags ; 1 u` (absolute set).
- **Feeding mode sequences to the unfed local emulator is safe**: that emulator is
  read for input modes/encoding but never rendered for termhost panes (frames come
  from Go), so the control sequences produce no visible output.
- **Where to detect changes (Go):** the flusher, gated on a dirty frame — modes only
  change as a result of program output, so a pane that just produced a frame is
  exactly when to re-query. Cheap (skips idle panes), low latency (16ms flush).

## Verification (all green)

- Go: default + `-tags ghostty` build; `go test -tags ghostty ./internal/...` (incl.
  the new Host modes test); gofmt/vet clean.
- Rust: `cargo build` (feature off) + `--features termhost` + clippy clean.
  `cargo test --bin herdr` = **1892**; `--features termhost` = **1913** (+2:
  pane_modes decode, apply_input_modes mirror).

## Commits

```
Go   (roh/phase-b):                 2b540bd feat: report terminal input modes on the termhost seam (Go side)
Rust (roh/phase-b-termhost-client): 87d0abf feat: mirror termhost input modes onto the local emulator
```

## Retire-the-Rust-backend roadmap (this was #1)

1. ~~Input-mode parity~~ ✅ this session.
2. **Text/scrollback extraction parity** — `visible_text/recent_*/snapshot_history/
   detection_text` read the unfed emulator → empty for termhost. Feeds `pane.read`,
   session save, agent resume, search. Next correctness gap; same request/response
   shape as selection.
3. **Session persistence + detach/reattach + handoff** — termhost panes don't survive
   server restart (daemon dies with herdr) or handoff (fd ops are no-ops). Likely needs
   a persistent daemon that outlives herdr restarts. Biggest single piece.
4. **Flip default + delete in-process path** — config + daemon discovery, then remove
   the unfed local `PaneTerminal` + `Actor` PTY path + `src/pane/terminal.rs`. (Once #1
   and #2 are done the local emulator is dead weight; herdr then no longer links ghostty.)
5. Build/dist (bundle daemon per-platform, CI cache libghostty) + smaller items
   (platform parity, `termhost_dirty_patch` changed-rows, restart-on-crash).

Note: kitty graphics remains back-burnered (experimental/off-by-default).
</content>
