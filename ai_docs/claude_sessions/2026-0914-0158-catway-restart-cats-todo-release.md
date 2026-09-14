# catapp restarts catway, cats-todo v0.31.2, and two cleanups

*catapp now answers a catway exit nobody asked for with the same launch again,
bounded by a restart budget, then an overlay with a "Restart catway" button.
cats-todo shipped the hangup fix as v0.31.2. catway's startup line prints a real
URL, and the last `cmd/catgen-dart` leftovers are gone.*

Session: https://claude.ai/code/session_012kHDH7fWkxr85jvxARw49y
Date: 2026-09-14
Repo: `~/projs/go/cats` (branch `main`), plus `~/projs/go/cats-todo` (branch `main`)
Previous doc: `2026-0914-0134-daemon-logs-bounded-cathost-and-next-list.md`

## Request

> Address these
> - catapp only logs an unexpected catway exit. The window stays blank. Consider restarting catway automatically (a catway-only restart is now proven to keep panes) or showing an error page.
> - Release cats-todo with the hangup fix (tag and version bump, as its releases usually are) so the plugin host picks it up.
> - catway's startup line prints `localhost127.0.0.1:18471` when `--addr` includes a host (`main.go`, the "serving at" log). Cosmetic, pre-existing.
> - Stale `cmd/catgen-dart` mentions in `wire/vocab.go` comments (lines ~267, 639, 1325) and regeneration steps in `ai_docs/plans/remote-*.md`. There's also an old compiled `catgen-dart` binary at the repo root.

The answer to "restart or error page" was both: restart first, the error page
(as an overlay) once restarts are spent.

## 1. catway auto-restart (`cmd/catapp`)

### Why a catway restart is safe

- **Panes** live in `cathost -persistent`; a catway-only restart kept the same shell pids in the previous session's live check.
- **Layout** is saved to disk and restored by the new catway.
- **The page** reconnects by itself: `ws.onclose` → `setTimeout(connect, 1500)` in `cmd/catway/web/js/37-session.js`.
- **Same address:** no lock or pid files in catway; the control and hook sockets are removed before listen (`control.go`, `hooks.go`); Go listeners set SO_REUSEADDR, so the port rebinds at once.

### Flow (`restart.go`)

```
catway exits ──▶ quitting? ──yes──▶ done
                    │ no
                    ▼
             restart budget ──spent──▶ overlay in every window
                    │ left                     │ "Restart catway"
                    ▼                          ▼
              sleep (backoff) ──────────▶ launch, same args
                                               │
                              ready? ──no──▶ counts as another exit
                                │ yes
                                ▼
                   clear the overlay, wait for the next exit
```

- **`restartBudget`:** a sliding window, not a streak. `restartMax = 5` launches within `restartWindow = 1m`; delay `250ms << n` capped at `4s` (250ms, 500ms, 1s, 2s, 4s). A refusal spends nothing; an occasional crash always gets the fast restart.
- **Why a budget at all:** a catway that dies on every start would write a goroutine dump into the 1 MiB `daemons.log` each time and rotate the first, most useful one out.
- **Giving up:** logs `ERROR catway exited 5 times within 1m0s (last: …) — no more automatic restarts`, drains any stale retry request, calls `ui.catwayDown(detail, canRetry)`, then waits on `retry` or `quit`.
- **The button** resets the budget and spends one slot, so a person gets a fresh five, not a single attempt.
- **`relaunch`:** `b.launch` → `b.ready(gw)`. A catway that doesn't come up is stopped and logged as `ERROR restarted catway (pid N) did not come up`; success logs `WARN catway restarted (pid N) at http://…` and calls `ui.catwayBack()`.
- **cathost is not restarted, on purpose:** its exit takes the panes, and relaunching it would silently swap live shells for fresh ones. The overlay says "cathost has exited too … Quit and reopen Cats", with no button (`canRetry = false`).

### `supervise.go` changes

