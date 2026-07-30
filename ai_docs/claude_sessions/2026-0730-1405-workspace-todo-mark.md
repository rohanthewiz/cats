# Session: A Workspace Wears a String Round Its Finger

- **Session ID:** `a1534ce9-747a-4a50-8429-319f5d31c272`
- **Date:** 2026-07-30
- **Branch:** main (both repos)
- **Repos:** `cats` (sidebar), `cats-todo` (the count it reads)

## Request

> It would be really nice to see on a workspace item in the Workspaces section if
> the workspace has an open cat-todo tab. The icon could be a finger with a string.

Then, after the first pass shipped and the mark turned out to appear on every
workspace: **"Yes take it further. Only indicate if cats-todo has a backlog with
unfinished items"** — followed by "Keep the tab name suffix, build both and let me
test", and finally "commit only our file changes".

## Two passes, and why the second one was needed

The first pass marked a workspace that had a **cats-todo pane open**. It worked,
and it was useless: this session keeps a manager pane in every project, so all six
workspaces lit up. A mark every row wears all day is decoration, not signal. That
was caught by running the detection over the live session's real `pane.list`
before claiming it worked — six of six marked.

The second pass moved the question from *is a manager open* to *does that manager
still have work in it*, which needed cats-todo to say so.

## The channel: the terminal title

`PaneMeta` has no field for a pane's program, plugin, or argv. Three candidates
were on the table:

| channel | verdict |
|---|---|
| `detect.IdentifyAgent` label | wrong vocabulary — it's agent-only, and drags the whole state-detection manifest and the AGENTS section along with it |
| `CATS_PLUGIN_ID` env | unambiguous but **write-only**: set at `tab.create`, never stored on `paneRuntime` or reported in `PaneInfo`. Also absent for a manager started by hand from a shell |
| **OSC title** | already carried for every pane, on-screen or not, and it's the one place a *count* can ride |

The title won on a point neither rival could reach: the plugin id says a manager
exists, but only cats-todo can say how much is left in the backlog — which is the
actual question.

## cats-todo: `model.paneTitle`

`RunContext.paneTitle()` (the name half) is unchanged; a new `model.paneTitle()`
decorates it, because the count needs the stores and the RunContext has none.

- **Nothing appended at zero.** `todo: cats` for an empty or all-done backlog,
  `todo: cats (3)` otherwise. The silence is load-bearing — a `(0)` would put a
  mark back on every workspace with a manager open, which is the bug pass one had.
- **A project pane ignores the global backlog**, even though its list shows it.
  The global list is the same list in every project, so counting it puts the same
  floor under every workspace. A global-only launch (`--global`, or no project)
  counts the global one, since it has nothing else to count.
- **The count rides the frame.** bubbletea v2 declares `WindowTitle` from `View`,
  so every add, edit and toggle refreshes it — no subscription needed.

`TestPaneTitleOpenCount` pins all five cases (empty / all-done / mixed /
project-ignores-global / global-only).

## cats: `todoMark`

```js
function todoOpenCount(t) {
  const m = /^todo(?:: .+?)? \((\d+)\)$/.exec(t || "");
  return m ? Number(m[1]) : 0;
}
```

Sums per workspace over `paneInv`, filtered by handle prefix (`w1:p3` → `w1`);
draws the glyph only on a nonzero total. Two decisions worth keeping:

- **Raw inventory title, not the live pane title.** The live one is the
  *effective* title, so a `pane.rename` would blank the count. `pane.list` reports
  `name` and `title` separately.
- **The inventory, not the layout.** `BuildLayout` carries the active tab's panes
  only, so a layout-sourced mark could never describe a workspace you aren't
  looking at — which is the whole use case.

`refreshPaneList` now calls `renderInventoryViews` (PANES + the Workspaces rows),
so a todo finished in one place reaches the row above it. `applyLayout`'s separate
`renderWorkspaces` call went away as redundant.

## The icon

Five rounds of headless-Chrome renders, judged at real sidebar size (16px beside
13.8px text) rather than large:

- Open-bottom capsule + two straight string wraps → reads as an **arch with
  ladder rungs**, or an "A".
- Straight wraps at any size → rungs. Curved wraps → still rungs.
- **What worked:** a closed capsule finger, the string as an *ellipse* (so it
  reads as passing behind), and a loose end hanging off the knot. Three strokes;
  anything more dissolves at 16px.

Inline SVG built with `createElementNS`, `currentColor` at `--heading` yellow,
sized off `--sb-scale` like everything else in the column. Count lives in the
hover title, not on the row — the row already carries `●1 ●2` on its right edge.

## Accepted side effect: the tab name

Tab auto-naming's rung 2 is the OSC title (`tabname.go:64`), so a todo tab now
reads `todo: cats (3)` and ticks as the backlog is worked. Raised as an unrequested
consequence; the user chose to keep it.

## Verification (each claim was checked, not assumed)

- Parser over 12 cases: `todo (2)`, `todo: cats (0)`, a project literally named
  `my (proj) (5)`, and the live session's agent titles containing the word "todo".
- Detection replayed against the **real** `pane.list` from the running session,
  with counts spliced onto two panes: `wD` and `wF` mark, the other four go dark.
- The installed manager binary run under a pty in a throwaway project (2 open,
  1 done) → it emits `ESC ] 2 ; todo: proj (2)`.
- `make macapp` + `go build` for the plugin; `grep -a todoOpenCount` confirms the
  new JS is embedded in both `/Applications/Cats.app` and `~/bin/catway`.

## Committing around another agent's work

cats-todo's tree already held an unrelated in-flight change (clickable drop-target
rows: `fuzzylist.go`, `updateMouse`/`clickTarget`, two test hunks) in the **same
two files**. `git add -p` is not available here, so hunks were selected
programmatically — keep `ui.go` hunk 2 and `ui_test.go` hunk 2, drop the rest —
and applied with `git apply --cached`.

Before staging, the extracted patch was applied to a **throwaway worktree at
HEAD** and built and tested there, so the commit is green on its own rather than
merely green sitting on top of the other work. Line counts reconcile: 87 committed
+ 211 left in the tree = the 298 that were there.

## Commits

- `cats-todo 47bdf8f` — feat(todo): the pane title counts what is left in the backlog
- `cats 5579a98` — feat(ui): a workspace wears a string round its finger when todos are left

## Loose ends

- **The mark needs a manager open.** It reports what a running cats-todo says, not
  what's on disk; a workspace with unfinished todos and no manager stays dark.
  Reading `.cats-todo/todos.json` per workspace cwd would fix it — server-side
  feature, different size.
- **Off-screen freshness.** `pane_title` is pushed for visible panes only, so a
  manager in a background workspace updates on the next layout or agents-rollup
  push. Same contract PANES has always lived under; no server change made.
- **The installed plugin diverged.** `~/.config/cats/plugins/rohanthewiz.cats-todo`
  is an *installed* clone (not linked); its binary was updated for testing but its
  source sat at the old commit until this push.
