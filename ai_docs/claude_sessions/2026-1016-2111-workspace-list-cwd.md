# 2026-10-16 21:11 — workspace.list carries the workspace cwd

Session: `30a8f166-bc0c-4946-aa24-bb6e7a51edcf`

## Request

Driven from a ced session (`~/projs/go/ced/ai_docs/claude_sessions/2026-1016-2111-recent-folders-local-and-workspace-routing.md`):
ced's Recent folders picker now routes a picked project to that project's
cats workspace (a running ced there, else a new ced tab there, else the
current workspace). `workspace.list` gave only a name, so ced had to infer
the workspace from pane cwds. The user asked for cats to report the
workspace cwd too.

## What was built

- `wire/vocab.go`: `WorkspaceEntry.Cwd` (`json:"cwd,omitempty"`), the
  workspace's identity directory, with a doc comment: STORED, not live
  (a shell that cd's away doesn't move it), a path on the workspace's own
  machine like `Host`.
- `internal/app/query.go`: `ListWorkspacesIn` sets `Cwd: ws.IdentityCwd` —
  the same field `DisplayName` derives from, deliberately not
  `ResolvedIdentityCwdFrom`'s live first-pane cwd, so name and cwd agree.
- `internal/app/query_test.go`: `TestDispatchWorkspaceList` checks every
  row's cwd against `IdentityCwd`, and that the first one is non-empty.
- `docs/protocols/control-api.md`: a paragraph under the host note.

Additive and omitempty: older clients ignore it; ced treats a missing
field as "fall back to pane evidence".

## Verification

`go test ./...` passed.

## Next

Closed: None. Declined: None. Raised: None. Deferred: None.
Promoted: None. Moved: None. Updated: None.
Full list: `ai_docs/todo/next-list.md`.
