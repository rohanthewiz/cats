# WS0 Stage C — delete the in-process terminal; tests on daemon/fake (Rust + Go)

**Session id:** `e236bf82-bc46-4189-bdb8-09ce7198a60f` (session name: `stage-c-impl-verif`)
**Date:** 2026-0702-2351 · **Repos:** `~/projs/rust/herdr` (branch `roh/phase-b-termhost-client`)
+ this repo (branch `roh/phase-b`: Go daemon change + docs).
**Continues:** `2026-0702-0024-ws0-stage-a-b-termhost-default-input-mirror.md`.

> **Implementation session.** Executed WS0 Stage C (C1–C7) end-to-end from
> `ai_docs/phase-c-ws0-ws1-tasks.md` (checked off there in detail, commit `5bd81de`).
> Stage D (delete `src/pty/`, `src/ghostty/`, Zig build) is next.

---

## Commits

- herdr `c7c89ef` **feat: delete the in-process terminal selection, IO actor, and detection
  task (C1–C3+C5)** — −2,893 lines.
- herdr `4875ed8` **feat: emulator-free test substrate — PaneTerminal is Mirror or Fake
  (C4+C6a)** — −6,744/+1,148 lines.
- herdr `70b6e29` **feat: integration suites on the real termhost daemon (C6b) +
  handoff/respawn fixes.**
- herdr-web `21f65ce` **feat(termhost): report XTMODKEYS modifyOtherKeys in pane_modes** +
  `5bd81de` docs checkoff.

## What Stage C did (per sub-task)

- **C1:** `termhost::required_client()` replaces `client_if_enabled()`/`BackendChoice`;
  `HERDR_TERMHOST_INPROCESS` and the `cfg(test)` in-process default are GONE. Under `cfg(test)`,
  `spawn*` returns a channel-backed fake runtime: seeds requested cwd (as OSC 7 would) + restore
  history, **echoes written input back into content** (tty-echo emulation), and **executes
  explicit argv/shell commands** (`argv.len() > 1`) as plain subprocesses with a PaneDied
  watcher — marker-file tests and exit-driven behaviors keep old semantics. Never records the
  child pid: the subprocess shares the test-runner session and `shutdown_pane_processes` would
  kill `cargo test` (this actually happened mid-session — the suite died silently mid-run).
- **C2:** `PaneRuntimeIo::Actor` + all arms deleted; `TestChannel` is the double. `src/pty/`
  stays compiled behind transitional `#![allow(dead_code, unused_imports)]` until D.
- **C3:** read path, child watcher, detection task, `pane/agent_detection.rs` deleted.
  `begin_graceful_release`/`reset_agent_detection`/`set_full_lifecycle_authority_active` are
  documented no-ops (agent lifecycle moves Go-side later). Dead detect/process-probe helpers
  carry "delete with the detect-port workstream" allow-markers.
- **C4:** `PaneTerminal` = `Mirror(InputMirror)` | `#[cfg(test)] Fake(FakePaneTerminal)`.
  `GhosttyPaneTerminal` + emulator trackers (`pane/{osc,cursor,input,xtgettcap}.rs`) deleted
  (~3.7k LOC + ~140 tests). Shared wire conversions extracted: `render_wire_frame` /
  `wire_dirty_patch` in `pane.rs` serve both the termhost arm and the fake.
- **C5 (decision recorded):** `from_handoff_fd` **deleted**, not redefined. Handoff = daemon
  reconnect + adopt (welcome.panes). Restore treats fd-passed panes from older binaries as
  failed imports → respawn with snapshot history, fd closed. `is_termhost()` skips in
  headless.rs are the only path; fd-dup/manifest machinery degenerates to empty sets (full
  removal deferred).
- **C6a (unit):** the six `test_with_*` helper **signatures are unchanged** — reimplemented on
  `src/pane/fake_terminal.rs`, so all ~119 call sites run unmodified. The fake is a deliberately
  tiny VT interpreter (text/line discipline, DEC private-mode whitelist, basic SGR, cursor
  addressing, DECSCUSR, OSC 8, kitty CSI-u, XTMODKEYS) over a wire-shaped grid snapshotting to
  `protocol::FrameData`; **unknown CSI finals / DEC modes panic** so new tests must consciously
  extend it. Modes/encoders delegate to the real `InputMirror`. Key contracts discovered while
  making the suite green:
  - wide-char continuation cells emit `{" ", skip:false}` (matches the ratatui-buffer→FrameData
    conversion AND the deleted ghostty patch — pinned by `retained_pty_update_matches_full_render_frame`);
  - `visible_hyperlinks` tuple order is `((x,y), symbol, uri)` (uri LAST — the resolver reads
    element 3);
  - selection endpoints are absolute buffer rows (row 0 = scrollback top; see `selection.rs`
    `viewport_top_row`).
  - `InputMirror` gained XTMODKEYS Enter encoding (CSI 27;mod;13~ under legacy protocol) — a
    real stage-B gap the shift-enter routing test caught once it ran on the mirror.
