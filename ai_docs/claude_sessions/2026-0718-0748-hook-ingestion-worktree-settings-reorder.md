# Hook-report ingestion seam + deferred chrome (worktrees, settings, reorder)

**Session id:** `dff9eead-84ba-4ae6-9410-215399ee130a`
**Date:** 2026-0718-0748 · **Branch:** `roh/phase-b` (herdr-web)
**Continues:** `2026-0718-0036-ws7-integration-installers-ws8-chrome-polish.md`
(its two leftovers — the hook-ingestion seam and the deferred WS8 chrome — are
exactly this session).

> Two workstreams closed. **Hook seam**: gateway2 now serves herdr's
> `pane.report_agent` / `pane.report_agent_session` / `pane.release_agent` API
> on a new unix socket, wire-compatible with the Rust server so the WS7
> installed assets work against either; panes get `HERDR_ENV`/`HERDR_PANE_ID`/
> `HERDR_SOCKET_PATH` at spawn. Detection parity is now real: hook authority
> arbitrates against the daemon's process/screen detection per the Rust rules.
> **Chrome**: worktree dialogs (new/open/remove), a settings modal
> (`config.get`/`config.set` over the §7 table), and tab/workspace pointer
> drag-reorder — all live-verified (socket probes + Playwright screenshots).

---

## Hook-report ingestion (cmd/gateway2/hooks.go + seams)

Spec-first: four exploration agents mapped the Rust API/arbitration
(`src/app/api/panes.rs`, `src/terminal/state.rs`, `src/agent_resume.rs`), the
Go landing zones, and the chrome surfaces before any code.

**Placement decision:** the API lives in **gateway2**, not termhost — hooks
address panes by *public* id and only gateway2 owns `PublicPaneID`, the agent
cache, the rollup, and the notify pipeline. termhost needed zero changes (its
`CreatePane.Env` → `buildEnv` seam already forwarded per-pane env).

