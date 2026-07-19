# herdr — Phase B: termhost backend wired into the live pane runtime (step 3)

**Date:** 2026-0621-0855
**Session ID:** `231ee2d1-0169-4dd7-b77b-7e2bbdf885c2`
**Project:** `~/projs/go/herdr-web` (Go side) · **work landed in** `~/projs/rust/herdr` (Rust orchestrator)
**Rust branch:** `roh/phase-b-termhost-client` · 1 commit this session, **nothing pushed**

> Continues the Go↔Rust orchestration seam work. Prior context:
> `2026-0619-2123-libghostty-go-integration.md` (toolchain + `internal/terminal`
> Emulator), `2026-0619-2154-go-rust-orchestration-seam.md` (the Go-side seam:
> `internal/orchestration`, `cmd/termhost`, the contract doc). The Rust client +
> feature-gated scaffolding (steps 1–2) were built in the immediately preceding
> session and resumed from `ai_docs/claude_sessions/last_convo.md` (an API error
> had interrupted it mid-"commit + study the render path + propose step 3").

---

## Goal

Resume the Rust side of the seam. The interrupted convo had just committed **step 2**
(the `termhost` backend seam scaffolding) and asked to: commit anything outstanding,
study the render path, and propose **step 3** (the consequential one — actually rewire
`PaneRuntime` to optionally use the Go backend and splice frames into the render path).

**Done this session:** confirmed nothing was outstanding to commit, studied the render
path, proposed step 3, got scope sign-off, and **implemented step 3**. Builds + tests
green in both modes. Committed as `038d45a`. **Not yet done:** a live end-to-end smoke
test (herdr server → running Go daemon → browser) — left for an interactive run.

---

## Starting state (verified)

- Rust `roh/phase-b-termhost-client`: clean (only untracked `.claude/`, `dist/`).
  Step 2 already committed at `14b2212`; step 1 (Rust client) at `1e4dd9a`.
- So "commit what we have so far" was a no-op — the tree was already clean.

## Render-path study (grounding for step 3)

Mapped the in-process PTY → emulator → compositor → frame path in `~/projs/rust/herdr`
(file:line):

- **`PaneRuntime`** (`pane.rs:759`) owns `terminal: Arc<PaneTerminal>` (ghostty emulator)
  + `io: PaneRuntimeIo` (the PTY actor) as **separate** fields. The `PaneRuntimeIo` enum
  (`pane.rs:777`) already abstracts I/O (`Actor` / `#[cfg(test)] TestChannel`) with arms
  for shutdown/resize/send_bytes/handoff.
- **Single spawn chokepoint:** all `spawn*` variants funnel through
  `spawn_command_builder` (`pane.rs:1610`), which builds the local `PaneTerminal`
  (`1626–1641`), spawns the PTY via `spawn_with_portable_pty` (`1644`), sets up the
  child-watcher (`1663` → `AppEvent::PaneDied`), the `on_read` actor (`1689` →
  `process_pty_bytes`), and the detection task.
- **Two render paths, both methods on `PaneRuntime`:**
  - retained fast path — `headless.rs:2855` calls `runtime.collect_dirty_patch()` →
    `apply_terminal_dirty_patch()` (`headless.rs:113`) splices whole rows
    (`Vec<(u16, Vec<CellData>)>`, each row exactly `area.width` long) into a cloned
    `FrameData`. **This is the FrameData the browser clients receive.**
  - full-render fallback — `ui/panes.rs:296` / `render_stream.rs:337` call
    `runtime.render(frame, area, …)` which writes the ghostty grid into a ratatui buffer.
- **Wire types** (`protocol/wire.rs`): `CellData{symbol,fg:u32,bg:u32,modifier:u16,skip,
  hyperlink}`, packed `0x02_RR_GG_BB` via `color_to_u32`; `u32_to_color`/`u16_to_modifier`
  were the (test-only) reverse; `CursorState{x,y,visible,shape}`.

**Key realization:** ~40 `PaneRuntime` methods (detection text, selection, scrollback,
hyperlinks, kitty, key encoding, cursor) read the **local emulator**. The Go backend only
provides cells/input/resize/exit. So slice-1 must keep an **unfed local `PaneTerminal`**
(those methods return empty) and source only display/IO/cursor from Go.

## Decisions (signed off via AskUserQuestion)

