package browserproto

import "github.com/rohanthewiz/cats/internal/orchestration"

// A β diff touching more than 3/5 (~60%) of cells is sent as a full frame —
// cheaper than the per-cell index overhead, and free to decide since β diffs
// carry the whole resolved grid (skip-flagged).
const fullFallbackNum, fullFallbackDen = 3, 5

// FrameTranslator converts one pane's β frame stream (skip-flag diffs,
// orchestration.FrameFromSnapshot) into browser pane_frame/pane_diff messages
// (sparse-index, D1; packed u32 colors pass through, D2).
//
// It is stateful per pane per connection: the def_fg/def_bg a full frame
// declares are what subsequent diff cells omit against, so they must stay
// fixed until the next full frame. Not safe for concurrent use.
type FrameTranslator struct {
	pane         uint32
	defFg, defBg uint32
	haveFull     bool
}

func NewFrameTranslator(pane uint32) *FrameTranslator {
	return &FrameTranslator{pane: pane}
}

// Reset forces the next Translate to emit a full pane_frame — used when the
// pane becomes visible in this connection's viewport (§8) or after a resync.
func (t *FrameTranslator) Reset() { t.haveFull = false }

// FrameView is one β frame resolved against the pane's whole grid — what a
// translator needs whichever shape the frame arrived in.
//
// A translator may have to answer a DIFF with a full browser frame (its
// connection just gained the pane, or most of the grid changed), so it needs
// every cell, not just the changed ones. A dense β diff carries them itself; a
// sparse one does not, and is resolved against the Grid catway keeps instead.
// Building the view once per β frame is also what lets every connection share
// the work: before, each connection's translator re-scanned the whole grid to
// count what changed.
type FrameView struct {
	Frame *orchestration.Frame // cursor, scroll, links, dimensions, Full
	Cells []orchestration.Cell // the whole grid after this frame, row-major
	// Changed lists the row-major indices this diff touched, ascending. Unused
	// when Frame.Full.
	Changed []int
}

// DenseView is the view of a frame that carries its own grid: a full frame,
// or a diff in the base (dense, skip-flagged) shape.
func DenseView(f *orchestration.Frame) FrameView {
	v := FrameView{Frame: f, Cells: f.Cells}
	if !f.Full {
		for i := range f.Cells {
			if !f.Cells[i].Skip {
				v.Changed = append(v.Changed, i)
			}
		}
	}
	return v
}

// Grid is catway's copy of one pane's resolved grid, kept so a sparse β diff
// (orchestration.Frame.Sparse) can be resolved into a FrameView. Owned by the
// orchestrator loop, like the translators that read the views it produces.
//
// It can be INVALID: before the first full frame, and whenever a frame was
// dropped unapplied (the pane was off every viewport). A sparse diff can only
// patch a grid that holds exactly the frame before it, so against an invalid
// grid it is refused, and the grid stays invalid until a full frame — which a
// pane entering a viewport always asks for (RequestResync) — or a dense diff,
// which carries the whole grid, rebuilds it.
type Grid struct {
	cols, rows uint16
	cells      []orchestration.Cell
	valid      bool
}

// Invalidate records that a frame went by unapplied.
func (g *Grid) Invalidate() { g.valid = false }

// Apply folds f into the grid and returns the view to translate. ok is false
// when f cannot be resolved (a sparse diff against an invalid grid, or one
// that does not fit it); the caller drops the frame and waits for a full one.
//
// The grid takes ownership of a dense frame's Cells rather than copying them:
// the frame was decoded for this call alone, and a later sparse diff patching
// them in place is exactly what the grid is for.
func (g *Grid) Apply(f *orchestration.Frame) (v FrameView, ok bool) {
	n := int(f.Cols) * int(f.Rows)
	if !f.Sparse {
		if len(f.Cells) != n {
			g.valid = false
			return FrameView{}, false
		}
		g.cols, g.rows, g.cells, g.valid = f.Cols, f.Rows, f.Cells, true
		return DenseView(f), true
	}
	if !g.valid || f.Full || f.Cols != g.cols || f.Rows != g.rows {
		g.valid = false
		return FrameView{}, false
	}
	v = FrameView{Frame: f, Cells: g.cells}
	for _, r := range f.Runs {
		if r.At < 0 || r.At+len(r.Cells) > n {
			// A run off the end of the grid means the two sides disagree about
			// what the grid is. Patching what fits would leave a screen that is
			// wrong in a way nothing will correct; refusing leaves it waiting
			// for the next full frame.
			g.valid = false
			return FrameView{}, false
		}
		copy(g.cells[r.At:], r.Cells)
		for k := range r.Cells {
			v.Changed = append(v.Changed, r.At+k)
		}
	}
	return v, true
}

