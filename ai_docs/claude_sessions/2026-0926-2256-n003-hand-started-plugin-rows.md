# Session: N-003, a PLUGINS row for plugins started by hand

Session ID: 2f295ebc-4e96-4404-9110-6f30e39b453e
Date: 2026-09-26
Driven from: cats

The user pasted next-list item N-003:

> A plugin started by hand from a shell (e.g. `cats-todo` typed at a prompt)
> has no `CATS_PLUGIN_ID`, so it gets no row in PLUGINS. Editors are the
> exception since `2026-0922-1713`: `editor.agents` types them `editor` however
> they were started.

## 1. Why it had no row

A plugin pane is known by its launch. `createPane` (`cmd/catway/catway.go`)
reads `CATS_PLUGIN_ID` / `CATS_PLUGIN_TYPE` out of the spawn env and records
them on the durable `PaneState`. `agentsMsg` builds the PLUGINS rows from that
record (via `panePlugin`). A plugin typed at a prompt never goes through a
launch, so nothing is recorded. Only editors got around this, through their
hook-API agent label.

## 2. Design

The daemon already runs a cheap `tcgetpgrp` every tick, plus a throttled full
probe of the foreground group for agent detection. So the signal is already
there: which program the pane's foreground job is running.

- **cathost reports a path and knows nothing about plugins.** `pane_job` gains
  `Exe`, the executable of the job's process-group leader, read with the new
  `detect.ProcessExe(pid)`. On darwin that is `proc_pidpath`; on linux it is
  `readlink /proc/<pid>/exe` with any ` (deleted)` suffix dropped; on other
  platforms it is a stub that returns "". The kernel path has symlinks
  resolved already.
- **catway matches by location, not by name.** Everything a typed plugin name
  resolves through ends inside the plugin dir:
  `~/.cats/bin/<name>` → `<plugins-root>/<id>/bin/<name>` → (for a dev link)
  `<checkout>/bin/<name>`. `pluginForExe` returns the installed plugin whose
  `EvalSymlinks`'d `Dir` is a path prefix of the exe. That keeps a same-named
  binary built elsewhere, or a reused bin name, from being claimed.
- **Runtime-only.** The match lives on `paneRuntime.handPlugin` /
  `handPluginType` and is never written to `PaneState.PluginID`. It describes
  the job running right now and clears as soon as `pane_job` says the job
  ended. After a catway restart, the daemon's resync replay of `pane_job`
  (which now carries `Exe`) restores it.
- **A launch record wins.** `paneRuntime.plugin(recorded, declared)` falls
  back to the hand match only when nothing was recorded. `agentsMsg` and
  `PaneMeta` (`pane.list`) both go through it, so they cannot disagree.
- **Disk work stays off the loop.** `applyPaneJob` drops a stale answer
  synchronously, then runs `plugin.List()` + matching on a goroutine and posts
  the result back. The posted result is discarded if the pane closed or its
  `jobExe` changed in the meantime. `pane_job` arrives for every command
  typed in any pane, so the loop must never wait on this.

### The fork/exec race and when the exe is read

The exe is read inside the throttled probe block, not on every tick. A group
change always forces a probe. A group with no agent in it opens the
acquisition window, whose fast re-probes repeat the read over the next few
seconds. That repetition fixes a read that landed between the shell's fork
and the child's exec (the leader briefly still runs the shell's image).
`setJobMeta(busy, exe)` treats an exe change as news even when `busy` did not
move. The pump now reports job end on the tick it happens (before the probe)
and a running job after the probe, so the first report of a new job already
names its program.

## 3. Changes

- `internal/detect/procscan_{darwin,linux,stub}.go`: `ProcessExe`.
- `internal/orchestration/protocol.go`: `PaneJob.Exe` (`exe,omitempty`, so a
  no-job report is byte-identical to the old shape) and
  `NewPaneJob(id, busy, exe)`.
- `internal/orchestration/host.go`: `pane.lastJobExe`, `setJobMeta(busy,
  exe)`, the resync replay carries it, and the `detectPump` reorder described
  above.
- `cmd/catway/handplugin.go` (new): `applyPaneJob`, `setHandPlugin`,
  `paneRuntime.plugin`, `lookupPluginForExe`, `pluginForExe`, and a header
  comment with the resolution diagram.
- `cmd/catway/catway.go`: the runtime fields `jobExe` / `handPlugin` /
  `handPluginType`; `agentsMsg` and `PaneMeta` go through `rt.plugin(…)`.
- `cmd/catway/daemon.go`: the `pane_job` case calls `o.applyPaneJob(ev)`.
- `wire/vocab.go` (`PaneMeta.Plugin`) and `wire/down.go`
  (`PluginPane.Plugin`) doc comments now describe the hand-started case.

## 4. Verification

- New tests:
  - `TestPluginForExeMatchesByLocation`: installed, dev-linked, name-prefix
    sibling, elsewhere, empty.
  - `TestAgentsRollupListsHandStartedPlugins`: a non-plugin job gives no row;
    a plugin job gives a row and the same `PaneMeta` pair, with nothing
    durable written; the launch record outranks the match; job end removes
    the row synchronously; a stale lookup is dropped.
  - `TestProcessExeResolvesALiveProcess` (darwin): the test binary's own path.
  - `protocol_test.go` round-trips the new field.
- `make check` is green, including `go vet -tags ghostty` and
  `go test -tags ghostty -race ./...`.
- Not run: a live check in a rebuilt Cats. cathost has to be rebuilt and
  restarted as well as catway, because an old daemon sends no `Exe` (so no
  row, same as before).

## 5. Known gap

A script-based plugin runs under its interpreter, so the leader's exe is
`/bin/sh` or `python` and nothing matches. Raised as N-047. Every plugin
shipped today is a Go binary.

## Next

Closed: N-003. Declined: None. Raised: N-047.
Deferred: None. Promoted: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
