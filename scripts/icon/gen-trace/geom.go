package main

import (
	"fmt"
	"math"
	"strings"
)

type Pt struct{ X, Y float64 }

func add(a, b Pt) Pt           { return Pt{a.X + b.X, a.Y + b.Y} }
func sub(a, b Pt) Pt           { return Pt{a.X - b.X, a.Y - b.Y} }
func scale(a Pt, s float64) Pt { return Pt{a.X * s, a.Y * s} }
func dist(a, b Pt) float64     { return math.Hypot(a.X-b.X, a.Y-b.Y) }
func fp(p Pt) string           { return fmt.Sprintf("%.2f,%.2f", p.X, p.Y) }

// Node is one point on a path. Corner nodes break curvature continuity, giving
// a sharp vertex (an ear tip, a faceted edge) instead of a rounded pass-through.
type Node struct {
	P      Pt
	Corner bool
}

func N(x, y float64) Node  { return Node{Pt{x, y}, false} } // smooth
func Cn(x, y float64) Node { return Node{Pt{x, y}, true} }  // corner

// openSpline emits the cubic Béziers interpolating p, using centripetal knot
// spacing (t advances by sqrt of chord length). Interior tangents use the
// standard non-uniform Catmull-Rom formula; the two ends are clamped to the
// one-sided difference so an open run leaves and enters straight.
func openSpline(p []Pt, withMove bool) string {
	n := len(p)
	var b strings.Builder
	if withMove {
		b.WriteString("M" + fp(p[0]))
	}
	if n == 2 {
		b.WriteString(" L" + fp(p[1]))
		return b.String()
	}
	t := make([]float64, n)
	for i := 1; i < n; i++ {
		t[i] = t[i-1] + math.Sqrt(dist(p[i], p[i-1]))
	}
	m := make([]Pt, n)
	for i := range p {
		switch i {
		case 0:
			m[i] = scale(sub(p[1], p[0]), 1/(t[1]-t[0]))
		case n - 1:
			m[i] = scale(sub(p[i], p[i-1]), 1/(t[i]-t[i-1]))
		default:
			in := scale(sub(p[i], p[i-1]), 1/(t[i]-t[i-1]))
			out := scale(sub(p[i+1], p[i]), 1/(t[i+1]-t[i]))
			span := scale(sub(p[i+1], p[i-1]), 1/(t[i+1]-t[i-1]))
			m[i] = add(sub(in, span), out)
		}
	}
	for i := 0; i < n-1; i++ {
		dt := t[i+1] - t[i]
		c1 := add(p[i], scale(m[i], dt/3))
		c2 := sub(p[i+1], scale(m[i+1], dt/3))
		fmt.Fprintf(&b, " C%s %s %s", fp(c1), fp(c2), fp(p[i+1]))
	}
	return b.String()
}

// closedSmooth splines a loop with no corner nodes, wrapping the tangent
// lookups so the seam is as smooth as every other point.
func closedSmooth(p []Pt) string {
	n := len(p)
	d := make([]float64, n) // d[i] = centripetal knot span from p[i] to p[i+1]
	for i := range p {
		d[i] = math.Sqrt(dist(p[(i+1)%n], p[i]))
	}
	m := make([]Pt, n)
	for i := range p {
		pr, nx := (i-1+n)%n, (i+1)%n
		in := scale(sub(p[i], p[pr]), 1/d[pr])
		out := scale(sub(p[nx], p[i]), 1/d[i])
		span := scale(sub(p[nx], p[pr]), 1/(d[pr]+d[i]))
		m[i] = add(sub(in, span), out)
	}
	var b strings.Builder
	b.WriteString("M" + fp(p[0]))
	for i := range p {
		j := (i + 1) % n
		c1 := add(p[i], scale(m[i], d[i]/3))
		c2 := sub(p[j], scale(m[j], d[i]/3))
		fmt.Fprintf(&b, " C%s %s %s", fp(c1), fp(c2), fp(p[j]))
	}
	return b.String() + " Z"
}

// openRuns splits a node list at its interior corners and splines each run
// separately, so curvature is continuous within a run and free at the joins.
func openRuns(nodes []Node) string {
	var out strings.Builder
	start, first := 0, true
	for i := 1; i < len(nodes); i++ {
		if !nodes[i].Corner && i != len(nodes)-1 {
			continue
		}
		run := nodes[start : i+1]
		pts := make([]Pt, len(run))
		for j, nd := range run {
			pts[j] = nd.P
		}
		out.WriteString(openSpline(pts, first))
		first, start = false, i
	}
	return out.String()
}

