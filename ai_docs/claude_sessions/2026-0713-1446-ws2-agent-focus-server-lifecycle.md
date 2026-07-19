# WS2 §7 agent.focus + server.reload_config/server.stop — the last deferred commands

**Session id:** `246b1c21-1b15-4141-ba44-1c69dcdcdd57`
**Date:** 2026-0713-1446 · **Branch:** `roh/phase-b` (herdr-web)
**Continues:** `2026-0713-1359-ws2-read-selection-extraction.md`. That session finished `read`
(the first daemon round-trip command) and flagged three remaining deferred §7 commands as "a
different shape again": `agent.focus`, `server.reload_config`, `server.stop`.

> Completed the §7 command table's remaining backend commands. Two commits, race-clean and
> green, each independently building, with unit tests + live end-to-end verification against a
> real ghostty daemon. `agent.focus` reveals a pane across workspace/tab boundaries;
> `server.reload_config` acks as a documented no-op; `server.stop` cleanly exits the gateway
> while the persistent termhost daemon survives.

This completes §7: **every command in the protocol table (`phase-c-ws9-protocol.md` §7) is now wired.**

---

## The plan (all four leftovers, first two implemented)

Presented a plan for all remaining leftovers, then the user chose to start the **two §7 backend
command groups** (headless-verifiable via wsprobe2, matching the recent cadence):

1. **agent.focus** — reveal+focus a pane that may live off-viewport.
2. **server lifecycle** — `reload_config` + `stop`.

Deferred (future sessions): the `pane_text` round-trip command (last unrequested β event);
hoisting `orch` behind a `PaneBackend`/`Sink` into `internal/app` (WS4 prereq); the
browser-side selection UI (front-end, not headless-verifiable).

## agent.focus — reveal across workspace AND tab

The one non-obvious part. `pane.focus` is click-to-focus: the pane is already in the current
viewport, so `Session.FocusPane` sets the active workspace + focuses in-layout but **does not
switch the tab**. `agent.focus` is different: the agents sidebar is **global** (§8), so its
target can be in another workspace *and* another tab — `pane.focus` wouldn't surface it.

New **`Session.RevealPane(id)`** (`internal/app/session.go`): `workspaceIndexOf(id)` →
`ws.SwitchTab(tabIdx)` → `tab.Layout.FocusPane(id)` → `s.active = idx`. Kept distinct from
`FocusPane` (which stays viewport-local by design). The gateway dispatch calls `o.applyModel()`
afterward because the viewport may change (like `tab.focus`/`workspace.focus`, not the
broadcast-only `pane.focus`).

## server.reload_config / server.stop — lifecycle, not model mutation

- **`server.reload_config`**: gateway2 is flag-configured only — no config subsystem exists yet.
  Wired as an **acknowledged no-op** (`reply(true)` + a log line), so the command round-trips
  end-to-end and is ready to re-read config when one lands. Honest stub, not a fake success.
- **`server.stop`**: `reply(true)` first (browser gets its `cmd_result`), then broadcast
  `browserproto.NewShutdown()` to all browsers, then fire **`orch.stop`** — a hook injected by
  `main`. rweb's `Server` has **no graceful shutdown** (it's a raw-listener server, not
  `http.Server`), so the hook `os.Exit(0)`s after a **250ms grace** that lets the final writes
  flush. The persistent termhost daemon is a **separate process and deliberately survives** —
  the whole point of `-persistent`. `orch.stop` is `nil` in tests (guarded no-op).

## Files

- `internal/app/session.go` — `RevealPane`; `internal/app/session_test.go` — cross-ws+tab reveal
  (create tab 2, focus tab 1, create ws2 active → reveal target proves both dimensions) +
  unknown-pane error.
- `cmd/gateway2/commands.go` — `CmdAgentFocus` (→ `RevealPane` → `applyModel`),
  `CmdServerReloadConfig` (ack + log), `CmdServerStop` (reply → broadcast shutdown → `o.stop`);
  `log` import added.
