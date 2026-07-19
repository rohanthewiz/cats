# herdr-web (Go) — Phase B: termhost persistence 3b — live handoff (scenario C)

**Date:** 2026-0625-1612 · **Repo:** `~/projs/go/herdr-web` (Go daemon) · paired with
`~/projs/rust/herdr` (orchestrator).
**Branch (Go):** `roh/phase-b` · **Branch (Rust):** `roh/phase-b-termhost-client`

> Continuation of the 3b follow-ups (`…1539…`). Picked up the last open follow-up:
> the **live handoff (scenario C) e2e**. Writing the test surfaced that scenario C
> **never actually worked** — the prior session note's "works because the old server
> doesn't kill the daemon" was wrong. Wrote the e2e *and* fixed the implementation.
> **Rust-only change; the Go daemon needed nothing** (it already keeps panes on
> disconnect). Not yet committed/pushed — left in the working tree for review.

---

## What the e2e exposed (scenario C was doubly broken)

New test `termhost_pane_survives_live_handoff` (managed daemon + termhost pane → trigger
`server.live_handoff` → assert the pre-handoff marker survived and the adopted shell runs
a new command). First run failed hard, then revealed a second, deeper problem:

1. **fd dup error.** `perform_live_handoff` dup'd a PTY master fd for *every* pane. A
   termhost pane owns no local fd (`PaneRuntimeIo::Termhost` → `duplicate_handoff_fd`
   returns `Err("termhost backend has no PTY master fd")`), so the whole handoff failed
   and rolled back: `{"error":{"code":"handoff_failed","message":"termhost backend has
   no PTY master fd"}}`.

2. **Deadlock + shell kill.** Even past #1, the daemon's **single-writer serial Attach**
   (`cmd/termhost/main.go`: the accept loop calls `h.Attach` inline; a second client
   waits in the backlog until the first detaches) deadlocks the handoff:
   - replacement can't get `welcome`/adopt until the old server detaches;
   - old server can't exit (and detach) until the replacement reports ready.

   And the old server's *clean* exit drops its termhost runtimes → `PaneRuntime::Drop` →
   `io.shutdown()` → `ClosePane` → kills the very shells the replacement must adopt.

## The fix (Rust-only — `~/projs/rust/herdr`)

The daemon already does the right thing: on a client EOF, `Attach` returns and panes are
**kept** (persistence). So the old server just has to (a) not fd-handoff termhost panes
and (b) detach from the daemon early so the slot frees.

- `TerminalRuntime::is_termhost()` (+ `PaneRuntime` / `PaneRuntimeIo`) — new accessor;
  returns false when the feature is off so `headless.rs` stays feature-agnostic.
- `perform_live_handoff` skips termhost panes when building `pane_by_terminal`, so they
  drop out of the fd-dup, manifest, pause, and preserve machinery. The captured snapshot
  still carries them, so the replacement's restore re-adopts via the daemon.
- `TermhostClient::detach_for_handoff()` — `shutdown(Shutdown::Both)` on the daemon
  socket. The daemon reads EOF, returns from serial `Attach`, frees the single-writer
  slot for the replacement; panes stay alive. After this, `send` fails on the dead
  socket, so the later `ClosePane` from dropping runtimes is a **harmless no-op** — the
  shells survive. `termhost::detach_for_handoff()` peeks the cached client and calls it.
- `perform_live_handoff` calls `termhost::detach_for_handoff()` right after snapshot
  capture (panes recorded) and before the fd dance — so the replacement can adopt while
  the old server finishes the local-PTY handoff and exits.

**Why no Go change:** the single-writer serial Attach + persistence is exactly the
"old disconnects → new connects → adopt" contract from the design doc (steps 5/7). The
bug was entirely on the Rust orchestrator side: it wasn't detaching, and it was fd-ing
panes that have no fd.

## The lifecycle contract — now all three exit paths e2e'd

| Exit path | Daemon | Termhost shell | Test |
|---|---|---|---|
| Clean quit (SIGINT/API shutdown) | dies | gone | `termhost_clean_server_quit_stops_daemon` |
| Crash (SIGKILL) | survives | survives | `termhost_managed_daemon_is_persistent_and_survives_herdr_death` |
| Restart after crash | survives → reconnect+adopt | survives | `termhost_pane_survives_herdr_restart` |
| **Live handoff (binary upgrade)** | **survives (detach, not kill)** | **survives → adopt** | **`termhost_pane_survives_live_handoff`** ← new |

## Verification (all green)

- Rust feature-on: `--bin herdr` unit suite **1921** pass (unchanged). clippy clean (only
  the pre-existing `actions.rs:2722` warning). feature-off `cargo check` builds.
- termhost e2e **12/12** across both daemon modes:
  - BIN-managed (clean-quit, persistence, restart, **live-handoff**) against a freshly
    built `-tags ghostty` daemon.
  - SOCKET (render, agent identity, agent working-state, reattach) against a
    hand-launched default-mode daemon.
- `tests/live_handoff.rs` (local-PTY, feature-off): 15/16. The one failure,
  `live_server_holds_one_pty_master_fd_per_pane`, fails **identically on the clean
  baseline** (`had 2 /dev/ptmx fds; expected 1` at the *pre-handoff* checkpoint) — a
  pre-existing macOS `lsof`/default-workspace count quirk, not this change.
- *Env:* same zig-0.16-vs-0.15 workaround — a no-op `ZIG` wrapper reuses the prebuilt
  `~/projs/rust/herdr/vendor/libghostty-vt/zig-out/lib/libghostty-vt.a`; Go daemon built
  with `PKG_CONFIG_PATH=…/zig-out/share/pkgconfig go build -tags ghostty ./cmd/termhost`.

## Files touched (Rust, uncommitted)

```
src/server/headless.rs   perform_live_handoff: skip termhost panes + detach daemon
src/termhost/client.rs   TermhostClient::detach_for_handoff (shutdown socket, keep panes)
src/termhost/mod.rs      termhost::detach_for_handoff (peek client)
src/pane.rs              PaneRuntime{,Io}::is_termhost
src/terminal/runtime.rs  TerminalRuntime::is_termhost
tests/termhost_e2e.rs    termhost_pane_survives_live_handoff (+ try/ready helpers)
```

## Remaining 3b follow-ups (now just two)

- **Single-writer hardening:** serial Attach gives the core guarantee; still no explicit
  token/lock against two concurrent herdrs.
- **Socket path length:** `data_dir()` socket can exceed `sockaddr_un`'s ~104B in
  pathologically deep config dirs.

Then **gap #4** = flip termhost default + delete the in-process PTY path (subtraction).
Kitty graphics still back-burnered.
