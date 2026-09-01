# Session: The Running Runbook's Dead Click

- **Session ID:** `9a9e5279-2217-47aa-a9cd-8164a4f7c954`
- **Date:** 2026-09-01
- **Branch:** main
- **Repo:** `cats`
- **Follows:** `2026-0901-0036-the-running-runbooks-palette-verb.md`, whose *Known
  limits* opened with "the sidebar row's click is the same dead verb… the
  identical shape and the identical fix". Sixth session running that closes the
  previous one's first lever.

## Requests

> fix the sidebar row's dead click on a running runbook

then, after the first change shipped with the menu flagged as out of scope:

> fix the menu's run… entry too

## The problem, precisely

`startRunbookRun` refuses a second run of the same name before sending it — the
server's concurrency slot is per runbook name, so the round trip would only come
back "already in flight". Right for the command, wrong for everything pointed at
it. Three surfaces pointed at it:

| surface | while a run is in flight |
|---|---|
| palette entry | fixed last session — becomes `preview runbook: …` |
| **sidebar row's click** | toast, and nothing else |
| **right-click `run…`** | toast, and nothing else |

Both of the remaining two are the shape the file's own comment forbids: a
surface that offers a verb it knows will be refused is one you stop trusting.

## The design decisions

### One function decides, two sites read it

`runbookRowVerb(rb, broken, run)` → `"open" | "preview" | "run"`.

It exists as a function rather than a condition inside the click handler because
the TOOLTIP has to name the same verb. A row that promises *click to run* and
then opens a preview is worse than either behaviour on its own — and two sites
drifting apart is exactly what `runbookHasPreview` was pulled out to stop
between the tooltip and the menu. Third time this shape has paid: `runbookCount`
→ `runbookRunNote`, `runbookHasPreview`, now this.

```
broken  → "open"     the file never parsed; the editor is the only verb
running → "preview"  the run verb is spoken for, and preview is what is
                     being asked for once panes appear by themselves
else    → "run"
```

### The residual case falls through to the toast, on purpose

A running runbook whose listing carries no `outline` (a server too old to send
one) has no preview to route to. It returns `"run"` and lands on
`startRunbookRun`'s refusal — which names *who* started the run, and on a row
with nothing else left to give that beats silence.

This is where the row and the palette part company, and the reason is worth
keeping: the palette **drops** such an entry, because an entry in a list can be
absent. A row cannot. The row is the thing reporting the run.

### The verb is read at CLICK time

`renderRunbooks` re-runs on every change to the run set, so the `run` captured
at render is normally still true when the click lands. Read fresh anyway:
routing off a snapshot is correct only for as long as that stays true, and
`startRunbookRun`'s refusal is meant to be the backstop, not the thing standing
between a stale row and a doomed command. Same for the menu, which is built at
the moment it opens and so can be built against what is true then.

### The menu: absence, not a disabled entry

`runbookMenuItems(rb, broken, run)` gates `run…` on `!broken && !run`. The
running menu therefore leads with *preview steps* — the verb the click now takes
— so the two agree.

Absence rather than greying out: `openCtx` has no disabled state, and inventing
one for a single entry is new shared machinery for a case that absence already
states. It is also the palette's precedent.

Running **and** outline-less leaves *open in editor · copy path · copy catctl
command*, which is precisely what that row's tooltip (`right-click for more`)
points at, so the menu is still worth opening.

### The tooltip moved with the verb

| state | last line, before | after |
|---|---|---|
| running, has outline | `right-click to preview the steps` | `click to preview the steps · right-click for more` |
| running, no outline | *(nothing)* | `right-click for more` |
| idle / broken | unchanged | unchanged |

The second row is a small gain that came free: it used to promise nothing at all.

## What shipped

- `cmd/catway/web/js/41-runbooks.js` — `runbookRowVerb`; the click handler's
  three-arm switch; the tooltip's running branch; `runbookMenuItems` takes the
  run; both handlers read `runbookRunOf` fresh; `previewRunbook`'s now-stale
  comment about the refused click.
- `cmd/catway/web/js/31-palette.js` — one stale phrase ("refuses the click").
- `cmd/catway/web/jstest/runbook-row.test.mjs` — **new suite**, 30 assertions.
- `cmd/catway/web/jstest/bundle.test.mjs` — `runbookRowVerb` added to the
  declared-once list.
- `docs/protocols/control-api.md` — the running row's click and menu.

No Go, no CSS.

## Checks

- `make check` — exit 0 end to end, both times.
- **jstest: 7 bundle + 41 palette + 30 row.**

New assertions worth naming:

- The routing table entire, including *broken beats running* — a file that never
  parsed has nothing to preview either way.
- The tooltip's `lacks("click to run")` on a running row, which is the assertion
  that would have caught the old behaviour.
- A guard that `runbookRowVerb` only ever returns one of the three the handler
  knows: its `switch` has `startRunbookRun` on the default arm, so a fourth
  value invented later would silently start a runbook.
- The menu asserted as a **full label list** per state (idle, running,
  running-without-outline, broken, and idle again after the run ends) rather
  than as presence checks, plus pressing the entries — pressing is what tells a
  reordering apart from a relabelling.

## Notes for next time

- Still unverified on screen — six sessions. Chrome will not reach localhost from
  the extension; the demo-instance recipe (port 8520, `/tmp/cats-demo.sock`,
  fixtures under the scratchpad) still stands and still has not been run.
- The `jstest` harness took a **third** suite with no changes. `runbookTitle`
  and `runbookMenuItems` lifted cleanly with two spies and two stubs, which is
  the first time the harness has been pointed at something that builds DOM-bound
  data rather than pure strings.

## Known limits / next levers

- **The dead-verb sweep is finished for runbooks.** All three surfaces (palette,
  row click, row menu) now change verb while a run is in flight. Worth asking
  whether any OTHER refuse-before-send command has the same shape — the recorder
  was the original precedent and is already right.
- **No surface re-renders while the palette is open.** Unchanged and now the
  oldest item on this list: a run starting or ending behind an open palette
  leaves the old verb until it is reopened. `recState.recording` has always had
  this, so it is surface-wide rather than a runbook question.
- **`expect` and `continue_on_error` remain invisible.** Five sessions now. Out
  of the outline; the preview notice is still the likelier home than the gate.
- **The outline's shaping is still spread** across `runbookLead`,
  `runbookOutlineText` and the callers' own field assignment. Two sessions have
  now said a reader should ask whether `runbookOutline` wants to own more of it,
  and neither asked. This session added no new reader, so the question is not
  yet more urgent than it was.
