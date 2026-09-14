# Daemon logs kept at warning level, cathost's emit can no longer block, and the rest of the Next list

*Fix 4 kept the daemons' warnings in a file. Working through the rest of the
previous Next list found two more latent causes behind the 2026-09-13 incident:
cathost's pty pumps could park behind a slow client, and cathost handed an
ignored SIGHUP to every pane process. Both are fixed and checked live, with a
before/after comparison against the pre-change cathost.*

Session: https://claude.ai/code/session_012kHDH7fWkxr85jvxARw49y
Date: 2026-09-14
Repo: `~/projs/go/cats` (branch `main`), plus `~/projs/go/cats-todo` (branch `main`)
Previous doc: `2026-0913-2330-agent-status-and-recovery-limits.md`

## Request

> Do fix 4 keep daemon logs but only for warning and higher severity levels.
> Next do the remaining items in the Next list

## How the work was split

Three independent items went to background agents while the daemon-side changes
were done inline:

| agent | repo | result |
| --- | --- | --- |
| cats-todo exit on hangup | cats-todo | root-caused and fixed with a pty test |
| stale inputenc tests | cats | re-homed onto a Go list of W3C codes |
| remaining flag icons | cats (web) | `?` `★` `⚠` `✓` drawn |

## Fix 4: daemon logs, warning level and above

### Levels: `internal/dlog`

The daemons had no log levels, only `log.Printf`. `dlog` adds `Warnf`,
`Errorf` and `Fatalf`. Each writes a level word at the start of the message,
straight after the log package's timestamp:

```
2026/09/13 22:08:31 WARN catway: cathost local answered no ping in 1m0s — closing the connection
```

- **Why a text prefix rather than slog:** the existing calls are Printf-style, the reader needs one bit per line, and the producer and reader share one package so the two can't drift.
- **`dlog.Classify(line)`** returns `Routine`, `Severe` or `Crash`. Crash matches the runtime's own `panic: `, `fatal error: ` and `SIG…: ` banners at column 0 (no timestamp), so a logged message that merely mentions a panic doesn't match.
- **`dlog.StripTimestamp`** lets the reader stamp lines with its own clock.
- **About 90 calls converted** (a perl script over file:line pairs, then removal of unused `log` imports). The rule used: `ERROR` for lost work or broken invariants (save failures, marshal errors, "released a slot it did not hold", ledger write failures, session restore failure); `WARN` for dropped connections, disabled features, refused requests, config fallbacks. `log.Fatalf` became `dlog.Fatalf`, and cathost's `fmt.Fprintln(os.Stderr, "cathost:", err)` exits became `dlog.Errorf`.
- `catway: WARNING auth disabled` became `WARN catway: auth disabled`. It appears once per launch in local mode, which is harmless.

### The kept file: `cmd/catapp/daemonlog.go`

```
daemon stderr ──▶ io.MultiWriter ─┬─▶ bootTap      (startup only)
                                  ├─▶ daemonTap ──▶ dlog.Classify ──keep──▶ daemons.log
                                  └─▶ bestEffort{our stderr}
```

- **Location:** `~/Library/Application Support/cats/daemons.log`, rotating to `daemons.log.1` at 1 MiB. The file is opened on the first kept line, so a quiet session creates nothing, and a relaunch continues it.
- **Line format:** `2026-09-14 00:10:56.123 catway[17449] WARN catway: …`, with the daemon's own timestamp replaced by catapp's.
- **Crash reports:** once a stream starts one, every later line on that stream is kept (the goroutine dump).
- **`bestEffort` wrapper, a latent freeze found on the way:** `io.MultiWriter` stops at the first failing writer, and exec's copy loop stops with it. If catapp's stdout failed (a dev terminal closed), the daemon's pipe would fill and the daemon would block on its next log line. The taps now come first, and our own stdio can't fail.

### Too much noise

