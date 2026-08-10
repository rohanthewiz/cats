# Session: the left column folds away, and an agent brings it back

- **Session ID:** `0fa432b3-af53-4c2c-8b5b-a2b4439fb97a`
- **Date:** 2026-08-09
- **Branch:** main (`a1e1f3b` → `26001fa`)
- **Repos:** `cats`, `cats-mobile` (`6d774a3` → `054ba10`)

Sixth session in a row on the sidebar, but the first one about the sidebar as a
whole rather than about a section inside it. The previous five all made the left
column say *more* — usage headlines, host CPU, a fold horizon, an amber
countdown. This one accepts that a column worth reading is still a column worth
getting rid of sometimes, and gives it a way out that does not cost anything
while it is gone.

> Give me the ability to temporarily hide the complete left aside, but pop it
> back open automatically when an agent is blocked or done. Also try not to take
> any vertical space just for a handle

Three requirements, and the third is the one that shapes the design.

## The handle constraint decides the whole thing

The obvious build is a thin strip along the top of `#main` with a `»` in it.
That is also the one build the ask forbids, and for a good reason: `#panes` is
laid out from `cols`/`rows` derived from its own client height, so a strip
across the top does not overlay anything — it *takes a terminal row from every
pane in the window*, permanently, in exchange for a control used twice a day.

The way out was already in the layout. `#app` is a four-track grid:

```
grid-template-columns: var(--sidebar-w) 5px 1fr var(--chat-w)
                       └ sidebar ─┘    └ splitter
```

The splitter is a full-height strip in its own grid column, already there,
already the thing your hand goes to when it wants to change how much room the
sidebar takes. Folded, it simply stops being invisible and starts taking a
click. Vertical cost: zero. Horizontal cost: the 5px that were already spent.

Fitts's law does the rest of the work — with the column gone the gutter sits
against the window's left edge, so it is a 5px target only in the arithmetic
sense. You can throw the pointer at the edge of the screen and land on it.

## Folding: a body-class re-declaration, not a width

`setSidebarWidth` writes `--sidebar-w` **inline on `:root`**. That is the
persisted, user-dragged width, and the reveal has to land back on it — folding
must not overwrite it. So the fold is a second declaration one level down:

```css
body.sb-hidden { --sidebar-w:0px; }
body.sb-hidden #sidebar { display:none; }
```

Custom properties inherit, and a declaration *on* `body` beats the value `body`
inherited from `:root` for everything inside it. The inline root value stays
exactly where it was, untouched, waiting.

Verified rather than assumed, in headless Chrome against a reduction of the real
rules — with `--sidebar-w:300px` set inline on the root:

```
shown:   300px 5px  895px 0px   | banner.left=300px
hidden:    0px 5px 1195px 0px   | banner.left=0px
```

`#banner` is the second consumer of the variable and lives outside `#app`
entirely, as a child of `body` — which is exactly why it follows for free. Any
future thing that has to clear the sidebar gets the same deal by reading the
same variable, which was the point of centralising it in the first place.

`display:none` on `#sidebar` rather than letting it be a zero-width track: the
content would otherwise paint straight through the collapsed column. Nothing
measures sidebar geometry from JS (checked: no `getBoundingClientRect` or
`scrollIntoView` against `#ws-list`/`#pane-list`/`#agent-list`/`#usage-list`),
so taking it out of the flow costs nothing. The lists keep rendering into hidden
DOM, which is what makes the reveal instant.

## Four ways in, and the drag is the one that teaches

- **⌘B / Ctrl+Alt+B** — deliberately the same modifier pair as ⌘K's palette,
  matched on `e.code` like every other binding here, so a non-QWERTY layout gets
  the physical key. The two window-chrome toggles are then learned once.
- **⌘K → "hide sidebar"** — label flips to "show sidebar" when folded, since
  `paletteCommands()` is rebuilt per open.
- **Drag the gutter left** past ~82px (`SBW_MIN * 0.55`).
- **Click the folded gutter** — the only thing a click there can mean.

The drag is worth its own note. The fold happens **live, under the pointer**, and
dragging back out past the same mark brings the column straight back. The
alternative — decide on release — is easier to write and much worse to use,
because nothing tells you the release is about to do something different from
every other release of this gutter. As a live preview the gesture explains
itself: *the column you dragged into nothing is the column that stays gone.*

Two collisions came out of putting a click and a drag on the same 5px:

- A press that never moved more than 3px is a click, so the reveal is decided in
  `up` from a `moved` flag rather than from the final pointer position — which
  would otherwise be indistinguishable from "dragged left and released".
