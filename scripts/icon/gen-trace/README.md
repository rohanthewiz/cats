# gen-trace

Vectorises the cats marks out of `reference.png`. The artwork is **traced**, not
redrawn: every curve comes from the reference's pixels, so line weights and
proportions are inherited rather than chosen.

    cd scripts/icon/gen-trace && go run .      # writes the SVGs into ../
    bash scripts/gen-icon.sh                   # rebuilds scripts/AppIcon.icns

## What it writes

| file | use |
|---|---|
| `cats-icon.svg` | the app icon — mark 06 full-bleed on the theme's dark surfaces |
| `cats-icon-small.svg` | the bold reduction, used for the 16 and 32px icns slices only |
| `trace.svg` | mark 06, full fidelity, in the theme accent |
| `trace-compact.svg` | mark 06 simplified for small sizes — the source of the sidebar glyph |
| `rest.svg`, `prompt.svg` | marks 05 and 10, in the reference's own cream |

The mark the product wears is one `cell` value in `main.go` (`logo`). Everything
that ships — the icns, its small sibling, the sidebar glyph — is cut from it, so
moving to another mark on the board is a one-line edit.

## The icon must be full-bleed

Both icon sources paint their background across the whole 1024 canvas, square,
with no corner radius. This is not a style choice. macOS 26 no longer draws a
legacy `.icns` as authored: it composites the artwork over a light plate and
masks the result to the system's own shape. Anywhere the art is transparent, the
plate shows through — the earlier drawing (a 96..928 squircle floating on a
transparent canvas) came back as a thick white ring around a shrunken tile.
Painting to the edges leaves the plate nowhere to show, and the system mask
supplies the rounding that used to be baked in.

That also moves the art's fill fraction: it is measured against the full canvas
now, and it has to clear the system mask's corners, which bite further in than
the old baked radius did.

## Two icon drawings, one family

The real mark is a fine-line tracing: its lines are about 2.2% of the head's
width, which is right from 64px up and hopeless below. At 16px that lands near a
quarter of a pixel and renders as a smudge. So `cats-icon-small.svg` is the same
drawing given weight by **stroking it in its own colour**. Every loop the tracer
emits is a closed ribbon, so a stroke of width w grows it by w/2 on both edges —
a true, uniform offset, which the obvious cheap alternative (fill between the
outline and a copy shrunk toward its centroid) is not: that makes weight
proportional to distance from the centre and comes out with heavier ears than
cheeks.

`gen-icon.sh` picks between the two by PIXEL size, not by the logical slice
name: `icon_16x16@2x` is a 32px render and takes the small art, while
`icon_32x32@2x` is 64px and takes the real mark.

`-smallw` sets the added weight as a fraction of the drawing's long side. The
default is large in relative terms — it takes a 2.2% line to nearly 10% — because
a 16px icon has no room to negotiate: below about one whole pixel a line is grey,
not a line. The eye slits close into solid marks at that weight, which is the
right outcome at these sizes.

## The sidebar glyph is a manual paste

`cmd/catway/web/index.html` inlines the `trace-compact.svg` path into its
`#brand` mark. Nothing keeps the two in sync automatically, so **after
regenerating, paste the new path over the one in `index.html`**. The CSS beside
`#brand .mark` derives its baseline nudge from that artwork's tight crop, so if
the crop changes the nudge must be re-derived too — the note there shows the
arithmetic.

The glyph fills with `currentColor` and the sidebar points that at the theme's
accent, so it follows a live theme switch. An `.icns` cannot, so the icon bakes
the default theme's accent and surface colours in; the constants at the top of
`main.go` have to be moved by hand if that theme's palette moves.

## Every emitted file is parsed before it is written

`writeSVG` runs the XML tokeniser over each file first. That guard is there for
one specific silent failure: XML forbids a double hyphen inside a comment, and
these files carry prose about the app's CSS custom properties, whose names all
start with two hyphens. Writing one produces a file every renderer refuses, and
nothing downstream notices until somebody looks at a blank glyph.

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
