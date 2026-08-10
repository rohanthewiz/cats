// Command gen-trace vectorises the cats marks out of the reference artwork.
// This is a trace, not a redrawing: every curve comes from the image's pixels.
//
// Pipeline, the same shape as potrace's:
//
//  1. Crop the cell and build a luminance field over it, padded with the
//     region's background so a shape touching the crop edge still closes.
//  2. Marching squares at the half-way luminance between background and ink,
//     interpolating along each cell edge. The source is antialiased, so that
//     interpolation recovers sub-pixel positions and the traced edge comes out
//     smoother than the pixel grid it came from.
//  3. Link the segments into closed loops.
//  4. Douglas-Peucker to drop redundant vertices, then classify each survivor
//     as smooth or corner by its turn angle, so sharp features stay sharp and
//     curves stay curved.
//  5. Emit all loops into one path with fill-rule="evenodd", which makes the
//     hole inside each stroke loop fall out for free.
//
// Each stroke in the source becomes a closed filled ribbon, so line weights are
// inherited from the artwork rather than chosen here. verify.go rasterises the
// emitted path data back onto the source grid and reports intersection-over-
// union against the source's own ink mask; the run prints that score for every
// mark, and it should stay above 0.95.
package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type field struct {
	w, h   int
	v      []float64 // luminance, row-major, already padded
	x0, y0 int       // crop origin in source pixels
}

func lumAt(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	return (299*float64(r>>8) + 587*float64(g>>8) + 114*float64(b>>8)) / 1000
}

func crop(img image.Image, x0, y0, x1, y1 int, bg float64) *field {
	const pad = 2
	w, h := (x1-x0+1)+2*pad, (y1-y0+1)+2*pad
	f := &field{w: w, h: h, v: make([]float64, w*h), x0: x0 - pad, y0: y0 - pad}
	for i := range f.v {
		f.v[i] = bg
	}
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			f.v[(y-y0+pad)*w+(x-x0+pad)] = lumAt(img, x, y)
		}
	}
	return f
}

func (f *field) at(x, y int) float64 { return f.v[y*f.w+x] }

// levels reports a region's background and ink luminance as the 20th and 99th
// percentiles. A percentile rather than the maximum keeps one bright
// antialiasing spike from dragging the threshold up.
func levels(img image.Image, x0, y0, x1, y1 int) (bg, ink float64) {
	var all []float64
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			all = append(all, lumAt(img, x, y))
		}
	}
	sort.Float64s(all)
	return all[len(all)*20/100], all[len(all)*99/100]
}

// inkColour averages the pixels well above threshold, so output can carry the
// source's own line colour instead of a guess.
func inkColour(img image.Image, x0, y0, x1, y1 int) string {
	bg, ink := levels(img, x0, y0, x1, y1)
	hi := bg + 0.85*(ink-bg)
	var rs, gs, bs, n float64
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if lumAt(img, x, y) < hi {
				continue
			}
			r, g, b, _ := img.At(x, y).RGBA()
			rs, gs, bs, n = rs+float64(r>>8), gs+float64(g>>8), bs+float64(b>>8), n+1
		}
	}
	if n == 0 {
		return "#FFFFFF"
	}
	return fmt.Sprintf("#%02X%02X%02X", int(rs/n), int(gs/n), int(bs/n))
}

type seg struct{ a, b Pt }

