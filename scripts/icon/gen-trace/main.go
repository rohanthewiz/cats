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
	"flag"
	"fmt"
	"image"
	_ "image/png"
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

// centroid returns a polygon's area centroid, falling back to the vertex mean
// for a degenerate ring.
func centroid(p []Pt) Pt {
	var a, cx, cy float64
	n := len(p)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		cross := p[i].X*p[j].Y - p[j].X*p[i].Y
		a += cross
		cx += (p[i].X + p[j].X) * cross
		cy += (p[i].Y + p[j].Y) * cross
	}
	if math.Abs(a) < 1e-9 {
		var sx, sy float64
		for _, q := range p {
			sx, sy = sx+q.X, sy+q.Y
		}
		return Pt{sx / float64(n), sy / float64(n)}
	}
	return Pt{cx / (3 * a), cy / (3 * a)}
}

func scaleAbout(p []Pt, c Pt, k float64) []Pt {
	out := make([]Pt, len(p))
	for i, q := range p {
		out[i] = Pt{c.X + (q.X-c.X)*k, c.Y + (q.Y-c.Y)*k}
	}
	return out
}

// smallMark builds the bold reduction used for the 16 and 32px icon slices.
//
// The verbatim artwork is a hairline drawing: its head outline is a ribbon
// about 2.65 units wide on a 127-unit head, so at 16px it lands near a fifth of
// a pixel and disappears into a smudge. This keeps the mark's actual silhouette
// and thickens it, rather than substituting some other drawing:
//
//   - take the outer edge of the head outline (the largest contour),
//   - shrink a copy of it toward its own centroid and fill between the two
//     even-odd, which turns a hairline ring into a heavy one,
//   - keep the cursor bar, which is already solid.
//
// Scaling about the centroid is a crude offset, not a true one — thickness ends
// up proportional to how far each point sits from the centre, so the ears come
// out heavier than the cheeks. On a roughly star-shaped head that reads as
// deliberate weight rather than as an error, and it avoids pulling in a polygon
// offsetting library for one glyph. The interior details (eyes, chevron, muzzle)
// are dropped: at 16px they are sub-pixel and only add mud.
func smallMark(loops [][]Pt, k, cornerDeg float64) (string, Pt, Pt) {
	if len(loops) == 0 {
		return "", Pt{}, Pt{}
	}
	area := func(l []Pt) float64 {
		lo, hi := bboxOf([][]Pt{l})
		return (hi.X - lo.X) * (hi.Y - lo.Y)
	}
	head := loops[0]
	lowest := loops[0]
	for _, l := range loops {
		if area(l) > area(head) {
			head = l
		}
		_, hiL := bboxOf([][]Pt{l})
		_, hiLow := bboxOf([][]Pt{lowest})
		if hiL.Y > hiLow.Y {
			lowest = l
		}
	}

	// k <= 0 means fill the head solid rather than ringing it. At 16px an
	// outline spends most of its pixels on the hole in the middle, where a
	// silhouette spends all of them on shape — the ear points and the chin
	// taper survive, which is what makes it read as a cat rather than a blob.
	keep := [][]Pt{head}
	if k > 0 {
		keep = append(keep, scaleAbout(head, centroid(head), k))
	}
	if area(lowest) < area(head)/8 { // the cursor bar, not part of the head
		keep = append(keep, lowest)
	}

	var d strings.Builder
	for _, l := range keep {
		d.WriteString(path(toNodes(l, cornerDeg), true) + " ")
	}
	lo, hi := bboxOf(keep)
	return strings.TrimSpace(d.String()), lo, hi
}

// Cell boxes were read off the reference's own ink profile: two bands of
// artwork at y 20..200 and y 268..432, five columns each. 05 Rest is row one
// column five, 06 Trace is row two column one, 10 Prompt is row two column five.
type cell struct {
	name, out      string
	x0, y0, x1, y1 int
	colour         string // default render colour; empty means take the source's
}

// brandGreen is the app's ok-green pulled down in lightness and saturation, so
// it reads as a line colour rather than a status LED.
const brandGreen = "#5FA070"

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
		// How far the inner edge is pulled toward the centroid to thicken the
		// head outline for the tiny icon slices. Lower is heavier.
		smallK = flag.Float64("smallk", 0.80, "inner-edge scale for the small icon mark")
	)
	flag.Parse()

	fh, err := os.Open(*src)
	must(err)
	defer fh.Close()
	img, _, err := image.Decode(fh)
	must(err)

	cells := []cell{
		// 05 and 06 keep the reference's cream; the logo takes the brand green.
		{"rest", "rest.svg", 812, 32, 957, 196, ""},
		{"trace", "trace.svg", 35, 281, 169, 418, ""},
		{"prompt", "prompt.svg", 819, 282, 951, 417, brandGreen},
	}

	var promptPath string
	var promptLo, promptHi Pt

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
		must(os.WriteFile(filepath.Join(*out, c.out), []byte(svg), 0644))

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

		if c.name == "prompt" {
			promptPath, promptLo, promptHi = d, lo, hi
		}
	}

	// ---- the app icon ---------------------------------------------------
	// macOS canvas is 1024 with the squircle inset to 96..928. The art is
	// placed from its INK box rather than a nominal viewBox, then scaled to
	// cover fillFrac of the tile and centred on the tile's true centre.
	//
	// fillFrac is high for a thin-line mark on purpose: art size is the only
	// lever on apparent stroke weight in a verbatim trace, since thickening the
	// strokes would mean offsetting every outline and the result would no
	// longer be the reference artwork.
	const (
		canvas   = 1024.0
		tileFrom = 96.0
		tileTo   = 928.0
		fillFrac = 0.78
	)
	inkW, inkH := promptHi.X-promptLo.X, promptHi.Y-promptLo.Y
	scale := (tileTo - tileFrom) * fillFrac / math.Max(inkW, inkH)
	tx := canvas/2 - (promptLo.X+promptHi.X)/2*scale
	ty := canvas/2 - (promptLo.Y+promptHi.Y)/2*scale

	icon := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024"
     viewBox="0 0 1024 1024" role="img" aria-label="cats">
  <title>cats</title>
  <!-- cats app mark: mark 10 of the reference exploration, traced from the
       artwork itself and drawn in a dimmed brand green on the app's dark
       squircle tile. Regenerate with scripts/icon/gen-trace; the path data is
       machine-produced and should not be hand-edited.

       Sizing follows the macOS slot: 1024 canvas, squircle inset to 96..928,
       and the art scaled from its ink box to span %d%% of that tile, centred,
       so it clears the corner radius at every .icns slice. The mark is a
       thin-line design drawn deliberately large — art size is the only lever
       on apparent stroke weight in a verbatim trace. -->
  <defs>
    <linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="#2a3346"/>
      <stop offset="1" stop-color="#12151c"/>
    </linearGradient>
  </defs>
  <rect x="96" y="96" width="832" height="832" rx="188" fill="url(#bg)"/>
  <g transform="translate(%.2f %.2f) scale(%.5f)">
    <path d="%s" fill="%s" fill-rule="evenodd"/>
  </g>
