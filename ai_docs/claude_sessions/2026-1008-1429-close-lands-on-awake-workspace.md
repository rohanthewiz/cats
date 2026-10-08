# Closing a workspace lands on an awake one

Session: `67fe4997-c716-499c-b7bd-858755cc09c7`

## The ask

From the cats-todo backlog: "When the last workspace (usually a worktree) is
deleted the next workspace shown could be one that is currently asleep."

## Cause

`Session.dropWorkspace` (`internal/app/session.go`) is the one place every close
goes through: workspace.close, tab.close on a last tab, pane.close on a last
pane, moving a workspace's last tab out, the worktree remove
(`cmd/catway/worktrees.go`), the reaper and auto-close. It kept `active`
valid by position only:

- the closed workspace was last in the list → `active = len-1`;
- otherwise → `active` stays put, and the workspace that slid into the slot
  becomes active.

Neither checked `Asleep`. Removing a worktree at the bottom of the list with a
sleeping workspace just above it made the sleeper active. That broke the
invariant in `internal/app/sleep.go` that the active workspace is never
asleep. `viewWorkspaceIndex` and `orch.viewWS` both fall back to the active
workspace for a sleeping id, so the window had nowhere else to go and showed a
pane with no terminal behind it.

## Fix

1. **`dropWorkspace`** now corrects a sleeping landing spot with
   `nearestAwake`, the same rule `SleepWorkspace` uses (later first, then
   earlier). A sleeping workspace is no longer made active while an awake one
   exists, and it is not woken needlessly.
2. **Nothing awake left.** This happens when you close the last awake
   workspace while the rest are asleep. The model can't heal this alone: a
   wake has to resume parked agents, which needs the runtime's
   `StageResume`. (`RestoreSession` heals the same state by waking and
   *dropping* parked agents, an acceptable cost for a corrupt file but not for
   a routine close.) So `dropWorkspace` leaves the sleeper active, and:
   - **`Dispatcher.WakeActiveIfAsleep`** (`internal/app/clean.go`) wraps the
     existing `wakeIfAsleep` for the active workspace. It does the same as
     clicking its row: the placeholder becomes a shell, and each parked agent
     gets its own pane, staged to resume.
   - **`orch.applyModel`** (`cmd/catway/catway.go`) calls it right after
     `syncPrimaryActive` and before `syncDaemon`. Every close path, including
     the orch-only ones (worktree remove, reaper, auto-close), ends in
     `applyModel`, so one call covers them all. On an ordinary apply it is a
     no-op. `StageResume` does not re-enter `applyModel`, so there is no
     recursion.

## Tests

Both are in `internal/app/clean_test.go`:

- `TestCloseWorkspaceSkipsSleepingNeighbour` covers the reported case: w1
  awake, w2 asleep, w3 active and last, then `workspace.close w3`. Active
  becomes w1, and w2 stays asleep. A second part puts the closed workspace in
  the middle with the sleeper after it. With the session.go change stashed,
  the test fails with `active after close = w2`.
- `TestWakeActiveIfAsleepAfterLastAwakeCloses`: park an agent on w1, sleep
  it, then close w2 (the last one awake). The sleeper is left active;
  `WakeActiveIfAsleep` wakes it and stages one resume; a second call reports
  nothing.

`go test ./internal/...` and `go test -tags ghostty ./cmd/catway/` pass, and
`go vet -tags ghostty ./cmd/catway/` is clean. Cats.app was not run, so the
hand check is in N-001.

## Housekeeping

`ai_docs/todo/next-list.md` already had an uncommitted restructure (the new
Validate section, with N-001 moved into it). This commit carries it along with
the N-001 addition.

## Next

Closed: None. Declined: None. Raised: None. Deferred: None. Promoted: None.
Moved: None. Updated: N-001. Full list: `ai_docs/todo/next-list.md`.
