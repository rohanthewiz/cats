# Session: Cats-Level Navigation — ⌘[ / ⌘] Back & Forward Through Focus History

> Session: https://claude.ai/code/session_01Lo4qB4VSXyAfk1p6RoqJ6e
> Date: 2026-08-26
> Repo: cats, on main (uncommitted at doc time; committed with this doc)

## The prompt

Add the concept of **Cats-level navigation**: Cmd+[ or the back mouse button
navigates back, Cmd+] or the forward mouse button navigates forward. Clarified
mid-plan: this is navigation **across panes and workspaces** — an editor-style
"go back / go forward" over focus locations, not browser URL history.

## The finding that framed it

The frontend already anticipated this feature. `syncWindowURL` deliberately
uses `replaceState` so workspace switches never pollute browser history, with
a comment saying filling the back button with them "would make ⌘[ mean
something unhelpful." ⌘[ / ⌘] were unclaimed everywhere — the web UI's `onKey`
if-chain and every macOS menu in `cmd/catapp/menu_darwin.m` — so both chords
reach the page cleanly in browser and Cats.app alike. The only prior art was
`pane.last`, a single-slot per-tab toggle (`layout.TileLayout.prev`).

## Design

- **Entry** = `NavLocation{Workspace, Tab, Pane}` — only `Pane` is
  load-bearing for restore (pane ids are session-global;
  `Session.RevealPaneView` reconstitutes workspace + tab + focus from it, the
  `agent.focus` recipe). Workspace/Tab exist only for coalescing comparisons.
- **Per-window stack.** Views are explicitly not session state, so the stack
  lives on catway's `view` struct; the pure type `app.NavHistory` sits in
  `internal/app/nav.go` beside the dispatcher. The dispatcher reaches the
  issuing window's stack through an optional `navHistoryBackend` interface —
  same pattern as `recorderBackend`, so no test fake changed. View-less
  callers (catctl, hooks, runbooks) act on the **primary view's** stack, the
  same rule as `setViewWorkspace(nil, …)`.
- **Recording is a generic post-dispatch snapshot** (`noteNav`), not
  per-command instrumentation: after any dispatch, snapshot the issuing view's
  focus location and offer it with adjacent-dedup. Catches focus moved as a
  side effect (split, pane.close refocus, tab.create, ledger.jump) for free.
- **Coalescing:** `pane.focus_direction` / `pane.cycle` within the same
  workspace+tab *replace* the current entry — a burst of hjkl is one entry
  (vim-jumplist feel). Clicks, tab/workspace switches, agent.focus each push.
- **Stack semantics:** cursor into a capped slice (100). New jump mid-stack
  truncates forward history. Back/forward never record (skipped by name).
  Stale entries (pane since closed) are dropped at walk time and the walk
  continues. Exhausted stack ⇒ silent `OK(nil)`, the `pane.last` convention.
- **Not persisted** — matches `TileLayout.prev` and views generally.

## What landed

- `internal/app/nav.go` (new): `NavLocation`, `NavHistory` (Note/Step),
  `navHistoryBackend` seam.
- `internal/app/command_vocab.go`: `CmdNavBack`/`CmdNavForward` constants +
  `commandSpecs` entries (`Recorded: true`, on the `pane.last` precedent —
  a relative motion whose replay means "do the same motion").
- `internal/app/commands.go`: two dispatch cases → `navigate(back, r)`:
  Step with a "pane still resolvable" validator, then
  `RevealPaneView` → `SetViewWorkspace` → `ApplyModel`.
- `internal/browserproto/cmd.go`: re-export aliases (needed by the wire).
- `cmd/catway/view.go`: `nav *app.NavHistory` on `view`.
- `cmd/catway/nav.go` (new): `navHistoryFor` (nil ⇒ primary view, lazy
  alloc), `(*orch).NavHistory()` for view-less dispatch,
  `viewBackend.NavHistory()` shadowing it (essential — the embedded *orch
  would otherwise answer for the primary view), and `noteNav`.
- Hooks: `handleCmd`, `controlDispatch` (inside `o.post`), runbook step
  dispatch — each calls `noteNav` after Dispatch; `registerConn` seeds entry
  zero ("where the window opened").
- `cmd/catway/web/index.html`: ⌘[ / ⌘] (Ctrl+Alt+[ ] non-mac) in `onKey`
  before the fall-through gate, shift excluded so ⌘⇧[/] stays the browser's;
  capture-phase `mousedown` for buttons 3/4 with preventDefault +
  `auxclick` suppressor; help modal rows (global + mouse sections); the
  `syncWindowURL` comment updated to point at the feature it predicted.
- `cmd/catctl/subcommands.go`: `back` / `forward` verbs beside `last`.
- `docs/protocols/control-api.md`: verb-table rows + prose paragraph.
- Dart golden regenerated (`go run ./cmd/catgen-dart -out …/testdata/golden`).
- `internal/app/record_test.go`: nav.back/forward added to
  `recordedParamClasses` (empty param sets) — the classification gate.

## Tests

- `internal/app/nav_test.go`: dedup, coalesce-replace (and cross-tab push),
  mid-stack truncation, cap eviction, stale-drop both directions, round trip,
  empty-history no-ops; dispatch tests — no-seam backend ⇒ silent OK, effect
  order `[setViewWorkspace applyModel ok]`, closed-pane skip.
- `cmd/catway/nav_test.go` (ghostty tag, on the `twoWorkspaceOrch` /
  `openWindow` harness): back returns across workspaces and forward returns;
  per-window independence (one window's walk doesn't move or drain another's);
  `o.NavHistory()` resolves to the primary window's stack.
- `make check` green end to end. One flaky race-ghostty FAIL on the first run
  did not reproduce; a full `-race ./...` rerun exited 0.

## Gotchas worth remembering

- Adding a §7 command is a **triple** (constant, spec, dispatch case) enforced
  by `TestCommandSpecsRouted` parsing the dispatcher's own source — plus the
  Dart golden regen, `record_test.go` classification, browserproto alias, docs
  table, and optionally a catctl verb.
- `viewBackend` embeds `*orch`, so any optional interface orch satisfies is
  silently inherited — a per-window seam **must** be shadowed on viewBackend
  or every window acts on the primary view.
- Browser back/forward mouse buttons fire `mousedown` (button 3/4) and
  navigate unless preventDefault'd there; pane mouse handling already drops
  `button > 2` so they never reach the PTY.
