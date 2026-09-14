# The UI went blank because catway and cathost were each waiting to write

*A live freeze diagnosed from the outside, without killing anything. The cause
was a write deadlock across the β socket, and the ping watchdog that should have
broken it was waiting on the same lock. Then three fixes: bound every write,
split the lock, and take socket writes off catway's loop.*

Session: https://claude.ai/code/session_012kHDH7fWkxr85jvxARw49y
Date: 2026-09-13
Repo: `~/projs/go/cats` (branch `main`)
Commits: `0236300` (fixes 1+2: stall-bounded writes, write lock split from
state lock), `67c784c` (fix 3: catway outbox + writer goroutine)

## Request

> I was working in Cats with a few agents and suddenly had a freeze-up -- I
> don't remember exactly when. When I tried to do a Refresh within the MacApp
> I normally run Cats in everything came up blank.
>
> Most importantly are there any agents still working. If they are still
> working I would like to let them finish in the background. I would also
> like to diagnose current state and possibly get to root cause.

then:

> Start on fixes 1 and 2 while fable finishes

then:

> commit and push. Then do fix 3 take socket writes off catway's main loop

## This session ran outside Cats

The shell was Ghostty (`TERM_PROGRAM=ghostty`), not a Cats pane, so the live
app could be inspected. Earlier sessions ran inside Cats.app and couldn't.
Everything below was read-only: `ps`, `lsof`, `netstat -f unix`, `sample`,
transcript tails. No process was signalled.

## State of the agents

| agent | pid | where | state |
| --- | --- | --- | --- |
| `claude --model fable` | 95000 | `~/projs/go/grmob` (workspace `wN`) | **working** throughout: `xcodebuild test`, adb, file reads; transcript still advancing at 22:40 |
| `claude --model opus` | 76685 | `~/projs/go/ced` (workspace `w9`) | finished at 21:43 (`end_turn`, pushed `3986bae`), idle |
| two church agents | — | church | **not killed**: both ended with `/exit` at 22:03:41 and 22:08:21 |

Useful trick: an agent's pane can be found from its environment
(`ps -E -ww -o command= -p <pid>` shows `CATS_PANE_ID=w9:p15`). That label is
the *public* workspace:number handle fixed at spawn, and it goes stale when a
tab moves (`workspace.go:306`), so it is not a runtime pane id.

**Do not quit Cats.app to recover.** `backend.stop` (`cmd/catapp/supervise.go`)
sends SIGTERM to cathost as well as catway, which ends every pane.

## Diagnosis

### The deciding evidence: both socket queues full

```
netstat -f unix -an
Address          Recv-Q  Conn              Addr
4130444bae209f22   6192  9cfce64e53370ee9  …/cats-th-59797.sock   ← cathost end: unread
9cfce64e53370ee9   8192  4130444bae209f22                          ← catway end: unread, full
```

Both ends of the catway↔cathost connection had unread data, and neither was
reading.

Supporting signs:
- catway at 0% CPU.
- Three browser connections stuck in `CLOSE_WAIT`: the page hung up, catway never closed.
- `GET /` still served.

### The cycle, confirmed in code

```
catway orch loop ── d.send, no deadline (daemon.go) ──▶ cathost socket buffer FULL
      ▲                                                        │
mailbox FULL (cap 256, catway.go:780)          cathost reader in dispatch → emit
      ▲                                                        │ out FULL (cap 256)
catway pump blocked in o.post ◀── catway socket buffer FULL ◀── cathost writer blocked
```

