# Sleeping and cleaning workspaces

Session: https://claude.ai/code/session_01AaoUtxuk5QmaSW7YqYSxJj
Date: 2026-09-04
Repo: ~/projs/go/cats (branch `main`)

## Request

> I want to always keep the list of workspaces for quick access, but I don't
> want to have to keep a terminal open on inactive workspaces, so as to save
> on resources

…and, once a design was on the table:

> Funny I was thinking of a feature to "clean" a workspace which would mean
> killing all the idle terminals. How could I work that in here?

Decisions made along the way:

1. Neither clean nor sleep saves the layout.
2. An idle agent is not cleanable by default — its context is worth more than
   the terminal. Options: leave it (default), run a command in it first
   (`/exit`, `/compact`), or park its session id so it can be resumed later.

## What existed

A workspace was either fully alive or gone. `desiredGrids` sized a PTY for
every pane of every workspace ("all are live PTYs on the daemon"), restore
respawned every one, and `workspace.close` deleted the entry with its name,
flag, lock and todos. There was no middle state.

The daemon reported detected agents (`pane_agent`) but nothing about a plain
shell's foreground job, so the runtime could not tell a shell at its prompt
from one running `make`.

## Design

One idle test drives both features; sleep is clean's empty case.

```
pane activity            clean / sleep verdict
───────────────────────  ─────────────────────────────────────────────
runtime unknown          keep   (host down, not yet spawned: no say)
exited                   close
agent, not idle          keep
agent, idle              leave: keep · park: close + park · command: send
foreground job / exec'd  keep   (a build, an editor, a plugin)
shell at its prompt      close
```

- `workspace.clean` acts on the verdict and stops. If everything goes and
  another workspace is awake, it sleeps the workspace whole instead of
  emptying it pane by pane. On the last awake workspace it keeps one shell.
- `workspace.sleep` requires the verdict to leave nothing behind, refuses
  (closing nothing) naming the busy panes otherwise, then sleeps.
- `workspace.wake` (and `workspace.focus`, i.e. the sidebar click) brings it
  back: the placeholder becomes a shell, each parked agent gets a pane split
  off it, staged to resume through the same `resumePlans` path a cold restart
  uses.

**The model keeps its invariants.** A sleeping workspace is not a workspace
with zero tabs — that would special-case every `ActiveTab()` caller. It holds
one fresh placeholder tab/pane (numbered past everything it has had; numbers
are never reused) and an `Asleep` flag. The backend has one rule: skip
sleeping workspaces in `desiredGrids`. That alone makes `syncDaemon` close the
old PTYs on sleep and create the placeholder's on wake.

**The active workspace is never asleep.** `SleepWorkspace` moves the active
index to the nearest awake workspace and refuses to sleep the last one.
`viewWorkspaceIndex` (app) and `viewWS` (catway) treat a sleeping id like a
closed one, so a window left pointing at it falls back to the active
workspace. `RestoreSession` heals a file whose active workspace is asleep.

**Nothing wakes as a side effect.** `tab.create`, a split, and
`tab.move_to_workspace` into a sleeping workspace are refused with a message
naming `workspace.wake`; the browser's "start in all workspaces" fan-out skips
sleeping ones as it skips locked ones.

## Files

- `internal/workspace/sleep.go` (+test) — `Asleep`, `ParkedAgents`,
  `Sleep`, `Wake`, `ParkAgent`, `PlaceholderPane`; snapshot fields
  `asleep` / `parked_agents` (omitempty, old files byte-identical).
- `internal/app/sleep.go`, `clean.go` (+`clean_test.go`) — session
  `SleepWorkspace` / `WakeWorkspace` / `ParkAgentIn`; the verdict and the
  three commands; `wakeIfAsleep` shared by wake and focus. Backend seam gains
  `PaneActivity(pane)` and `StageResume(pane, ref)`.
- `wire/vocab.go`, `wire/down.go` — `workspace.clean` / `.sleep` / `.wake`,
  `CleanWorkspaceParams{id, agents, command}`, `CleanWorkspaceResult`,
  `ParkedAgentInfo`; `asleep` + `parked` on `WorkspaceInfo` and
  `WorkspaceEntry`. (cats-todo carries a copy of the wire vocab — keep it in
  lockstep.)
- `internal/orchestration` — new `pane_job` event from the detect pump
  (`foregroundPgid != childPid`), replayed on resync.
- `cmd/catway/clean.go` — `PaneActivity` (unknown unless created and host
  connected; exited; agent + resumable ref; else `job || execCmd`) and
  `StageResume` (via `resumeArgv`, local panes only). `desiredGrids` skips
  sleeping workspaces; `createPane` records `execCmd`; `viewWS` falls through
  a sleeping workspace.
- `cmd/catctl` — `clean-ws [id] [park | run <text...>]`, `sleep-ws` (same
  shape), `wake-ws <id>`.
- Browser — a third "asleep" shelf with a crescent mark carrying the parked
  count; click wakes; hover card lists parked agents; context menu and palette
  offer clean/sleep (plain or park) and wake; PANES hides the placeholder.
- Docs — control-api.md "Clean, sleep and wake"; cli.md verbs.

## Not done / next

- No auto-sleep timer (`workspaces.sleep_after`). The pieces are all there:
  it is `clean` run over workspaces no window is showing.
- A sleep under `agents:"command"` stays awake and reports; nothing watches
  for the agents to exit and sleeps afterwards.
- `internal/inputenc` has two pre-existing failures (missing catgen-dart
  golden after its removal), unrelated to this change.
