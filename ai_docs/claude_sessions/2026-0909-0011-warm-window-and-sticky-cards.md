# The warm window, and the cards that would not go

Session: https://claude.ai/code/session_01WJgUMAUWQZGk1h2JEaxxQe
Date: 2026-09-09
Repos: `~/projs/go/cats` (branch `main`) and `~/projs/go/cats-todo` (branch `main`)

## Request

> Look into why cards tend to be sticky when focus is lost. Next build the warm
> window so the next card opens instantly

Continues `2026-0904-1753-a-dwell-before-the-hover-card`, which put a 400ms dwell
in front of both front ends' hover cards and listed the warm window as the one
deliberate non-goal ("one constant to tune if 400ms turns out to be wrong").

## Part 1 — why the cards were sticky

One root cause, six ways in. **Every teardown path the card had was a pointer
event on the row itself** — `mouseleave`, `mousedown`, or a row that stopped
qualifying. That covers the hand moving on and nothing else, so any departure
that does not move the pointer left the card standing, and the card is fixed to
the viewport above everything:

| departure | what happened |
|---|---|
| window blur (⌘-tab, another app, the mac app losing key) | no `mouseleave` is dispatched for a pointer that never moved, so the card was still there on the way back — describing a row whose flag or agent state had moved on without it. A *pending* dwell fired too, opening a card into a backgrounded window. |
| the tab being hidden | the same thing by another route. |
| the hand going back to the keyboard | in catway that means typing into a pane with the pointer parked on the sidebar; the card then sits over the list the keys are moving through. cats-todo already cleared on any keystroke — catway did not. |
| a press on a PANES row or the build badge | WORKSPACES rows hid the card on `mousedown`; **those two never did**. A click on a pane row — the ordinary way to reach a pane from the sidebar — left a card over whatever the click brought up, with the pointer never moving again to take it down. |
| a menu or dialog opening from a key | it came up *underneath* a card nothing was going to clear. |
| a row removed mid-dwell | the lists rebuild on every rollup, and a removed node dispatches no `mouseleave` to cancel with, so the timer fired against a detached node. |

The precedent was already in the tree and unfollowed: `28-ctxmenu.js` has closed
itself on `window blur` since it was written. The card is the same kind of
transient floating surface and simply never got the same promise.

**cats-todo had the same root cause with a different surface.** Focus reporting
was never asked for, so `tea.BlurMsg` could not arrive — a card would be drawn
into every frame of a pane nobody was looking at, and no further motion message
was ever coming to take it down.

### The fix — catway

`09-hovercard.js` now splits the teardown in two:

```
hideTip()   the POINTER's teardown — card down, titles back, dwell dropped,
            and the warm window left standing (the hand is still on the list)
dropTip()   everything else — hideTip, then the warm window down too
```

`dropTip` is wired to `window blur`, `visibilitychange`, capture-phase `keydown`
(before 20-keys.js can `preventDefault` and return early), `openCtx` and
`openOverlay`. A null-`relatedTarget` `mouseout` on the document goes through
`hideTip` — the pointer leaving the window over a row's own edge is the one
departure the row cannot see, and it is still the pointer doing the leaving.
PANES rows and the build badge got the `mousedown → hideTip` the workspace rows
always had, and the deferred show checks `currentTarget.isConnected`.

### The fix — cats-todo

`View.ReportFocus = true` on every stage, and a `tea.BlurMsg` case above the
stage switch that calls `clearHover`. It is set on every stage rather than the
list alone because it is one mode set once, and a background pane should not be
blinking a caret at nobody either.

This rides wire that already existed: catway's `syncAppFocus`
(`cmd/catway/catway.go`) forwards the browser window's focus to **every** pane
whose program enabled DEC 1004, so a blurred catway window now takes the card
down inside a cats-todo pane too.

## Part 2 — the warm window

`TIP_WARM_MS` / `hoverWarm`, both **800ms**, kept in step the way the 400ms
dwell already is. One dwell per row is the right price for a pointer arriving
from elsewhere and the wrong one for a pointer already reading the list.

```
pointer  ──past──▶│ row A │──── rest ────▶│ row B │──▶│ row C │──▶
card               ·       400ms           ███████    ███████
                 (nothing) dwell           opens on   still warm:
                                           the dwell  opens at once
```

Only the pointer keeps it warm. `dropTip` / `clearHover` close it with the card,
so a card never appears on contact when the hand comes back from the keyboard or
another app — that would be the eagerness the dwell exists to fix, arriving by
another door.

Design choices worth keeping:

- **A row with nothing to say neither refreshes nor closes the window.** The
  warmth is measured from the last card actually *read*, so crossing a plain
  workspace row, a group heading or the chrome between the rows and the bar does
  not extend it and does not end it.
