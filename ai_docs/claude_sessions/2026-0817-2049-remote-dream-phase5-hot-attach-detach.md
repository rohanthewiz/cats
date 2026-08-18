# Session: remote dream phase 5 — a roster you can edit while it runs

- Session id: `6416829a-814c-4c75-8647-001ce5a0dfcb`
- Date: 2026-08-17
- Branch: `feat/working-with-remotes` (cats)
- Plan/record: `ai_docs/plans/remote-dream.md` — Phase 5 marked **DONE** with its
  "as built" section
- Predecessor: the phase 1–4 commits `a212d81`, `8a9431e`, `ae5d4c5`, `5649d7c`

## What this session was

Phase 5: `hosts:` stops being a restart-only setting. `host.attach` /
`host.detach` over §7, `server.reload_config` diffing the roster, buttons in the
aside, and `catctl attach-host` / `detach-host`. Two new files and 17 modified;
`make test` and `make test-ghostty` green, `gofmt` and `go vet` clean.

The plan's one-line sketch was "`orch.attachHost/detachHost` … `ReloadConfig`
diffs the roster". Almost everything interesting came from refusing to write it
that way.

## One path, not three

There is no `attachHost`/`detachHost` pair. Every roster change — attach, detach,
and a config reload — goes through one function:

```go
func (o *orch) applyHostRoster(configured []config.Host) error
```

It takes the config file's `hosts:` half, re-derives the effective roster
(`config.EffectiveHosts(o.cathostSocket, configured)` — the same synthesis
startup does), and diffs that against what is running.

Why it matters: the commands' *other* half is a config write. If attach patched
the live map and separately appended to `cfg.Hosts`, the two could drift, and
"the roster" would have two definitions. Deriving the live roster from the config
every time means the file is the only source of truth and a reload is not a
special case — it is the ordinary case with a different caller.

I did briefly add an `orch.effHosts` field to hold "the roster the daemons were
built from" and then deleted it for exactly this reason. What *did* have to be
remembered is one string: `o.cathostSocket`, the local host's socket **as
resolved at startup, flags included** — a `--socket` flag may have overridden the
file, and the config alone cannot reconstruct it.

## The order is the design

```
build  →  retire  →  install  →  re-home  →  start  →  announce
```

Each step is where it is because the alternative breaks something:

- **build first.** Every new or changed daemon is constructed before the first
  one is retired, so an address catway cannot dial fails the *whole* edit rather
  than half-applying it. `TestApplyHostRosterIsAllOrNothing` pins it.
- **retire before install.** A departing host's panes are found with
  `o.paneHostID(pid) == id`, which only answers correctly while that host is
  still in the map. Flushing its pending reads/captures/waits has to happen in
  the same window, so callers get an error instead of a timeout.
- **re-home after install.** The panes land on the *new* default host, which the
  install step is what decides — a detach of the default host has to move things
  somewhere that exists.

## Diff on the dial target, not the entry

```go
func sameDialTarget(a, b config.Host) bool {
	return a.ID == b.ID && a.Addr == b.Addr && a.Token == b.Token &&
		a.TokenFile == b.TokenFile && a.Fingerprint == b.Fingerprint
}
```

