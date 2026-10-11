# Agent sessions: tell a /clear from a fresh start (and a resume)

Session: `38db0bb4-1095-424a-b28d-b7d709f7029e` · 2026-10-11

## Ask

From the cats-todo backlog: "Are we able to differentiate when a claude code
session has been cleared or is a brand new session?" After the answer (no,
not today), the user asked to implement both approaches below, without
caring about interchangeability with the Rust server.

## Findings (before the change)

- Cats only noticed that a pane's claude session id changed, never why.
- **Hook seam:** the claude integration's SessionStart hook
  (`internal/integration/assets/claude/cats-agent-state.sh`) read only
  `session_id`, throwing away Claude Code's `source` field
  (`startup` | `clear` | `resume` | `compact`).
- **Detection seam:** the host re-reads `~/.claude/sessions/<pid>.json` every
  3s (`internal/orchestration/host.go`, `agentSessionInterval`), so a `/clear`
  shows up as a new id under the same pid — but the pid was never kept, so the
  comparison was impossible.
- **Latent bug:** `setSessionRef` (`cmd/catway/hooks.go`) refused any hook id
  that conflicted with the held one (guard against nested/sub-agent sessions).
  So after a `/clear` the hook ref stayed on the old conversation, and
  `historySession` prefers the hook ref — the model readout and the new
  **prompts ▾** list kept reading the pre-clear history.
- Real transcripts show claude writes the `/clear` command into the **new**
  session's transcript head: `<local-command-caveat>…` then a user record
  whose string content is `<command-name>/clear</command-name>…`.

## What was built (commit `6d1bd71`)

### Vocabulary — `internal/detect/sessionorigin.go`
- `SessionOriginStartup` / `Clear` / `Resume`; `NormalizeSessionOrigin` maps
  anything else (notably `compact`, which keeps the session id) to `""`.

### Detection inference (host side)
- `detect.AgentSession{PID, ID, StartedAt}` and `AgentSessionOf(agent, pids)`;
  `AgentSessionID` kept as a wrapper. The claude probe now also parses
  `startedAt` (epoch ms) from the registry.
- `AgentSessionOrigin(agent, cur, prev, prevSeen)` — first rule wins:
  1. transcript has a record older than the switch → `resume`
  2. transcript head has a `/clear` (`/reset`, `/new`) command record → `clear`
  3. same pid as the previous conversation → `clear`
  4. otherwise → `startup`

  "The switch" = `prevSeen` (last read that still saw the old id) for the same
  pid, else the process `startedAt`. The `/clear` marker covers the no-previous
  case (host restart, first sight of a long-running cleared session).
- Transcript found by glob `projects/*/<id>.jsonl` (no slug rules), head
  bounded at 64 KB, earliest timestamp is a minimum (records are not strictly
  time-ordered).
- Pump (`host.go`) keeps `lastSession` / `lastSessionSeen` / `lastSessionOrigin`,
  classifies once per id change, keeps `lastSession` through empty reads.
- `PaneAgentSession` gains `Origin` (`origin,omitempty`); resync replays it.

### Hook seam
- Claude hook asset → `CATS_INTEGRATION_VERSION=6` (`claudeIntegrationVersion`),
  forwards `session_source`. `.ps1` left alone (calls the Rust CLI; not embedded).
- `hookReportParams.SessionSource`; `agentSessionRef` gains `origin`;
  `sameConversation` compares identity without origin.
- `setSessionRef`: a conflicting id is accepted when `replacesSessionRef` —
  origin is `clear` (only the pane's interactive agent can clear) or detection
  already names that id. Otherwise it is parked in `rt.pendingSession` and
  adopted when `applyPaneAgentSession` reports the same id. A re-report with
  no origin (compact, old integration) keeps the known origin. Pending is
  cleared on exit and on detection-driven ref drops.

### Surface
- `wire.PaneMeta`: `AgentSession`, `AgentSessionOrigin` (additive; cats-todo /
  cats-mobile pins unaffected). Filled from `rt.sessionOrigin(agent)`, which
  uses the same ref as the history readers and fills a missing hook origin
  from detection when both name the same id.
- One `log.Printf` per session change (hook and detected).

### Tests
- `internal/detect/sessionorigin_test.go`: every origin path, head parsing
  (block-list content, prose mentions and assistant records don't count).
- `cmd/catway/hooks_test.go`: clear replaces, compact keeps origin, resume
  parks then is adopted, detection-first startup accepted, origin fallback.
- `host_test.go`: live pump emits `origin: startup`.
- `assets_test.go`: claude asset must mention `session_source`.
- Full `go vet` / `go test -tags ghostty ./...` green. A throwaway check
  against real files: this live session → `startup`, an old /clear transcript
  → `clear`, same treated as a `--resume` launch → `resume`.

## Notes

- 3s polling can merge two quick boundaries into one (e.g. `/clear` then
  `/resume` within a poll); the hook seam has no such gap.
- A `go build -tags ghostty ./cmd/catway` overwrote the gitignored `./catway`
  binary in the repo root.
- Users with the integration installed must re-run `catctl integration install`
  for the v6 hook; detection covers the meantime.

## Next

Closed: None. Declined: None. Raised: N-056, N-057.
Deferred: None. Promoted: None. Moved: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
