package browserproto

import "github.com/rohanthewiz/herdr-web/internal/orchestration"

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

// Translate converts one β frame into the message to send: a *PaneFrame when
// β sent full, no full has been emitted yet (first frame / after Reset), or
// the diff would exceed the full-fallback threshold; otherwise a *PaneDiff.
func (t *FrameTranslator) Translate(f *orchestration.Frame) any {
	changed := 0
	if !f.Full {
		for i := range f.Cells {
			if !f.Cells[i].Skip {
				changed++
			}
		}
	}
	if f.Full || !t.haveFull || changed*fullFallbackDen > len(f.Cells)*fullFallbackNum {
		return t.translateFull(f)
	}
	return t.translateDiff(f, changed)
}

func (t *FrameTranslator) translateFull(f *orchestration.Frame) *PaneFrame {
	fg, bg := dominantColors(f.Cells)
	out := &PaneFrame{
		T:      MsgPaneFrame,
		Pane:   t.pane,
		W:      f.Cols,
		H:      f.Rows,
		DefFg:  fg,
		DefBg:  bg,
		Cells:  make([]Cell, len(f.Cells)),
		Scroll: scrollFrom(f.Scroll),
	}
	if f.Cursor != nil {
		out.Cur = cursorFrom(f.Cursor)
	}
	if len(f.Hyperlinks) > 0 {
		out.Links = append([]string(nil), f.Hyperlinks...)
	}
	for i := range f.Cells {
		out.Cells[i] = cellFrom(f.Cells[i], fg, bg)
	}
	t.defFg, t.defBg, t.haveFull = fg, bg, true
	return out
}

func (t *FrameTranslator) translateDiff(f *orchestration.Frame, changed int) *PaneDiff {
	out := &PaneDiff{
		T:      MsgPaneDiff,
		Pane:   t.pane,
		Cells:  make([]DiffCell, 0, changed),
		Scroll: scrollFrom(f.Scroll),
	}
	if f.Cursor != nil {
		cur := cursorFrom(f.Cursor)
		out.Cur = &cur
	}
	for i := range f.Cells {
		if f.Cells[i].Skip {
			continue
		}
		out.Cells = append(out.Cells, DiffCell{I: i, Cell: cellFrom(f.Cells[i], t.defFg, t.defBg)})
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
// selection, and alt-screen gating the scrollbar. The rest stays server-side
// with the input encoder (D4).
func ModesFrom(m orchestration.PaneModes) PaneModes {
	return PaneModes{
		T:         MsgPaneModes,
		Pane:      m.PaneID,
		Mouse:     m.MouseMode != 0,
		AltScreen: m.AlternateScreen,
	}
}
