# Session: the mark grows, and the face gets its latitudes

- **Session ID:** `15c42974-262a-4a68-aefa-ba72b9a96baf`
- **Date:** 2026-08-06
- **Branch:** main (`1fce293` → this commit)
- **Repos:** `cats`

Immediately after `1fce293`, which put a cat's head left of the wordmark.
That commit reasoned its way to a 45-degree yaw and then placed the
features by eye against that reasoning. Drawn at 16px the gap did not
show. Asked to grow the mark, it did.

## Request

> please make the cats logo a tad bigger and show me how it looks on an
> artifact

Then, against the rendered result, twice:

> The eyes are not lining up on the same axis
> Imagine a lattitude line when the earth is tilted. Both eyes should
> line up tip-to-tip on the lattitude, no matter what!
> Now line up the nose on a parallel latitude

Four exchanges, and only the first was about size. The other three are
the size-up doing its real work: **a mark that is legible is a mark whose
errors are legible.** Nothing about the geometry changed between 16px and
19px; what changed is that it could be seen.

## The size, and the number that had to follow it

```
width/height    16px -> 19px
vertical-align   -3px -> -3.5px
```

Both still multiply by `--sb-scale`, so the column keeps scaling as one
piece. The second value is not a taste adjustment — it is determined:

The head's ink ends at y=19.75 on the 24-unit grid, so the box carries
`(24 - 19.75)/24` = 17.7% of its own height as empty space beneath the
chin. The nudge cancels exactly that gap, which is what parks the chin on
the text baseline. At 19px it is `19 * 0.177 ~= 3.4`, hence -3.5.

That derivation is now a comment above the rule. It was not before, which
is why the pairing looked like two magic numbers rather than one number
and its consequence. Resize the box without re-deriving the nudge and the
mark floats.

Known cost: at 19px against 13px type the mark outgrows the line box, so
the brand row is ~3px taller at scale 1. Accepted — the row is a header,
not a dense list.

## The eye latitude

Three attempts, and the middle one was wrong. Worth recording as such,
because the wrong one was wrong in an instructive way.

**The complaint** was that the eyes did not share an axis. **The first
fix** read that as *the eyes must share a vertical center* — a yaw is a
rotation about a vertical axis, y is preserved, therefore both almonds
should center on the same y. That reasoning is sound and the conclusion
was still wrong, which is the interesting part.

It moved the near eye *down* 0.4 to y-center 11.9. The rendered result
was worse.

**The correction** came from the user's framing, which is the better
model: a latitude drawn around a head stays a straight line however the
head is turned or tilted. Not "the same height" — *the same line*, with
every tip on it.

```
y = 12.2 - 0.1875 * (x - 11.5)        the eye latitude

  x = 11.5 -> 12.2      far, inner    (unchanged)
  x = 14.7 -> 11.6      far, outer    (unchanged)
  x = 15.5 -> 11.45     near, inner   (was 11.8  — 0.35 low)
  x = 17.5 -> 11.08     near, outer   (was 11.2  — 0.12 low)
```

So the original was already below the line, and the first fix pushed it
0.4 further. The right move was **up**.

Why "same center y" is the wrong invariant: it constrains one point per
eye and leaves the slope free, so two eyes can share a center and still
sit on two different lines — which is exactly what the first fix
produced. Collinearity of all four tips constrains both, and is what the
eye actually reads.

The far eye is untouched throughout, so the latitude is the one the mark
already had rather than a new one imposed on it.

### What foreshortening is allowed to do

Only act **along** the line. The near almond is squeezed to 2.0 wide
against the far one's 3.2, so it covers less ground and rises less in
absolute terms while tracking the same slope.

Height is not squeezed — the yaw is about a vertical axis. Both almonds
therefore keep the same ±1.5 control offset, and the near one reads
rounder purely for being narrower. (It previously carried ±1.25, a small
inconsistency that the same principle removes.)

## The nose latitude

Its top edge ran from (14.4, 13.9) to (16.0, 13.9) — dead level, on a
face whose eyes ran at -0.1875. It read as a second coordinate system
sitting on the first.

```
eyes   y = 12.2 - 0.1875 * (x - 11.5)
nose   y = 14.6 - 0.1875 * (x - 11.5)      same slope, +2.4
```

