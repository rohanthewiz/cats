# Session: global todos shown as global, everywhere they surface

- **Session ID:** `97c04439-2aa8-4672-9a6f-43a4b1950cee`
- **Date:** 2026-08-04
- **Branch:** main — cats (`4b5561b` → `968ab3a`, pushed), cats-todo (`1223382` → `a21e658`, pushed)
- **Repos:** `cats`, `cats-todo`

## Request

> Where should we show global todos? It is currently shown no
> differently than a proj todo.

A design question first, then "implement the header mark and the TUI
row badge". The screenshot that came with it showed the tell:
`ced:pH todo: global (1)` in the panes list, and CED's workspace row
wearing a finger-and-string mark whose ¹ was actually the machine-wide
backlog.

## Diagnosis

Two independent places erased the scope, one per repo.

### catway sidebar

`workspaceRollups()` (in `cmd/catway/web/index.html`) parsed every
pane title for `todo: … (N)` and credited the count to the workspace
owning the pane's handle (`"w1:p3" → "w1"`). Right for a project
backlog; wrong for `todo: global (N)`, with two artifacts:

- the global count **migrated** with the pane — close the manager in
  one workspace, reopen it in another, and the "work" moved;
- two global managers in different workspaces wore the **same items
  twice** (the rollup summed per workspace).

### cats-todo list

`rebuildList` draws dim `Project` / `Global` group headings — but only
when *both* scopes have visible rows, and headings are non-selectable
separator rows, which `filter()` drops entirely while a query is
typed. So a lone global todo under a project's, or any filtered view,
showed scope-less rows.

## What was built

### 1. The heading mark (cats `968ab3a`)

Global counts now route out of the per-workspace map to the
`Workspaces` section heading — the one element that spans every
workspace, which is exactly the mark's scope:

```
workspaceRollups()
  ├─ "todo: cats (3)"    → todos[ws of pane]   (sum, as before)
  ├─ "todo: global (1)"  ┐
  └─ "todo (2)"          ┘ → globalTodos = max(…)   ← not sum
```

- `isGlobalTodoTitle()`: `todo: global (N)` is a `--global` launch;
  bare `todo (N)` is a no-project launch whose `openCount` falls back
  to the global store (cats-todo `model.openCount`) — the project name
  in the title is the only thing that marks a count as project-scoped.
- **Max, not sum**: every global manager advertises the same list, so
  two panes agreeing is not twice the work. This is also what fixes
  the double-count for free.
- Rendered into `<span id="ws-global-todo">` inside the `<h2>`, same
  `todoMark()` as the rows so it reads as the same kind of reminder;
  the tooltip says "N unfinished **global** todos". The
  `.todo-mark`/`.todo`/`.todo-n` CSS was de-scoped from `#ws-list li`
  so both containers share one set of rules.

### 2. The row tag (cats-todo `a21e658`)

Global rows carry a faint ` · global` right after the name — on the
row itself, because a fact that must survive filtering cannot live on
a separator row.

- New `listItem.tag`, drawn between name and description; new
  `tagStyle` at `colFaint`, one tier *below* the description's grey —
  at the description's own tier it read as the prompt's first words.
  The `·` separator and one-space hug (vs the desc's two) do the rest.
- Tag set only when `m.project.available()`: in a `--global`-only
  launch every row is global, the header already says "global only",
  and tagging them all would be noise.
- Group headings unchanged — the tag covers the cases they miss.

Tests pass in both repos; the TUI's frame-layout tests
(`TestListRowsMatchWhatIsDrawn` etc.) were the ones worth watching and
the tag adds no lines.

## Deliberately not done

The finger-with-a-string icon itself. Ro has an upcoming todo to
replace it with a **cats-paw** icon (the finger is hard to recognize;
the paw fits the theme). Both surfaces now render through the single
`todoMark()` in `index.html`, so that swap is one function's SVG paths
plus the shared CSS — noted in auto-memory as `todo-icon-to-cats-paw`.
