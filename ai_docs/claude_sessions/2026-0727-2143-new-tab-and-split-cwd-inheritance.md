# Session: New Tabs and Splits Inherit the Neighbor's CWD

- **Session ID:** `492ff652-7265-4c55-8e5a-6603aebd2a63`
- **Date:** 2026-07-27
- **Branch:** main

## Request

> In a workspace when opening a new tab adjacent to another tab, the new tab
> should inherit the CWD of the left previous tab.

Then, after the tab work landed: *"yes, do splits too"* — a split should open in
the working directory of the pane it splits off.

## Diagnosis

Two separate problems, one stacked on the other.

### 1. Nothing resolved a neighbor's directory

`Session.CreateTab` (`internal/app/session.go`) spawned every tab in the
workspace's `IdentityCwd`, falling back to the session cwd. The domain layer is
deliberately pure — it cannot know a pane's live cwd — but the **dispatcher**
can: `Backend.PaneMeta(pane).Cwd` already exposes exactly that for
`pane.list`/`pane.get`, and `Backend.StageSpawn` is the one hook that influences
a pane's real PTY spawn (`orch.createPane` consumes `spawnPlans` before the
`create_pane` send). So the fix belongs in the dispatcher, between the session
mutation and `ApplyModel`.

Positional rule: `CreateTab` always appends, so the tab immediately left of a new
one is the workspace's **last** tab — not the active tab. `pane.split` needs no
such rule; a split has exactly one source.

### 2. `PaneMeta.Cwd` was frozen at the spawn directory

The first end-to-end probe failed: after `cd /usr/local` in tab 1, the new tab
still opened in the repo. `catctl pane 1` proved why — the reported cwd never
moved.

A pane's cwd only ever came from **OSC 7**, and most default shell setups never
emit it (macOS `/etc/zshrc` only wires `update_terminal_cwd` for
`TERM_PROGRAM=Apple_Terminal`). cathost seeds `p.lastPwd` with the spawn dir at
create and announces it once (`host.go:549`), and then nothing updates it ever
again. The live instance showed the same thing — every pane reporting `cwd: "/"`.

So the inheritance was correct but inherited a stale constant. Without fixing the
cwd source it would have been a no-op on this machine.

## What was done

### `internal/detect/procscan_{darwin,linux,stub}.go` — read a process's cwd

New `ProcessCwd(pid int) string`, alongside the existing per-platform foreground
scan (same cgo/build-tag matrix, so no new package):

- **darwin:** `proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, …)` → `pvi_cdir.vip_path`,
  via a small `proc_cwd` C helper.
- **linux:** `os.Readlink("/proc/<pid>/cwd")`, rejecting the ` (deleted)` suffix
  so a removed directory is never inherited into a spawn.
- **stub:** `""` — those platforms keep the OSC 7 path only.

### `internal/orchestration/host.go` — probe the shell's cwd

- `pane.oscCwd atomic.Bool` — set by `readPump` on any OSC 7 hit. A shell that
  reports its own cwd is authoritative from then on, since it can name a
  directory the local probe cannot see at all (an ssh session's remote path), and
  the probe permanently stands down for that pane.
- `pane.childPid()` — the pane's own process, i.e. the shell whose `cd` moves the
  directory.
- `detectPump` now probes `detect.ProcessCwd(p.childPid())` each tick when
  `!oscCwd`, gated on `detectSeq` changing since the last probe (a `cd` always
  draws a fresh prompt, so an idle pane costs nothing). `setCwdMeta` already
  dedupes, so `pane_cwd` still only fires on a real change.

Three sources now, in ascending authority: spawn dir → process probe → OSC 7.

### `internal/app/session.go`

- `NewTabNeighborPane()` — root pane of the active workspace's last tab, i.e. the
  pane a tab created *now* would land beside.
- `ResolvePaneTarget()` — exported wrapper over the existing private resolver, so
  the dispatcher resolves a split's source with the identical "nil means focused"
  rule the mutation will use.

### `internal/app/commands.go`

- `inheritedTabCwd()` / `inheritedSplitCwd(target)` — ask the session which pane
  is the source, ask the Backend for its live cwd.
- `CmdTabCreate`: resolves the neighbor **before** `CreateTab` (while it is still
  the last tab), then folds the result into the spawn override. An explicit
  `cwd` param always wins; `""` leaves the old fallback chain intact.
- `CmdPaneSplit`: resolves the source's cwd before the split, stages it on the
  new pane. Staging precedes `ApplyModel` in both cases — that call is what
  creates the PTY.

### Docs

- `command_vocab.go` — `TabCreateParams` / `SplitParams` doc comments.
- `ai_docs/phase-c-ws9-protocol.md` — the `pane_cwd` three-source note and the
  `tab.create` / `pane.split` inheritance rules.

### Tests

- `internal/app/commands_test.go` — `TestDispatchTabCreateInheritsCwd` (inherits;
  neighbor is positional, not focused; explicit cwd wins and keeps the rest of
  the override) and `TestDispatchPaneSplitInheritsCwd` (inherits from the split
  pane; stages nothing when the backend knows no cwd). Adjusted the existing
  "bare tab.create stages nothing" assertion, which now holds only because that
  harness has no pane metadata.
- `internal/orchestration/host_test.go` — `TestHostReportsPaneCwdWithoutOSC7`:
  a `/bin/sh` that `cd`s and emits no OSC 7 at all still reports the move.
  (`filepath.EvalSymlinks` on the temp dir — the kernel path resolves
  `/var` → `/private/var` on macOS.)

## Verification

- `make check` — exit 0, 37 packages, including the ghostty-tagged race tests.
- End-to-end against real `cathost` + `catway` on a scratch port/socket, driven
  by `catctl probe` (the same WebSocket commands the browser sends):
  - `cd /usr/local` → `catctl pane 1` reports `/usr/local` (was frozen before).
  - `cd /usr/local`, new tab → opens in `/usr/local`.
  - `cd /etc` in tab 2, new tab → `/etc`; then focus tab 1 and open another →
    still `/etc`, proving the neighbor is positional rather than focused.
  - `cd /usr/local`, split → new pane in `/usr/local`.
  - Focused pane in `/etc` but splitting a pane still in `/usr/local` → the new
    pane is `/usr/local`, proving it follows the *addressed* pane (the browser's
    pane context menu sends an explicit id; the palette omits it).
- Test daemons used `/tmp/cats-probe*.sock` and their own `--control-socket` so
  nothing touched the user's live instance; all reaped afterwards.

## Left to the user

`/Applications/Cats.app` still has the pre-change binaries — `make macapp` (or
`make local`) plus a restart to pick this up.

## Notes / possible follow-ups

- Fixing pane cwd tracking silently improves three existing consumers that read
  it: the browser cwd tooltip, worktree anchoring (`worktree.list` resolves the
  repo from the focused pane — previously anchored to the spawn dir, so cd'ing
  into another repo gave the wrong one), and session restore, which now
  re-spawns panes where you left them.
- `Workspace.DisplayNameFrom` / `ResolvedIdentityCwdFrom` (the live-cwd identity
  seam) are still unwired — everything calls the `IdentityCwd`-based
  `DisplayName()`. So workspace labels do **not** drift as panes `cd`. Worth
  knowing before wiring that seam: it would now actually track.
- A spawn cwd that no longer exists (neighbor cd'd into a since-deleted dir)
  would fail the PTY start. Pre-existing exposure — `restoredCwds` has the same
  shape on cold start — and the linux probe already filters ` (deleted)`. A
  generic "retry without Dir" fallback in `Host.createPane` would close it for
  every path at once.
