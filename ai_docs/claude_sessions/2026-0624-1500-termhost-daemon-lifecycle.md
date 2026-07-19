# herdr-web (Go) — Phase B: termhost daemon lifecycle (spawn + supervise)

**Date:** 2026-0624-1500
**Repo:** `~/projs/go/herdr-web` (Go terminal backend) · paired with `~/projs/rust/herdr` (Rust orchestrator)
**Branch (Go):** `roh/phase-b` · **Branch (Rust):** `roh/phase-b-termhost-client`

> Continues termhost cleanup. Prior follow-ups this day: selection passthrough
> (`…1408…`), scroll-lock + hyperlink resolver (`…1432…`). This session has the
> orchestrator **spawn and supervise** the Go daemon instead of requiring a
> hand-launch — the step that makes termhost usable as a default. **Kitty graphics
> was explicitly deferred** (experimental, off by default, high cost — lowest
> priority).

---

## What shipped

### Go daemon — commit `3a3ad94` (`cmd/termhost/main.go`)
- **`--exit-on-disconnect`**: in managed mode the orchestrator is the daemon's only
  client and owns its lifecycle, so the daemon serves that one connection **inline**
  and exits when it disconnects — a backstop against a crashed parent leaving it
  listening forever. Standalone/dev mode keeps the goroutine-per-connection loop so
  it can serve reconnects.
- **Graceful `SIGHUP`** (added to `SIGINT`/`SIGTERM` in `signal.NotifyContext`):
  when the orchestrator dies or the controlling terminal closes, the daemon is hung
  up; treat it as graceful shutdown so the deferred `os.Remove(socket)` runs instead
  of the default terminate (which leaks the socket file). In managed mode the served
  connection is also closed on `ctx.Done()` so a blocked `Serve` read unblocks and
  the graceful path runs even when a **signal**, not a client EOF, ends the session.

### Rust orchestrator — commit herdr `273b818` (`src/termhost/mod.rs`, `src/main.rs`)
- **Enablement precedence** in `client_if_enabled` (first match wins):
  - `HERDR_TERMHOST_SOCKET` → attach to a hand-launched daemon (dev/manual, unchanged).
  - `HERDR_TERMHOST_BIN` → **spawn that binary in managed mode**, wait for listen,
    connect, supervise.
  - neither → disabled (in-process PTY path).
- **`spawn_and_connect`**: picks a short pid-named socket under `TMPDIR`
  (`herdr-termhost-<pid>.sock`; `sun_path` is ~104 bytes on macOS), launches the
  binary with `--socket … --exit-on-disconnect`, redirects the daemon's stdio to a
  sibling `.log` (so it can't corrupt the TUI), then **retries `connect` until the
  daemon is listening** — pre-listen attempts fail with connection-refused and the
  daemon never accepts them, so the first *successful* connect is the kept client.
  On any failure it kills the child and falls back to in-process.
- **`shutdown()`** (called from `main` after the tokio runtime stops): `SIGTERM`s the
  spawned daemon and reaps it. The daemon's `--exit-on-disconnect` is the backstop if
  the orchestrator exits without calling it (panic/SIGKILL). Either way **no orphan
  lingers**.

## Verification

- **Real lifecycle e2e** (`tests/termhost_e2e.rs::termhost_managed_daemon_spawns_and_is_supervised`,
  gated on `HERDR_TERMHOST_BIN`): built the real daemon (`go build -tags ghostty -o
  /tmp/herdr-termhost-daemon ./cmd/termhost`), pointed herdr at it, asserted (a) the
  managed socket appears after pane creation (herdr spawned + connected — confirmed
  in the server log: *"spawned and connected to termhost backend"*), and (b) after
  herdr is SIGKILLed the daemon exits and removes its socket (the `SIGHUP` graceful
  path). **Passes in 1.6s; no orphan daemons.**
  - *Debug note worth keeping:* herdr runs in a PTY in the test; killing it sends the
    daemon `SIGHUP` via the terminal hangup. Before the `SIGHUP` fix the daemon died
    but skipped cleanup, leaking the socket — that's what drove adding `SIGHUP` to the
    graceful set. The clean user-quit path is `shutdown()`→`SIGTERM` (same graceful
    handler).
- Go: `-tags ghostty` build + vet + `go test ./internal/orchestration/`; gofmt clean.
- Rust: `cargo test --bin herdr` = **1892**; `--features termhost` = **1911**; clippy
  clean in changed files.

## ✅ Render e2e failure — RESOLVED (herdr `bb1de5f`)

Investigated immediately after this session. **It was a test-harness bug, not a
product regression — the seam's render path is fine.** Root cause: the server
auto-creates a default workspace (focused), so the workspace the test creates is a
*second*, unfocused one. A client renders the **focused** workspace, so the test's
echo went to its pane while the client kept rendering the default workspace's pane →
the marker never appeared. (Protocol v13 / `bincode` `SemanticFrame` decode were
fine — `workspace.list` + frame dumps showed two workspaces, `w1` "kro" focused vs
the test's `w2` "e2e".)

**Fix:** `workspace.focus` the created workspace before attaching the client/driving
input. With that, the marker renders (`sh-3.2$ echo … → marker`) and **all four
termhost e2e tests pass against the real daemon in ~6.5s**. The agent/cwd tests poll
`pane.get` (no rendering), so they were unaffected.

Debugging aids that paid off: dumping every received frame's text, then
`workspace.list` — which immediately showed the focused-vs-created mismatch. Also
noted the daemon spawns *its own* `$SHELL` (panes run the daemon's shell, not
herdr's), so a heavy interactive rc shows up in socket-mode runs — harmless given
the 15s timeout, but launch the daemon with a simple `SHELL` for fast repro.

## Key facts for future me

- **Three enablement states:** `HERDR_TERMHOST_SOCKET` (attach), `HERDR_TERMHOST_BIN`
  (spawn+supervise), neither (in-process). Socket wins if both set.
- **Managed daemon dies cleanly on parent exit** three ways: `shutdown()` SIGTERM
  (normal quit), `SIGHUP` (PTY hangup / parent death), or `--exit-on-disconnect`
  (socket EOF). The socket file is removed on all graceful paths.
- **Readiness = retry connect, not file-poll** — avoids consuming a probe connection
  (which `--exit-on-disconnect` would miscount as the client).
- Build/run env unchanged.

## Commits

```
Go   (roh/phase-b):                 3a3ad94 feat: termhost daemon managed-mode flag + graceful SIGHUP shutdown
Rust (roh/phase-b-termhost-client): 273b818 feat: spawn and supervise the Go termhost daemon
```

## Remaining termhost work

- ~~Investigate the render e2e failure~~ — ✅ done (herdr `bb1de5f`, see above).
- **Kitty graphics** — deferred (experimental/off-by-default/high-cost). Back-burner.
- Optionally: surface `HERDR_TERMHOST_BIN` as a config field; PATH discovery of the
  daemon binary so neither env is needed; restart-on-crash supervision (today a
  daemon crash mid-session isn't auto-restarted).
- Eventually: make termhost the default and retire the Rust in-process PTY/detect path.
</content>
