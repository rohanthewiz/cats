# live_handoff harness fix — macOS process reaper (leaked replacement servers)

**Session id:** `64f0fde1-3fdd-4da7-80cd-0136bd679577`
**Date:** 2026-0712-2147 · **Branch:** `roh/phase-b` (herdr-web, session docs only)
**Change repo:** `~/projs/rust/herdr` @ branch `roh/phase-b-termhost-client` — **test harness
only** (`tests/support/mod.rs`), no production Rust touched.
**Continues:** `2026-0711-0237-ws9-stage-4.3-4.4-acceptance.md` — cleared the last flagged
WS9 leftover: the live_handoff harness leak.

> Fixed the long-standing "leaked attached `herdr server` clients defeat the daemon idle
> reaper" flag. Root cause was **macOS-specific**: the harness's whole server-reaping
> machinery is gated on `/proc`, which macOS lacks, so it was a silent no-op. Added
> `ps`+`lsof` process discovery for macOS. All integration suites green; verified no
> leaked processes/dirs after runs.

---

## Root cause

`tests/support/mod.rs` reaps stray `herdr server` processes via `/proc` scanning
(`iter_worktree_server_pids`, `process_runtime_dir` reading `/proc/<pid>/environ`, the
watchdog, `terminate_servers_for_runtime_dirs`). **macOS has no `/proc`**, so
`fs::read_dir("/proc")` → NotFound → `Ok(vec![])` → the entire sweep is a **silent
no-op**. On macOS only two things cleaned up:
1. `SpawnedHerdr::Drop` — kills the *original* tracked server child.
2. `cleanup_registered_herdr_pids` — kills only *registered* PIDs (original servers).

`server.live_handoff` spawns a **replacement** `herdr server` (`src/server/handoff.rs`
`spawn_handoff_import`: re-execs `current_exe()` with argv `server --handoff-import …`,
`process_group(0)`). Its PID is never registered → on macOS **nothing ever kills it**. It
stays attached to the Go termhost daemon; the daemon's 10-min idle reaper only arms when
**no** client is attached, so a live leaked client defeats it → daemons persist for days.
Confirmed by stale `/tmp/hlh-*` debris dated Jul 4/7/11.

**The Go daemon reaper is correct** (`cmd/termhost` + `internal/orchestration/host.go`):
`Attach`'s read loop breaks on a *dead* client (EOF/reset) and calls `armIdle()`. Only a
genuinely **live** leaked client defeats it — so this is a harness fix, not a daemon fix.

macOS `ps -E` does **not** expose another process's env (SIP), so the Linux env-based
`process_runtime_dir` can't be ported — used `lsof` instead.

## The fix (`tests/support/mod.rs`, +147/-8, all new code `#[cfg(target_os = "macos")]`)

New macOS discovery helpers:
- `ps_pid_command_pairs()` — `ps -ax -o pid=,command=`.
- `command_is_test_backend` = `command_is_test_herdr_server` (argv[0] matches
  `is_test_herdr_binary` + a `server` arg — catches the re-exec'd replacement) **or**
  `command_is_test_termhost_daemon` (exe basename `termhost`/`herdr-termhost` + `--persistent`).
- `pid_has_open_path_under(pid, prefixes)` — `lsof -w -p <pid> -Fn`, matches `n`-lines under
  the test base. `base_match_prefixes` canonicalizes for the `/tmp`→`/private/tmp` symlink
  (lsof reports the canonical path) with a `/`-boundary check to avoid prefix false-matches.
- `terminate_test_processes_under_base(base)` ties them together → `terminate_pid`.

Wired into all three cleanup entry points (macOS branches only; Linux unchanged):
- `cleanup_test_base` — after the (no-op on mac) runtime-dir sweep, scope by whole base so
  the replacement server **and** the daemon are reaped **before** `remove_dir_all`.
- `cleanup_servers_with_missing_runtime_dir` (watchdog + init) — **inverted** on macOS: for
  each registered runtime dir whose owning test died (`should_terminate_runtime_dir`), reap
  under its base. A live test's dir is skipped (owner alive) → never kills in-flight tests.
  Linux path unchanged under `#[cfg(not(target_os = "macos"))]`.
- `cleanup_registered_herdr_pids` (panic/ctrlc/atexit hook) — unconditionally reap under each
  drained base (the whole test binary is going down; registry already drained so the
  follow-up missing-dir pass sees it empty).

Scoping is by the per-test **base** dir (not just runtime dir) so it also covers named-session
servers (api sockets live under `config/herdr-dev/…`, still under base). Base-dir `lsof`
ownership is what proves the daemon is ours, so daemon matching by basename+`--persistent` is
safe (the user's Homebrew herdr is a different path, never under a `/tmp` test base).

## Verification (macOS)

- `cargo test --tests --no-run` → all 11 integration binaries compile.
- `live_handoff` **16/16 pass** (incl. 2 named-session/multi-session tests); support unit
  tests `watchdog_scoping_*` / `test_binary_matcher_*` pass.
- `detach_reattach` **11/11 pass** (shares the harness; no regression).
- **After both runs: zero leaked `herdr`/`termhost` processes; zero new leftover dirs** —
  every test fully cleaned its base. Only leftovers were pre-fix debris (Jul 4/7/11).
- Go daemon built as the harness sibling: `PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/
  libghostty-vt/zig-out/share/pkgconfig go build -tags ghostty -o
  ~/projs/rust/herdr/target/debug/herdr-termhost ./cmd/termhost`.

## Notes / leftovers

- Change is uncommitted in the **Rust** repo (`~/projs/rust/herdr`) — offered to commit; this
  session doc + commit lives in **herdr-web**. The two repos are committed separately.
- Verified only on macOS (this host); Linux path is untouched but not re-run here.
- Stale pre-fix `/tmp/hlh-*` debris still present — a `/tmp` wildcard-delete was blocked by a
  safety guard; clear with `! rm -rf /tmp/hlh-*` if desired.
- Panic-path (watchdog/atexit) exercised the passing-path cleanup, not an actual panic;
  logic parallels the verified path and `watchdog_scoping_*` unit tests cover the decision.
- **WS9 fully done** (Stage 4 + this leftover). Next per the phase plan out-of-scope list:
  WS10 auth/TLS, WS8 styled chrome, WS3 tagged-union Node, WS11 wire/herdrconn removal.
