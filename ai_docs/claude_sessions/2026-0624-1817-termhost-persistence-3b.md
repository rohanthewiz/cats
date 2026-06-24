# herdr-web (Go) — Phase B: termhost persistence 3b (persistent daemon + reconnect/adopt)

**Date:** 2026-0624-1817 · **Session:** 73da5cf4-bb13-4940-861c-4ac85372e90e
**Repo:** `~/projs/go/herdr-web` (Go daemon) · paired with `~/projs/rust/herdr` (orchestrator).
**Branch (Go):** `roh/phase-b` · **Branch (Rust):** `roh/phase-b-termhost-client`

> The big piece of the persistence plan (`ai_docs/termhost-persistence-design.md`).
> Makes termhost shells **survive a herdr restart** as *live processes*, not just
> replayed history (3a). Scenario B at full fidelity — strictly better than the
> in-process cold restore, which loses the live process.

---

## What shipped — e2e proven

A termhost shell now survives a full herdr restart: herdr A spawns a persistent
daemon + pane, herdr A is **killed**, the daemon and the live shell persist, herdr B
restarts the same session, **reconnects** to the daemon and **adopts** the surviving
shell — its pre-restart output is intact and it runs new commands.

### Go — commits `c2710bc`, `35207c0`, `f7752c8`
- **Persistent lifecycle** (`host.go`): split `Host` into `Start`/`Attach`/`Stop`.
  The flusher + per-pane read/detect pumps run for the **daemon** lifetime, not the
  connection. A connection is a swappable `out` sink — `emit` drops when no client is
  attached (panes keep absorbing PTY output; the next client gets a full resync).
  `Serve` is now a thin managed-mode wrapper (`Start`+`Attach`+`Stop`). `h.done` →
  `h.closed` (daemon teardown); added `sessDone` so an in-flight `emit` unblocks on
  detach.
- **Reconnect/resync**: on `hello`, reply `welcome{panes:[ids]}` then replay each live
  pane's state (full frame + modes + cwd + title + agent). Last-emitted chrome is
  recorded under a new `pane.metaMu` (`setCwdMeta`/`setTitleMeta`/`setAgentMeta`) so
  resync can read it from another goroutine.
- **`request_resync` command** (`35207c0`): a reconnecting herdr adopts panes
  *asynchronously* (one per restored pane), so it can't rely on the post-`hello`
  auto-replay (those events race pane registration and get dropped). After adopting,
  the client pulls each pane's state on demand — registered-then-requested, no race.
- **GC / lifecycle**: `shutdown` command (clean quit → daemon exits) + idle timeout
  (no client attached for N min → exit). New `--persistent` and `--idle-timeout`
  flags + a `runPersistent` accept loop (serial `Attach` = single writer).
- **SIGHUP** (`f7752c8`): persistent mode **ignores SIGHUP** so herdr's death (which
  closes its controlling tty and SIGHUPs the session) doesn't kill the daemon. Honors
  only explicit SIGINT/SIGTERM.
- Protocol additions are backward-compatible (`Welcome.panes` omitempty, new
  `shutdown`/`request_resync` commands) — inert until herdr opts into persistent mode.
- Tests: reconnect proof (shell survives a client cycle over `net.Pipe`, welcome lists
  it, resync replays the screen, same shell drives), `request_resync`, shutdown, idle
  timeout. Race-clean.

### Rust — commit herdr `222f0cb`
- **`mod.rs`**: socket keyed by **session** (`data_dir()/herdr-termhost.sock`, not
  pid). `connect_or_spawn` **reconnects** to a surviving daemon first; only spawns a
  fresh one (with `--persistent`, **setsid**-detached) if none is reachable.
  `shutdown()` sends the `shutdown` command on a clean quit (whether we spawned or
  reconnected); crash/handoff just drops the link.
- **`client.rs`**: capture `welcome.panes` → `surviving_panes()`; `adopt_pane()`
  registers a pane's `PaneState` + signal sink **without** `CreatePane`, then
  `request_resync` to repaint. `request_shutdown()` for clean quit.