// marching emits the iso-line pieces crossing every 2x2 cell. Ambiguous saddles
// (opposite corners inside) are resolved by the cell average, which keeps thin
// strokes connected instead of pinching them apart.
func marching(f *field, thr float64) []seg {
	var out []seg
	lerp := func(pa, pb Pt, va, vb float64) Pt {
		t := (thr - va) / (vb - va)
		if math.IsNaN(t) || math.IsInf(t, 0) {
			t = 0.5
		}
		return Pt{pa.X + t*(pb.X-pa.X), pa.Y + t*(pb.Y-pa.Y)}
	}
	for y := 0; y < f.h-1; y++ {
		for x := 0; x < f.w-1; x++ {
			v00, v10 := f.at(x, y), f.at(x+1, y)
			v01, v11 := f.at(x, y+1), f.at(x+1, y+1)
			idx := 0
			for _, c := range []struct {
				v   float64
				bit int
			}{{v00, 1}, {v10, 2}, {v11, 4}, {v01, 8}} {
				if c.v > thr {
					idx |= c.bit
				}
			}
			if idx == 0 || idx == 15 {
				continue
			}
			fx, fy := float64(x), float64(y)
			T := lerp(Pt{fx, fy}, Pt{fx + 1, fy}, v00, v10)
			R := lerp(Pt{fx + 1, fy}, Pt{fx + 1, fy + 1}, v10, v11)
			B := lerp(Pt{fx, fy + 1}, Pt{fx + 1, fy + 1}, v01, v11)
			L := lerp(Pt{fx, fy}, Pt{fx, fy + 1}, v00, v01)
			switch idx {
			case 1, 14:
				out = append(out, seg{L, T})
			case 2, 13:
				out = append(out, seg{T, R})
			case 3, 12:
				out = append(out, seg{L, R})
			case 4, 11:
				out = append(out, seg{R, B})
			case 6, 9:
				out = append(out, seg{T, B})
			case 7, 8:
				out = append(out, seg{L, B})
			case 5, 10:
				centre := (v00 + v10 + v01 + v11) / 4
				if (idx == 5) == (centre > thr) {
					out = append(out, seg{L, T}, seg{R, B})
				} else {
					out = append(out, seg{T, R}, seg{L, B})
				}
			}
		}
	}
	return out
}

// link stitches undirected segments into closed loops by matching endpoints.
// Winding is irrelevant because the result is filled even-odd.
func link(segs []seg) [][]Pt {
	key := func(p Pt) [2]int64 {
		return [2]int64{int64(math.Round(p.X * 4096)), int64(math.Round(p.Y * 4096))}
	}
	type ref struct{ seg, end int }
	at := map[[2]int64][]ref{}
	for i, s := range segs {
		at[key(s.a)] = append(at[key(s.a)], ref{i, 0})
		at[key(s.b)] = append(at[key(s.b)], ref{i, 1})
	}
	used := make([]bool, len(segs))
	var loops [][]Pt
	for i := range segs {
		if used[i] {
			continue
		}
		used[i] = true
		loop := []Pt{segs[i].a, segs[i].b}
		cur, curEnd := i, 1
		for {
			p := segs[cur].b
			if curEnd == 0 {
				p = segs[cur].a
			}
			next, nextEnd := -1, 0
			for _, r := range at[key(p)] {
				if r.seg != cur && !used[r.seg] {
					next, nextEnd = r.seg, 1-r.end
					break
				}
			}
			if next < 0 {
				break
			}
			used[next] = true
			q := segs[next].b
			if nextEnd == 0 {
				q = segs[next].a
			}
			loop = append(loop, q)
			cur, curEnd = next, nextEnd
			if key(q) == key(loop[0]) {
				break
			}
		}
		if len(loop) >= 6 {
			loops = append(loops, loop)
		}
	}
	return loops
}

func douglasPeucker(pts []Pt, tol float64) []Pt {
	if len(pts) < 3 {
		return pts
	}
	a, b := pts[0], pts[len(pts)-1]
	dx, dy := b.X-a.X, b.Y-a.Y
	norm := math.Hypot(dx, dy)
	worst, wi := -1.0, 0
	for i := 1; i < len(pts)-1; i++ {
		var d float64
		if norm < 1e-9 {
			d = math.Hypot(pts[i].X-a.X, pts[i].Y-a.Y)
		} else {
			d = math.Abs(dy*(pts[i].X-a.X)-dx*(pts[i].Y-a.Y)) / norm
		}
		if d > worst {
			worst, wi = d, i
		}
	}
	if worst <= tol {
		return []Pt{a, b}
	}
	left := douglasPeucker(pts[:wi+1], tol)
	return append(left[:len(left)-1], douglasPeucker(pts[wi:], tol)...)
}

// toNodes marks each vertex smooth or corner by its turn angle, so the spline
// rounds through a gentle bend but holds a true vertex at a sharp one.
func toNodes(pts []Pt, cornerDeg float64) []Node {
	n := len(pts)
	out := make([]Node, n)
	for i, p := range pts {
		prev, next := pts[(i-1+n)%n], pts[(i+1)%n]
		a := math.Atan2(p.Y-prev.Y, p.X-prev.X)
		b := math.Atan2(next.Y-p.Y, next.X-p.X)
		turn := math.Abs(math.Atan2(math.Sin(b-a), math.Cos(b-a))) * 180 / math.Pi
		out[i] = Node{p, turn > cornerDeg}
	}
	return out
}

