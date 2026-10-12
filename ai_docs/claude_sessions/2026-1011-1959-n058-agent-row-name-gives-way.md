# AGENTS row: the name gives way, not the state

Session: `c9b1f191-798c-4d0d-851b-4f779446764c` · 2026-10-11

Picked up N-058 from the next list (pasted from the cats-todo backlog).

## Ask

At the default sidebar width (278px) an AGENTS row measured 309px, so the
state word ("idle") was cut off at the right edge, because `#sidebar li` is
`overflow:hidden`. The state is what the row is for, so the name or the
handle should give way first.

## Design

- **The name gives way, the meta line never does.** Plugin rows already had
  an ellipsizing `.aname` with `.ameta { flex:none }`. The comment said agent
  rows didn't need it, since a trimmed model label is three words at most.
  That was wrong once the handle, age and state sit beside it. The rule now
  covers every row in AGENTS and PLUGINS.
- **The agent word survives longest.** It leads `.aname` and carries the
  identity hue, so the ellipsis eats the model half first. The full model (or
  plugin id) is still in the row's tooltip.
- **The flag left `.aname`.** It was the last child of the name span, so it
  would have been the first thing the ellipsis cut. It is now a row item of
  its own between the name and the meta line, in both agent and plugin rows.
  This matches how pane rows place their flag (`08-panelist.js`). One visible
  difference: the flag now has the row's 6px gap before it.
- **The handle keeps its workspace half.** That was the note's other option.
  AGENTS is the one list that spans every workspace, so the workspace name is
  what says where an agent lives. Shortening the name was enough.

## Changes

- `cmd/catway/web/css/11-agentlist.css`: the `li.plug`-scoped ellipsis rule
  now covers all rows, with a before/after diagram in the comment.
- `cmd/catway/web/js/17-agentlist.js`: `agentRow` and `pluginRow` append the
  flag mark to the `<li>` between the name and the meta line.
- `cmd/catway/web/jstest/agentlist.test.mjs`: `world()` takes stub
  overrides. A new case checks that the row's children are dot, name, flag,
  meta, with no flag inside `.aname`, on both kinds of row.

## Verification

- A headless-Chrome harness (in the scratchpad, not committed) loads the real
  CSS bundle and builds the row the way `agentRow` does, with the sidebar at
  278px:
  - **Before:** row content was 309px, the same as the note, and "idle" was
    clipped. A flagged row was 317px.
  - **After:** both rows are 265px, and the meta line ends exactly at the
    row's content edge.
  - The name reads "claude o…". With a flag it shrinks to about "claud…".
- `make jstest` (all suites), `go build ./...` and `go vet ./cmd/catway/`
  pass.
- Not checked in a live catway with a stand-in agent.

## Next

Closed: N-058. Declined: None. Raised: None. Deferred: None. Promoted: None.
Moved: None. Updated: None. Full list: `ai_docs/todo/next-list.md`.
