# Session: the logo stops being drawn and starts being traced

- **Session ID:** `5303d269-7ee0-4cab-ae85-dfc7b493b04d`
- **Date:** 2026-08-09 → 2026-08-10
- **Branch:** main (`92aa955` → this commit)
- **Repos:** `cats`

A logo session that took four rounds to find out it was the wrong kind of work.
The ask was a cat-face mark to replace the terminal-chevron app icon. Three
rounds went into *drawing* one; the fourth threw all of that away and traced a
reference image pixel by pixel. The lasting artefacts are the tracer and the
two-drawing icon family, not the drawings I made along the way.

> I need a better logo. I am looking for a cat face outline. […] Perhaps a
> spacey (outer-space) look. Give me 10 options if possible. I am not looking
> biological accuracy, rather symboligy

## Three rounds of drawing, and what each one got wrong

Round one was hand-placed Bézier handles and looked, accurately, "like they were
drawn by kids." Round two replaced eyeballed handles with constructed geometry —
heads as true circles, ear edges as computed tangents to them, eyes as exact
circular arcs — which fixed the wobble and not the problem. Round three finally
identified the real fault as **anatomy, not rendering**: tall triangle ears
sitting on top of the skull, smiling closed eyes, radiating whisker dashes. Big
cats have small rounded ears set wide and low, a heavy brow, a broad blunt
muzzle.

The mechanism that made round three tractable is worth keeping: generate the SVG
from a Go program, render it locally with `qlmanage`, and **look at it** before
showing anyone. Every round produced marks I would have shipped and shouldn't
have — a mane that read as a sunburst, a profile that read as a fish, a
full-body cat that read unmistakably as a dog. None of that is visible from the
path data. The failure list is now recorded in the generator's own comments so
it does not get retried:

- **Side profiles** failed three separate constructions. A short muzzle on a
  round skull reads as a fish; lengthening it reads as a bird; the ear is a
  shark fin whether its tip is a corner or a curve.
- **Full body** reads as a dog regardless of the head, because at mark size the
  giveaway is leg thickness and back line, not the face.
- **Manes**: every lobed ring read as a sunburst. Lobe *shape* is irrelevant — a
  ring of repeated radial points **is** a sunburst. What eventually worked was a
  smooth mass with the fur supplied by separate strokes inside the band.
- A wide muzzle stroke floating under a nose is a moustache. Always.

## The turn

Round three's board went out as ten named marks. It came back rendered by
Gemini at a far higher level of detail — same names, same numbering, real
artwork — with:

> These are much closer to what I was looking for. Make SVGs of 5, 6, and 10

I read that as "in this spirit" and redrew them by hand again. Wrong:

> These look absolutely nothing like the image I posted! Do a pixel match […] I
> just want an SVG of the given image verbatim.

That was the correct correction. "Verbatim" is a vectorisation task, and I had
been treating it as an illustration task.

## The tracer

No `potrace`, no `autotrace`, no ImageMagick, no PIL on this machine — but Go's
standard library decodes PNG, so `scripts/icon/gen-trace` is a vectoriser
written from scratch. The pipeline is potrace-shaped:

1. Crop the cell, build a luminance field padded with the region's background so
   shapes touching the crop edge still close.
2. **Marching squares** at the half-way luminance between background and ink,
   interpolating along each cell edge. The source is antialiased, so that
   interpolation recovers sub-pixel positions — the traced edge is smoother than
   the pixel grid it came from. Ambiguous saddles resolve by cell average, which
   keeps thin strokes connected instead of pinching.
3. Link segments into closed loops by endpoint matching (winding is irrelevant;
   the output fills even-odd).
4. Douglas-Peucker, then classify each surviving vertex smooth-or-corner by turn
   angle, so sharp features stay sharp and curves stay curved.
5. All loops into one `fill-rule="evenodd"` path, so the hole inside each stroke
   ribbon drops out for free.

Each source stroke becomes a closed filled ribbon, which means **line weights
are inherited from the artwork** rather than chosen. Cell boxes came from the
image's own ink profile: two bands of artwork, five columns each.

### The measurement caught a real bug

`verify.go` rasterises the emitted path data back onto the source's own pixel
grid and reports intersection-over-union against the source ink mask. First run:
**0.70**. Sweeping the simplification tolerance from 0.05 to 0.42 barely moved
it — flat error means systematic error, not approximation error.

The cause was in the verifier, not the tracer: **the tracer puts pixel centres on
integers**, and the rasteriser was sampling at `+0.5`, the usual convention when
integers sit on corners. Half a pixel diagonally, which on a three-pixel-wide
stroke costs about a quarter of the score. After the fix, at tolerance 0.10:

| mark | IoU | shared | source only | trace only |
|---|---|---|---|---|
| 05 Rest | 0.971 | 3108 px | 54 | 40 |
| 06 Trace | 0.981 | 1459 px | 18 | 10 |
| 10 Prompt | 0.983 | 2003 px | 26 | 9 |