The edge is rotated **about its own midpoint**, so it keeps its 1.6
length and the nose neither rises nor sinks on the face:

```
was  M14.4  13.9  L16.0  13.9  L15.2  15.1  Z
now  M14.41 14.05 L15.99 13.76 L15.42 15.09 Z
```

### The apex moves too

The part that is easy to miss. A slope of -0.1875 is a roll of
`atan(0.1875)` ~= 10.6 degrees, and **a roll turns the face's own
vertical with it**. So the apex's 1.2-unit drop is taken along the normal
`(0.1843, 0.9829)`, not straight down the page — which carries it 0.22
right of the edge's midpoint, to (15.42, 15.09).

```
    ___                    ___
    \ /  <- along normal   \ /   <- straight down: leans off the muzzle
     v                      v
```

Dropping it straight down would have left a nose pointing somewhere the
face is not.

## Everything is now derived, not placed

The three edits share one property worth stating plainly: each replaced a
hand-placed number with a rule that generates it.

| feature | was | now |
|---|---|---|
| baseline nudge | a number that looked right | the box's own empty space below the chin |
| near eye | its own slant, its own height | tips on the eye latitude, ±1.5 like the far one |
| nose edge | level | the eye latitude, offset 2.4 |
| nose apex | straight down | along the latitude's normal |

Every one carries its derivation in the SVG comment. The next retouch
gets constraints to work against instead of a coordinate to nudge.

## The artifact

Published and revised in place across all four exchanges (same URL, four
labelled versions: initial, `eye-axis-fix`, `eye-latitude`,
`nose-latitude`).

Three sections:

- **In place** — two full sidebar columns, before and after, carrying the
  app's real tokens and the real path data, with a live `--sb-scale`
  slider driving both. Built to check the 150px floor case, where the
  build hash is what ellipses away.
- **Chin on the baseline** — the mark at 5×, with the text baseline
  drawn. The rule is an empty inline-block with `height:0` and
  `margin-right:-100%`: its baseline is its bottom margin edge, so
  `vertical-align:baseline` lands the border on the baseline itself, and
  the negative margin cancels its width so the run draws over it rather
  than being pushed off the line.
- **Two parallel latitudes** — both heads at 210px with both lines drawn
  through them, and the six governed points marked. Red where a point
  misses its line, so collinearity is checked rather than asserted.

The specimen colors are the app's own tokens and deliberately do **not**
follow the viewer's theme — a mockup that recolored itself would be
lying about what the sidebar looks like. The page chrome around them does
follow it.

The mark is defined once as an SVG `<symbol>` and instanced everywhere,
so no specimen can drift from the committed path data. The two heads in
the latitude figure are the exception: they inline their paths, because
one of them has to be the *old* geometry.

## Verification

- **Rendered, not just read.** Every geometry decision was checked
  against the published artifact at 210px, and the user's three
  corrections all came from looking at that render. Which is the point:
  the errors were arithmetically invisible and visually obvious.
- **Clearances checked by hand.** The near eye's outer tip at x=17.5
  against the contour's inner edge near 17.95 (~0.25, tight but no
  tighter than before); its lower extreme at ~12.0 against the nose edge
  at 13.76.
- **No test touches this.** It is one static SVG in a template.

## Pushed

| Commit | Change |
|---|---|
| (this) | `feat(sidebar): the mark grows, and the face gets its latitudes` |

## Open

- **Not seen in the MacApp.** `make macapp` still not run — carried
  forward from every session in this thread. Everything here was verified
  in a published artifact against the real path data, which is closer
  than reading, and still not the installed app. The one thing that
  needs a real look is the ~3px taller brand row.
- **The eye latitude's slope is inherited, not chosen.** -0.1875 comes
  from the far eye as originally drawn. It happens to imply a 10.6 degree
  roll, which is now load-bearing for the nose apex. Nobody decided the
  head should be tilted 10.6 degrees; it fell out of a hand-placed
  almond. If the tilt is ever unwanted, both latitudes and the apex
  normal move together.
- **The ears were not checked against any of this.** They were placed by
  the same eye that placed the eyes and nose, in `1fce293`. The yaw
  reasoning covers their x positions; nothing has verified their y.
- **No `--sb-scale` sweep in the real app.** The artifact's slider
  exercises the mockup, not the sidebar. The floor case (150px) was
  reasoned about and rendered, not dragged.
