# Workspace silently dropped → auto-close and reaper now sleep it

Session: `61290c6f-c6ac-4800-b91c-4304e1ec17f2`

## Report

A recent workspace (`~/cbre_projs/gws/chronos_converter`) was in neither the
Open nor the Asleep list, and the user did not remember closing it or putting
it to sleep. "I seem to silently drop a workspace occasionally."

## Diagnosis

- `~/.local/state/cats/session.json` had no workspace for that path, and
  `history.json` had no pane scrollback from it. The model had dropped the
  workspace for real, not just hidden it in the UI.
- Ruled out:
  - **Restore.** `app.RestoreSession` is all-or-nothing: one bad workspace
    makes the whole session start fresh, so it cannot drop a single one.
  - **Worktree remove.** The checkout and its `git worktree` were intact.
  - **Sleep.** It closes panes through the model and never produces a
    `pane_exited`.
- **Cause.** The runtime's two housekeeping closes, both in
  `cmd/catway/reap.go`, closed through `Session.ClosePaneIn`:
  - the clean-exit auto-close (`panes.autoclose_exited`, 10s after exit 0);
  - the exited-pane reaper (`panes.reap_exited`, 4h after any exit).

  `ClosePaneIn` drops a workspace when its last pane closes. The only guard
  either path had was against closing the **session's** last pane. So `exit`
  in a single-pane workspace dropped that workspace 10s later, often while it
  was not on screen.
- **Why nothing was logged.** A successful auto-close logs nothing. The reaper
  logs through `log.Printf`, and `daemons.log` keeps warnings and up only.
- **Exposure.** 10 of the 17 workspaces in the live session have one pane.
- What exactly ended chronos_converter's shell (`exit`, an extra Ctrl-D, or a
  crash 4h earlier) is unknown, since there is no trace left.

## Fix (cats `7e4647f`)

- `internal/app/sleep.go` adds `Session.RetirePane(id) (wsID, slept, err)`,
  the close used by housekeeping:
  - **Pane has siblings:** `ClosePaneIn`, unchanged.
  - **Workspace's last pane, another workspace awake:** `SleepWorkspace`, the
    same thing a clean does with a workspace it empties.
  - **Last pane of the only awake workspace:** refused with
    `ErrLastAwakeWorkspacePane`, and the dead pane is kept.
- `Session.CanRetirePane(id)` gives the same verdict without acting.
  `armAutoclose` now uses it in place of `PaneCount() <= 1`, so no countdown
  starts where the close would be refused.
- In `reapExitedPanes` and `fireAutoclose`, `ClosePaneIn` is replaced by
  `RetirePane`. When a workspace is put to sleep, they log it with
  `dlog.Warnf`, so the line lands in `daemons.log`.
- Manual closes are unchanged: `pane.close`, `tab.close`, `workspace.close`
  and worktree remove still drop an emptied workspace.
- Docs updated: `docs/subsystems/session-model.md` (the reaper / auto-close
  section) and the `pane_exited` row in `docs/protocols/browser-protocol.md`.

## Tests

- `internal/app/retire_test.go` (6 tests):
  - a last pane sleeps its workspace, and the name is kept;
  - sleeping the active workspace moves the active index;
  - a pane with a sibling just closes;
  - the only awake workspace's last pane is refused;
  - the session's last pane is refused;
  - an unknown pane is refused.
- `cmd/catway/reap_test.go` (+2) and `autoclose_test.go` (+2) cover the reaper
  and the auto-close, reusing `twoWorkspaceOrch` from `view_test.go`. All four
  fail against the old `reap.go` and pass with the fix.
- `make race-ghostty` and `go test ./...` both pass.

## Notes

- The installed Mac app has the old behavior until it is rebuilt and
  reinstalled.
- chronos_converter has to be reopened by hand. Its old pane history is gone.

## Next

Closed: None. Declined: None. Raised: N-064, N-065. Deferred: None.
Promoted: None. Moved: None. Updated: None.
Full list: `ai_docs/todo/next-list.md`.