The residue is single-pixel boundary fringe — the floor set by the source's own
antialiasing. Every run prints these; below ~0.95 means the trace has drifted.

## Three surfaces, three drawings

The mark landed in more places than the `.icns`, and each wanted something
different from the same trace.

**The app icon** (`cats-icon.svg`) is sized from its **ink box**, not its
viewBox. The art does not reach its own edges, so scaling the box left it
filling 48% of the tile and looking lost; from the ink box it spans 78%, centred
on the 96..928 squircle. It is drawn deliberately large because with a verbatim
trace, **art size is the only lever on apparent stroke weight** — thickening the
strokes would mean offsetting every outline, and the result would no longer be
the reference artwork.

**The sidebar glyph**, top-left of the app, was the surface I nearly missed; the
user flagged it. Swapping it meant re-deriving the CSS baseline nudge, which the
existing comment derived from where the ink sat: the old artwork ended at
y=19.75 on a 24-unit grid (17.7% empty below the chin, hence −3.5px at 19px).
The trace is cropped tight, so only the 2-unit pad sits below the lowest ink —
1.4% of a 133.42-unit box, or −0.5px. Rendered against −3.5, −0.5 and 0 at true
size to confirm.

The alignment target moved too. **The lowest ink is the cursor bar, not the
chin** — the bar is the mark's ground line, so sitting *it* on the text baseline
puts mark and wordmark on one line; aligning the chin would hang the bar below
the text like a descender.

That glyph uses `prompt-compact.svg`, a coarser simplification of the same
trace. At 19px a whole source pixel of deviation is about a sixth of a screen
pixel: full and compact are indistinguishable at 19 and 26px, and the path drops
28KB → under 7KB in a file that ships whole. Nothing syncs the inlined copy
automatically, so the README says plainly that regenerating means re-pasting.

**The 16 and 32px icns slices** needed their own drawing. The real mark's head
outline is ~2% of the head's width — right from 64px up, hopeless below, landing
near a fifth of a pixel at 16px and rendering as a smudge. `cats-icon-small.svg`
keeps the same silhouette and thickens it: keep the outer edge of the head
outline, shrink a copy toward its centroid, fill the two even-odd. Interior
detail is dropped as sub-pixel mud.

Scaling about the centroid is a crude offset, not a true one — thickness ends up
proportional to distance from the centre, so the ears come out heavier than the
cheeks. On a roughly star-shaped head that reads as deliberate weight, and it
avoids a polygon-offsetting library for one glyph. `-smallk 0` fills the head
solid instead; kept the ring so the family stays an outline.

The split in `gen-icon.sh` follows **pixel size, not the logical slice name**.
`icon_16x16@2x` is a 32px render and takes the small art; `icon_32x32@2x` is
64px and takes the real mark. Going by the label would have put bold art at 64
and thin art at 32 — exactly backwards. 64px was checked, not assumed: the
verbatim mark still reads there.

## `gen-icon.sh` could not run at all

Unrelated to the logo but blocking it: the script needed `rsvg-convert` or
`magick`, and **neither ships with macOS**, so a clean machine could not
regenerate the icon. Added a third fallback that rasterises via QuickLook and
downsamples with `sips` — softer at 16px than rendering the vector directly, so
it stays last in the chain and the script still prefers the real rasterisers.

## Files

| path | state |
|---|---|
| `scripts/icon/gen-trace/` | new — the tracer, its own Go module, with `reference.png` committed so the result is reproducible |
| `scripts/icon/cats-icon.svg` | replaced — traced mark 10, `#5FA070` |
| `scripts/icon/cats-icon-small.svg` | new — bold reduction for 16/32px |
| `scripts/icon/prompt-compact.svg` | new — small-size trace, source of the sidebar glyph |
| `scripts/icon/{prompt,rest,trace}.svg` | new — marks 10, 05, 06; 05 and 06 keep the reference's cream `#F2E6D2`, sampled from the image |
| `scripts/gen-icon.sh` | two sources + QuickLook fallback |
| `scripts/AppIcon.icns` | regenerated, all ten slices extracted and checked |
| `cmd/catway/web/index.html` | `#brand` mark swapped, baseline nudge re-derived |

The brand green `#5FA070` is the app's ok-green pulled down in lightness and
saturation, so it reads as a line colour rather than a status LED.

## What I would tell the next session

The generator comments carry the failure list; read them before reintroducing a
profile, a full body, or a lobed mane. Two more things that are easy to get
wrong and cost a lot:

- **Render and look.** Path data hides everything that matters here. Four of the
  worst marks this session were only caught by rasterising them.
- **When a measurement disagrees with your eyes, suspect the measurement.** A
  flat error across every tolerance is the signature of a systematic offset, and
  it was a coordinate convention, not the artwork.

Ready for a Mac app rebuild; the icon is wired to both surfaces.