// Translate converts one dense β frame into the message to send — see
// TranslateView, which it is a shorthand for.
func (t *FrameTranslator) Translate(f *orchestration.Frame) any {
	v := DenseView(f)
	return t.TranslateView(&v)
}

// TranslateView converts one resolved β frame into the message to send: a
// *PaneFrame when β sent full, no full has been emitted yet (first frame /
// after Reset), or the diff would exceed the full-fallback threshold;
// otherwise a *PaneDiff.
func (t *FrameTranslator) TranslateView(v *FrameView) any {
	f := v.Frame
	if f.Full || !t.haveFull || len(v.Changed)*fullFallbackDen > len(v.Cells)*fullFallbackNum {
		return t.translateFull(v)
	}
	return t.translateDiff(v)
}

func (t *FrameTranslator) translateFull(v *FrameView) *PaneFrame {
	f := v.Frame
	fg, bg := dominantColors(v.Cells)
	out := &PaneFrame{
		T:      MsgPaneFrame,
		Pane:   t.pane,
		W:      f.Cols,
		H:      f.Rows,
		DefFg:  fg,
		DefBg:  bg,
		Cells:  make([]Cell, len(v.Cells)),
		Scroll: scrollFrom(f.Scroll),
	}
	if f.Cursor != nil {
		out.Cur = cursorFrom(f.Cursor)
	}
	if len(f.Hyperlinks) > 0 {
		out.Links = append([]string(nil), f.Hyperlinks...)
	}
	for i := range v.Cells {
		out.Cells[i] = cellFrom(v.Cells[i], fg, bg)
	}
	t.defFg, t.defBg, t.haveFull = fg, bg, true
	return out
}

func (t *FrameTranslator) translateDiff(v *FrameView) *PaneDiff {
	f := v.Frame
	out := &PaneDiff{
		T:      MsgPaneDiff,
		Pane:   t.pane,
		Cells:  make([]DiffCell, 0, len(v.Changed)),
		Scroll: scrollFrom(f.Scroll),
	}
	if f.Cursor != nil {
		cur := cursorFrom(f.Cursor)
		out.Cur = &cur
	}
	for _, i := range v.Changed {
		out.Cells = append(out.Cells, DiffCell{I: i, Cell: cellFrom(v.Cells[i], t.defFg, t.defBg)})
	}
	return out
}

// cellFrom translates a resolved β cell, zeroing (⇒ omitting) colors equal to
// the frame defaults. β's link index becomes 1-based (0 = none).
func cellFrom(c orchestration.Cell, defFg, defBg uint32) Cell {
	out := Cell{S: c.Symbol, M: c.Modifier}
	if c.Fg != defFg {
		out.F = c.Fg
	}
	if c.Bg != defBg {
		out.B = c.Bg
	}
	if c.Hyperlink != nil {
		out.H = *c.Hyperlink + 1
	}
	return out
}

func cursorFrom(c *orchestration.Cursor) Cursor {
	return Cursor{X: c.X, Y: c.Y, Vis: c.Visible, Shape: c.Shape}
}

func scrollFrom(s *orchestration.ScrollInfo) *Scroll {
	if s == nil {
		return nil
	}
	return &Scroll{Off: s.OffsetFromBottom, Max: s.MaxOffsetFromBottom, Rows: s.ViewportRows}
}

// dominantColors picks the frame defaults that maximize color omission: the
// most frequent fg and bg across the grid (ties break to the smaller packed
// value for determinism). β cells arrive fully resolved, so the terminal's
// own defaults are unknown here — the mode is at least as good.
func dominantColors(cells []orchestration.Cell) (fg, bg uint32) {
	fgCount := make(map[uint32]int, 8)
	bgCount := make(map[uint32]int, 8)
	for i := range cells {
		fgCount[cells[i].Fg]++
		bgCount[cells[i].Bg]++
	}
	return dominant(fgCount), dominant(bgCount)
}

func dominant(counts map[uint32]int) uint32 {
	var best uint32
	bestN := 0
	for v, n := range counts {
		if n > bestN || (n == bestN && v < best) {
			best, bestN = v, n
		}
	}
	return best
}

// ModesFrom reduces β's full mode report to the display-relevant subset the
// browser needs (§3): mouse capture gating pointer handling vs native text
// selection, alt-screen gating the scrollbar, and the kitty keyboard flags
// gating ⌘-chord forwarding (see PaneModes.Kitty). The rest stays
// server-side with the input encoder (D4).
func ModesFrom(m orchestration.PaneModes) PaneModes {
	return PaneModes{
		T:         MsgPaneModes,
		Pane:      m.PaneID,
		Mouse:     m.MouseMode != 0,
		AltScreen: m.AlternateScreen,
		Kitty:     m.KittyKeyboardFlags,
	}
}
