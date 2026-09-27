# Session: clicking a workspace's paw print opens its first todo pane

Session ID: 2efe691d-b260-4603-b506-8070db1c672f
Date: 2026-09-26

## 1. The ask

> If the workspace has todos and therefore a paw icon, then a click on the paw
> icon should take me to the first todo pane on that workspace

## 2. What was there

- The paw is built by `todoMark(n)` in `cmd/catway/web/js/07-workspaces.js`.
  `renderWorkspaces` appends it to a workspace row when that workspace's
  todo count is non-zero. It was display-only.
- The counts come from the pane inventory (`paneInv`, the last `pane.list`
  snapshot), not from the agents rollup. `todoOpenCount` parses the
  cats-todo manager's terminal title, e.g. `todo: cats (3)`.
  `isGlobalTodoTitle` excludes global managers, whose count belongs on the
  paw on the WORKSPACES heading.
- `wsTodoPanes(id)` already itemized the panes in a workspace that owe todos,
  for the hover card. It returned `{ref, n}` only.
- To reach a pane in another tab or workspace, the PANES and AGENTS lists send
  `agent.focus`, whose server side is `RevealPaneView` +
  `SetViewWorkspace`. `pane.focus` only moves focus within the current
  viewport.
- The workspace row's own `mousedown` calls `beginReorderDrag`, whose
  `onClick` sends `workspace.focus`, and `hideTip`.

## 3. The change (cats `273c67d`)

`07-workspaces.js`:

- `wsTodoPanes` now also returns `pane` (the numeric id) with each entry.
- New `gotoTodoPane(w)`:
  - It refuses a locked workspace with a toast (`… is locked — unlock it to
    reach its todos`), as the AGENTS rows do. The reveal is a workspace
    switch, which the lock forbids.
  - It takes `wsTodoPanes(w.id)[0]` **at click time**, not when the row was
    drawn. The inventory is re-fetched under a stationary pointer, so a
    manager that was closed or cleared since the render is never the target.
    If nothing is left, the click does nothing, and the next render removes
    the paw.
  - It sends `agent.focus {pane}`, so the jump works across tabs and from
    another workspace.
  - "First" is inventory order, which is the order the PANES section and the
    hover card list those panes.
- In `renderWorkspaces`, the row's paw:
  - gets the `jump` class and a tooltip suffix, "— click to open the todo pane";
  - calls `e.stopPropagation()` and `hideTip()` on a left-button
    `mousedown`, so the row's reorder/switch gesture never arms and no
    `workspace.focus` races the reveal. Right-click still reaches the row's
    context menu. The trade-off: a drag that starts on the paw doesn't
    reorder.
  - calls `pressActivate(todo, () => gotoTodoPane(w))`. It acts on the press
    because the list is rebuilt under the pointer.

`css/07-workspaces.css`: `#ws-list li .todo-mark.jump { cursor:pointer; }`.
The cursor is set directly, so a locked row's inherited `cursor:default`
doesn't hide it. The click there still answers with a toast.

## 4. Verification

- `node --check` on the JS passes, and `go test ./cmd/catway/...` passes
  (catway and web).
- Not driven by hand. Sessions run inside Cats.app, where GUI automation is
  blocked, so the hands-on check is added to N-001.

## 5. Left alone

The paw on the WORKSPACES heading (the global backlog) is still not
clickable. It is raised as N-041.

## Next

Closed: None. Declined: None. Raised: N-041.
Deferred: None. Promoted: None.
Updated: N-001 (paw-click hands-on check). Full list: `ai_docs/todo/next-list.md`.
