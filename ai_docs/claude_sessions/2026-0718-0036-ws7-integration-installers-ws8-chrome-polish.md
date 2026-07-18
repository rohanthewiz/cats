# WS7 integration installers + WS8 chrome polish

**Session id:** `7ee39d32-c657-4231-a891-3627bf3dc920`
**Date:** 2026-0718-0036 · **Branch:** `roh/phase-b` (herdr-web)
**Continues:** `2026-0717-2357-ws5-manifest-updates-ws6-notifications.md` (its
"what remains before WS11" list was exactly WS8 polish + WS7).

> Two workstreams closed in one session. **WS7**: the Rust `src/integration`
> module (5,913 LOC — shell-hook installers for 12 coding agents + embedded
> assets) is now Go (`internal/integration`, ~3.8k LOC incl. 93 tests), wired
> as the offline `herdrctl integration` verb family. Assets are byte-identical
> with the Rust tree, so installs interoperate across the transition. **WS8**:
> the gateway2 front-end grew the missing chrome — split-border drag resize,
> pane scrollbars, real modal dialogs, right-click context menus, a ⌘K command
> palette/navigator, keyboard help, workspace agent badges, app-level browser
> title — all screenshot-verified via a headless-Chrome Playwright drive
> against the live gateway+termhost.

---

## WS7 — integration installers (`internal/integration` + `cmd/herdrctl`)

**Spec-first flow:** an exploration agent produced
`ai_docs/ws7-integration-port-spec.md` (committed) from the Rust sources; a
second agent implemented from it; I reviewed, rebranded, and live-verified.

**Package layout** (`internal/integration/`):
- `assets/` — verbatim `cp -R` of the Rust asset tree (verified `diff -r`
  identical; `.ps1` files copied but not embedded). `assets.go` embeds per-file
  (a bare dir pattern would skip underscore-prefixed `hermes/__init__.py`).
- `integration.go` — Target enum (12 targets, snake_case labels), dir
  resolution (env override → tilde expansion → `$HOME`), statuses /
  recommendations, `HERDR_INTEGRATION_VERSION=` marker parsing (missing marker
  ⇒ legacy ⇒ Outdated), availability (PATH executable-bit lookup + codex
  standalone layout), `PaneEnv(socket, paneID, publicID)`.
- `installers.go` — all 12 per-target Install*/Uninstall* + path/result structs.
- `jsonobj.go` — **order-preserving JSON** (token-stream decode, serde-style
  2-space pretty marshal) so unrelated user settings keys survive in place.
- `hooks.go` — the three hook shapes: nested (claude/codex/droid/qodercli),
  flat (copilot, `bash` field), simple (cursor, + top-level `version: 1`);
  exact idempotency + prune-empty-groups/events semantics; sh single-quoting.
- `toml.go` / `yaml.go` — line-based editors ported by hand (no parser libs;
  format preservation is load-bearing): codex top-level-`[features]`-only
  `hooks = true`, kimi sentinel block (`# >>> herdr kimi integration`) with 10
  `[[hooks]]` events, hermes `plugins.enabled` YAML editor (flow lists, quoted
  scalars, inline comments, flat block lists).
- `version.go` — version-triple parsing/ordering + the kimi gate (`kimi
  --version`: warning line when unrunnable, hard error < 0.14.0).

