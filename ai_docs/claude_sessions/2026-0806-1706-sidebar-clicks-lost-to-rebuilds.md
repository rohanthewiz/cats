# Session: sidebar clicks lost to list rebuilds — activate on the press, not the click

Session id: `a9dbea7f-a9b0-45b9-912e-385d4631569d`

## Task

"Sometimes when the system is busy, my clicks to switch to another workspace does
not register." Then, after the diagnosis: "fix the pane rows and agent rows too".

## Diagnosis

Not a dropped command, not a server-side race — a DOM race in the browser UI.

Every list in the sidebar is rebuilt wholesale. `renderWorkspaces` opens with
`wsListEl.innerHTML = ""` and re-creates every `<li>`; `renderTabbar`,
`renderPaneList` and `renderAgents` do the same to their own containers. All four
hang off the agents rollup:

- `renderAgents` → `renderWorkspaces` + `renderTabbar` directly (the workspace
  badges and tab attention markers derive from the rollup), then
  `refreshPaneList`.
- The server broadcasts that rollup on **every** agent state transition
  (`cmd/catway/notify.go:95`), on model changes (`agentmodel.go:176`) and on every
  `applyModel` (`catway.go:650`).

So a session with several agents working repaints these lists several times a
second, while a mouse press is held for ~100ms.

That is a lost click. Per the UI Events spec, `click` is dispatched on the
**nearest common ancestor of the mousedown and mouseup targets**. When a rebuild
lands mid-press, the old row takes the mousedown, its replacement takes the
mouseup, and the click surfaces on the `<ul>` — where nothing is listening:

```
  mousedown ──► row A ──┐
                         ├── rebuild ──► click lands on <ul>  (dropped)
  mouseup   ──► row A' ─┘
```

The row does nothing, silently, and it happens most often exactly when the
session is busy enough that you want to switch away from it — which is the
correlation the user noticed.

The drag code already knew about this hazard and had defended itself: *"A mid-drag
re-render replaces the container's children (and drops the bar); re-query and
re-attach every move so the drag survives it."* The click path never got the same
treatment.

## Fix

Activation hangs off the press itself. `mousedown` records where the press began;
a **window-level** `mouseup`, closing over the row's identity, decides what it
meant. Neither the listener nor the closure cares whether the element still
exists, so a rebuild in between is irrelevant.

Two entry points, one rule:

- `pressActivate(el, fn)` (new, `cmd/catway/web/index.html:1865`) — for rows that
  do not drag. The mouseup listener unregisters itself, so no press leaves one
  behind (the leak the pane-selection code was bitten by, whose comment documents
  what permanent per-element window listeners cost).
- `beginReorderDrag`'s new `cfg.onClick` — for rows that *do* drag, so one press
  can only ever be read one way.

`DRAG_SLOP = 4` is now a single named constant shared by both readings: under it
the press is an activation, at or over it the draggable rows read it as a drag.

### Converted

| Element | Line | Action |
|---|---|---|
| Workspace rows | 1483 | `workspace.focus` (via `onClick`), lock toast intact |
| Workspace `+` | 1493 | `newWorkspace()` |
| Pane group headers | 1582 | fold / unfold |
| Pane rows | 1611 | `agent.focus` / `pane.focus` |
| Tabs | 1796 | `tab.focus` (via `onClick`) |
| Tab close ✕ | 1806 | `tab.close` |
| Tab `+` | 1813 | `tab.create` |
| Agent rows | 2402 | `agent.focus`, lock toast intact |

### `dragConsumedClick` retired

The flag existed to suppress the native click trailing a completed drag. With
both readings measured against the one threshold, a drag's release can no longer
reach an activation to suppress — a drag by definition passed the distance that
blocks the activation. Every reader was gone after the conversion, leaving it
write-only, so both setters went with it. `beginPaneSwapDrag`'s drop acts on the
target pane directly and never went through a click either.

### Left deliberately

The **usage** group headers (`:2231`) keep a plain `click`. That list only
rebuilds when a usage message lands — the server polls every couple of minutes —
so a mid-press rebuild there is a rounding error.

## Verification

- `node --check` on the extracted page script — OK.
- `go build ./...`, `go test -count=1 ./cmd/catway/` — green.
- No behaviour change to double-click (rename), right-click (context menu), or
  either drag; `contextmenu` is untouched and both drag helpers still bail on
  `ev.button !== 0`.

Not exercised by an automated test — there is no browser/DOM harness in this repo,
and the failure needs a real mid-press rebuild to reproduce.

## Note

`web/index.html` is `go:embed`-ed (`cmd/catway/main.go:83`), so this needs a
`make macapp` before it shows up in the installed app.
