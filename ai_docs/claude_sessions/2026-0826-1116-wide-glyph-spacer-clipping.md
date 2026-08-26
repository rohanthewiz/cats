# Session: The Half-Eaten Apple — Wide Glyphs Clipped by Their Own Spacer Cell

> Session: https://claude.ai/code/session_015qW94FKFkXqPVcuF29MvSw
> Date: 2026-08-26
> Repo: cats, on main (uncommitted at doc time; committed with this doc)
> Started in: cats-todo (`~/projs/go/cats-todo`), which turned out to be innocent

## The prompt

> In the list view for the selected row the apple icon is getting truncated

With a screenshot of a cats-todo backlog: two rows carrying the 🍏
low-hanging-fruit annotation. On the unselected row the apple is whole. On the
highlighted row only its left half survives — the right half is the selection
green.

## The finding that framed it

The report named cats-todo, so that is where the hunt started. It was the wrong
repo, and proving that was most of the work.

`bin/cats-todo` was driven under a real pty (a small `pty.fork()` harness at
100×30, arrow-down twice to land the highlight on a fruit row) and the raw
output captured. The selected row goes out as:

```
\x1b[…;48;2;59;82;69;1m❯ \x1b[…;22m○ \x1b[39m🍏 \x1b[…;1madd a record button…\x1b[K\x1b[90G
```

Two things settle it. The apple and its pad space are a single styled segment,
and the trailing `\x1b[90G` is Bubble Tea resyncing the cursor to a column it
computed as `2 + 2 + 3 + 40 + 2 + 40 = 89` — i.e. **the app accounts the emoji
as two cells**, correctly. The rows above and below are byte-identical apart
from the background. cats-todo emits valid, self-consistent ANSI; nothing there
to fix.

So the clipping is downstream, and the only difference on the offending row is
that a non-default background is live across the fruit column.

## The bug

`cmd/catway/web/index.html`, in `draw()` — one pass over the grid, painting each
cell's background and then its glyph:

```js
if (bg !== defBg) { ctx.fillStyle = css(bg); ctx.fillRect(px, py, cellW + 0.5, cellH + 0.5); }
const ch = c.s;
if (ch && ch !== " " && !(m & M_HIDDEN)) { …; ctx.fillText(ch, px, py + 1); }
```

A wide grapheme is anchored in one cell but the canvas draws it at its natural
advance, spilling into the next — which the VT grid leaves as a blank **spacer**
carrying the same background. (`orchestration.resolveCell` turns that spacer's
empty rune into a literal `" "`, and the wire `Cell` — `{s,f,b,m,h}` in
`internal/browserproto/down.go` — has no wide/spacer flag, so the renderer
cannot tell it apart from an ordinary blank.)

Painted cell by cell, the loop drew the apple at column 4, then reached column 5
and filled a `#3b5245` rect straight over its right half:

```
one pass:   [bg][🍏 →][bg over the right half]   ✗
two passes: [bg][bg][🍏 → drawn over both]       ✓
```

Why only the highlighted row: on an unselected row the spacer's `b` is omitted
(it equals `def_bg`), the `bg !== defBg` guard skips the rect entirely, and the
apple survives untouched. The bug is invisible until something colours the row.

## The fix

Split `draw()`'s grid walk into two passes — every background, then every glyph.

Chosen over the alternative (carry a wide/spacer flag from `terminal.Cell`
through `browserproto.Cell` to the renderer, and skip painting spacers): the
two-pass version needs no protocol change, no version bump, and no frontend
knowledge of which cells are spacers. It also covers the rest of the class for
free — italics leaning past their box, a font's ligatures, box-drawing that
overshoots by a hairline. Cost is one extra walk of ≤ a few thousand cells per
frame, against a pass that was already doing the expensive half.

The rect math is unchanged, `+0.5` seam fudge included, so the diff carries no
second decision.

## Verification

No headless canvas here, so the *actual* `draw()` was brace-matched out of
`index.html`, `eval`'d in Node against a recording stub `ctx`, and fed the row
from the pty capture (`❯ ○ 🍏` + spacer + pad + name, all at the selection
background). It records every `fillRect`/`fillText` in order, then asks whether
any rect covering column 5 lands *after* the apple:

- **Before:** exactly one — `x=50, w=10.5, #3b5245`, painted immediately after
  the glyph. That is the green sliver in the screenshot, to the pixel.
- **After:** zero; all 11 background rects precede the apple.

## What landed

- `cmd/catway/web/index.html`: `draw()` split into a background pass and a glyph
  pass, with a comment explaining the spacer and the paint order.
- `cmd/catway/page_test.go`: `TestPagePaintsBackgroundsBeforeGlyphs` +
  `drawFuncSource` helper (brace-matches `draw()` so the assertions cannot be
  satisfied by an unrelated part of the page). Asserts the grid is walked twice,
  the cell-background rect belongs to the first pass, and `fillText` to the
  second. Follows `TestPageForwardsWorkspaceQueryInInit`'s precedent of
  asserting on the page source, for the same stated reason.
- Confirmed the test fails on the pre-fix page (`draw() walks the grid once`)
  and passes after — checked with `git stash push` of `index.html` alone, so the
  test itself stayed present.
- `make fmt-check vet` clean; `go test ./...` green.
- **cats-todo unchanged.** The apple stays.

## Gotchas worth remembering

- `internal/terminal.Cell` has **no wide/spacer flag**, and nothing under
  `internal/terminal` consults a width table at all — no `runewidth`/`uniseg`
  anywhere in the tree. Any renderer downstream of a Snapshot is therefore blind
  to grapheme width. The two-pass fix routes around that; anything that needs to
  *reason* about width (selection, copy, hit-testing a click on an emoji) will
  have to add the flag properly.
- `resolveCell` (`internal/orchestration/protocol.go`) substitutes `" "` for an
  empty rune, so a spacer is indistinguishable from a blank by the time it hits
  the wire. Worth remembering before trusting cell text for anything.
- The wire `Cell.Skip` flag is **diff bookkeeping** ("unchanged since last
  frame"), not ratatui's wide-char skip. Do not overload it for spacers.
- A single-pass canvas grid renderer is a latent clipper for *any* glyph that
  overflows its cell — the emoji is just the loudest case, and it only shows
  itself once a background is involved.
- When a TUI bug is reported against the TUI, capture the pty bytes before
  reading its code. Bubble Tea's absolute-column resyncs (`ESC[NG`) are a free,
  precise statement of what widths the app believes in.