- **`backend` gained:** `mu`, `stopped`, `quit`, `log` (the `rotatingLog`; `daemonLog` in the app, a temp file in tests), `catwayPath`/`catwayArgs`, `ready func(*daemonProc) error`, `restarts`, `retry chan struct{}` (buffer 1).
- **`launch(slot, path, args…)`:** checks `stopped` and assigns the slot under the same lock `stop` takes. This also closes an old gap: a daemon launched during a quit could previously be orphaned.
- **`stop()`:** sets `stopped` and closes `quit` *before* signalling, under the lock; reads both slots under it; safe to call twice. The supervisor therefore reads every exit after a quit as ours.
- **`waitReadyOrExit(addr, timeout, p)`:** the readiness dial also selects on `p.done`. Startup now fails at once with the exit status instead of waiting the full 10s. `waitReady` stays as a wrapper with `nil`.
- **`daemonProc.exitStatus()`** factored out of `watch()`.
- **Helpers:** `currentCatway`, `isStopped`, `cathostExited` (nil cathost counts as up).
- **`main.go` `bootLocal`:** after the windows restore, `activeBackend.Store(b)` and `go b.superviseCatway(windowBackendUI{})`.

### Overlay (`backenddown.go`) and the bridge

- **Injected, not swapped in:** `winManager.evalAll(js)` → new native `catsEvalAll` runs the script in every window. `showHTML` would replace the page, and `catsWindowsJSON` reads each window's workspace off its URL, so a moved window would be saved as the primary view.
- **The page underneath keeps retrying its WebSocket,** so recovery is just `backendClearJS()`.
- **Markup** is built in Go, HTML-escaped, and passed as a JSON string literal (`<`, `>`, `&` escaped), so the detail can't break out of a tag or a string.
- **Updates in place** by element id `cats-backend-down`: stopped → "Restarting catway…" → stopped never stacks overlays.
- **Button:** disables itself, shows "Restarting…", calls `window.catsRestartBackend()`.
- **Bridge:** `kBridgeJS` gained `catsRestartBackend` → `catsApp` `{op:'restart'}`. `catappConnectForm` routes `restart` to `requestBackendRestart()` *before* its `activeRemote == nil` return, since local mode has no remote runtime. `activeBackend` is an `atomic.Pointer[backend]`.

### Tests

- **`restart_test.go`:**
  - `TestRestartBudgetBacksOffThenRefuses`
  - `TestSuperviseRestartsACatwayThatExits` (shell script exits once, then `exec sleep 30`)
  - `TestSuperviseGivesUpThenRestartsOnRequest` (exits until a marker file exists; the test creates it and calls `requestBackendRestart`)
  - `TestStopEndsTheSupervisorDuringBackoff` (1h backoff; also checks `launch` after stop returns `errBackendStopped`)
  - `TestCatwayDownDetail`
- **`backenddown_test.go`:** detail escaping (decodes the JSON literal back out), button only when `canRetry`, clear targets the id.

## 2. cats-todo v0.31.2 (cats-todo repo)

- **Version bump:** patch, per the repo's rule (a fix). Both `const version` in `main.go` and `cats-plugin.toml`.
- **Release:** commit `66319bc chore(release): v0.31.2`, annotated tag `v0.31.2 — exit when the terminal goes away`, `git push origin main v0.31.2`. `go vet` and `go test ./...` passed first.
- **Plugin host:** takes it only through `catctl plugin update rohanthewiz.cats-todo` (`internal/plugin/update.go`: fetch + hard reset + rebuild).

## 3. catway startup line (`cmd/catway/serveurl.go`)

`serveURL(scheme, addr)`: `SplitHostPort`, then a wildcard host (`""`,
`0.0.0.0`, `::`) becomes `localhost` and `JoinHostPort` brackets IPv6. An
unparseable address is printed as given. The file and `serveurl_test.go` carry
`//go:build ghostty`, because `main.go` does.