**CLI** (`cmd/herdrctl/integration.go` + main.go dispatch): `herdrctl
integration install|uninstall <target>`, `status [--outdated-only]`, `help`.
Fully offline — dispatched before any socket dialing (and before the flag
re-parse, so `--outdated-only` survives). Rust's exact usage strings, exit
codes (0/1/2), and stdout/stderr split; printed commands rebranded
`herdr` → `herdrctl` so help text is runnable today (prose like "installed
herdr integrations" kept).

**Verified live** against a scratch `CLAUDE_CONFIG_DIR`: install wrote the
0755 hook + `SessionStart→session` (matcher `*`), **removed a deprecated
herdr `PreToolUse` entry while preserving the user's own hook with key order
intact**; second install byte-identical (idempotent); `status` → `current
(v5)`; uninstall removed only herdr entries; unknown target exit 2, missing
dir exit 1 with Rust's message.

**Deliberate scope/divergences:** unix-only (Windows → Rust's not-supported
error; no `.ps1` install path); no `logging::integration_action` equivalent;
`OutdatedUpdateNotice` returns the string (CLI prints it); `PaneEnv` adds
`HERDR_ENV=1`.

## WS8 — gateway2 chrome polish (server + `web/index.html`, 845 → ~1.6k lines)

**Gap analysis first:** an exploration agent mapped every ratatui chrome
surface (`src/ui`) against the web front-end; most gaps were pure front-end
(protocol already wired but ignored client-side).

**Server (small):**
- New app-level **browser-tab title**: `appTitle()` = focused pane's effective
  title, else active workspace name; `broadcastTitle()` dedupes via
  `orch.lastTitle`; emitted from applyModel/BroadcastLayout/
  BroadcastPaneTitle/daemon pane_title + sent on connect. (The `title` handler
  had existed client-side since WS9 with zero server call sites.)
- **Bug fix:** `registerConn` sent `rt.title` instead of `effectiveTitle(pid)`
  — a pane.rename didn't survive a page reload.

**Front-end:**
- **Split resize**: `layout.borders` (sent since WS9, ignored) now render as
  drag handles → `pane.resize_border`; layout rebroadcast per step, drag
  survives div recreation (listeners on window, border id/area stable).
- **Pane scrollbar**: thumb from per-frame `scroll {off,max,rows}`, hidden on
  alt-screen, brightens when scrolled up; draggable (relative `scroll` deltas,
  rAF-throttled).
- **Modal system** (`openOverlay`/`dialogInput`/`dialogConfirm`): rename
  pane/tab/workspace (double-click titles or menus — tab/workspace rename
  finally has UI; `window.prompt` gone), confirm close-workspace, confirm
  stop-server. Enter/Esc per herdr.
- **Context menus**: pane header + canvas (with paste; replaces browser menu
  only when the app isn't mouse-capturing), tabs, workspaces.
- **⌘K / Ctrl+Alt+K command palette / navigator**: fuzzy subsequence scoring
  over panes **across all workspaces** (live `pane.list` query + agent-state
  chips from the rollup; jump via `agent.focus`), tabs, workspaces, and ~15
  §7-backed commands. Also a status-bar "⌘K palette" button.
- **Keyboard-help modal** (copy-mode table hydrated from
  `window.__herdrKeys`/defaults), **update banner** (`update_ready` handler,
  previously dead protocol), non-agent notify kinds always toast, **copy-mode
  status-bar indicator** (replaces the transient toast), **zoom indicator**
  (click to unzoom), **tab-bar overflow** (active scrolled into view, wheel
  scrolls the bar).
- **Workspace agent badges**: computed client-side from the agents rollup
  (`item.workspace` groups) — chosen over populating the server's
  `AgentSummary` field: no staleness, no extra traffic (termhost re-emits
  agent state every tick and the rollup already rides each one).

**Key routing:** overlays own the keyboard (`uiOpen()` early-return in onKey;
modal inputs stopPropagation; capture-phase Escape closes). Terminal keys
otherwise untouched.

## Verification

- Full repo green: `go build/vet/test -count=1 ./...` untagged **and**
  `-tags ghostty -race` (PKG_CONFIG_PATH →
  `~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig`);
  `gofmt` clean (fixed pre-existing drift in `notify_test.go`); `go mod tidy`.
- **Live WS8 (protocol):** wsprobe2 — `resizeborder r 0.3` → pane width 36/120
  exactly; `Title` ×2 (connect + focused-pane rename).
- **Live WS8 (browser):** Playwright (`playwright-core` + system Chrome,
  headless) drove the real gateway+termhost: 11 screenshots covering base
  chrome, split, border drag (0.5→0.31, matching the 200px drag), palette +
  fuzzy filter, palette→rename-dialog→applied rename, context menu,
  confirm-close-workspace, scrollback with visible thumb, help modal. Only
  console error: favicon 404. Driver: scratchpad `drive.mjs`; shots in
  `/tmp/herdr-ws8/shots/`.
- **Gotcha hit:** `pkill -f "herdr-ws8/gateway2"` didn't match `./gateway2`
  invoked from cwd — a stale instance kept the port and contaminated the first
  drive (which is what exposed the registerConn title bug). Kill by port:
  `lsof -ti:8431 -sTCP:LISTEN | xargs kill`.

## Files

- **WS7:** **new** `internal/integration/` (9 source + 12 test files + assets),
  **new** `cmd/herdrctl/integration.go` (+`integration_test.go`),
  `cmd/herdrctl/main.go` (offline dispatch + docs), `go.mod` (tidy:
  go-toml/v2 now direct), **new** `ai_docs/ws7-integration-port-spec.md`.
- **WS8:** `cmd/gateway2/web/index.html` (the bulk), `cmd/gateway2/gateway.go`
  (lastTitle/appTitle/broadcastTitle + registerConn title fix),
  `cmd/gateway2/daemon.go` (broadcastTitle on pane_title),
  `cmd/gateway2/notify_test.go` (gofmt only).

## Notes / leftovers

- **Hook-report ingestion is the missing seam**: installed hooks speak
  herdr's JSON-RPC (`pane.report_agent_session` / `pane.report_agent` /
  `pane.release_agent`) to `HERDR_SOCKET_PATH`; the Go side has no such API
  and panes don't get `HERDR_ENV`/`HERDR_PANE_ID`/`HERDR_SOCKET_PATH` yet
  (β `CreatePane.Env` exists; `integration.PaneEnv` is ready). WS5/WS2 tail —
  do this before calling detection parity done.
- **WS8 deliberately not ported** (need new protocol/vocab): worktree dialogs
  (new/open/remove — the largest dialogs.rs surface), settings modal, global
  launcher beyond reload-config, tab/workspace drag-reorder, bell/activity
  markers, onboarding/release-notes overlays, dedicated mobile chrome.
- Palette hover moves the selection (row mousemove → sel), so a resting
  pointer highlights a second row alongside keyboard selection — cosmetic.
- Playwright note: no global playwright module; `npm i playwright-core` in the
  session scratchpad + `channel: 'chrome'` (system Chrome) works headless.
- Per the workstream map: WS0–WS10 now all have their cores done; what remains
  before **WS11 cutover** is the hook-ingestion seam above, the deferred
  chrome, and packaging/CI.
