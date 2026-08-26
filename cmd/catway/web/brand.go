package web

import (
	"strings"

	"github.com/rohanthewiz/element"
)

// Brand is the sidebar's masthead: the logo mark, the wordmark, and the button
// that folds the column away. The build badge is a fourth child that the
// front-end appends at runtime (initBuildBadge in js/10-buildbadge.js).
//
// Nothing here may be separated by whitespace. #brand is a plain inline run
// with white-space:nowrap — not a flex box — so a line break between the mark
// and the wordmark would collapse to a rendered space. The original markup used
// `--><svg …>` comment stitching to achieve the same thing; here the comments
// are Go comments and the builder simply never writes the whitespace.
type Brand struct{}

func (Brand) Render(b *element.Builder) (x any) {
	b.Div("id", "brand").R(
		BrandMark{}.Render(b),
		b.T("Cats Mux"),
		// Folds the column away; the reveal handle in the gutter brings it
		// back. tabindex="-1" is deliberate — see the CSS note beside #sb-fold
		// in css/04-brand.css. The build badge is appended into #brand after
		// this at runtime, which is why the button being out of flow matters:
		// DOM order stops deciding what shows up where, so the badge still
		// lands right after the wordmark.
		b.Button("id", "sb-fold", "type", "button", "tabindex", "-1",
			"title", "hide the sidebar (⌘B)", "aria-label", "Hide the sidebar").T("&#x2039;"),
	)
	return
}

// BrandMark writes mark.svg — the cat's-head logo — into the page verbatim.
//
// Mark 06 of the logo exploration: a big cat's head, front on, drawn as one
// unbroken outline with two eye slits and nothing else. It replaced mark 10,
// which carried a chevron eye, a muzzle and cheek lines that all fell below one
// pixel once the mark was shrunk. Both are the same hairline weight; this one
// simply has less to lose.
//
// The artwork is TRACED from the reference drawing, not drawn by hand, and the
// path data in mark.svg is machine-generated: each stroke of the original is a
// closed filled ribbon, which is why it is one even-odd path rather than a set
// of stroked outlines. Do not hand-edit the numbers. Regenerate with
//
//	cd scripts/icon/gen-trace && go run .
//
// and paste the path from the trace-compact.svg it writes. That file is the
// same trace simplified for small sizes: at the 19px this renders at, a full
// source pixel of deviation is a sixth of a screen pixel, so the reduction is
// invisible here and saves ~25KB in a file that ships whole.
//
// It fills with currentColor, which the CSS points at the theme's accent, so
// the mark follows a live theme switch along with everything else in the
// sidebar. The app icon cannot do that and bakes the default theme's accent in
// instead; see scripts/icon/gen-trace.
//
// Two attributes in mark.svg are tuning, not trace output, and both are on the
// <svg> rather than on the <path> so the machine-generated d= stays the only
// thing a regeneration has to replace. Stroke properties are inherited, so
// hanging them on the <svg> reaches the path just the same.
//
//	preserveAspectRatio="none"  breaks the aspect on purpose. The CSS box is
//	  19x16 against a 130.77x133.93 viewBox, so the glyph is stretched ~2%
//	  wider and squashed ~16% shorter than it was drawn. Without this it would
//	  letterbox — scale to the box's height and centre — and the shorter box
//	  would just make a smaller square mark.
//
//	stroke-width="3.6"  thickens the lines. This artwork has no strokes: the
//	  "lines" are closed filled ribbons about 3.2 units wide, so a stroke on the
//	  same path is what widens them — half a stroke-width steps outward off the
//	  outer contour and half steps inward off the inner one, so the ribbon grows
//	  by the full 3.6 units, to 6.8. That matters because the squash thins the
//	  horizontal runs (they scale with the height, which shrank at 19x16):
//	  unstroked, the ribbon renders 0.44px wide there, well under one device
//	  pixel on a 2x display, which is why the mark read as a faint hairline
//	  rather than as accent-colored ink. With this stroke the horizontal runs
//	  are ~0.93px and the vertical ~1.14px, or 1.87 and 2.27 device px on a 2x
//	  panel — both clear of the one-device-pixel floor, which is the whole point
//	  of the weight. (Those are css px at --sb-scale 1.15, the default; every
//	  length here scales with it.)
//
//	  Round joins are not cosmetic: the trace is hundreds of near-collinear
//	  segments and a miter join on any of them throws a spike once there is a
//	  stroke to miter.
//
//	  Two ceilings, both measured on the shipping path rather than guessed
//	  (rasterise it and sweep the width — the eyes in particular are not
//	  predictable from the path data):
//
//	    ~2.6u  the chin leaves the viewBox. Passed deliberately; see the
//	           overflow:visible note in css/04-brand.css, which is what keeps
//	           the overhang from being clipped flat.
//	    ~4.8u  the eye slits' counters close and the cat goes blind. Each slit
//	           is a ribbon like everything else, so the stroke eats its hole
//	           from both sides at once — the counters lose a full stroke-width
//	           of width, and at 3.6 they are down to about a third of the open
//	           area they have unstroked. That is still legible at 19x16 on a 2x
//	           panel, checked by magnifying the real device pixels, but it is
//	           the reason this is the last step up rather than one of several.
//
// The viewBox is cropped tight to the ink; the baseline nudge in the CSS is
// derived from that crop AND from the stroke above, so all three move together.
//
// This is the one place the package writes raw markup instead of building it.
// The alternative is transcribing a machine-generated 4KB path into builder
// calls, which would make the file harder to regenerate and no safer: the
// content is a build-time constant compiled into the binary, not input.
type BrandMark struct{}

func (BrandMark) Render(b *element.Builder) (x any) {
	b.WriteString(strings.TrimRight(markSVG, "\n"))
	return
}