- **cats-todo needs one asymmetry.** catway gets a `mouseleave` before the next
  `mouseenter`, so by the time `armTip` asks, the card it is stepping off is
  already gone and only the window remains. In the TUI *one* motion message is
  both the leaving and the arriving, so `hoverIsWarm` also counts a card that is
  open this instant. Same condition either way, reached from two different
  places in the sequence — and it must be read **before** the teardown, since
  taking a card down is what opens the window.
- **`hoverCardFor` was extracted** so the dwell and the warm arrival build the
  card the same way: the row index names a line on the screen, and what is on
  that line is resolved at the moment the card appears rather than carried along
  from wherever the pointer last was.
- `clearHover` (cold) vs `hoverMovedOn` (the pointer's own teardown) is the same
  split as `dropTip` vs `hideTip`, named for the front end it lives on.

## Files touched

**cats**
- `cmd/catway/web/js/09-hovercard.js` — `hideTip` rewritten, `dropTip` added,
  `TIP_WARM_MS` + `tipWarmUntil`, the warm branch in `armTip`, the `isConnected`
  guard, and the five non-pointer teardown listeners.
- `cmd/catway/web/js/08-panelist.js`, `10-buildbadge.js` — `mousedown → hideTip`.
- `cmd/catway/web/js/28-ctxmenu.js`, `25-modal.js` — `dropTip()` on open.
- `cmd/catway/web/jstest/hovercard.test.mjs` — **new**, 18 assertions.

**cats-todo**
- `listhover.go` — `hoverWarm`, `hoverIsWarm`, `hoverMovedOn`, `hoverCardFor`;
  `hoverMotion`'s warm branch; `clearHover` clears the window.
- `ui.go` — `hoverWarmUntil` model field, `case tea.BlurMsg`, `v.ReportFocus`.
- `listhover_test.go` — five new tests.

No CSS, no Go wire change, no protocol change.

## Tests

**cats** — a new `jstest` file, the harness the repo already had and the last
hover session did not use. The card's timing is three module-level variables and
a timer, none of which a screenshot can see: the difference between "opened at
once" and "opened after 400ms" is invisible in a still, and it is the whole
feature. The lifted functions get a clock the test advances by hand and a timer
queue it fires by hand.

Covers: the dwell opens nothing on arrival and opens on the tick; drift keeps one
timer and re-places the card; a row that left the DOM builds nothing; the warm
window carries B and C; it expires; a row with nothing to say neither refreshes
nor closes it; `dropTip` takes the window with the card and disarms a pending
dwell.

Each was negative-checked against a deliberately broken tree —
`TIP_WARM_MS = 0` fails 4, a `dropTip` that stops clearing the window fails 2,
removing the `isConnected` guard fails 1.

**cats-todo** — `TestHoverWarmWindowOpensTheNextCardAtOnce`,
`…SurvivesChrome`, `…ClosesWhenTheHandLeaves`,
`TestHoverCardGoesWhenTheTerminalLosesFocus`, `TestListAsksForFocusReports`.

## Verification

- cats: `go build ./...`, `go test ./cmd/catway/...`, `node --check` per touched
  file **and** on the full concatenated bundle, all seven `jstest` files.
- cats-todo: `gofmt -l .` clean, `go build ./...`, `go vet ./...`,
  `go test ./...`.
- Not driven in a real browser or a real terminal this session.

## Next

- **Drive both cards by hand.** Neither this session nor the dwell session ran
  the feature in a real browser or terminal. The two numbers (400ms dwell,
  800ms warm) are guesses that read well on paper; the hand is what settles
  them. Raised 2 sessions ago, still open.
- **Confirm the DEC 1004 path end to end.** `ReportFocus` reaching cats-todo
  through catway's `syncAppFocus` is reasoned from the code, not observed:
  blur a catway window with a card up in a cats-todo pane and watch it go.
  Newly raised.
- **`paneRuntime.execCmd` is still only set in `createPane`** and never restored
  for an adopted pane, so `clean.go`'s busy test under-reports after a catway
  restart. Raised 4 sessions ago (`2026-0905-1938`), untouched since.
- **`make check` has two pre-existing `internal/inputenc` failures** from an
  untracked `cmd/catgen-dart/testdata/golden/keys.g.dart`. Raised 4 sessions
  ago, still there.
- **A per-front-end warm constant — deliberate non-goal.** The two are kept
  equal on purpose (one feature, two front ends, one timing to learn), same
  bargain the 400ms dwell makes. If they ever need to diverge that is a finding
  about one of the front ends, not a knob to add ahead of it.
- **The trailing rule under the last plugin block — deliberate non-goal.** The
  request said "after each group"; one line in `renderAgents` if it ever reads
  as unfinished.