- **Dial retries:** `catway: cathost dial … (retrying)` stays routine. A single `WARN … unreachable for 10s … (still retrying)` fires once per outage (`dialOutageWarnAfter`).
- **False error at quit:** `control.go` logged `ERROR control server stopped: … use of closed network connection` on every graceful quit, because cleanup closes the listener. It is now skipped for `net.ErrClosed`. A live quit then logged nothing severe.

## catapp supervision (`cmd/catapp/supervise.go`)

`backend` now holds `*daemonProc`s, not raw `*exec.Cmd`s.

- **Watcher:** `startDaemon` starts `watch()`, which calls `cmd.Wait` (so output copiers finish first and a crash report lands before the exit line). An exit nobody asked for is logged: `ERROR catway (pid N) exited while the app was running: exit status 2`, or `WARN` if the status was 0 (`catctl server.stop`, cathost idle timeout).
- **`WaitDelay = 2s`:** stops a grandchild holding the pipes from hiding the exit.
- **`stop(grace)`:** SIGTERM, then after `grace` a logged SIGKILL, then an `ERROR` if the process is still alive `daemonKillWait` (2s) later. Grace is 5s for catway and 3s for cathost. `waitOrTimeout` is gone, since it was the thing that "left it to the OS", which does nothing to a child.

## catway's SIGTERM path (`cmd/catway/signals.go`)

`handleSignals(sigc, post, shutdown, exit, deadline)`:
- The first signal starts `time.AfterFunc(shutdownDeadline=3s, exit(1))` and `go post(shutdown)`, so a full mailbox can't block the handler.
- A second signal exits at once.
- `sigc` is buffered for 2.
- The normal graceful path took **0.31s** live.
- The 3s deadline sits below catapp's 5s grace, so a quit from the app never needs SIGKILL for catway.

## cathost: `emit` can no longer block

### The shared outbox

`cmd/catway/outbox.go` moved to `internal/orchestration/outbox.go`, exported as
`Outbox` (`Push`, `Take`, `Sent`, `Close`, `CloseWhenDrained`, `IsClosed`, plus
new `Queued` and `Budget`). catway's `daemon.go` and `writestall_test.go` were
updated, and the `TestOutbox*` tests moved with it.

### `host.go`

- **`out chan any` (cap 256) replaced by `box *Outbox`,** plus `sessCancel` captured with it under `connMu`. `sessEnd` was removed; nothing selected on it once emit couldn't park.
- **`emit`** encodes on the emitter's goroutine and pushes without blocking.
  - Past the budget: `WARN cathost: client is more than 64 MiB behind … dropping it (panes preserved)`, then close the box and cancel the session.
  - Encode failure: `ERROR`, then drop the client. That keeps the old behaviour: a skipped frame would leave the client's screen diverged.
  - The `endSession` sentinel now maps to `box.CloseWhenDrained()`.
- **Writer** loops over `box.Take()` and `defer cancel()`. The ctx goroutine closes both the box and the conn.
- **Backpressure:** `flushDirty` returns early while `box.Queued() > flushBackpressure` (1 MiB). Dirty flags survive, so the next frame is one diff against the last taken snapshot. This keeps the adaptive behaviour the blocking channel gave a slow but live remote catway, without blocking anyone.
- **Knob and caller:** `Host.OutboxBudget` is a test knob. `hookrelay.attached()` now checks `h.box`.

### Tests (`attachstall_test.go`)

- **`TestAttachDropsAClientThatStopsReading` (rewritten):** 300 emitters × 10 events must all return at once, and Attach ends after the stall bound.
- **`TestAttachDropsAClientPastTheBudget`:** stall bound 1h, budget 64 KiB; the drop is immediate.
- **`TestClientBackloggedTracksTheQueue`.**

## cathost handed SIGHUP=ignored to every pane process

The cats-todo agent found this. `runPersistent` did `signal.Ignore(SIGHUP)`, and
an ignored disposition survives fork and exec. A scratch Go program confirmed it:

```
ignore: child SIGHUP disposition = 1   (SIG_IGN)
notify: child SIGHUP disposition = 0   (SIG_DFL)
```

