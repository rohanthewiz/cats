# A dwell before the hover card

Session: https://claude.ai/code/session_01QWp6p8ZabmtUYuNCnpYGZH
Date: 2026-09-04
Repos: `~/projs/go/cats` (branch `main`) and `~/projs/go/cats-todo` (branch `main`)

## Request

> It looks good but I wonder if we could increase the delay before popup a bit
> and apply the same to the cats-todo prompt list

Continues the previous session (`2026-0904-0935-hover-card-mutes-native-tooltips`),
which made the card own the row's native tooltips.

## The problem

There was no delay at all. Both front ends built the card on the *first* pointer
motion the row saw:

- **catway** — `07-workspaces.js` and `08-panelist.js` each bound
  `mouseenter`/`mousemove` straight to `showWorkspaceTip`/`showPaneTip`.
- **cats-todo** — `listhover.go:hoverMotion` built and placed the card inline.

So crossing a sidebar list on the way to somewhere else (the PANES list below it,
a row further down, the scrollbar) opened and tore down a card per row, each one
landing under the pointer and covering the rows still to be crossed. Muting the
native tooltips had removed the second popup; what remained was the first one
being too eager.

## What was built

One number, `400ms`, on both front ends — deliberately the same, since these are
one feature on two front ends and a hand that learns the timing in one should not
have to learn it again in the other.

### catway — `armTip` (09-hovercard.js)

```
armTip(e, show)   // defer show(snapshot) until the pointer has rested TIP_DELAY_MS
cancelTip()       // drop a wait that never earned its card
```

- The event cannot be kept — `currentTarget` is nulled once dispatch ends — so
  the deferred call gets a plain `{clientX, clientY, currentTarget}` snapshot,
  which is exactly the three fields the card builders read (place, build, mute).
- **The clock is not restarted by motion within the row.** The snapshot is
  refreshed so the card lands where the pointer came to rest, but the dwell still
  runs from when the row was entered. Restarting per cell would mean a hand that
  drifts a cell or two while it reads never waits long enough anywhere.
- **The wait is only paid on the way in.** `armTip` checks
  `paneTipEl.classList.contains("show")` and passes straight through while a card
  is up, so it keeps riding the pointer and re-reading live state as before.
- `hideTip()` calls `cancelTip()`, so every existing teardown path (mouseleave,
  mousedown, a row that stopped qualifying) disarms a pending card too.
- Wired at all three `showTip` call sites — workspace rows, pane rows, **and the
  build badge**: one popup element with two different opening rules would read as
  two behaviours in the same corner.

### cats-todo — a dwell tick (listhover.go)

`hoverMotion` no longer builds anything. Arriving on a row arms

```go
type hoverPending struct { armed bool; row int; x, y int; gen uint64 }
```

and returns a one-shot `hoverTick(gen)` (`tea.Tick(hoverDelay, …)` →
`hoverTickMsg{gen}`). The card is built by the new `hoverDwell` when that tick
comes back, handled above the stage switch in `ui.go`.

Design choices worth keeping:

- **`gen` is the cancellation.** A `tea.Cmd` already in flight cannot be recalled,
  so every stale tick has to be recognisable when it lands. Each arming takes the
  next number; a tick carrying any other one does nothing. That covers the pointer
  moving on, the hand returning to the keyboard, and a menu opening in the
  meantime — with no cancellation channel for any of them.
- **`hoverDwell` re-checks everything `hoverMotion` checked**, and re-resolves the
  row index. The wait is time in which any of it can have stopped holding: a menu
  opened, the stage changed, a peer deleted the todo, the list rebuilt. Reading
  the todo there is not merely where the delay forced it — it is the more correct
  place, since the card then describes the row at the moment it appears.
- **`clearHover` disarms as well as hides.** A dwell that survived a keystroke
  would open a card a few hundred ms after the hand had moved on, which is the one
  thing a delay must not introduce.
- Moving to a *different* row takes the current card down immediately rather than
  holding it through the new row's wait: a card naming the row above the pointer
  is worse than no card.

## Files touched

**cats**
- `cmd/catway/web/js/09-hovercard.js` — `TIP_DELAY_MS`, `tipTimer`/`tipArmed`,
  `armTip`, `cancelTip`; `hideTip` cancels.
- `cmd/catway/web/js/07-workspaces.js`, `08-panelist.js`, `10-buildbadge.js` —
  both listeners now go through one `tip` handler wrapping `armTip`.

**cats-todo**
- `listhover.go` — `hoverDelay`, `hoverPending`, `hoverTickMsg`, `hoverTick`,
  `hoverDwell`; `hoverMotion` arms instead of builds; `clearHover` disarms.
- `ui.go` — `hoverPend`/`hoverGen` model fields, `case hoverTickMsg`.
- `listhover_test.go` — helper change plus four new tests.

No CSS, no Go wire change, no protocol change.

## Tests

`hoverOver` now delivers the armed tick directly — the same message `tea.Tick`
would send, without sleeping out 400ms in every test that hovers anything — and
`hoverAt(t, m, x, y)` was split out for the placement test. New:

- `TestHoverCardWaitsForTheDwell` — motion arms, does not open; the tick opens.
- `TestHoverDwellIsAbandonedWhenThePointerMovesOn` — the stale generation is
  dropped without spending the row the pointer is actually on.
- `TestHoverDwellDroppedByTheHand` — a keystroke disarms; a late tick opens nothing.
- `TestHoverDwellSurvivesDriftWithinTheRow` — same generation, no second timer,
  and the pending placement follows the pointer.

## Verification

- cats: `go build ./...`, `go test ./cmd/catway/web/` (ok), `node --check` on each
  touched file **and** on the full concatenated bundle (the files are one closure,
  so per-file checks alone are not enough).
- cats-todo: `go build ./...`, `go vet ./...`, `gofmt -l .` (clean),
  `go test ./...` (ok).
- Not driven in a real browser or a real terminal this session.

## Possible follow-ups

- A "warm" window, as browsers have: while one card is up, the next row's card
  opens instantly. Deliberately not built — both front ends currently make the
  same simple bargain (every entry waits), and it is one constant to tune if 400ms
  turns out to be wrong in the hand.
