// verify.go measures how closely the traced vectors reproduce the source
// pixels, by rasterising the emitted path data back onto the source's own grid
// and comparing ink masks.
//
// This checks the artwork that actually ships: the path string is parsed and
// flattened rather than the intermediate polygons being reused, so a bug in
// curve emission would show up here instead of hiding behind the data it came
// from. The score is intersection-over-union of the two ink masks.
package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// flatten parses the subset of path syntax this program emits (M, L, C, Q, Z)
// and returns each subpath as a dense polygon.
func flatten(d string) [][]Pt {
	toks := strings.FieldsFunc(d, func(r rune) bool { return r == ' ' || r == ',' || r == '\n' })
	var polys [][]Pt
	var cur []Pt
	var pos Pt
	num := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

	const steps = 24
	cubic := func(p0, p1, p2, p3 Pt) {
		for i := 1; i <= steps; i++ {
			t := float64(i) / steps
			u := 1 - t
			cur = append(cur, Pt{
				u*u*u*p0.X + 3*u*u*t*p1.X + 3*u*t*t*p2.X + t*t*t*p3.X,
				u*u*u*p0.Y + 3*u*u*t*p1.Y + 3*u*t*t*p2.Y + t*t*t*p3.Y,
			})
		}
	}
	quad := func(p0, p1, p2 Pt) {
		for i := 1; i <= steps; i++ {
			t := float64(i) / steps
			u := 1 - t
			cur = append(cur, Pt{
				u*u*p0.X + 2*u*t*p1.X + t*t*p2.X,
				u*u*p0.Y + 2*u*t*p1.Y + t*t*p2.Y,
			})
		}
	}

	for i := 0; i < len(toks); {
		switch t := toks[i]; {
		case strings.HasPrefix(t, "M"):
			if len(cur) > 2 {
				polys = append(polys, cur)
			}
			x := num(strings.TrimPrefix(t, "M"))
			y := num(toks[i+1])
			pos = Pt{x, y}
			cur = []Pt{pos}
			i += 2
		case strings.HasPrefix(t, "C"):
			p1 := Pt{num(strings.TrimPrefix(t, "C")), num(toks[i+1])}
			p2 := Pt{num(toks[i+2]), num(toks[i+3])}
			p3 := Pt{num(toks[i+4]), num(toks[i+5])}
			cubic(pos, p1, p2, p3)
			pos = p3
			i += 6
		case strings.HasPrefix(t, "Q"):
			p1 := Pt{num(strings.TrimPrefix(t, "Q")), num(toks[i+1])}
			p2 := Pt{num(toks[i+2]), num(toks[i+3])}
			quad(pos, p1, p2)
			pos = p2
			i += 4
		case strings.HasPrefix(t, "L"):
			p := Pt{num(strings.TrimPrefix(t, "L")), num(toks[i+1])}
			cur = append(cur, p)
			pos = p
			i += 2
		case t == "Z":
			if len(cur) > 2 {
				polys = append(polys, cur)
			}
			cur = nil
			i++
		default:
			i++
		}
	}
	if len(cur) > 2 {
		polys = append(polys, cur)
	}
	return polys
}

// rasterise fills the polygons even-odd onto a w*h mask whose origin is (ox,oy)
// in the same coordinate space as the polygons.
func rasterise(polys [][]Pt, ox, oy, w, h int) []bool {
	// Sampling happens at INTEGER coordinates, because the tracer's coordinate
	// space puts pixel centres on integers: field index p maps back to source
	// pixel p. Sampling at p+0.5 instead — the usual convention when integers
	// sit on pixel corners — shifts the whole comparison half a pixel diagonally
	// and costs about a quarter of the score on a three-pixel-wide stroke.
	mask := make([]bool, w*h)
	for row := 0; row < h; row++ {
		yc := float64(oy + row)
		var xs []float64
		for _, poly := range polys {
			n := len(poly)
			for i := 0; i < n; i++ {
				a, b := poly[i], poly[(i+1)%n]
				if (a.Y <= yc) == (b.Y <= yc) {
					continue
				}
				t := (yc - a.Y) / (b.Y - a.Y)
				xs = append(xs, a.X+t*(b.X-a.X))
			}
		}
		if len(xs) < 2 {
			continue
		}
		for i := 0; i < len(xs); i++ {
			for j := i + 1; j < len(xs); j++ {
				if xs[j] < xs[i] {
					xs[i], xs[j] = xs[j], xs[i]
				}
			}
		}
		for k := 0; k+1 < len(xs); k += 2 {
			x0 := int(math.Ceil(xs[k] - float64(ox)))
			x1 := int(math.Floor(xs[k+1] - float64(ox)))
			for x := x0; x <= x1; x++ {
				if x >= 0 && x < w {
					mask[row*w+x] = true
				}
			}
		}
	}
	return mask
}

func iou(a, b []bool) (score float64, inter, union int) {
	for i := range a {
		if a[i] || b[i] {
			union++
		}
		if a[i] && b[i] {
			inter++
		}
	}
	if union == 0 {
		return 1, 0, 0
	}
	return float64(inter) / float64(union), inter, union
}

func reportMatch(name, d string, srcMask []bool, ox, oy, w, h int) {
	got := rasterise(flatten(d), ox, oy, w, h)
	score, inter, union := iou(got, srcMask)
	var srcOnly, gotOnly int
	for i := range got {
		if srcMask[i] && !got[i] {
			srcOnly++
		}
		if got[i] && !srcMask[i] {
			gotOnly++
		}
	}
	fmt.Printf("%-7s IoU %.4f   shared %d  missed %d  added %d  (union %d)\n",
		name, score, inter, srcOnly, gotOnly, union)
}
