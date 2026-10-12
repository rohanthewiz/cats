# Session origin shown after the first prompt

Session: `90c84443-afd2-4a3c-a9d4-0af7d881fabe` · 2026-10-11

Continues `2026-1011-1920-blank-session-dot` (loaded with `/sl`).

## Ask

"show the session origin after the first prompt too" (N-057). Before this,
the browser showed how a conversation began only while it was blank, through
the light-brown dot's "new" / "cleared". After the first prompt it was gone,
and "resumed" never showed at all.

## Design

- **What is shown:** a past-tense verb plus an age: "started 2h ago",
  "cleared 5m ago", "resumed 1d ago". "new" works for a blank slot but reads
  oddly after a turn, so the verb form is used once the pane has worked.
- **Where the time comes from:** cathost. It sees the switch and outlives a
  catway restart, and it replays `pane_agent_session` on resync. If catway
  stamped the time on arrival, every conversation in progress would look as
  if it had just begun after a restart. `detect.AgentSessionBegan` sets it:
  - a launch (new pid) uses claude's registry `startedAt`, so a fresh start
    or `claude --resume` is exact;
  - a switch in the same pid (/clear, /resume) uses the read time, at most
    one poll late;
  - otherwise the read time. A start time in the future is not believed.
- **Hook path:** a hook ref is stamped by catway when it first names a new
  conversation. A re-report of the same one (a compact) keeps the stamp.
  `sessionOrigin` merges the two by taking the earlier, since each can only
  be late. A ref restored across a restart has no time and borrows the
  detected one.
- **Wire:** relative `origin_ms` rather than an absolute instant, like
  `since_ms`, so a browser with a skewed clock still shows the right age. A
  known age is at least 1, because 0 is the omitempty "unknown". `origin`
  uses `PaneMeta.AgentSessionOrigin`'s vocabulary (startup / clear / resume),
  and the browser maps it to the verb.
- **Republish:** `historyMoved` publishes `pane_agent` plus the rollup when
  the origin changes (`rt.pubOrigin`). The history read only republishes on a
  model or blank change, so a /resume past its first prompt changed neither,
  and remote panes get no read at all.

## What was built

### Host
- `internal/detect/sessionorigin.go`: `AgentSessionBegan`.
- `internal/orchestration`: `PaneAgentSession.BeganMs` (`began_ms`,
  omitempty). `NewPaneAgentSession` takes `began`. The pump computes it on
  the same read as the origin, and `setAgentSessionMeta` stores and replays
  it.

### catway
- `agentSessionRef.began`. `applyPaneAgentSession` sets it from `BeganMs`.
  `setSessionRef` stamps new conversations and keeps the held stamp on
  re-reports. The stamped copy is never written through, because `ref` may
  be the parked `pendingSession`.
- `sessionOrigin` now returns `(session, origin, began)`.
- `originStamp`, `originAgeMs`, and `paneAgentMsg` / `agentsMsg` carry
  `Origin` and `OriginMs`.

### Wire
- `AgentItem.Origin` / `OriginMs` and `PaneAgent.Origin` / `OriginMs`.

### Browser
- `07-workspaces.js`: `originWord`, `originPhrase`, `paintOrigin`,
  `fillOrigin`.
- **Pane header** (`06-chrome.js`): a muted `.aorigin` field after the state
  word. Its tooltip adds the clock time, and the agents 5s tick re-ages it.
  It is `flex:none`: when it could shrink, a long path squeezed it to "s…".
- **AGENTS row** (`17-agentlist.js`): only in the row's tooltip, on a line
  under the model, refreshed on mouseenter. The row itself has no room (see
  N-058).
- **Pane hover card** (`09-hovercard.js`): a "Session" row, shown while
  blank as well. Off-screen panes take it from the rollup (`08-panelist.js`).
- The header and tooltip skip it while the conversation is blank, because
  the blank word already says it.

### Tests
- `TestAgentSessionBegan`, plus `began_ms` asserted in
  `TestHostReportsAgentSession`.
- `cmd/catway/agentorigin_test.go`:
  - the origin outlasts working;
  - the hook stamp, compact keep and earlier-wins merge;
  - a restored ref borrows the detected time.
- `jstest/origin.test.mjs` (14 assertions), and the AGENTS tooltip cases in
  `agentlist.test.mjs`.
- Full `go test -tags ghostty ./...`, `go vet` both ways, gofmt, and
  `make jstest` all pass.

### Live check
- A scratch cathost and catway on 127.0.0.1:8531, with their own sockets
  (`/tmp/czo-*.sock`), a fixture `CLAUDE_CONFIG_DIR`, and a Go stand-in
  `claude`. The stand-in writes the pid registry and transcripts, and
  switches conversation on `clear`.
- The header read "started 5m ago (7:34 PM)" both before and after a catway
  restart, matching the registry's `startedAt`. After `clear` it read
  "idle · cleared 18s ago" on a non-blank conversation, and the AGENTS
  tooltip read "claude-opus-5-5 / cleared 13s ago".
- Scratch processes stopped and sockets removed. The user's catway on 8499
  was untouched.

## Notes

- The pane shell's rc puts the real `claude` ahead of a PATH-prefixed
  stand-in, so `catctl run 1 claude` started the real CLI (onboarding, in
  the fixture config dir). Run stand-ins by absolute path. That one process
  was stopped by pid.
- The AGENTS row already overflowed before this change: 309px in a 278px
  sidebar, so "idle" was clipped. That is N-058.
- A blank resumed conversation still shows "new" in the blank slot. The
  hover card's Session row says "resumed".

## Next

Closed: N-057. Declined: None. Raised: N-058.
Deferred: None. Promoted: None. Moved: None.
Updated: N-056. Full list: `ai_docs/todo/next-list.md`.
