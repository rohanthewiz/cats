# Session: a living next-list, and adopted exec panes stay busy

Session ID: faf51bc9-7ce7-479d-9777-a1f3ed838d14
Date: 2026-09-22
Continues: `2026-0922-1713-plugin-types-and-releases` (same session, later work)

## 1. `/next-list seed`

Created `ai_docs/todo/next-list.md`, the project's living follow-up list. From
now on sessions edit it in place, and a session doc's `## Next` records only
what changed there.

- **Window:** the last 15 session docs (`2026-0905-1911` … `2026-0922-1713`).
  Items that predate the window were dated by grepping all 173 docs, so
  N-001 is `raised 2026-0904-1753` (age 17).
- **Result:** 21 open (N-001…N-021), Roadmap empty (the user sorts items
  into it), 4 non-goals (N-022…N-025), and 17 items found already done and
  listed under Closed.
- **Lapsed items were the norm.** Only N-011, N-012, N-015 and N-016 were
  still being carried when the list was rebuilt. The rest had dropped out of
  some `## Next` without being done. The worst was the `execCmd` gap, last
  carried in `2026-0909-0011`.
- **Premises re-checked against the code:**
  - Done and closed (among others): the inputenc golden tests, the drawn
    flag icons, `daemons.log`, the non-blocking cathost emit, `-race` in CI,
    catapp's SIGKILL on quit, catway's signal handling (`signals.go`),
    `serveURL`, the catgen-dart comments, `scripts/agent-status`.
  - **N-006 corrected:** 8 days of the installed app's `daemons.log` (32
    WARNs) show that the lines fire-on-every-launch are `auth disabled
    (--auth none)` and the codex `manifest requires engine` failure, not the
    `session ended` / slow-browser lines guessed when it was raised.
  - **N-001 is a merge** of seven "see it in the real app" checks that share
    one action and one blocker (GUI automation is blocked inside Cats.app).
- The AWK used to find follow-up sections missed several headings, because
  macOS awk has no `IGNORECASE`. Those docs were read by listing their
  headings instead.

## 2. N-002: adopted exec panes read as idle

**The bug.** `paneRuntime.execCmd` ("this pane execs a command, not a shell")
was set only in `createPane`. A restarted catway adopts surviving panes in
`reconcile`, which never calls `createPane`, so the fresh runtime had
`execCmd = false`. `PaneActivity` then called an adopted editor, plugin or
`tab.create` build an idle shell (`Busy = job || execCmd`), and "clean idle
panes" could close it. cathost can't help: an exec'd program is its own session
leader, so the foreground-pgid job probe sees nothing. catway restarting itself
after a crash (`2026-0914-0158`) made adopted panes routine, which is why the
item was rated high.

**The fix** follows the durable `PluginID` pattern:

```
createPane (every spawn) ──▶ rt.execCmd = cp.Command != ""
                         └─▶ Session.SetPaneExec → PaneState.ExecCmd ──▶ snapshot `exec_cmd`
catway restart ──▶ restore snapshot ──▶ reconcile(alive) ──▶ rt.execCmd = Session.PaneExec(id)
```

- `internal/workspace`: `PaneState.ExecCmd`, persisted as `exec_cmd,omitempty`
  (an old snapshot restores as a shell, which is how it always behaved).
- `internal/app`: `Session.SetPaneExec` (reports a change, so saves only when
  something moved) and `PaneExec`.
- `cmd/catway/catway.go`: `createPane` writes it and saves. A respawn as a
  shell clears it.
- `cmd/catway/daemon.go`: `reconcile` restores it onto an adopted survivor.
  `job` needs nothing: cathost's `resyncPane` replays `pane_job` (checked).

**Tests**
- `TestReconcileAdoptionRestoresExecCmd`: an adopted pane with the durable
  flag keeps `execCmd`, and `PaneActivity` reads it as Known + Busy. Removing
  the restore line makes it fail ("adopted pane lost its exec flag"); putting
  it back makes it pass.
- `TestCreatePaneRecordsAndClearsExecCmd`: an exec spawn sets both the runtime
  and the durable flag, and a shell respawn clears both.
- The workspace snapshot round trip now covers `ExecCmd`, and also
  `PluginType`, which the plugin-types commit had left without a persistence
  test.
- `make fmt-check vet test test-ghostty vet-ghostty jstest` pass.

The installed Cats.app keeps the old catway until it is rebuilt and
reinstalled.

## Next

Seeded: `ai_docs/todo/next-list.md` (N-001…N-025; see §1).
Closed: N-002. Declined: None. Raised: None.
Deferred: None. Promoted: None.
Updated: N-006 (the routine WARN lines, from the real log).
Full list: `ai_docs/todo/next-list.md`.