func path(nodes []Node, closed bool) string {
	if !closed {
		return openRuns(nodes)
	}
	corner := -1
	for i, nd := range nodes {
		if nd.Corner {
			corner = i
			break
		}
	}
	if corner < 0 {
		pts := make([]Pt, len(nodes))
		for i, nd := range nodes {
			pts[i] = nd.P
		}
		return closedSmooth(pts)
	}
	// Rotate the loop to begin on a corner and repeat that corner at the end,
	// which turns the closed path into one open run list without a seam.
	rot := make([]Node, 0, len(nodes)+1)
	for i := 0; i <= len(nodes); i++ {
		rot = append(rot, nodes[(i+corner)%len(nodes)])
	}
	return openRuns(rot) + " Z"
}

// symLoop mirrors a right-hand profile onto the left about x=50, so the two
// halves of a face are identical by construction rather than by eye.
func symLoop(top Node, right []Node, bottom Node) []Node {
	out := append([]Node{top}, right...)
	out = append(out, bottom)
	for i := len(right) - 1; i >= 0; i-- {
		r := right[i]
		out = append(out, Node{Pt{100 - r.P.X, r.P.Y}, r.Corner})
	}
	return out
}

func mirrorNodes(nodes []Node) []Node {
	out := make([]Node, len(nodes))
	for i, n := range nodes {
		out[i] = Node{Pt{100 - n.P.X, n.P.Y}, n.Corner}
	}
	return out
}

// arcCubics approximates a circular arc as cubics (k = 4/3·tan(Δθ/4)),
// sidestepping SVG arc flags entirely.
func arcCubics(c Pt, r, th0, th1 float64, withMove bool) string {
	at := func(th float64) Pt { return Pt{c.X + r*math.Cos(th), c.Y + r*math.Sin(th)} }
	dv := func(th float64) Pt { return Pt{-r * math.Sin(th), r * math.Cos(th)} }
	var b strings.Builder
	if withMove {
		b.WriteString("M" + fp(at(th0)))
	}
	n := int(math.Ceil(math.Abs(th1-th0) / (math.Pi / 4)))
	if n < 1 {
		n = 1
	}
	step := (th1 - th0) / float64(n)
	for i := 0; i < n; i++ {
		a0 := th0 + float64(i)*step
		a1 := a0 + step
		k := 4.0 / 3.0 * math.Tan((a1-a0)/4)
		p0, p1 := at(a0), at(a1)
		d0, d1 := dv(a0), dv(a1)
		fmt.Fprintf(&b, " C%s %s %s",
			fp(add(p0, scale(d0, k))), fp(sub(p1, scale(d1, k))), fp(p1))
	}
	return b.String()
}

// lens is a closed almond from two mirrored circular arcs — the eye shape.
func lens(cx, cy, hw, sag float64) string {
	off := (hw*hw - sag*sag) / (2 * sag)
	yc1 := cy + off
	r1 := yc1 - (cy - sag)
	yc2 := cy - off
	r2 := cy + sag - yc2
	return arcCubics(Pt{cx, yc1}, r1, math.Atan2(cy-yc1, -hw), math.Atan2(cy-yc1, hw), true) +
		arcCubics(Pt{cx, yc2}, r2, math.Atan2(cy-yc2, hw), math.Atan2(cy-yc2, -hw), false) + " Z"
}

func towards(v, o Pt, d float64) Pt {
	dx, dy := o.X-v.X, o.Y-v.Y
	l := math.Hypot(dx, dy)
	return Pt{v.X + dx/l*d, v.Y + dy/l*d}
}

// roundedPoly closes a polygon, softening each vertex with a quadratic of the
// given radius — the difference between machined and hand-cut corners.
func roundedPoly(pts []Pt, rad float64) string {
	n := len(pts)
	var b strings.Builder
	for i, v := range pts {
		a := towards(v, pts[(i-1+n)%n], rad)
		c := towards(v, pts[(i+1)%n], rad)
		if i == 0 {
			b.WriteString("M" + fp(a))
		} else {
			b.WriteString(" L" + fp(a))
		}
		b.WriteString(" Q" + fp(v) + " " + fp(c))
	}
	return b.String() + " Z"
}

func slash(a, b Pt, w float64) string {
	d := sub(b, a)
	l := math.Hypot(d.X, d.Y)
	nx, ny := -d.Y/l*w/2, d.X/l*w/2
	return roundedPoly([]Pt{
		{a.X + nx, a.Y + ny}, {b.X + nx, b.Y + ny},
		{b.X - nx, b.Y - ny}, {a.X - nx, a.Y - ny},
	}, 1.2)
}

const (
	wHead = 4.0 // outer contour weight
	wFeat = 3.3 // interior feature weight
	wHair = 2.2 // tear lines and other hairlines
)

func stroke(d string, w float64, color string) string {
	return fmt.Sprintf(`<path d="%s" fill="none" stroke="%s" stroke-width="%.2f" stroke-linecap="round" stroke-linejoin="round"/>`, d, color, w)
}

func fill(d, color string) string {
	return fmt.Sprintf(`<path d="%s" fill="%s" stroke="none"/>`, d, color)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