1. **Slice-1 scope:** *Display + IO only* — termhost panes render, accept input, resize,
   close on exit; detection/selection/scrollback/hyperlinks/kitty stay degraded (empty)
   pending the deferred Go→Rust OSC/scrollback passthrough. Key encoding stays in Rust.
2. **Enablement:** *Env var* `HERDR_TERMHOST_SOCKET` gates **use** at runtime; the
   `termhost` Cargo feature gates **compilation**. Unset env → in-process path (default).
   Chosen over config-field / CLI-flag to avoid config/arg-parsing surface.

---

## What was built — commit `038d45a` (4 files, +415/-18)

Lowest-blast-radius approach: a process-wide client behind a `OnceLock` + an early branch
in the single spawn chokepoint — **no public spawn-signature churn**, and branches live
*inside* the `PaneRuntime` render methods so **no call sites changed**.

- **`src/termhost/mod.rs`** — `client_if_enabled() -> Option<Arc<TermhostClient>>`:
  `OnceLock`-cached, connects from `HERDR_TERMHOST_SOCKET` (const `SOCKET_ENV_VAR`); on
  unset/empty/connect-failure logs and returns `None` (→ falls back to in-process).
- **`src/termhost/client.rs`** — replaced the diff-losing `latest: Option<FrameData>`
  with an **accumulating `PaneGrid`**: folds Go frames (full + per-cell `skip` diffs) into
  a retained full grid, tracks a `dirty` flag + cursor. Added `TermhostPane::snapshot()`
  / `take_dirty()` / `cursor()`. (Also fixed 2 pre-existing clippy nits in this file:
  `io::Error::other`, `while let` reader loop.)
- **`src/pane.rs`** —
  - `PaneRuntimeIo::Termhost(Arc<TermhostPane>)` variant + `termhost_pane()` accessor;
    match arms route input/resize/shutdown to the backend (`TerminalBackend` trait),
    handoff/fd ops are no-ops/unsupported.
  - `spawn_command_builder`: after building the (unfed) local terminal, branches
    `#[cfg(feature="termhost")] if let Some(client) = client_if_enabled() { return
    Self::finish_termhost(...) }`. Conditional move of `events`/`terminal` is fine
    because the branch diverges (returns).
  - `finish_termhost(...)`: builds `PaneSpec` from the `CommandBuilder`
    (`get_argv`/`get_cwd`/`iter_extra_env_as_str`, portable-pty 0.9), `create_pane`, sets
    `io = Termhost`, spawns an **exit-watcher** task (polls `exit_status()` →
    `AppEvent::PaneDied`), reuses `spawn_basic_detection_task` against the unfed emulator.
  - `render()` / `collect_dirty_patch()` / `cursor_state()` branch on `termhost_pane()`.
    New free fns `render_termhost_frame` (FrameData→ratatui cells via
    `u32_to_color`/`u16_to_modifier`) and `termhost_dirty_patch` (dirty→full-grid patch,
    rows sized exactly to `area_width`).
- **`src/protocol/wire.rs`** — `u32_to_color` + `u16_to_modifier` made `pub(crate)`,
  un-`#[cfg(test)]`'d, with `#[cfg_attr(not(any(test, feature="termhost")), allow(dead_code))]`
  so the default build stays warning-free.

---

## Verification

- **Default build (feature off):** `cargo build` clean; **1891 bin unit tests pass** —
  in-process path unchanged (all termhost code is `#[cfg(feature="termhost")]`).
- **`--features termhost`:** `cargo build` clean; `cargo clippy` clean except one
  pre-existing unrelated `app/actions.rs:2709` warning; 4 `termhost::proto` tests pass;
  full feature-on suite green.
- **Go side:** `go build -tags ghostty ./cmd/termhost` succeeds.
- Build env: Rust links ghostty via `export ZIG="~/projs/go/herdr-web/.tools/zig-wrapped"`;
  Go needs `export PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig`.

## Live end-to-end smoke test — PASSED (commit `9cd8f35`)

Built a feature-gated integration test `tests/termhost_e2e.rs` (gated `#![cfg(feature =
"termhost")]`; skips/passes when `HERDR_TERMHOST_SOCKET` is unset/unreachable). It drives
the **whole seam** through the real binaries: starts the Go daemon, spawns a real
`herdr server` with `HERDR_TERMHOST_SOCKET` set, creates a workspace over the JSON-RPC API
(root pane → spawned on the Go daemon), attaches a client, sends `echo <marker>` via
`pane.send_text`, and asserts the marker renders in a client `SemanticFrame`.

