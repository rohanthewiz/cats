# The hover card mutes the row's native tooltips

Session: https://claude.ai/code/session_01QWp6p8ZabmtUYuNCnpYGZH
Date: 2026-09-04
Repo: ~/projs/go/cats (branch `main`)

## Request

> Where we show popup cards over workspaces ensure the regular tooltip is
> disabled so it doesn't compete with the card

…then, after the workspaces change landed:

> do the same for the panes list

## The problem

Both sidebar lists build their rows out of small glyph marks that each carry a
native `title` attribute:

- WORKSPACES (`07-workspaces.js:renderWorkspaces`) — `flagMark` (`flagTitle`:
  glyph, label, note, age), `lockMark`, `todoMark` ("3 unfinished todos"), the
  `@host` badge, `otherWindowMark`, and each `●N` dot in `workspaceSummary`.
- PANES (`08-panelist.js:paneRow`) — the same `flagMark`.

Since the previous session those same rows also open a `#panetip` hover card
(`showWorkspaceTip` / `showPaneTip`) that says all of it, and more, in full. So
hovering produced two popups for one row: the card immediately beside the
pointer, and ~1s later the browser's own tooltip in its own box, repeating a
slice of what the card was already showing — often drawn right over it.

## What was built

One mute/restore pair in `09-hovercard.js`, alongside the `showTip` primitive
that both cards already share:

```
muteTitles(el)    // strip title from el and every [title] descendant,
                  // stashing each old value in data-tiptitle on the node
restoreTitles()   // put them all back, clear the record
```

Wiring:

- `hideTip()` calls `restoreTitles()`, so **"no card showing" and "titles live"
  are the same state** — no matter which way the card went away (mouseleave,
  the workspace row's mousedown-before-switch/reorder, or a row that stopped
  qualifying for a card).
- `showWorkspaceTip` mutes *after* it actually shows a card. Its `!items` path
  calls `hideTip()`, so a plain workspace row (no flag, no todos, no flagged
  panes inside it) keeps its glyph tooltips — they are all it has.
- `showPaneTip` mutes unconditionally: a pane row always opens a card.

### Design choices worth keeping

**Dynamic mute on hover, not a render-time decision.** The alternative was to
simply not set `title` on the marks of a row that qualifies for a card. That
needs the qualifying predicate (`flagOf(w) || wsTodoPanes(w.id).length ||
wsFlaggedPanes(w.id).length`) evaluated per row on every render — and
`wsTodoPanes`/`wsFlaggedPanes` each scan the whole pane inventory. That is
exactly the O(workspaces × panes)-per-render cost `workspaceRollups` exists to
avoid. The hover path pays for one row, only while it is hovered.

**The stash lives on the node (`data-tiptitle`), not in a map keyed by
element.** These lists are rebuilt on every rollup, under a stationary pointer.
An attribute travels with the node and dies with it, so a record can never
outlive the row it describes; restoring into a since-replaced row writes to a
detached node nobody sees again. The fresh row arrives with its titles intact
and is re-muted on the next `mousemove` — and a browser will not open a tooltip
for an element swapped in under a motionless pointer, so there is no window in
which both appear.

**Idempotent per row** (`if (el === mutedRow) return`). The handlers run on
every `mousemove`; without the guard the second pass would stash the
already-emptied value and hand the row back a blank tooltip.

**`titleNodes(el, attr)` includes the root**, not just descendants — a row that
titles itself competes with the card exactly as a glyph inside it does.

## Files touched

- `cmd/catway/web/js/09-hovercard.js` — `mutedRow`, `titleNodes`,
  `muteTitles`, `restoreTitles`; `hideTip` restores; both card entry points mute.
- `cmd/catway/web/js/07-workspaces.js` — comment on the row's hover wiring.
- `cmd/catway/web/js/08-panelist.js` — same, on the pane row's.

No CSS, no Go, no wire change.

## Deliberately left alone

- **Panes-list group headers** (the expand/collapse workspace rows and the
  "more workspaces…" shelf) and the **agents list** rows: they set `li.title`
  but open no card, so nothing competes there.
- **The build badge** (`10-buildbadge.js`), the third `showTip` caller.

## Verification

`go build ./...`, `go test ./cmd/catway/web/` (ok) and `node --check` on all
three touched files. Not driven in a real browser this session.