</svg>
`, int(fillFrac*100), tx, ty, scale, promptPath, brandGreen)
	must(os.WriteFile(filepath.Join(*out, "cats-icon.svg"), []byte(icon), 0644))

	// ---- the small in-app glyph -----------------------------------------
	// The sidebar mark in cmd/catway/web/index.html is this path, inlined. It
	// is a separate simplification of the same trace rather than a reuse of the
	// icon's, because the icon needs full fidelity and the glyph needs to be
	// small enough to paste into a file that ships whole.
	//
	// The viewBox is left tight around the ink, which is what the sidebar CSS
	// derives its baseline nudge from — see the note beside #brand .mark. Move
	// the crop and that nudge has to be re-derived.
	uiPath, uiLo, uiHi := traceCell(img, 819, 282, 951, 417, *uiTol, *corn)
	const uiPad = 2.0
	glyph := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg"
     viewBox="%.2f %.2f %.2f %.2f" role="img" aria-label="cats">
  <title>cats</title>
  <!-- The sidebar glyph: mark 10 traced from the reference and simplified for
       small sizes. Generated by scripts/icon/gen-trace; if it is regenerated,
       the copy inlined in cmd/catway/web/index.html must be replaced too. -->
  <path d="%s" fill="currentColor" fill-rule="evenodd"/>
</svg>
`, uiLo.X-uiPad, uiLo.Y-uiPad, uiHi.X-uiLo.X+2*uiPad, uiHi.Y-uiLo.Y+2*uiPad, uiPath)
	must(os.WriteFile(filepath.Join(*out, "prompt-compact.svg"), []byte(glyph), 0644))

	// ---- the bold reduction for the 16 and 32px icon slices --------------
	// Drawn larger in its tile than the full mark: with the interior detail
	// gone there is no crowding, and at these sizes every extra pixel of art
	// is another pixel of stroke.
	smallD, sLo, sHi := smallMark(traceLoops(img, 819, 282, 951, 417, 1.4), *smallK, *corn)
	const smallFill = 0.82
	sScale := (tileTo - tileFrom) * smallFill / math.Max(sHi.X-sLo.X, sHi.Y-sLo.Y)
	sTx := canvas/2 - (sLo.X+sHi.X)/2*sScale
	sTy := canvas/2 - (sLo.Y+sHi.Y)/2*sScale

	small := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024"
     viewBox="0 0 1024 1024" role="img" aria-label="cats">
  <title>cats</title>
  <!-- The small-size app mark, used ONLY for the 16 and 32px .icns slices;
       everything from 64px up uses cats-icon.svg, which is the verbatim trace.

       Same silhouette, thickened. The verbatim head outline is a ribbon about
       2.65 units wide on a 127-unit head, which at 16px is roughly a fifth of a
       pixel — it renders as a smudge rather than a cat. Here the outer edge of
       that outline is kept and a copy shrunk toward its centroid is filled
       against it even-odd, turning the hairline into a heavy ring. Interior
       detail is dropped because at 16px it is sub-pixel and only adds mud.

       Regenerate with scripts/icon/gen-trace; -smallk controls the weight. -->
  <defs>
    <linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="#2a3346"/>
      <stop offset="1" stop-color="#12151c"/>
    </linearGradient>
  </defs>
  <rect x="96" y="96" width="832" height="832" rx="188" fill="url(#bg)"/>
  <g transform="translate(%.2f %.2f) scale(%.5f)">
    <path d="%s" fill="%s" fill-rule="evenodd"/>
  </g>
</svg>
`, sTx, sTy, sScale, smallD, brandGreen)
	must(os.WriteFile(filepath.Join(*out, "cats-icon-small.svg"), []byte(small), 0644))

	fmt.Printf("wrote rest.svg trace.svg prompt.svg prompt-compact.svg cats-icon.svg to %s\n", *out)
	fmt.Printf("glyph viewBox %.2f %.2f %.2f %.2f, path %d bytes\n",
		uiLo.X-uiPad, uiLo.Y-uiPad, uiHi.X-uiLo.X+2*uiPad, uiHi.Y-uiLo.Y+2*uiPad, len(uiPath))
}
