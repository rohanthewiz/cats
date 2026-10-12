# Sidebar: a light-brown dot for blank agent sessions

Session: `38db0bb4-1095-424a-b28d-b7d709f7029e` · 2026-10-11

Continues `2026-1011-1617-agent-session-origin` (same session, after a
context compaction).

## Ask

"Now differentiate blank sessions (freshly cleared or new) with a
light-brown dot."

## What "blank" means

An agent pane whose current conversation has had nothing asked of it yet:
no prompt addressed to the model and no main-chain assistant record. Slash
commands (`/clear`, `/model`, …) and `!` shell escapes run locally and do
not count. A skill invoked as a slash command does count, because the model
answers it (an assistant record).

- Read only for a conversation pinned to a session id, never off the
  newest-transcript-under-cwd fallback (that may be a neighbour's history).
- A missing history is blank only when the origin is `startup` or `clear`
  (claude writes its transcript lazily). A missing file with any other
  origin may just be a history kept under another `CLAUDE_CONFIG_DIR`.
- The word comes from the origin: `clear` → "cleared", anything else → "new".
- Cleared without a read the moment the pane goes working (`publishAgent`),
  so the dot never lingers for a whole turn behind the throttled re-read.

## What was built (commit `0b066cd`)

### Server — `cmd/catway`
- `agentblank.go` (new): `claudeBlank` (transcript by id, ≤1 MiB, skips
  `<command-…>` / `<bash-input>` records, uses `claudePromptText`),
  `copilotBlank` (`user.message` or `assistant.message` ends it),
  `resolveBlank`, `blankWord`, `readSmallHistory`.
- `agentmodel.go`: the `modelResolver` gained a `blank` reader (claude,
  copilot). `refreshAgentModel` does the blank read in the same background
  goroutine as the model and posts an `agentReadResult` to `setAgentRead`.
  A read that started before `rt.workingAt` is dropped as stale. Sessions
  seen non-blank are cached in `rt.nonBlankSession` and never re-read.
  `historyMoved(rt)` resets blank/model state and re-reads when the
  conversation changes, from either the hook or detection.
- `paneAgentMsg` builds the pane message with `Blank`; `agentsMsg` and
  resync carry it too.
- Side fix: a hook-reported session change now refreshes the model readout
  and blank state straight away, as a detected change already did.

### Wire
- `wire.AgentItem.Blank` and `wire.PaneAgent.Blank` (`blank,omitempty`,
  additive; cats-todo / cats-mobile pins unaffected).

### Browser — `cmd/catway/web`
- `markerState(item)`: idle + blank → `"blank"`, checked before `done` (a
  conversation with nothing in it has finished nothing). Blank ranks with
  idle and earns no tab mark.
- Shown in the AGENTS list ("… · 18s ago · cleared"), pane header ("new" /
  "cleared" in brown), workspace dropdown pane rows, and the workspace badge
  as its own count (tooltip "blank (new or cleared)", via `stateCountLabel`).
- `.st-blank { color: var(--blank) }`.

### Theme
- New key `blank`, derived from `ws-heading` in `internal/theme/theme.go`;
  base `#c8a27c`. Explicit light browns for darcula, dark-game and
  solarized-dark (the derived colour came out olive/yellow, too close to
  their working amber), and a darker `#8a5a32` for the two light themes.
  Checked across all ten built-ins with headless Chrome.
- `docs/reference/configuration.md` documents the key.

### Tests
- `agentblank_test.go`: claude/copilot readers, `resolveBlank`, end to end
  on the orchestrator (cleared → working drops it → answered history cached
  non-blank), stale read dropped.
- `jstest/statemarker.test.mjs`: 16 assertions on the marker, class, rank,
  label and tab mark.
- Full `go test -tags ghostty ./...`, `go vet`, `gofmt`, `make jstest` green.
- Live: scratch catway + cathost on 127.0.0.1:8531 with a stand-in Go
  `claude` binary and a fixture `CLAUDE_CONFIG_DIR` showed brown "new" and
  "cleared" dots, a worked session stayed green, badge ●1 ●3, header
  "cleared" in brown. Scratch processes stopped and sockets removed.

## Notes

- A blank resumed conversation reads "new"; only `/clear` gets "cleared".
- Unix socket paths for scratch runs must be short (`/tmp/czb-*.sock`), and
  cathost and catway need separate control sockets.
- A fixture /clear transcript with past timestamps classifies as `resume`
  (history predates the switch) — correct by design.
- Not yet seen with a real claude pane or in Cats.app; folded into N-056.

## Next

Closed: None. Declined: None. Raised: None.
Deferred: None. Promoted: None. Moved: None.
Updated: N-056, N-057. Full list: `ai_docs/todo/next-list.md`.