## 4. catgen-dart leftovers

- **`wire/vocab.go`:**
  - `CommandSpec` doc: what walks the table now (`CommandNames`, `vocab_test.go`), and the generator as history.
  - `FlagInfo`: the Dart `flagInfo` getter sentence replaced.
  - `HostInfo.Default`: `is_default` kept because the key is the contract.
- **Plans:** regeneration instructions rewritten in `remote-catalog.md`, `remote-extras.md`, `remote-dream.md` and `multi-window.md` (not a `remote-*` file, but it had the same steps). Phase-log lines "catgen-dart goldens regenerated" stay: they record what happened.
- **Binary:** the root `catgen-dart` was tracked in git, so it shows as a deletion. `.gitignore` never had a line for it. `internal/inputenc/keycodes_ghostty_test.go` already described it as history.

## Verification

- **Tests:**
  - `go test -race -count=1 ./cmd/catapp/` passes; `go vet ./cmd/catapp/`, `gofmt -l` clean.
  - `go test -count=1 ./...` and `go test -tags ghostty -count=1 ./...`: all pass.
  - `go vet -tags ghostty ./cmd/catway/ ./wire/` clean; `TestServeURL` passes.
- **Mutation via `go test -overlay`:** `stop` without `close(b.quit)` makes `TestStopEndsTheSupervisorDuringBackoff` fail (5.1s).
- **Overlay JS in node:** a throwaway overlay test dumped the three scripts, and node ran them against a stub DOM: created with fixed position; the button calls the bridge and disables; the restarting state updates in place (one append); a repeat down stays in place; clear removes it; a second clear doesn't throw.
- **No orphans:** `pgrep -fl "sleep 30"` found nothing after the runs.
- **Not verified live:** the real Cats.app was not run, so the overlay in WKWebView, the `catsEvalAll` path and the button bridge are untested end to end. The running app predates these changes.

## Tooling notes

- A test-only file can be added to a package without touching the tree: an `-overlay` JSON `Replace` entry mapping a non-existent `cmd/catapp/zz_dump_test.go` to a scratchpad file.
- zsh aborts a whole command on an unmatched glob (`grep --include=*.go` → `no matches found`). Quote the pattern or use `-rl` with explicit dirs.

## Next

- **Rebuild and reinstall Cats.app** (`make macapp`) to pick up this session's and the previous session's fixes; the running app predates both.
- **See the restart path in the real app** after the reinstall. Kill the app's catway once: the window should come back by itself, with `WARN catway restarted` in `daemons.log`. Kill it 6 times inside a minute: the overlay should appear, and **Restart catway** should recover it with the panes intact.
- **Update the installed cats-todo plugin:** `catctl plugin update rohanthewiz.cats-todo` picks up v0.31.2. A user action.
- **Resume the agents if wanted.** `claude --resume` in the grmob pane (fable, work pushed as `fb75a4b`) and the ced pane (opus, finished). A user action.
- **Confirm the freeze trigger with logs.** Needs a recurrence. Stalls, drops, unexpected exits and now restarts land in `daemons.log`; the autoclose reflow after a clean agent exit is still only inferred.
- **Check `daemons.log` after a few days of real use.** Demote any WARN lines that fire routinely (candidates: `session ended` on ordinary disconnects, `dropping slow browser connection` for sleeping tabs). Any `catway restarted` line is now also a crash worth reading.
- **A window opened or reloaded while catway is down stays blank after recovery.** The failed load leaves no page to reconnect, and the overlay only reaches pages that exist. A fix would reload windows with no loaded URL on `catwayBack`. Minor, since automatic restarts take well under a second.
- **`waitReady` has no production caller now** (only the `waitReadyOrExit(…, nil)` wrapper remains). Remove it or leave it.
- **Non-goal: restarting cathost automatically.** Its exit takes the panes; relaunching it would hide that loss. The overlay tells the user to quit and reopen instead.