Deliberately not `a == b`. `Label` and `Default` are presentation and policy: a
rename must not drop a live connection and every PTY's stream with it just to
change a string in the sidebar. Verified live — B's log shows one `client
connected` across a rename-and-reload.

A changed `addr` *does* rebuild, and keeps that host's panes: same id, same
machine as far as catway can tell, new route. Its PTYs are deliberately **not**
closed either — if the new address reaches the same box (an ssh forward moved, a
socket path changed), the new connection's reconcile adopts the survivors, and
closing them would have killed the terminals it was about to re-adopt.

## Stopping a daemon is not losing one

`daemon` grew `spec`, `quit` and `stopped`. `stop()` closes the live conn (which
unblocks the pump's read) and the dial loop checks `stopping()` at both of its
waiting points — the backoff became a `select` on `time.After`/`quit`, because a
host that is *down* and being detached must not keep a goroutine and a roster row
alive for another five seconds.

The load-bearing detail is the early return after the session ends:

```go
if d.stopping() {
	return
}
```

Everything below it announces an outage — `flushPendingFor`, `flushWaitersFor`,
the "cathost connection lost — reconnecting" toast. A detach is not an outage.
The panes have already been dealt with, and toasting the user about a machine
they just removed is how a deliberate action reads as a failure.

## Two bugs the tests did not find first

**A re-homed pane inherited the departed machine's directory.** `paneCwd` used
`o.workspaceHostID(ws) == host` to decide whether to hand a pane its workspace's
identity cwd. But `workspaceHostID` resolves an *unknown* host to the default —
so a workspace pinned to `devbox`, after `devbox` is detached, matched every
local pane and handed it `/srv/app` from a filesystem this catway can no longer
reach. Now `workspaceHostOwns`, which answers false for a host that has left the
roster: a departed host owns nothing.

**A second departing host was told to close the first one's panes.** The orphan
list accumulates across the whole edit, and the first draft passed the running
total to each host's `closePanesOn`. Pane ids are globally unique, so the second
host would have been asked to close ids it does not have — and the first host's
close would never have been sent. Fixed by scoping to `mine := o.panesOnHost(id)`
and appending after; `TestApplyHostRosterDropsTwoHostsIndependently` is the
regression.

## What force actually costs, and saying so

A terminal cannot move between machines: the process is on the other box, and
detaching is precisely the loss of the channel that could talk to it. So:

- a host holding panes is refused unless `force`, and the refusal names the count
  and the destination — `host devbox still holds 1 pane — detach with force to
  move it to <hostname> (the terminals there are abandoned and respawn as new
  shells)`
- with `force`, each pane keeps its id, its place in the layout, its name and its
  public handle, and gets a **fresh shell** on the default host
- the departing cathost is asked to close its PTYs first, best effort — it is
  usually unreachable, which is usually *why* it is being detached, and a
  disconnected send simply drops
- nothing is seeded into the new pane. The scrollback catway holds for it is
  another machine's, and replaying it here would produce a convincing history of
  things that never happened on this box.

Two smaller decisions in the same spirit:

- `Session.SetPaneHost` is new, and it stores **`""`, not the default host's id**.
  A pane that was *displaced* should track the default rather than being pinned to
  whatever it happened to be at that moment. It has to be stored at all because a
  session file still naming a detached host would put the pane back on a ghost at
  the next restore.
- `Workspace.HostID` is left alone. A pane's host is where it *is*; a workspace's
  is a policy for new panes. A host that comes back should get its workspace again.

## Surfaces

Dispatcher-side the checks are shape-only (`id`/`addr` present; `checkHost` for
detach) — the roster and the file are the backend's, and a second `HostExists`
seam was the thing Phase 3 already refused to add.

catctl got `attach-host <id> <addr> [label...]` and `detach-host <id> [force]`.
`force` is a **word, not a flag**: it is the argument that throws work away, so a
script that types it has said so and a fat-fingered `-f` cannot mean it by
accident. Tokens and fingerprints go through `catctl host.attach --params …`,
which is the shape a script would rather write anyway.

Completion needed its own kind. `argHost` became **`argDetachHost`**, offering
the roster *minus* `local`, because detaching local is always refused and a
completion that offers a value the command cannot take is worse than one that
offers nothing. A future `--host` slot that wants the whole roster should add its
own kind rather than widen this one.

The browser got `＋` on the HOSTS heading and a right-click detach per row (with a
confirm dialog when it holds panes) — plus **attach host… in the gear menu**. That
last one is not decoration: the HOSTS section is hidden while there is one host,
which is the whole point of the single-host rule, and without a second entry
point the *first* attach would still have been a config edit and a restart.

## Verified live — two cathosts, one catway, no restarts

- `catctl attach-host devbox unix:///tmp/…` → the row appears, connects, and the
  `hosts:` block is written to the config file (with no synthesized `local` entry
  in it)
- a workspace created on it spawns there — an `echo` into a file proves which box
- plain detach refused, naming the pane count and the destination
- forced detach → pane re-homed, and `catctl probe` shows it streaming a live
  shell **with its branch badge** on the other host
- `catctl reload` after a hand-edit: attaches, renames without redialing, detaches
- every refusal (cleartext off the loopback, duplicate id, `local`, unknown id)
  leaves the roster and the file untouched
- `catctl __complete` offers `devbox` and not `local`

## Odds and ends

- macOS unix-socket path limit again: the scratchpad directory is too long for a
  `.sock`, so the live run used `/tmp/p5-*.sock`.
- The first version of the force-detach test expected two `create_pane` messages
  on the local pipe. `desiredGrids` only covers the visible viewport, so the
  non-visible original pane was never created — the test was wrong, not the code.
- catgen-dart goldens regenerated (two new commands). **cats-mobile has not been
  regenerated** — that needs cats pushed first.

## Left standing

Unchanged from Phase 3, and now the last known gap in the slice: an unqualified
`pane.split` still takes the *workspace's* default host rather than the split
pane's, so splitting a guest pane yields one on the workspace's machine. Phase 6
is the follow-ups list (per-host meters, remote `path.list`, hook relay, latency
in `HostItem`).