// traceLoops returns the simplified contours of a cell, in source coordinates.
func traceLoops(img image.Image, x0, y0, x1, y1 int, tol float64) [][]Pt {
	bg, ink := levels(img, x0, y0, x1, y1)
	f := crop(img, x0, y0, x1, y1, bg)

	var out [][]Pt
	for _, loop := range link(marching(f, (bg+ink)/2)) {
		if len(loop) > 1 && math.Hypot(loop[0].X-loop[len(loop)-1].X, loop[0].Y-loop[len(loop)-1].Y) < 1e-6 {
			loop = loop[:len(loop)-1]
		}
		simp := douglasPeucker(loop, tol)
		if len(simp) < 4 {
			continue
		}
		for i := range simp {
			simp[i] = Pt{simp[i].X + float64(f.x0), simp[i].Y + float64(f.y0)}
		}
		out = append(out, simp)
	}
	return out
}

func bboxOf(loops [][]Pt) (lo, hi Pt) {
	lo, hi = Pt{math.Inf(1), math.Inf(1)}, Pt{math.Inf(-1), math.Inf(-1)}
	for _, l := range loops {
		for _, p := range l {
			lo.X, lo.Y = math.Min(lo.X, p.X), math.Min(lo.Y, p.Y)
			hi.X, hi.Y = math.Max(hi.X, p.X), math.Max(hi.Y, p.Y)
		}
	}
	return
}

func traceCell(img image.Image, x0, y0, x1, y1 int, tol, cornerDeg float64) (string, Pt, Pt) {
	loops := traceLoops(img, x0, y0, x1, y1, tol)
	var d strings.Builder
	for _, l := range loops {
		d.WriteString(path(toNodes(l, cornerDeg), true) + " ")
	}
	lo, hi := bboxOf(loops)
	return strings.TrimSpace(d.String()), lo, hi
}

// writeSVG writes one emitted file after checking it is actually parseable.
//
// The check exists because of a specific silent failure: XML forbids a double
// hyphen inside a comment, and these files carry long prose comments about the
// app's CSS custom properties, whose names all begin with two hyphens. Writing
// one produces a file that every renderer refuses, and nothing downstream
// notices until somebody looks at a blank glyph. Failing here instead costs one
// parse per file.
func writeSVG(path, content string) {
	dec := xml.NewDecoder(strings.NewReader(content))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			must(fmt.Errorf("%s is not well-formed XML: %w", filepath.Base(path), err))
		}
	}
	must(os.WriteFile(path, []byte(content), 0644))
}

// Cell boxes were read off the reference's own ink profile: two bands of
// artwork at y 20..200 and y 268..432, five columns each. 05 Rest is row one
// column five, 06 Trace is row two column one, 10 Prompt is row two column five.
type cell struct {
	name, out      string
	x0, y0, x1, y1 int
	colour         string // default render colour; empty means take the source's
}

// The palette is the app's own, not a separate brand one: themeAccent is
// --accent from cmd/catway/web/index.html and the tile is that theme's two
// darkest surfaces (--panel2 over a step below --bg). The sidebar glyph follows
// a live theme switch because it inherits currentColor from var(--accent); an
// .icns cannot, so it bakes in the default theme's value and these constants
// have to be moved by hand if that theme's accent moves.
const (
	themeAccent = "#4db380"
	tileTop     = "#2b322c"
	tileBottom  = "#12160f"
)