- The gutter's `dblclick` resets the width to the stylesheet default. Reveal on
  the first click means the second click of a double lands on a *shown* sidebar
  and resets it — one gesture that both restores and re-defaults the column.
  Guarded with a `revealedAt` timestamp and a 600ms window.

## Coming back: edge-triggered, per pane, seeded silently

`agentAttentionSweep(items)` runs from `renderAgents`, on every rollup. It keeps
a `Map` of pane → `markerState`, and fires only on a **transition into**
`blocked` or `done` (idle + unseen — the teal tier).

Three properties, each of which is the fix for a specific way the naive version
is wrong:

1. **Edge-triggered, not level-triggered.** A rollup where an agent is merely
   *still* blocked must not re-open a column the user has folded away since. The
   user folding it back is a decision; only a fresh transition overrides it.
2. **The first rollup seeds the map without firing.** `agentAttn` starts `null`.
   Without this, a page load with three finished-but-unseen agents reads as three
   brand-new transitions, and a hidden sidebar could never survive a reload.
3. **The map outlives a dropped socket.** It is not reset in `connect()`, so the
   rollup that arrives after a reconnect re-states states the user has already
   seen and correctly fires nothing.

Exercised as a standalone harness before wiring it up — load with an
already-done agent, the same rollup twice, a real block, a fold-away while still
blocked, an answer-then-finish, and a new agent that appears already blocked:

```
no-pop · no-pop · REVEAL 2 blocked · no-pop · no-pop · REVEAL 2 done · REVEAL 3 blocked
```

The reveal carries a toast naming the agent and why — a column that comes back
on its own is a surprise otherwise, and the row that caused it is about to be
one of several in AGENTS.

## The gap the edge trigger cannot cover

Fold the column away *while* an agent is already blocked and no transition is
coming, by construction. So the folded gutter carries the strongest state
outstanding: `--err` for blocked, `--done` teal for done.

`working` deliberately gets no mark. It is the ordinary case, and a permanently
lit gutter would hide the two states that are actually addressed to the user —
the same reasoning that keeps `attentionRank`'s top two tiers separate from the
bottom two everywhere else in this sidebar. The attention rules are ordered
*ahead* of `:hover` in the stylesheet, at equal specificity, so pointing at the
gutter still answers.

## Verification

`go test ./... -count=1` clean; the inline script parsed through `vm.Script`
(4671 lines, no syntax slips); the CSS cascade claim confirmed in headless
Chrome as above; the sweep logic exercised as a table before it went in.

Not verified in the running app — the change is `go:embed`ed into catway, so it
needs a `make macapp` to reach the installed build.

## cats-mobile

Second ask: update the phone client with its own tool, and write the flow down.

`tool/regen.sh`, run from the cats-mobile root with no arguments (it defaults to
`../cats`). It runs `go run ./cmd/catgen-dart` into
`packages/catsproto/lib/src/generated/` and re-pins `CATS_REV` to cats's
`HEAD` — which is why cats has to be committed and pushed **first**, or the pin
names a commit nobody else can resolve.

The four generated files came out **byte-identical**; only `CATS_REV` moved
(`25415a2` → `26001fa`). Correct and expected: only wire types and the command
table reach Dart, and the two cats commits since the old pin were a session doc
and a web-UI change. Committed anyway — the pin is what cats-mobile's CI
regenerates against and diffs, so a stale one means the drift gate is validating
an older cats than the one shipping, and that failure stays silent until someone
actually changes the wire types.

`dart test` 72 passing, `dart analyze` clean. Two gotchas the script itself
flags and the memory now records: `FLUTTER_ROOT` is unset here, so `keys.g.dart`
is left as committed (its input is a Flutter SDK data file and only moves on an
SDK upgrade — not a warning to chase); and the generated files carry
`// dart format off` so they stay byte-identical to cats's golden under
`cmd/catgen-dart/testdata/golden`, so never reformat them.

## Files

- `cmd/catway/web/index.html` — the fold CSS beside the splitter rules;
  `setSidebarHidden`/`sidebarHidden`/`paintSplitterAttention`/`syncSplitterTitle`
  and the rewritten pointer handlers in the splitter section;
  `agentAttentionSweep` above `renderAgents`; the ⌘B branch in `onKey`; the
  palette entry; three lines in the keyboard/mouse reference.
- `~/.claude/projects/…/memory/cats-mobile-regen-flow.md` — the regen flow.

## Still open

- `make macapp` to get this into the installed build.