**Result: green.** Two assertions make it definitive:
1. The marker renders in a client frame — `workspace.create` → Go `create_pane` → `input`
   → Go VT emulator → `pane_frame` → spliced into the compositor → client. ✅
2. `pane.read` (which reads the **local** Rust emulator) returns **len=0** — the local
   emulator is unfed, proving the pane is genuinely Go-backed, **not** an in-process
   fallback that coincidentally works. ✅ (This is exactly the slice-1 degraded behavior.)

Gotchas found while bringing it up (worth remembering):
- The headless server does **not** auto-spawn a pane; you must `workspace.create` over the
  API socket to get one. (First attempt asserted on frames with no pane → failed.)
- Server logs go to `config_dir()/herdr-dev/herdr-server.log` (under the test's
  `XDG_CONFIG_HOME`), not `XDG_DATA_HOME`; on macOS `XDG_DATA_HOME` is ignored.
- `pane.read`/`pane.*` text APIs read the local emulator → empty for termhost panes; assert
  via the rendered client frame instead.
- Run env: `ZIG=~/projs/go/herdr-web/.tools/zig-wrapped` (Rust),
  `PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig` (Go).

Reproduce:
```bash
# daemon
cd ~/projs/go/herdr-web && export PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig
go build -tags ghostty -o /tmp/td ./cmd/termhost && /tmp/td --socket /tmp/herdr-th.sock &
# test
cd ~/projs/rust/herdr && export ZIG="~/projs/go/herdr-web/.tools/zig-wrapped"
HERDR_TERMHOST_SOCKET=/tmp/herdr-th.sock cargo test --features termhost --test termhost_e2e -- --nocapture
```

A **browser-rendered** run (vs. the programmatic SemanticFrame client used here) is still
worth doing manually, but the seam is now proven end-to-end.

```bash
# terminal 1 — Go daemon
cd ~/projs/go/herdr-web
export PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig
go run -tags ghostty ./cmd/termhost --socket /tmp/herdr-termhost.sock

# terminal 2 — herdr with the backend enabled
cd ~/projs/rust/herdr
export ZIG="~/projs/go/herdr-web/.tools/zig-wrapped"
HERDR_TERMHOST_SOCKET=/tmp/herdr-termhost.sock cargo run --features termhost -- <usual-args>
# expect log "connected to Go termhost terminal backend"; a pane should render shell output
```

Watch for: handshake (welcome), `create_pane`, frames rendering, input echo, resize,
exit → pane closes. Likely first issues to check if it misbehaves: row width vs
`area_width` in `termhost_dirty_patch` (must equal pane inner width), cursor mapping,
and whether the retained path vs fallback is taken (`render_prof` events in headless).

## Remaining Phase B work (beyond the smoke test)

- **OSC passthrough** (`pane_title`/`pane_cwd`/`pane_clipboard` Go→Rust) — unblocks
  detection + chrome for termhost panes (currently degraded).
- **Scrollback/selection** exposure; **hyperlinks**; **kitty graphics**.
- **Input encoding** could move to Go (key/mouse encoders); today Rust encodes → raw bytes.
- **Diff optimization:** `termhost_dirty_patch` currently emits all rows when dirty;
  switch to changed-rows once correctness is confirmed.
- **CI:** cache the libghostty-vt `.a` so `-tags ghostty` / `--features termhost` run in CI.

## Commits on `roh/phase-b-termhost-client`

```
9cd8f35 test: e2e smoke test for the termhost backend (step 3)        ← this session
038d45a feat: route the live pane runtime through termhost (step 3)   ← this session
14b2212 feat: termhost terminal-backend seam (feature-gated, no rewiring)
1e4dd9a feat: Rust client for the Go↔Rust orchestration seam
```

**Push target note:** the Rust repo has two remotes — `herdr-origin` →
`ogulcancelik/herdr` (upstream, **no write access** for rohanthewiz, push 403) and
`origin` → `rohanthewiz/herdr-go` (own fork, **pushable**). Pushed to `origin`. Landing on
the upstream needs a PR. The Go repo pushes to `rohanthewiz/herdr-web` (`roh/phase-b`).