- **C6b (integration):** the 20 `HERDR_TERMHOST_INPROCESS=1` pins → `HERDR_TERMHOST_BIN` via
  `tests/support::termhost_daemon_bin()` (env override, then sibling of the herdr binary under
  test; **hard error with build instructions if absent** — no silent skip-passes).
  `live_server_holds_one_pty_master_fd_per_pane` inverted to `live_server_holds_no_pty_master_fds`.

## Product bugs found by running suites on the real daemon (all fixed in `70b6e29`)

1. **Failed-handoff rollback left panes dark:** `detach_for_handoff()` drops the daemon socket
   before the attempt; nothing reattached on rollback. Added `TermhostClient::reattach()`
   (reconnect in place — same Arc, same pane handles; re-handshake, new reader thread,
   `RequestResync` per pane) + `termhost::reattach_after_failed_handoff()` called from
   `rollback_handoff_before_commit`.
2. **Aborted handoff import killed live shells:** the failed replacement's runtime Drops sent
   `close_pane` for panes it had adopted. `PaneRuntime::drop` now returns early when
   `preserve_processes_on_drop` (closing the daemon pane kills the preserved shell);
   `App::preserve_runtimes_for_failed_handoff()` invoked on any pre-commit abort in
   `run_handoff_import_server`.
3. **Respawn-after-exit raced the daemon pane-id namespace:** new pane spawned before the dead
   runtime dropped → old handle's `close_pane` tore down the fresh pane.
   `respawn_shell_for_launch_pane` drops the old runtime first. Also adoption is **claim-once**
   (`claim_surviving_pane`) — the static welcome list used to let a respawn re-adopt a dead pane.
4. **Launch-argv respawn semantics lost for adopted panes:** restore only set
   `with_launch_argv/.with_respawn_shell_on_exit` for fd-imported panes. Now follows
   `PaneRuntime::adopted_live_shell()` (the post-C carrier of "original process still alive").

## Go daemon change (herdr-web `21f65ce`)

`pane_modes` now carries `modify_other_keys` (additive, omitempty): a raw-stream
`xtmodkeysScanner` (same pattern as the OSC passthrough scanners; libghostty-vt doesn't surface
XTMODKEYS state) feeds an atomic on the pane; injected in `emitModeChanges` AND `resyncPane`.
Rust side: proto field (serde default), `PaneInputModes.modify_other_keys`,
`InputMirror::apply_input_modes` applies it. Closes the stage-B known divergence — modified
Enter survives handoffs.

## Verification (C7)

- Unit: **1736/1736** (`cargo test --bin herdr`; the 9 machine-flaky detect/keybinding tests
  passed on the final runs). clippy/fmt clean (one pre-existing `actions.rs:2722` clippy lint
  untouched).
- `termhost_e2e`: **12/12, zero skips**, BOTH modes in one invocation (set
  `HERDR_TERMHOST_SOCKET` to a hand-launched daemon AND `HERDR_TERMHOST_BIN`).
- Integration vs live daemon: live_handoff **16/16**, client_mode 16/16, server_headless 15/15,
  detach_reattach 11/11, cross_area 7/9, api_ping 10/11, multi_client 10/11.
- **Pre-existing machine-environmental failures (4, unchanged):** `api_ping::events_subscribe_
  streams_output_and_agent_status_events`, `cross_area_two_clients_shared_view…`,
  `cross_area_agent_process_survives…`, and `multi_client_broadcasts_frame_updates_to_all_
  clients` — the last one **verified failing identically at the stage-B commit** via a
  `git worktree` build (raw-client typed input → pane output never appears on this machine;
  the marker echoes into the startup pane w1:p1, suggesting per-client focus routing — same
  family as the other three). `auto_detect`/`cli_wrapper` suites are `#![cfg(not(macos))]`.
- Baselining method that made this trustworthy: `git stash` → full-suite run → capture failing
  list → `git stash pop`; new failures were diffed against that set at every step.

## Build & environment notes

- `cargo build/test` still needs `ZIG=<herdr-web>/.tools/zig-wrapped` (Zig dies in stage D).
- Integration tests now REQUIRE `target/debug/herdr-termhost` (build from this repo:
  `PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig
  go build -tags ghostty -o ~/projs/rust/herdr/target/debug/herdr-termhost ./cmd/termhost`).
  A fresh build (with `21f65ce`) was left in place.
- `--no-default-features` (no-termhost) builds are effectively dead as of C (spawn errors);
  D2 removes the feature.

## Next: WS0 Stage D

- D1 delete `src/pty/`, `src/ghostty/`, collapse `pane/terminal.rs` remnants (relocate
  `InputState`/`ScrollMetrics`/`TerminalCursorState`/`TerminalDirtyPatch*` — they survive);
  D2 drop `portable-pty` + the `termhost` feature; D3 delete the Zig invocation from `build.rs`;
  D4 acceptance: no-Zig `cargo build --release` + tests + e2e, `cargo tree` clean of
  `portable-pty`, no `ghostty-vt` in link logs.
- `ghostty/mod.rs` tests (20) still run in C — they die with the module in D1.
- Leftover transitional `#[allow(dead_code)]` markers to sweep in D: `src/pty/mod.rs`,
  detect/process-probe helpers, `terminal_theme::osc_set_default_color_sequence`,
  `terminal/state.rs::stabilize_agent_detection`, `handoff_runtime.rs`.