- **`proto.rs`**: `Welcome.panes`, `Command::Shutdown`, `Command::RequestResync` (+tests).
- **`pane.rs`**: `finish_termhost` gains an `adopt` branch — restore adopts a surviving
  pane when `surviving_panes().contains(pane_id.raw())`; otherwise cold-seeds a fresh
  shell as before.
- **e2e** (`tests/termhost_e2e.rs`): `termhost_pane_survives_herdr_restart` (the
  headline proof, driven over the JSON API with a named session); managed test
  reworked to the persistent contract (daemon outlives herdr's death; `shutdown`
  command reaps it). Helpers: session-managed spawn, `wait_for_pane_text`,
  `termhost_send_shutdown` (framed-protocol cleanup).

## Key facts for future me

- **`run_server()` never calls `termhost::shutdown()`** (only the monolithic TUI path
  does). This is *correct* for handoff (daemon must survive) but means a clean **server**
  exit leaves the daemon until the idle timeout. (Follow-up.)
- **Adoption requires session restore**: herdr B only adopts panes whose `pane_id.raw()`
  the restore re-creates (stable from `LayoutSnapshot::Pane(u32)`) AND that are in
  `surviving_panes()`. Named session (`HERDR_SESSION`) gives reliable restore in the e2e.
- **Reconnect repaint = `request_resync`, not the auto-replay.** Auto-replay-on-hello is
  kept but our async-adopt flow relies on the explicit per-pane pull.
- **Daemon detaches** (setsid + ignore SIGHUP) — both needed; setsid removes the
  controlling tty, SIGHUP-ignore is the portable backstop / hand-launched case.

## Verification (all green)

- Go: `-tags ghostty` build; `go test -tags ghostty ./internal/...` (incl. new
  persistence/resync/shutdown/idle tests); `-race` on host tests; vet/gofmt clean.
- Rust: feature-off **1892** + feature-on **1920** (+3 proto) + clippy clean.
  **10/10** e2e against real daemons — incl. `termhost_pane_survives_herdr_restart`
  and the reworked managed-persistence test; render/reattach/agent still pass.
- *Env note:* this box has zig 0.16 (vendored libghostty-vt needs 0.15.2). Reused the
  prior `vendor/libghostty-vt/zig-out/lib/libghostty-vt.a` via a no-op `ZIG` wrapper
  to build the herdr test binary; the Go daemon builds with `-tags ghostty` directly.
- *Test gotcha:* a single `--persistent` shared daemon contaminates socket-based e2e
  tests (panes persist across sequential herdrs → id collisions). Run those against a
  **default-mode** daemon (fresh Host per connection); BIN-based tests spawn their own.

## Commits

```
Go   (roh/phase-b):                 c2710bc persistent daemon — reconnect/resync (Go side)
                                    35207c0 request_resync command (deterministic per-pane repaint)
                                    f7752c8 persistent daemon ignores SIGHUP
                                    c79bc0c docs: 3b shipped + follow-ups
Rust (roh/phase-b-termhost-client): 222f0cb reconnect + adopt surviving shells (3b)
```

## Next — 3b follow-ups, then gap #4

3b follow-ups (not blockers, see design doc "3b status"):
- **Orphan panes**: close `surviving_panes − adopted` after restore (session/daemon drift).
- **Server-mode clean-quit shutdown**: wire `run_server()` to `shutdown()` distinguishing
  clean-quit from handoff (today relies on idle timeout).
- **Live handoff (scenario C)**: shares the reconnect/adopt path (works because the old
  server doesn't kill the daemon); add a dedicated e2e (restart/SIGKILL = B is covered).
- **Single-writer hardening** (token/lock); **socket path length** in deep config dirs.

Then **gap #4** = flip termhost default + delete the in-process PTY path (mostly
subtraction — the local `PaneTerminal` only serves mirrored modes by then; herdr stops
linking ghostty). Kitty graphics still back-burnered.
