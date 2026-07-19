# WS2 orchestrator — event-loop actor + multi-workspace/tab command table

**Session id:** `0686f4c3-d357-4663-82d2-16010191fb5c`
**Date:** 2026-0713-0934 · **Branch:** `roh/phase-b` (herdr-web)
**Continues:** `2026-0712-2257-ws10-auth-tls-ws8-styled-chrome.md`. Starts WS2 ("the big one",
`fbl_go_port_feasibility_analysis.md` §WS2), extracting a real orchestrator out of gateway2's
hard-coded single-workspace, mutex-based mini-orchestrator.

> First WS2 increment: a pure, tested session-domain package (`internal/app`) implementing the
> §7 command table over the WS1 workspace/layout model, and a full rewrite of gateway2 from a
> `sync.Mutex` model into a **single event-loop actor** driving that session — with §8
> per-connection viewport frame filtering. Multi-tab, multi-workspace, and viewport-switch
> resync now work live; split/close/focus preserved. Race-clean.

---

## Decisions locked up front (both flagged forks)

1. **Concurrency = event-loop actor.** One goroutine owns all state; browser readers + the
   daemon pump post closures onto a mailbox; no lock. (Doc recommended this; it's the idiomatic
   Go port of the Rust AppEvent loop and stops the lock surface growing with the command table.)
2. **Scope = core table + rewire.** `internal/app` + gateway2 rewire with viewport filtering,
   lighting up the WS8 tab bar / workspace list. Deferred: pane.focus_direction/cycle/last/swap/
   zoom/rename/resize_border, read, agent.focus, server.* — pure additions to the dispatch table.

## `internal/app` — the domain layer (pure, no daemon/goroutines; unit-tested)

- `session.go`: `Session` owns `[]*workspace.Workspace` + `active` index. It sits ABOVE the
  daemon seam (`internal/orchestration`) — it owns *what the session is* and *how commands change
  it*, never *how PTYs run*. So it tests like the WS1 models it composes (`session_test.go`, all
  green incl. `-race`).
- Command table (§7 subset): `FocusPane`, `SplitPane`, `ClosePane`; `CreateTab`/`CloseTab`/
  `FocusTab`/`RenameTab`; `CreateWorkspace`/`CloseWorkspace`/`FocusWorkspace`/`RenameWorkspace`.
  Semantics ported from Rust src/app actions: session keeps ≥1 pane; closing a workspace's last
  pane (or last tab) drops the workspace when another remains; new tabs/workspaces auto-activate.
- Queries the runtime needs: `AllPaneIDs` (every pane — all are live PTYs), `VisiblePaneIDs`
  (active ws+tab = the viewport, §8), `FocusedPane`, `PublicPaneID`, `Workspaces`, `ActiveIndex`.

## gateway2 rewrite — the event-loop actor (`gateway.go`, `commands.go`, `daemon.go`)

- **`orch`** replaces `gateway`: sole owner of `*app.Session`, per-pane `paneRuntime`
  (input encoder + cached chrome + desired grid + `created` flag), `conns`, `daemon`, `area`, the
  `visible` set, and a `mailbox chan func()`. `run()` drains the mailbox serially — **no mutex**;
  `post(fn)` is how the per-connection readers, the daemon pump, and the writer mutate state.
- **`applyModel()`** is the post-command pipeline: `syncDaemon` (reconcile the daemon's PTY set to
  the session — spawn missing, resize changed, close dropped; **all** panes across all tabs are
  created, not just visible, so background tabs survive a daemon restart) → `refreshViewport`
  (recompute `visible`, diff newly-visible) → broadcast layout + agents → for newly-visible panes:
  resend cached chrome, `FrameTranslator.Reset()`, request a daemon resync (full frame, §8).
- **`commands.go`**: the §7 dispatch — parse params, call the `Session` method, then `applyModel`
  (mutations) / rebroadcast layout (focus, rename) / daemon passthrough (scroll).
- **`daemon.go`**: the pump posts every β event onto the loop. Frames forward **only** for
  `visible` panes; chrome (title/cwd/agent/modes/exited) is cached on the runtime regardless of
  visibility and forwarded only when visible; the agents rollup is always global (§8). `reconcile`
  reseeds `created` from the daemon's surviving-pane set and re-applies the model.
- Default session is now **one** workspace/tab/pane (dynamic model), not the old hard-coded 2-pane
  split. `main.go`: `newOrch` + `go o.run()`.

## Front-end (`web/index.html`)

- Tab bar: click a tab → `tab.focus{num}`, `✕` → `tab.close{num}`, `+` → `tab.create`.
- Sidebar workspaces: click → `workspace.focus{id}`, `+ workspace` → `workspace.create`.
- Viewport switches ride the existing `applyLayout` (panes not in the new layout are removed, new
  ones created; the server's resync repaints them). Handlers send the exact params wsprobe2 proved.

## wsprobe2 additions (`cmd/wsprobe2`)

- Ops: `tabnew`, `tabfocus:NUM`, `tabclose[:NUM]`, `wsnew`, `wsfocus:ID`; `tabs:N`/`workspaces:N`
  count asserts alongside `panes:N`; `expect`/`absent`/`click_text` accept `f` = the focused pane
  (content checks survive pane-id churn).

## Verification (live, macOS — socket under /tmp for the sun_path limit)

- Unit: `go test ./...` green; `-race` on `internal/app`/gwauth/gwtls green.
- Build/vet: `go build ./...` (untagged) + `-tags ghostty ./...` + `go vet` (both) all clean.
- **Multi-tab/workspace/viewport** (one probe, real shells): base pane works; `tab.create`→
  `tabs=2`, viewport=1 pane, the new tab's shell streams + echoes; `tab.focus 1`→back, `BASE_T1`
  resynced; `workspace.create`→`workspaces=2`, fresh tab/pane, its shell works; `workspace.focus
  w1`→back, resynced; `split`→2, `close`→1. **All ✓.**
- **Daemon-restart reconcile**: probe A creates a 2nd tab; kill+restart termhost; gateway redials
  + reconciles → session survived (2 tabs), **both** tabs' panes recreated, focused shell accepts
  input (`AFTER_RESTART`). ✓
- **Race**: `-race` gateway2 under two concurrent probes → **0 data races, no panics** (one probe
  rc=1 is expected logical interference: v1 viewport is global/shared across connections, so
  concurrent `wsfocus`/`tabnew` from two clients move the viewport under each other — by design).
- **Headless Chrome** render: tab bar `1`+`+`, sidebar `● herdr-web`/`+ workspace`, single-pane
  default, `connected · 108×29`; no JS throw; screenshot matches.

## Notes / leftovers

- **Uncommitted** (this session did not commit). New: `internal/app/`, `cmd/gateway2/commands.go`.
  Modified: gateway2 `gateway.go`/`daemon.go`/`main.go`/`web/index.html`, `cmd/wsprobe2/main.go`.
- **Viewport is global for v1** (§10 allows per-connection but doesn't require it): one browser is
  the norm; multiple browsers intentionally share the viewport. Per-connection viewports = later.
- The **actor loop lives in gateway2** (package main) for now — transitional. WS4 (CLI/control API)
  will want the same command table, at which point hoist `orch` (or a reusable `Orchestrator`) into
  `internal/app` behind a PaneBackend/Sink interface. The pure `Session` already sits in the right
  place for that.
- Deferred §7 commands are cheap follow-ups (each is one `Session` method + one dispatch case).
- Two stray root binaries (`gateway2`, `wsprobe2`) from single-package `go build` were removed —
  not committed. Build with `-o $scratch/...` to avoid them.