- `cmd/gateway2/gateway.go` — `orch.stop func()` field.
- `cmd/gateway2/main.go` — wires `o.stop` (250ms grace → `os.Exit(0)`).
- `cmd/gateway2/commands_test.go` (**new**, `//go:build ghostty`) — server.stop
  (ack→shutdown→hook order), nil-hook safety, reload_config ack, agent.focus unknown-pane. Bare
  `orch` + fake `client` (reuses `newReadHarness` from `read_test.go`); agent.focus test builds a
  real `app.NewSession` for the error path (no daemon needed — it fails before `applyModel`).
- `cmd/wsprobe2/main.go` — `agentfocus` / `reloadconfig` / `serverstop` ops + shared
  **`awaitCmd(name, params, timeout)`** helper (mints an id, sends, blocks on `cmd_result`,
  fails on not-ok/dead/timeout) + header op docs.

**Pre-built machinery reused:** command constants (`CmdAgentFocus`/`CmdServerReloadConfig`/
`CmdServerStop`) and `PaneParams` already existed in `browserproto/cmd.go`; `NewShutdown()` +
`MsgShutdown` already existed in `browserproto` (§5). Nothing new below the seam.

## Verification (macOS, harness unchanged from prior sessions)

- Sockets under `/tmp`. Build: `PKG_CONFIG_PATH=~/…/libghostty-vt/zig-out/share/pkgconfig go
  build -tags ghostty` for termhost/gateway2; wsprobe2 untagged. Run `termhost -socket … -persistent`,
  then `gateway2 --addr :PORT --socket … --auth none`.
- `go test ./...` green; `go test -tags ghostty ./cmd/gateway2` green; **`-race` clean** on both
  the unit tests and a live `-race`-built gateway2 driving the full script (agent.focus crosses
  loop + daemon-pump + writer goroutines; server.stop crosses loop + `AfterFunc` + `os.Exit`).
  `go vet` + build clean for untagged **and** `-tags ghostty`. All binaries to `$scratch`.

### Live results (deterministic, real ghostty)

- **agent.focus cross-tab reveal**: type `RACEMARK` in pane 1 → `tabnew` (layout shows **only**
  pane 2, pane 1 absent) → `agentfocus:1` → pane 1 back in the streaming viewport with `RACEMARK`
  (asserted via `rect:1:x:eq:0` + `expect:1:RACEMARK`).
- **server.reload_config**: acked; logged "no config subsystem yet; nothing to reload".
- **server.stop**: acked → gateway2 logged "server.stop received — shutting down" and **exited**;
  the **termhost daemon stayed alive** (the key requirement).

## Notes / leftovers (unchanged priorities for next session)

- **`pane_text`-backed round-trip command.** `pane_text` is the last unrequested β event. Follow
  the `read` pattern — generalize `pendingReads`/FIFO/timeout/flush into a shared pending-request
  helper, then add a buffer-capture command over `RequestText`→`pane_text`. `awaitCmd` (wsprobe2)
  is now the client-side driver pattern for any such command.
- **Hoist `orch` behind a `PaneBackend`/`Sink` interface into `internal/app`** — the WS4
  (CLI/control-API) prerequisite so the same command table serves both protocols. The actor loop
  still lives in gateway2 (package main); `RevealPane` and the read/stop helpers move cleanly.
- **Browser-side selection UI** (mouse-drag to originate `read` coordinates) — front-end,
  `web/index.html`, not headless-verifiable; same category as deferred keybindings/drag-handles.

## Split-commit note

Two commits on `roh/phase-b`, split by feature (`agent.focus` vs `server.*`). The dispatch,
test, and wsprobe2 files each contained both features, so the split was done by editing the
working tree down to the agent.focus-only state (backing up the mixed files first), verifying it
built + tested, committing, then restoring the full state for the second commit. Each commit's
tree was confirmed to build and pass tests independently.

- `c29ea32` feat(gateway2): WS2 §7 agent.focus — reveal a pane across workspace/tab
- `58d2d31` feat(gateway2): WS2 §7 server.reload_config + server.stop