- **catway**:
  - `daemon.send` ran on the loop and held `d.mu` across a blocking `WriteMessage`.
  - The pump (`session`'s read loop) hands every event to `o.post`, which is `o.mailbox <- fn` and blocks when the mailbox is full.
- **cathost**:
  - The `Attach` reader calls `dispatch`, which calls `emit`, which blocks on `out <- ev`. Ping answers go the same way.
  - The single writer drains `out` into the conn.
  - The pty `readPump` also blocks on `emit` for title, cwd, clipboard and streamed output.
- **Why the page was blank**: `registerConn` is posted into that same stuck mailbox, so the welcome is never sent.
- **Why it never recovered**: `sendPing` takes `d.mu` before checking `hostPingTimeout` (60s), and `send` held `d.mu` for the whole stuck write. The watchdog could never run.

### Ruled out

- **catway's stderr pipe filling** (a blocked `log.Printf` would give the same picture). `lsof` pipe `SIZE` 16384 is the capacity on all 277 pipes on the machine, not a backlog. `bootTap.Write` returns immediately after boot. catapp sends daemon stdio to `/dev/null`.
- **My own `/ws` probe was not evidence.** It never sent `init`, so of course no welcome came back. Corrected mid-session.

### Timeline (inferred, since no logs exist)

| time | event |
| --- | --- |
| 22:08:21 | church agent `/exit`, exit code 0 |
| ~22:08:31 | default tidy-exit autoclose fires (`defaultAutocloseTTL = 10s`, `reap.go`); pane close → reflow → resize + resync burst |
| 22:08:33 | last write of `session.json` and `history.json` (`~/.local/state/cats`) |
| by ~22:09:35 | loop frozen: the 60s `runHistoryCapture` sweep never wrote again despite busy panes |

The cause is structural and certain. The autoclose burst as the trigger is
likely but unproven: after boot, catway's log output goes to `/dev/null`.

### Side effects while frozen

- Hook replies time out after 10s (`hookRelayReplyTimeout`), so agents don't hang on hooks.
- cats-todo and `catctl` control calls hang.
- A pane changing its title or cwd would park its `readPump` and stop draining that pty.

## Fix 1: bound every β write (`0236300`)

- **`internal/orchestration/stallwrite.go`** adds `NewStallWriter(w, timeout)` and `DefaultWriteStallTimeout = 20s`.
  - The deadline is re-armed per 64 KiB chunk, so it measures **lack of progress, not total duration**. A multi-MB history seed over a slow but draining link is not cut.
  - A write deadline left set would fire on a later write, so it is cleared on success.
  - Writers that can't take a deadline, or a timeout ≤ 0, pass through unchanged.
- **cathost** (`host.go`):
  - `Host.WriteStallTimeout` defaults to 20s, and the `Attach` writer wraps the conn.
  - **The second hang this exposed:** a writer that gave up still left `Attach` hung, because the reader was itself parked in `emit`, and `sessDone` closes only after the reader leaves its loop. The serial accept loop (`cmd/cathost/main.go`) would then have refused a restarted catway forever.
  - Fixed with `sessEnd` (the session ctx's `Done`), which `emit` now also selects on.
  - A stalled write, and only a stall, is returned as the reason the session ended.
- **catway**: `send` writes through the stall writer and closes the conn on failure.

## Fix 2: writes take their own lock (`0236300`)

- `wmu` split from `d.mu`, with `d.mu` never held across I/O.
- `sendPing` used `TryLock`: it skipped a probe when the writer was busy, still started the silence clock, and closed the conn when its own write failed.
- It also fixed a latent bug: `sendPing` used to write without the lock, so a ping could land between another message's header and payload.

Fix 3 then replaced this mechanism (see below). Every property it guaranteed
is still tested.

## Fix 3: socket writes off catway's loop (`67c784c`)

```
send ─encode on caller─▶ outbox.push ─▶ [frame][frame]… ─take─▶ writePump ─▶ stall writer ─▶ conn
                            never blocks        budget: queued bytes (64 MiB)
```

| piece | where | what |
| --- | --- | --- |
| `EncodeMessage` | `internal/orchestration/protocol.go` | a whole frame as one slice, byte-identical to `WriteMessage`. Encoding stays on the loop, the only goroutine allowed to read model data that `m` shares |
| `outbox` | `cmd/catway/outbox.go` | FIFO of encoded frames with a byte budget; push never blocks; `close` drops the queue; `closeWhenDrained` refuses new frames and lets the writer finish |
| `writePump` | `cmd/catway/daemon.go` | one per connection, started in `setConn`; the only post-handshake writer; closes the conn when its outbox ends |
| `send` | same | encode, push; over budget → `dropBackloggedConn` closes box and conn |
| `sendPing` | same | enqueues like everything else (so latency includes the queue); the watchdog's close frees a stuck writer |
| `stop` | same | `closeWhenDrained`, not an immediate close (see below) |

Design choices:
- **Unbounded with a byte budget, not a fixed channel.** A full channel blocks its sender, which would put the loop back on the socket's schedule.
- **The budget** is `8 × MaxFrameSize` (64 MiB). Any legal frame fits, and a whole session's history seeds are only hundreds of KB.
- **Marshal errors** are logged and dropped without closing the conn, because nothing reached the wire. Before this change they closed it incidentally.

**The trap found in review:** a detach (`applyHostRoster`, `hosts.go`) calls
`closePanesOn(d, mine)` and then `d.stop()` on the next line. With a plain
close, those `close_pane` frames would be discarded and the detached machine's
shells left running. `stop` therefore drains without waiting. The flush is
bounded by the writer's stall timeout.

## Tests and verification

New tests:
- **`internal/orchestration`**:
  - `TestStallWriterFailsWhenThePeerStopsReading`
  - `TestStallWriterToleratesASlowPeerThatKeepsReading`
  - `TestNewStallWriterLeavesOtherWritersAlone`
  - `TestEncodeMessageMatchesWriteMessage`
  - `TestEncodeMessageRefusesOversizedFrames`
  - `TestAttachDropsAClientThatStopsReading` (ghostty): places writer, `out`, 300 pumps and the reader exactly as in the live freeze.
- **`cmd/catway`** (ghostty):
  - `TestDaemonSendDropsAPeerThatStopsReading`
  - `TestStuckWriterLeavesTheLoopAndWatchdogFree`
  - `TestBackloggedConnectionIsDropped`
  - `TestSendsArriveInOrder`
  - `TestStopFlushesWhatWasQueued`
  - `TestLoopOutlivesACathostThatStopsReading`, the freeze end to end
  - `TestOutbox*` ×4

Each fix was mutation-checked with `go test -overlay`, which leaves the source
untouched. Every mutant failed as intended:

| mutant | failing test |
| --- | --- |
| `emit` without `sessEnd` (swapped for a nil channel; deleting the line doesn't compile) | `TestAttachDropsAClientThatStopsReading`: Attach never returned |
| `sendPing` queues on `wmu` (fix 2) | `TestStuckWriteDoesNotBlindTheWatchdog`: probe queued |
| `send` writes to the conn synchronously | `TestLoopOutlivesACathostThatStopsReading`: loop stopped |
| `stop` calls `box.close()` | `TestStopFlushesWhatWasQueued`: host received 0 of 50 |

Other checks:
- New tests ran with `-count=5`.
- `go build -tags ghostty ./...` and `vet` are clean.
- `go test -tags ghostty ./...`: 40 packages pass, and the one failure was already there (see Next).
- `-race` could not run: it needs `libghostty-vt-static`, which isn't installed.

## Tooling notes

- Tests need `-tags ghostty`. Without it, most of `orchestration` and all of `cathost` are skipped or excluded, and the untagged "ok" means little.
- `sample <pid>` on these Go binaries shows no Go symbols, so it can't show goroutines. Sockets told the story instead.
- `netstat -f unix -an` Recv-Q is the quickest way to spot a write deadlock across a unix socket.
- The state dir is `~/.local/state/cats`; mtimes of `session.json` (500ms debounce) and `history.json` (60s sweep) bound when the loop last ran.
- zsh gotchas hit this session:
  - A path starting with `-` must be written `./-Users-…` for `tail`.
  - Unquoted `--include=*.go` gets glob-expanded.
  - A bare `===` in `echo` breaks the command.

## Recovery still pending

The running app (built Sep 11) has none of these fixes. Recover only after
fable finishes, with one of:

1. **Quit and relaunch Cats.app.** Every pane ends, layout restores from the 22:08:33 save, and agents can be resumed with `claude --resume`.
2. **Restart only catway and keep the panes.**
   - Send SIGTERM to catway pid 60149 twice. The first sticks in the jammed mailbox (`main.go` posts `Shutdown`); the second calls `os.Exit(1)`.
   - cathost then detaches with its panes alive.
   - Relaunch catway with its original arguments (`--addr 127.0.0.1:8422 --auth none --socket …/cats-th-59797.sock --control-socket …/cats-ctl-59797.sock --hook-socket …/cats-hooks-59797.sock`), logging to a file, and hit Refresh.
   - `reconcile` closes live panes that aren't in the model, but every live pane predates the 22:08:33 save.
   - Caveats: catapp will not supervise that catway, and it can't notice a catway exit.

Then rebuild and install the app to pick up the fixes.

## Next

- **Recover the frozen app once fable (pid 95000) finishes.** Use option 1 or 2 above, then rebuild and install Cats.app. Until then the fixes are only in source.
- **Fix 4: keep daemon logs.**
  - catapp tees catway and cathost stdio to `/dev/null` after boot, so this incident left no log.
  - Write them to a rotating file beside `boot.log`.
  - Have catapp notice a catway exit; today it doesn't (`supervise.go` has no watch).
- **Pre-existing test failure in `internal/inputenc`**: `TestGeneratedKeyCodesParse` and `TestCommonKeysSurviveTheChain` read `cmd/catgen-dart/testdata/golden/keys.g.dart`, which `5add396` deleted. Remove or re-home those tests.
- **Consider bounding cathost's `out` the way catway's outbox is now bounded.** cathost's reader and pty pumps still block in `emit`. That's no longer a deadlock, since the stall writer frees them within 20s, but a stall still freezes pty draining for that window.
- **Run the new tests under `-race`** once `libghostty-vt-static` is available.
- **Confirm the trigger with logs** (depends on fix 4). Today it is only inferred from timestamps as the autoclose reflow burst after a clean agent exit.
- **Carried from 2026-09-11:** the other four flag kinds (`?` `★` `⚠` `✓`) are still tinted text. `FLAG_ICONS` is where drawn icons would go. Deliberately left half drawn at the time; open only if wanted.
