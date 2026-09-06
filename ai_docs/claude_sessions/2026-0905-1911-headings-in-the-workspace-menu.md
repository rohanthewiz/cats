# Headings in the workspace context menu

Session: https://claude.ai/code/session_01AaoUtxuk5QmaSW7YqYSxJj
Date: 2026-09-05
Repo: ~/projs/go/cats (branch `main`)

## Request

> Let's organize the workspaces context menu. Let's put all the workspace
> things at the top followed by a horizontal separator then the worktree
> things. Perhaps we can do sub-headings in the context menu so we have a
> Workspace heading followed by all the workspace verbs, so we don't have to
> say "new workspace" just "new". Then followed up by the Worktree heading and
> its items

Then, after the first pass, an explicit ordering:

>     WORKSPACE
>       new            rename            flag
>       open in new workspace            lock
>       clean  ▸        sleep…  ▸        close…
>     ────────────────────────
>     WORKTREE
>       new…            open…            delete checkout…

## What existed

`wsMenuItems` (28-ctxmenu.js) returned one flat list whose rows addressed two
different subjects. Every label had to carry its subject — `new workspace…`,
`rename workspace…`, `new worktree…`, `delete worktree checkout…` — which put
the repeated word first and the verb, the part that actually differs, out at
the right. The one `-` separator sat between the worktree rows and
`close workspace…`, so the destructive workspace row lived *below* the
worktree group.

The item vocabulary was `{label, fn, danger?}`, `{label, sub:[…]}`,
`{icon:{text,cls}}`, and the string `"-"`.

## The change

**A third item type: `{head: "Workspace"}`.** An inert section heading, handled
in `buildCtx` before the row path:

```js
if (it.head) {
  const h = document.createElement("div");
  h.className = "head"; h.textContent = it.head;
  m.appendChild(h);
  continue;
}
```

The `continue` is the whole point: a heading gets no `mouseenter`, no `click`,
and — unlike an ordinary row — it does not call `closeSub()`. An ordinary row's
`mouseenter` closes an open sibling submenu, so if a heading were built as a
row, sliding the pointer out of the `clean ▸` submenu across the `WORKTREE`
heading would collapse it mid-motion.

**Naming the subject once buys the labels back.** Under `WORKSPACE`: `new…`,
`rename…`, `flag…`, `open in new window`, `lock`/`unlock`, `clean`, `sleep…`,
`close…`. Under `WORKTREE`: `new…`, `open…`, `delete checkout…`.
`close workspace…` moved up into its own group, so the separator now splits
exactly what the request asked it to.

**Final order** (second pass): the three rows that address the workspace
itself — make one, name one, mark one — then the two that change how it is
viewed or held (`open in new window`, `lock`), then the winding-down rows in
the order they escalate: `clean` → `sleep…` → `close…`. The `w.asleep` branch
still swaps `clean`/`sleep…` for a single `wake`.

**CSS** (`18-ctxmenu.css`): `#ctxmenu .head` is muted, 10px, uppercase, `.8px`
tracking, `cursor:default` — deliberately quieter than a row, matching the
sidebar's own `h2`, so the eye reads the column of verbs first and only drops
to the heading when a bare label needs its subject. `:first-child` kills the
top margin, since a menu opening on a heading is the common case.

## Two labels left alone

- **`open in new window`**, not "open in new workspace" as the mock had it —
  that row calls `openWindow(w.id)` and opens a second window *onto this same
  workspace*. Making a new workspace is the first row.
- **The `…` on `new…` / `rename…` / `flag…`.** The mock dropped them; they were
  kept, and the convention was then confirmed:

  > The ellipses are correct bc they suggest another step before actual action
  > is performed

  So a trailing `…` marks a dialog, prompt, or submenu ahead of the action, and
  its absence (`lock`, `clean`, `open in new window`) means the click acts. This
  went into memory as a project convention.

## Not touched

`31-palette.js` keeps its fully-qualified labels (`rename workspace…`,
`lock workspace (no plugins or agents)`, `close workspace…`). The palette is a
flat searchable list — typing "workspace" has to find them, and there is no
heading above them to supply the noun.

## Verified

Built `cathost`/`catway` with `-tags ghostty` into the scratchpad, ran a
throwaway instance on 127.0.0.1:8531, dispatched `contextmenu` on the workspace
row and screenshotted the result — headings render, the separator splits the
groups, submenu arrows survive. Instance and tab cleaned up.
`go build ./...` and `go test ./cmd/catway/... ./internal/...` pass;
`node --check` on 28-ctxmenu.js passes.

## Files

- `cmd/catway/web/js/28-ctxmenu.js` — `{head}` item type in `buildCtx`;
  `wsMenuItems` regrouped and relabelled
- `cmd/catway/web/css/18-ctxmenu.css` — `#ctxmenu .head`