- **Fix:** `signal.Notify(hup, SIGHUP)` with a goroutine that discards the signals. cathost still survives a hangup, and children start with the default.
- **Not visible from a pane shell:** interactive zsh resets SIGHUP for its own children, so a pane test showed `0` under both builds. The difference is for processes cathost spawns directly (the plugin host's cats-todo) and for anything that doesn't reset.

## cats-todo: exits when its terminal goes away (cats-todo repo)

Root cause, as the agent found it:
- **SIGHUP:** Bubble Tea v2.0.8 catches only SIGINT and SIGTERM (`tea.go:656`), and under cathost SIGHUP arrived ignored anyway (see above).
- **EOF:** on macOS a closed master gives the slave 0-byte reads. ultraviolet treats `io.EOF` as no error (`terminal_reader.go:152-154`). Bubble Tea's `readLoop` then ends silently while `eventLoop` keeps rendering and ticking, which was the steady CPU use. EIO does propagate.

Changes:
- **`hangup.go`:** `terminalWatch`, one cancellable context via `tea.WithContext`, cancelled on SIGHUP (the handler also overrides an inherited ignore) and by a `hangupInput` that embeds `*os.File` and cancels on EOF or EIO. Embedding keeps Bubble Tea's raw-mode path.
- **`launch.go`:** `runProgram` skips the error print and the terminal reset when the terminal has gone.
- **Tests:** `hangup_test.go` runs a helper in a real pty with SIGHUP ignored, with subtests `master_closed` and `sighup`. `pty_darwin_test.go` and `pty_linux_test.go` open ptys without a library.
- **`go.mod`:** `golang.org/x/sys` moved to direct (same version).
- **Repro:** before the fix, a process with SIGHUP ignored was still running 6s after the master closed. After it, the process exits with status 0 in 0.06s. Removing the fix makes both subtests fail at 5s.

## inputenc tests re-homed

- **What they guarded:** `keys.g.dart` (deleted in `5add396`) was the phone's USB HID → W3C table, and the tests checked every code either reaches `libghostty.ParseKey` or is on a list of 107 codes ghostty doesn't support.
- **What's left of that chain:** cats-mobile now types text through `pane.send_input` and sends no `wire.Key`, so the phone half is gone. The web client (`20-keys.js`, `code: e.code`) still produces codes.
- **The change:** `knownW3CCodes` holds the 269 names from the last table. `TestGeneratedKeyCodesParse` became `TestW3CCodesReachGhostty` (the same two-way check), and `TestCommonKeysSurviveTheChain` gained `Home`/`End`, which the web client makes itself. 162 of 269 codes parse, the same split as before.

## Flag icons

`FLAG_ICONS` in `07-workspaces.js` gained `question`, `star`, `warn` and `done`,
drawn with the existing `drawIcon` builder on the 16-unit grid with fixed
colours matching each kind's default theme token:
- **question:** amber speech bubble with a `?`.
- **star:** gold star with a darker bevel.
- **warn:** orange rounded triangle with a `!`.
- **done:** green disc with a tick.

Header comments in the JS and `28-flags.css` no longer call the set partial. A
stale duplicate of the header comment was removed (comment only). Custom glyphs
still take the text path.

## `scripts/agent-status`

The previous doc's `fable-status`, generalised:
- **Pids:** `[pid …]`, defaulting to every process whose `comm` basename is `claude`. `pgrep -x claude` finds nothing on macOS, because pgrep matches the versioned executable's own name.
- **Extra output:** the `CATS_PANE_ID` and `rc` exit codes (1 not running, 2 stuck). `status` is read-only in zsh.
- **Why a script, not `catctl`:** catctl hangs along with catway's loop, which is exactly when this is needed.

## Verification

### Live, on isolated instances (never the running app)

Setup:
- Binaries built into the scratchpad.
- Sockets passed as **relative** paths with the scratch dir as cwd: the absolute scratchpad path is over the 104-byte unix socket limit (`bind: invalid argument`).
- Separate port, `--state-dir`, empty `--config`, `-hook-socket -`, `-control-socket -`, `-manifest-update=false`.
- Panes driven with `CATS_CONTROL_SOCKET=ctl.sock catctl`.

Scenario: pane 2 runs a loop printing a line and an OSC 2 title every 20ms;
catway gets SIGSTOP; the pane's TIOCOUTQ is sampled.

| cathost | +3s | +10s | +18s | +26s | log |
| --- | --- | --- | --- | --- | --- |
| **pre-change** (HEAD `9a04ec2`, built in a temp worktree) | 0 | **1024** | **1024** | 0 | `session ended: peer stopped reading` at +23s |
| **new** | 0 | 0 | 0 | 0 | `WARN session ended: …no progress for 20s` at +20s |

Then SIGKILL catway, start a new one:
- cathost logged `client connected`.
- The new catway logged `restored session … (1 workspaces, 2 panes)`.
- The shell pids were unchanged, and the loop kept ticking (1877 → 1910).

**A catway-only restart keeps panes.** Graceful quit: catway exited in 0.31s,
and cathost logged `client disconnected (panes preserved)`.

### Tests

- **`go test -tags ghostty -race ./...`:** all pass (the static libghostty `.pc` is now present).
- **`go test ./...`, `go test -tags ghostty ./...`, both `go vet`s, `gofmt -l`, `make jstest`:** clean.
- **Mutation checks via `go test -overlay`:**

| mutant | failing test |
| --- | --- |
| `handleSignals` posts synchronously with no deadline | `TestSecondSignalExitsWhenTheLoopIsJammed`, `TestShutdownDeadlineExitsWhenTheLoopNeverRunsShutdown` |
| `daemonProc.stop` without `Kill` | `TestStopEscalatesToSIGKILL` |

- **cats-todo:** `go vet ./... && go test ./...` pass.

## Tooling notes

- Unix socket paths cap at 104 bytes; the scratchpad path alone nearly fills that. Use relative socket paths with the right cwd.
- Background daemons started with `&` inside a tool call can die with the call. `nohup … & disown` survives, but then they have to be tracked and stopped by hand. One stray catway from a failed first attempt held the test port.
- A mutant test left a `sleep 60` orphan: `exec sleep 60` shows as `sleep 60` in `ps`, so `pkill -f 'exec sleep 60'` missed it.
- macOS `ps` has no `ignored` keyword; probe a signal disposition with `python3 -c 'import signal; print(signal.getsignal(signal.SIGHUP))'` in the child.
- To build a pre-change binary without touching the tree: `git worktree add --detach <scratch> HEAD`, build with the main repo's `PKG_CONFIG_PATH`, then `git worktree remove --force`.

## Next

- **Resume the agents if wanted.** `claude --resume` in the grmob pane (fable, work pushed as `fb75a4b`) and the ced pane (opus, finished). A user action.
- **Rebuild and reinstall Cats.app** (`make macapp`) to pick up this session's fixes; the running app predates them.
- **Confirm the freeze trigger with logs.** Needs a recurrence. Stalls, drops and unexpected exits now land in `daemons.log`; the autoclose reflow after a clean agent exit is still only inferred.
- **catapp only logs an unexpected catway exit.** The window stays blank. Consider restarting catway automatically (a catway-only restart is now proven to keep panes) or showing an error page.
- **Check `daemons.log` after a few days of real use.** Look for WARN lines that fire routinely (candidates: `session ended` on ordinary disconnects, `dropping slow browser connection` for sleeping tabs) and demote any that do.
- **Release cats-todo** with the hangup fix (tag and version bump, as its releases usually are) so the plugin host picks it up.
- **catway's startup line prints `localhost127.0.0.1:18471`** when `--addr` includes a host (`main.go`, the "serving at" log). Cosmetic, pre-existing.
- **Stale `cmd/catgen-dart` mentions in `wire/vocab.go`** comments (lines ~267, 639, 1325) and regeneration steps in `ai_docs/plans/remote-*.md`. There's also an old compiled `catgen-dart` binary at the repo root.