func main() {
	var (
		src  = flag.String("src", "reference.png", "reference artwork")
		out  = flag.String("out", "..", "directory to write the SVGs into")
		tol  = flag.Float64("tol", 0.10, "Douglas-Peucker tolerance, source pixels")
		corn = flag.Float64("corner", 48, "turn angle above which a vertex is a corner")
		// The sidebar glyph renders at 19px, where the source's 130px grid is
		// scaled by about 0.15 — so a deviation of a whole source pixel lands
		// well inside one screen pixel and cannot be seen. Simplifying that far
		// costs nothing visible below ~26px and cuts the path data from 28KB to
		// under 7KB, which matters because it is inlined into index.html.
		uiTol = flag.Float64("uitol", 1.0, "tolerance for the small in-app glyph")
		// Weight added to every line of the small-size icon art, as a fraction
		// of the drawing's long side. See the small-icon section below for why
		// it has to be this large.
		smallW   = flag.Float64("smallw", 0.075, "extra line weight for the small icon mark, fraction of its long side")
		smallTol = flag.Float64("smalltol", 0.6, "Douglas-Peucker tolerance for the small icon mark")
	)
	flag.Parse()

	fh, err := os.Open(*src)
	must(err)
	defer fh.Close()
	img, _, err := image.Decode(fh)
	must(err)

	// The mark the product actually wears. Everything the app ships — the
	// .icns, its small-size sibling and the sidebar glyph — is cut from this
	// one cell, so moving to another mark on the board is a one-line edit.
	//
	// Mark 06 rather than 10: it is the same hairline weight (both sit near
	// 2.2% of their cell width) but carries a tenth of the interior detail, so
	// it survives being shrunk. 10's chevron eye, muzzle and cheek lines all
	// land sub-pixel below about 48px and turn the face into noise.
	logo := cell{"trace", "trace.svg", 35, 281, 169, 418, themeAccent}

	cells := []cell{
		// Everything that is not the logo keeps the reference's own cream.
		{"rest", "rest.svg", 812, 32, 957, 196, ""},
		logo,
		{"prompt", "prompt.svg", 819, 282, 951, 417, ""},
	}

	var logoPath string
	var logoLo, logoHi Pt

	for _, c := range cells {
		d, lo, hi := traceCell(img, c.x0, c.y0, c.x1, c.y1, *tol, *corn)
		colour := c.colour
		if colour == "" {
			colour = inkColour(img, c.x0, c.y0, c.x1, c.y1)
		}
		w, h := hi.X-lo.X, hi.Y-lo.Y
		const pad = 2.0
		svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="512" height="%d"
     viewBox="%.2f %.2f %.2f %.2f" style="color:%s" role="img" aria-label="cats — %s">
  <title>cats — %s</title>
  <!-- Traced from the reference artwork, not redrawn: marching squares at the
       half-way luminance with sub-pixel interpolation, simplified, then fitted
       with curvature-continuous splines. Each source stroke becomes a closed
       filled ribbon, so weights come from the artwork. Filled even-odd so the
       inside of every stroke loop drops out. Regenerate with
       scripts/icon/gen-trace; do not hand-edit the path data. -->
  <path d="%s" fill="currentColor" fill-rule="evenodd"/>
</svg>
`, int(512*(h+2*pad)/(w+2*pad)), lo.X-pad, lo.Y-pad, w+2*pad, h+2*pad, colour, c.name, c.name, d)
		writeSVG(filepath.Join(*out, c.out), svg)

		// Pixel match: rasterise the emitted path back onto the source grid and
		// compare ink masks. Anything below ~0.95 means the trace has drifted.
		bg, ink := levels(img, c.x0, c.y0, c.x1, c.y1)
		thr := (bg + ink) / 2
		mw, mh := c.x1-c.x0+1, c.y1-c.y0+1
		srcMask := make([]bool, mw*mh)
		for yy := 0; yy < mh; yy++ {
			for xx := 0; xx < mw; xx++ {
				srcMask[yy*mw+xx] = lumAt(img, c.x0+xx, c.y0+yy) > thr
			}
		}
		reportMatch(c.name, d, srcMask, c.x0, c.y0, mw, mh)

		if c.name == logo.name {
			logoPath, logoLo, logoHi = d, lo, hi
		}
	}

	// ---- the app icon ---------------------------------------------------
	// The background covers the WHOLE 1024 canvas, square, with no corner
	// radius of its own. That is not a style choice, it is what macOS 26
	// requires. Tahoe no longer draws a legacy .icns as authored: it composites
	// the artwork over a light plate and masks the result to the system's own
	// squircle. Anywhere the art is transparent, the plate shows through — so
	// the previous drawing (a 96..928 squircle floating on a transparent 1024
	// canvas) came back as a thick white ring around a shrunken tile. Painting
	// to the edges leaves the plate nowhere to show, and the system mask
	// supplies the rounding that used to be baked in.
	//
	// The art is placed from its INK box rather than a nominal viewBox, then
	// scaled to span fillFrac of the canvas and centred. fillFrac is measured
	// against the full canvas now rather than the old inset tile, and it is
	// generous for a thin-line mark on purpose: art size is the only lever on
	// apparent stroke weight in a verbatim trace, since thickening the strokes
	// would mean offsetting every outline and the result would no longer be the
	// reference artwork. It still has to clear the system mask's corners, which
	// bite further in than the old baked radius did — 0.62 was picked by
	// rendering the icon through Icon Services and looking at it, not derived.
	const (
		canvas   = 1024.0
		fillFrac = 0.62
	)
	inkW, inkH := logoHi.X-logoLo.X, logoHi.Y-logoLo.Y
	scale := canvas * fillFrac / math.Max(inkW, inkH)
	tx := canvas/2 - (logoLo.X+logoHi.X)/2*scale
	ty := canvas/2 - (logoLo.Y+logoHi.Y)/2*scale

	icon := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024"
     viewBox="0 0 1024 1024" role="img" aria-label="cats">
  <title>cats</title>
  <!-- cats app mark: mark 06 of the reference exploration, traced from the
       artwork itself and drawn in the app theme's accent on that theme's two
       darkest surfaces. Regenerate with scripts/icon/gen-trace; the path data
       is machine-produced and should not be hand-edited.

       The tile is deliberately a full-bleed SQUARE with no corner radius.
       macOS 26 masks app icons to its own shape and paints a light plate
       behind them, so a baked-in squircle on a transparent canvas renders as a
       white ring around a shrunken icon. Do not re-inset this rect.

       The art spans %d%% of the canvas from its ink box, centred, which keeps
       the ears and chin clear of the system mask's corners. It is a thin-line
       design drawn large — art size is the only lever on apparent stroke
       weight in a verbatim trace. -->
  <defs>
    <linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="%s"/>
      <stop offset="1" stop-color="%s"/>
    </linearGradient>
  </defs>
  <rect x="0" y="0" width="1024" height="1024" fill="url(#bg)"/>
  <g transform="translate(%.2f %.2f) scale(%.5f)">
    <path d="%s" fill="%s" fill-rule="evenodd"/>
  </g>
</svg>
`, int(fillFrac*100), tileTop, tileBottom, tx, ty, scale, logoPath, themeAccent)
	writeSVG(filepath.Join(*out, "cats-icon.svg"), icon)

	// ---- the small in-app glyph -----------------------------------------
	// The sidebar mark in cmd/catway/web/index.html is this path, inlined. It
	// is a separate simplification of the same trace rather than a reuse of the
	// icon's, because the icon needs full fidelity and the glyph needs to be
	// small enough to paste into a file that ships whole.
	//
	// The viewBox is left tight around the ink, which is what the sidebar CSS
	// derives its baseline nudge from — see the note beside #brand .mark. Move
	// the crop and that nudge has to be re-derived.
	uiPath, uiLo, uiHi := traceCell(img, logo.x0, logo.y0, logo.x1, logo.y1, *uiTol, *corn)
	const uiPad = 2.0
	uiOut := logo.name + "-compact.svg"
	glyph := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg"
     viewBox="%.2f %.2f %.2f %.2f" role="img" aria-label="cats">
  <title>cats</title>
  <!-- The sidebar glyph: mark 06 traced from the reference and simplified for
       small sizes. It fills with currentColor so the sidebar's accent custom
       property reaches it and it follows a live theme switch. Generated by
       scripts/icon/gen-trace; if it is regenerated, the copy inlined in
       cmd/catway/web/index.html must be replaced too.

       (No double hyphen may appear in this comment or any other emitted here:
       it terminates an XML comment early and every renderer rejects the file.) -->
  <path d="%s" fill="currentColor" fill-rule="evenodd"/>
</svg>
`, uiLo.X-uiPad, uiLo.Y-uiPad, uiHi.X-uiLo.X+2*uiPad, uiHi.Y-uiLo.Y+2*uiPad, uiPath)
	writeSVG(filepath.Join(*out, uiOut), glyph)

	// ---- the bold reduction for the 16 and 32px icon slices --------------
	// The verbatim mark is a hairline drawing: its lines are ribbons about 3
	// source units wide on a 131-unit head, or 2.2%. At 16px that lands near a
	// quarter of a pixel and renders as a smudge rather than as a cat, so the
	// small slices get their own drawing.
	//
	// It is the SAME drawing, given weight by stroking it in its own colour.
	// Every loop the tracer emits is a closed ribbon, so a stroke of width w
	// grows each one by w/2 on both edges — a true, uniform offset. That is
	// worth spelling out because the obvious cheap alternative (fill between
	// the outline and a copy shrunk toward its centroid) is not uniform: it
	// makes weight proportional to distance from the centre, so ears come out
	// heavier than cheeks. Stroking costs one attribute and is exact.
	//
	// The weight needed is large in relative terms — the default takes a 2.2%
	// line to nearly 10% of the drawing — because a 16px icon has no room to
	// negotiate: below about one whole pixel a line is grey, not a line. The
	// eye slits close up into solid marks at this weight, which is the right
	// outcome at these sizes.
	//
	// Simplified harder than the icon too (-smalltol): under a stroke this
	// heavy, a tenth of a source pixel of contour detail is invisible.
	smallLoops := traceLoops(img, logo.x0, logo.y0, logo.x1, logo.y1, *smallTol)
	var smallD strings.Builder
	for _, l := range smallLoops {
		smallD.WriteString(path(toNodes(l, *corn), true) + " ")
	}
	sLo, sHi := bboxOf(smallLoops)
	// Stroke width is set from the drawing's long side so the weight is a
	// property of the design rather than of whatever crop it came from, and the
	// box is grown by half of it on every side before fitting — the stroke is
	// ink, and fitting the unstroked box would push it under the system mask.
	sw := *smallW * math.Max(sHi.X-sLo.X, sHi.Y-sLo.Y)
	sLo, sHi = Pt{sLo.X - sw/2, sLo.Y - sw/2}, Pt{sHi.X + sw/2, sHi.Y + sw/2}

	// Drawn larger than the full mark: with the lines this heavy the interior
	// no longer reads as detail to crowd, and at these sizes every extra pixel
	// of art is another pixel of stroke.
	const smallFill = 0.68
	sScale := canvas * smallFill / math.Max(sHi.X-sLo.X, sHi.Y-sLo.Y)
	sTx := canvas/2 - (sLo.X+sHi.X)/2*sScale
	sTy := canvas/2 - (sLo.Y+sHi.Y)/2*sScale

	small := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024"
     viewBox="0 0 1024 1024" role="img" aria-label="cats">
  <title>cats</title>
  <!-- The small-size app mark, used ONLY for the 16 and 32px .icns slices;
       everything from 64px up uses cats-icon.svg, which is the verbatim trace.

       Same drawing, given weight by stroking it in its own colour. Each traced
       loop is a closed ribbon, so a stroke grows it by half the stroke width on
       both edges — a uniform offset, which scaling a copy toward the centroid
       is not. The verbatim lines are 2.2%% of the head's width, about a quarter
       of a pixel at 16px; the stroke takes them to something a pixel grid can
       actually show.

       Full-bleed square background for the same reason as cats-icon.svg: macOS
       26 masks the icon itself and shows a light plate through any transparency.

       Regenerate with scripts/icon/gen-trace; -smallw controls the weight. -->
  <defs>
    <linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="%s"/>
      <stop offset="1" stop-color="%s"/>
    </linearGradient>
  </defs>
  <rect x="0" y="0" width="1024" height="1024" fill="url(#bg)"/>
  <g transform="translate(%.2f %.2f) scale(%.5f)">
    <path d="%s" fill="%s" fill-rule="evenodd"
          stroke="%s" stroke-width="%.3f" stroke-linejoin="round" stroke-linecap="round"/>
  </g>
</svg>
`, tileTop, tileBottom, sTx, sTy, sScale, strings.TrimSpace(smallD.String()), themeAccent, themeAccent, sw)
	writeSVG(filepath.Join(*out, "cats-icon-small.svg"), small)

	fmt.Printf("wrote rest.svg trace.svg prompt.svg %s cats-icon.svg cats-icon-small.svg to %s\n", uiOut, *out)
	fmt.Printf("glyph viewBox %.2f %.2f %.2f %.2f, path %d bytes\n",
		uiLo.X-uiPad, uiLo.Y-uiPad, uiHi.X-uiLo.X+2*uiPad, uiHi.Y-uiLo.Y+2*uiPad, len(uiPath))
	fmt.Printf("small mark: %d loops, stroke %.2f source units (%.1f%% of long side)\n",
		len(smallLoops), sw, *smallW*100)
}
