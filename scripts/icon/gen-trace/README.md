# gen-trace

Vectorises the cats marks out of `reference.png`. The artwork is **traced**, not
redrawn: every curve comes from the reference's pixels, so line weights and
proportions are inherited rather than chosen.

    cd scripts/icon/gen-trace && go run .      # writes the SVGs into ../
    bash scripts/gen-icon.sh                   # rebuilds scripts/AppIcon.icns

## What it writes

| file | use |
|---|---|
| `cats-icon.svg` | the app icon — mark 10 on the dark squircle, sized for the macOS slot |
| `cats-icon-small.svg` | the bold reduction, used for the 16 and 32px icns slices only |
| `prompt.svg` | mark 10, full fidelity |
| `prompt-compact.svg` | mark 10 simplified for small sizes — the source of the sidebar glyph |
| `rest.svg`, `trace.svg` | marks 05 and 06, in the reference's own cream |

## Two icon drawings, one family

The real mark is a fine-line tracing: its head outline is roughly 2% of the
head's width, which is right from 64px up and hopeless below. At 16px that
stroke lands near a fifth of a pixel and renders as a smudge. So
`cats-icon-small.svg` keeps the same silhouette and thickens it — the outer edge
of the head outline is kept and a copy shrunk toward its centroid is filled
against it even-odd, turning a hairline ring into a heavy one. Interior detail
(eyes, chevron, muzzle) is dropped because at these sizes it is sub-pixel and
only adds mud.

`gen-icon.sh` picks between the two by PIXEL size, not by the logical slice
name: `icon_16x16@2x` is a 32px render and takes the small art, while
`icon_32x32@2x` is 64px and takes the real mark.

`-smallk` sets the weight (lower is heavier); `-smallk 0` fills the head solid
instead of ringing it, which is more visible at 16px but drops the outline
character the larger sizes have.

## The sidebar glyph is a manual paste

`cmd/catway/web/index.html` inlines the `prompt-compact.svg` path into its
`#brand` mark. Nothing keeps the two in sync automatically, so **after
regenerating, paste the new path over the one in `index.html`**. The CSS beside
`#brand .mark` derives its baseline nudge from that artwork's tight crop, so if
the crop changes the nudge must be re-derived too — the note there shows the
arithmetic.

Two tolerances exist because the two uses want different things: `-tol` (0.10)
keeps the icon faithful, `-uitol` (1.0) keeps the inlined glyph small. At the
19px the sidebar renders at, a whole source pixel of deviation is about a sixth
of a screen pixel, so the reduction is invisible there and saves ~22KB in a file
that ships whole.

## It measures itself

`verify.go` rasterises the emitted path data back onto the source's own pixel
grid and reports intersection-over-union against the source ink mask. Every run
prints a score per mark; at the default tolerance they sit near 0.97–0.98, and
anything below ~0.95 means the trace has drifted. The remaining disagreement is
single-pixel boundary fringe — the floor set by the source's antialiasing.

Watch the coordinate convention if you touch the verifier: the tracer puts pixel
*centres* on integers. Sampling at `+0.5` instead shifts the comparison half a
pixel diagonally and costs roughly a quarter of the score on a three-pixel
stroke, which reads as a bad trace rather than a bad measurement.

This directory is its own Go module so it stays out of the app's build.