- **Transport** (`serveHooks`/`serveHookConn`): one newline-framed JSON request
  per connection (1 MiB cap, 5 s deadline), reply in herdr's exact shape —
  `{"id","result":{"type":"ok"}}` / `{"id","error":{"code","message"}}` (NOT
  ctlproto's `{ok}` envelope; the shape is asset-interop contract). Socket
  0600, stale-socket cleanup, `--hook-socket` flag + `server.hook_socket`
  config (default `/tmp/herdr-hooks.sock`, `"none"` disables), non-fatal on
  listen failure (env injection is then disabled too).
- **Arbitration port** (simplified but rule-faithful):
  - *Reserved native sources* (claude/codex/copilot/droid/qodercli/cursor):
    session-ref only — `report_agent` state is ignored, release is a no-op.
    Claude's SessionStart hook only ever anchors the resume `session_id`.
  - *Full-lifecycle sources* (pi/omp/hermes/opencode/kilo): authority overrides
    detection entirely while live.
  - *Everything else* (incl. kimi): authority wins except a detected visible
    blocker upgrades a non-blocked hook state (same agent, detection not older).
  - `seq` per-source monotonic (stale/equal dropped silently with ok; missing
    seq accepted only as a source's first report). Release/exit record a
    suppression (agent+session) so late packets can't resurrect; a changed
    session ref (new conversation) clears it. A conflicting *detected* agent
    drops a live authority. `pane_exited` clears authority (daemon.go).
  - Session refs: official `herdr:<agent>` sources only; pi may use the
    absolute-path form; id ≤512 / path ≤4096, control chars rejected.
- **Publish refactor** (notify.go): `onPaneAgent` and the hook handlers both
  funnel through `publishAgent(rt)` — arbitrated `effectiveAgent()` state is
  what broadcasts/rollups/events/toasts see; the notify transition baseline is
  the last *published* pair (`rt.pubAgent/pubState`), so hook- and
  detection-driven changes dedupe against each other. `agentsMsg`,
  `broadcastPaneChrome`, and `registerConn` all emit effective values now.
- **Reverse resolver**: `Session.PaneByPublicID` accepts `"w1:p3"` and the
  `p_<raw>` fallback — the two forms `PaneEnv` emits.
- **Env injection**: `createPane` sets `cp.Env` from `integration.PaneEnv`
  (first production caller of that WS7 function).
- **Tests**: hooks_test.go (lifecycle, reserved-native downgrade, blocker
  override, conflict drop, exit suppression, seq table, session-ref validation,
  env map, socket end-to-end with herdr's exact claude request) + resolver
  tests in internal/app.

**Live-verified** (scratch termhost+gateway2, sockets in a short /tmp dir —
macOS `sun_path` 104-byte limit bites scratchpad-length socket paths): real
pane env showed the three vars; python socket client ran claude session report,
hermes working (chrome showed `hermes/working` on a fresh connection), stale
seq dropped, `pane_not_found` for unknown pane, release cleared, late duplicate
stayed dead.

## Deferred chrome (backend by me for reorder; worktrees/settings via an implementation agent against written briefs; all reviewed + live-verified)

- **Reorder**: `tab.move` {num,index} / `workspace.move` {id,index} — insert-gap
  semantics + active-identity preservation ported from Rust `move_tab`/
  `move_workspace`; `Workspace.MoveTab` already existed, `Session.MoveWorkspace`
  is new. BroadcastLayout (arms saveSoon → order persists via snapshot slice
  order). Front-end `beginReorderDrag`: 4px threshold, window-level listeners,
  accent `.dropbar` insertion indicator, click-suppression after drop.
- **Worktrees**: new pure `internal/worktree` (Rust word-list slug gen,
  `BranchToPathSlug`, porcelain parse, dirty-error detect — both substrings —
  command builders w/ stderr-carrying errors). §7 `worktree.list/create/open/
  remove`; git runs off-loop (StartRead pattern: goroutine + o.post).
  `worktree.remove` runs from the main worktree (git refuses `-C` into the
  checkout being removed) and signals `dirty_worktree_requires_force:` so the
  UI escalates to "delete anyway". Create → new workspace at the checkout
  (new `Session.CreateWorkspaceAt`; `CreateTab` now inherits workspace
  `IdentityCwd`). Config `worktrees.directory` default `~/.herdr/worktrees`.
  Dialogs: new (slug prefill select-all-on-type, live path preview, "branch is
  required", git stderr inline), open (filterable list, open/detached/root
  status), remove (danger confirm + red force escalation). Menu + palette wired.
- **Settings modal**: `config.get`/`config.set` (validate → `config.Save`
  whole-struct YAML marshal → ReloadConfig re-render; adopts the default path
  on first save; deliberately writes config-file values, not flag-overridden
  effective ones). Theme rows (swatch+hex, live `:root` preview, Esc rollback
  via new `modalCleanup` close hook), font, copy-mode keys, read-only server
  section; gear button + palette entry.

## Verification

- Full repo green untagged **and** `-tags ghostty -race` (`PKG_CONFIG_PATH` →
  `~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig`);
  `gofmt` clean repo-wide (also fixed pre-existing drift in browserproto
  down.go/proto_test.go + workspace/persist.go — toolchain-update alignment).
- Playwright (playwright-core + system Chrome, headless) against the live
  stack: 16 shots — tab reorder `[1,2,3]→[3,1,2]` with mid-flight indicator,
  workspace reorder, ws context menu, new-worktree dialog (prefill visibly
  selected), real `git worktree add` → workspace opened → danger-confirm
  remove → workspace closed (branch preserved per spec; test branch deleted
  after), open-worktree dialog, settings modal, accent save → YAML written →
  next page load re-themed (visible in shots). Only console error: favicon 404.
- **Gotchas hit:** Playwright `.click()` into the prefilled branch input
  deselects the select-all (concatenated name in the first drive) — type
  without clicking; `#tabbar .tab` matches the "+" add button — use
  `:not(.add)`; scratch gateway2 initially loaded the user's real
  `~/.config/herdr/config.yaml` — settings tests MUST run with `HERDR_CONFIG`
  pointed at a scratch file or config.set will overwrite the real one.

## Files

- **Hook seam:** **new** `cmd/gateway2/hooks.go` (+`hooks_test.go`);
  notify.go (publishAgent), gateway.go (paneRuntime hook fields, hookSocket,
  createPane env, effective-value emits), daemon.go (exit clear), main.go
  (flag+serve+stop), internal/app/session.go (PaneByPublicID),
  internal/config (Server.HookSocket).
- **Chrome:** **new** `internal/worktree/`, `cmd/gateway2/worktrees.go`,
  `cmd/gateway2/settings.go` (+tests); command_vocab.go/commands.go/
  browserproto cmd.go (8 new commands total incl. tab.move/workspace.move),
  session.go (MoveTab/MoveWorkspace/CreateWorkspaceAt), internal/config
  (Worktrees, Save, DefaultPath), web/index.html (dialogs, settings,
  drag-reorder), main.go wiring.

## Notes / leftovers

- **Agent-session persistence not ported**: hook-reported session refs
  (claude's resume id) live only in memory — a gateway restart loses them, so
  resume-on-restore (Rust `agent_resume::plan`, `pending_agent_resume_plan`)
  has no Go counterpart yet. Next candidate before WS11.
- `config.Save` marshals the whole struct: hand-written YAML comments are lost
  on first modal save (noted in code). Copy-mode rebinds apply on next page
  load (toasted).
- Rust's `pane.report_metadata` / `pane.clear_agent_authority` (display
  overlay + TTL) not ported — no asset sends them; CLI-only in Rust.
- Not ported from Rust worktrees: linked-worktree membership persisted on the
  workspace (sidebar grouping), "start from parent workspace" refusal —
  the web adaptation resolves repo from the focused pane statelessly.
- Per the workstream map, remaining before **WS11 cutover**: agent-session
  persistence above, packaging/CI, and the still-deferred niche chrome
  (global launcher, drag-reorder of panes, bell/activity markers, onboarding).
