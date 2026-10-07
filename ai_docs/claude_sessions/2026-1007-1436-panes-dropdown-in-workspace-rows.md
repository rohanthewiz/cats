# Session: the PANES section folds into a dropdown on each workspace row

Session ID: 1f9c2cc9-51fc-4892-86d8-715ec907b094
Date: 2026-10-07
Driven from: cats

The request came from the user's cats-todo backlog, not the next-list. It was
pasted as a prompt:

> Instead of having a PANES section separate from the WORKSPACES section why
> not integrate the panes list for a workspace into a dropdown? The dropdown
> arrow could live at the far right of the workspace row.

A follow-up asked that pane rows drop the workspace namespace (`p1`, not
`cats:p1`). Commit: `f1a9671` ("Sidebar: panes fold into a dropdown on each
workspace row"), pushed to main.

## Background

The sidebar had a WORKSPACES section and, under it, a PANES section listing
the whole session's pane inventory (`pane.list`), grouped by workspace under
header rows. Those headers repeated every name the section above had just
listed, so the session was read twice and there were two sets of folds,
shelves and scroll caps to keep in step. PANES also sorted by attention: the
current workspace pinned first, workspaces with agents next, idle ones folded
behind a "more panes…" shelf.

## 1. The change

```
WORKSPACES                  ⊞ ⊟ ▼
  ● cats  ⚑       ●1 ●2   ▼        caret, hard against the right edge
      p1  vim                       its panes, indented under the name
      p2  build  claude
  ○ api           ●1      ▶        shut: the row alone
  ○ old ☾                           asleep: nothing to list, no caret
  + workspace
```

- **The PANES section is gone** (`sidebar.go`). Each awake workspace row gets a
  `▶/▼` caret (`paneCaretEl`), and an open dropdown's pane rows are appended as
  **siblings** of the workspace row in `#ws-list`, not a nested `<ul>`. The row
  is one flex line whose hover, focus colour and drag all assume that. The
  reorder drag measures only `li.ws`, so a drop between two workspaces lands
  after the first one's panes with no change to `beginReorderDrag`.
- **`08-panelist.js` was rebuilt around the dropdown.** `renderPaneList`
  became `wsPaneRows()`, which returns rows bucketed by workspace id. The merge
  of layout state, local pane state, the snapshot and the agents rollup is
  unchanged. `paneGroupEl`, `paneMoreEl` and the heading's ⊞/⊟ IIFE are gone.
  `renderWorkspaces` (07) calls `wsPaneRows()` once per render and emits
  `paneRowEl(row)` under each open row.
- **One render path.** The inventory frame used to track two "due" flags
  (Panes vs Workspaces). Both views are now one list, so the frame redraws
  `renderWorkspaces` alone. `renderInventoryViews` and `renderWorkspacesSoon`
  are both kept as entry points, because their callers name what changed.

### Open state

- **Explicit choice wins, otherwise follow the focus.** `wsPanesOpen` is a
  null-prototype map of `id → bool` stored in localStorage
  `cats.workspaces.panesopen`. An absent id means "untouched": open if it is the
  workspace this window shows, shut otherwise (`wsPanesAreOpen`). It is a map
  rather than a set of open ids because a set cannot tell "shut on purpose"
  from "never touched". The default keeps the one part of PANES's attention
  sort worth keeping, the current workspace pinned and open. The idle-shelf
  half is not needed, since workspace rows already show agent states.
- **Null prototype**: lookups key on server-chosen ids, and `{}` would read
  `"constructor"` back as a truthy function.
- **Pruning**: `prunePanesOpen` drops ids the layout no longer has, so a choice
  for a closed workspace is not inherited by a new one given the same id. It
  only writes storage when it actually dropped something.
- **Old keys are not migrated.** `cats.panes.collapsed` and
  `cats.panes.moreopen` recorded folds of a differently shaped list. Read as
  dropdown state, they would open every workspace on first load. They are left
  in storage unread.

### The caret

- **Alt+click toggles every workspace** (`togglePanes(w, all)`). It replaces
  the old PANES heading's ⊞/⊟, because the Workspaces heading's ⊞/⊟ already
  mean "fold the shelves". Every id is written explicitly, or an Alt-collapse
  would leave the current workspace reopened by its default. Alt is read from
  the mouseup event that `pressActivate` hands back.
- **The press stops at the caret.** Mousedown is `stopPropagation` +
  `preventDefault` + `hideTip`, so the row never arms switch/reorder (the todo
  paw's arrangement). Dblclick is stopped too, so a double press doesn't
  rename. Right-click passes through to the row's menu.
- **The tooltip carries the counts** the old group header showed in full:
  "show 4 panes (2 running an agent) · Alt+click: every workspace". The row
  has no room for them at 150px.

### CSS (`10-panelist.css`)

- `#pane-list …` rules moved to `#ws-list li.pn …`. Pane rows are indented
  `calc(22px * var(--sb-scale))` so they sit just past the dot. The 34vh cap
  on the old list is gone; the Workspaces section can be capped by its grip.
- **The workspace name now ellipses** (`#ws-list li.ws > span:first-child
  { min-width:0 }`). Flex items don't shrink past their content by default, so
  a long name would have pushed the caret (and before it the agent badge) out
  of the clip.
- **The working-state colour and pulse are scoped to the badge.** The rule
  was `#ws-list .st-working`; it is now `#ws-list .sum .st-working`. Otherwise
  a pane row's agent label, now inside `#ws-list`, would take `--warn`
  instead of `--warn-fg` and blink.

### Pane rows read `p1` (follow-up)

`paneLocalRef(pub, id)` strips the workspace part for the dropdown row's
label only. The hover card still shows `cats:p1`, the form `catctl` wants;
the header, palette, agents list and dialogs keep it as well. With no handle
it falls back to `paneRef`'s `#id`. The `paneRef` doc comment names the
dropdown as its one exception.

### Elsewhere

- The keybindings help lists the two gestures.
- Stale "PANES" comments were updated across the front-end, `catway.go` and
  `theme.go`. The `ws-heading` theme key's description now reads "group
  headers inside the sidebar's Workspaces and Usage lists" (it never meant
  Panes alone, since Usage uses it too).
- Docs updated: `configuration.md`, `cli.md`, `control-api.md`,
  `browser-protocol.md`.

## 2. Verification

- `go test ./cmd/catway/... ./internal/theme/...`, `go vet ./cmd/catway/...`
  and `make jstest` all pass. A new `jstest/panedrop.test.mjs` adds 39
  assertions: the default and explicit-choice rules, Alt in both directions,
  pruning (and no write when nothing is pruned), bucketing (asleep workspace
  excluded, inventory order, snapshot vs custom name, the layout fallback
  before the first `pane.list`), `paneLocalRef` vs `paneRef`, and the caret
  (press isolation, right-click passthrough, dblclick, the tooltip).
- **Headless Chrome against an isolated instance.** The scratchpad got its own
  `catway` + `cathost` (`-tags ghostty`) on `127.0.0.1:9471` with `--auth
  none`, relative socket paths (`h.sock`, `ctl.sock`) to stay under the
  Unix-socket path limit with the cwd in the scratchpad, `-persist=false`, and
  its own config, state, plugins and todo dirs. It was seeded with `catctl`:
  four workspaces, one asleep, two tabs, a renamed and a starred pane. A CDP
  script used real `Input.dispatchMouseEvent` presses. It confirmed:
  - a fresh profile opens only `cats`;
  - the caret on `api` opened it and did not switch workspace;
  - Alt shut every dropdown (storage held explicit `false` for all), and Alt
    again opened them all;
  - pressing a pane row from another workspace revealed it;
  - choices survived a reload;
  - at 150px every caret stayed inside its row and the long name ellipsed;
  - dragging `api` above `cats` with both open reordered correctly;
  - zero page errors.
  After the follow-up the rows rendered `p1`/`p2`. The script's step 5 then
  failed by design, because it searched for an `api:p2` label. Chrome, catway
  and cathost were stopped by PID only.
- **Not checked in Cats.app's WKWebView** (GUI automation is blocked there).
  That check was added to N-001.

## 3. cats-mobile

Asked whether cats-mobile needs a Next item to follow. **No.** The phone has
no workspace list and no Panes section (its tabs are Agents, Alerts, Windows
and More, and panes are reached from an agent row). Its roster rows have no
workspace row above them, so the full handle is still the right label there.
The commit does not touch `wire/` or `internal/orchestration`, so the next
routine pin bump has nothing to react to. cats-mobile also has no
`ai_docs/todo/next-list.md`. If the phone ever grows a workspace browser, this
nested layout is the one to copy, in that feature's own plan.

## Next

Closed: None. Declined: None. Raised: N-053. Deferred: None. Promoted: None.
Updated: N-001. Full list: `ai_docs/todo/next-list.md`.
