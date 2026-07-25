# cats-todo project scoping — walk up to the project root, root drops there too

- **Session ID:** `eabb3bbe-e29b-41dc-81d8-ac3624330f27`
- **Date:** 2026-07-25 09:23
- **Branch:** `main`
- **Scope:** `cmd/cats-todo/{context,cli,store,client,drop,ui}.go`,
  `cmd/cats-todo/{context,store}_test.go`, `README.md`

## Request

Opened as a question — "cats-todo keeps todos per project; how do I actually
start it in another project, or does it assume the directory of the left
adjacent pane?" — then turned into two fixes that fell out of the answer.

## The answer (no pane inspection anywhere)

cats-todo scopes purely off its own process cwd. `gatherRunContext` calls
`os.Getwd()`; the control socket is used only for workspace label, pane
identity, and drops — never for the directory. So another project is just
`cd <proj> && cats-todo`, or `catctl plugin run rohanthewiz.cats-todo` from
that directory (catctl forwards its own cwd as the tab's spawn cwd).

Answering surfaced two real gaps, both fixed below.

## Fix 1 — new-session drops ignored the project directory

`client.tabCreate()` sent `tab.create` with nil params, so a drop into a fresh
tab spawned in the *workspace's identity cwd*, not the project the todo came
from. Where those differ, the agent opened on the wrong tree.

- `tabCreate(cwd string)` now sends `app.TabCreateParams{Cwd: cwd}`. An empty
  cwd still sends nil params, preserving the historical no-params wire shape
  and the server default for any caller with no directory to pin.
- `pendingAction` gained a `cwd` field, filled at dispatch in `ui.go`. Captured
  at dispatch rather than read inside `performDrop` because `performDropCmd`
  already snapshots everything the drop goroutine needs — that goroutine holds
  no model reference.
- Existing-pane drops are untouched: they inherit that pane's own directory,
  which is already correct.

## Fix 2 — the TUI didn't walk up, but `add` did

`cats-todo add` resolved its backlog via `findProjectRoot` (nearest
`.cats-todo`, then nearest `.git`); the TUI used the raw cwd. Running `add`
from `internal/app/` wrote to the repo root's backlog while opening the manager
from that same directory showed an empty list pointing at
`internal/app/.cats-todo/`. One project, two backlogs, depending on entry point.

- `findProjectRoot` moved `cli.go` → `context.go`, now shared by both entry
  points rather than living in the CLI file.
- `RunContext` gained `ProjectRoot`, resolved in `gatherRunContext` by walking
  up from the cwd. `WorkDir` is deliberately **not** overwritten — the pane's
  actual location stays available — and a `projectDir()` method returns
  `ProjectRoot` with a `WorkDir` fallback, so a context built from a bare
  directory (tests, any future caller) still scopes sensibly.
- `loadStores`, both `baseName(...)` display labels, and the drop cwd all moved
  to `ctx.projectDir()`.

### Walk order, now documented in the code

The existing-backlog pass runs to completion **before** the `.git` pass. So a
backlog already living in a subdirectory keeps winning over an enclosing repo
root, instead of silently starting a second one at the top.

### Knock-on to fix 1

A new session now opens at the project root rather than the pane's
subdirectory. That follows fix 1's own reasoning (the tab should root where the
todo is scoped), but it is a behavior choice worth remembering — flagged to the
user as reversible if they'd rather the tab track the pane's literal cwd.

## Tests

- `TestFindProjectRoot` — backlog beats an enclosing repo root, `.git`
  fallback, no-marker self-root.
- `TestRunContextProjectDir` — fallback order incl. the empty case.
- New `gatherRunContext` subtest: the root must land on an *ancestor* of the
  cwd, never the cwd itself (the test file lives in `cmd/cats-todo`, so the
  subdirectory case is the real thing, not a fixture).
- `TestLoadStores` gained the subdirectory launch: `WorkDir=<root>/sub`,
  `ProjectRoot=<root>` must load `<root>`'s backlog.

`go build ./...`, `go vet ./cmd/cats-todo/`, and the full `go test ./...` are
clean.

**Not covered:** that `pendingAction.cwd` actually reaches the wire. Drop tests
assert on `dropResultMsg`, which doesn't carry cwd, and `catsClient` is a
concrete type with no interface seam — covering it needs either a fake control
socket or an interface introduced solely for the test. Left undone by choice.

## Also in this commit

- `README.md`: the cats-todo section never stated the scoping rule; it now
  documents the walk and that a fresh tab roots there too.
- `README.md`: fixed a pre-existing uncommitted typo, "the model is sqaved to"
  → "saved" (present in the working tree before this session; not ours).
- `.cats-todo/todos.json`: this repo's own backlog, created while using the
  tool. Project todos are designed to travel with the repo, so it is committed
  rather than ignored.

## Follow-ups

- Live pass in a real cats pane: open the manager from a subdirectory, confirm
  it shows the root backlog, and drop into a new session to confirm the tab's
  cwd. Everything above is verified by tests and code reading only.
- If the drop-tab cwd should track the pane instead of the project root, it is
  a one-line change at the `pendingAction{...}` construction in `ui.go`.
