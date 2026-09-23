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
	pane uint32
	translatorState
}

// translatorState is everything a translation depends on besides the view.
// Two translators of one pane in the same state turn the same view into the
// same bytes, which is what lets ViewEncoder encode once for all of them.
type translatorState struct {
	defFg, defBg uint32
	haveFull     bool
	// shift: this connection applies PaneDiff.Shift (wire.FeaturePaneShift).
	// Without it, a shifted β frame is answered with a full frame, which is
	// exactly what scrolling output cost before shifts existed.
	shift bool
}

func NewFrameTranslator(pane uint32) *FrameTranslator {
	return &FrameTranslator{pane: pane}
}

// Reset forces the next Translate to emit a full pane_frame — used when the
// pane becomes visible in this connection's viewport (§8) or after a resync.
func (t *FrameTranslator) Reset() { t.haveFull = false }

// AllowShift records that the connection can apply PaneDiff.Shift.
func (t *FrameTranslator) AllowShift() { t.shift = true }

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
	// when Frame.Full. With a Shift they are relative to the SHIFTED grid:
	// every other cell equals the cell Shift rows below it in the previous
	// grid (or the fill, in the vacated rows).
	Changed []int
	// Shift is the rows the grid scrolled up by before Changed applied
	// (orchestration.Shift); 0 for none.
	Shift int
}

// DenseView is the view of a frame that carries its own grid: a full frame,
// or a diff in the base (dense, skip-flagged) shape.
func DenseView(f *orchestration.Frame) FrameView {
	v := FrameView{Frame: f, Cells: f.Cells}
	if f.Shift != nil && !f.Full {
		v.Shift = f.Shift.Rows
	}
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
	// What the last applied frame said besides its cells, kept so FullView
	// can stand in for a full frame without asking the daemon for one.
	cursor *orchestration.Cursor
	scroll *orchestration.ScrollInfo
	links  []string
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
		g.cursor, g.scroll, g.links = f.Cursor, f.Scroll, f.Hyperlinks
		return DenseView(f), true
	}
	if !g.valid || f.Full || f.Cols != g.cols || f.Rows != g.rows {
		g.valid = false
		return FrameView{}, false
	}
	v = FrameView{Frame: f, Cells: g.cells}
	if sh := f.Shift; sh != nil {
		// Scroll first, then patch: the runs were taken against the shifted
		// grid (orchestration.Shift). A shift the grid cannot hold is the same
		// disagreement as a run off the end — refuse and wait for a full frame.
		if sh.Rows <= 0 || sh.Rows >= int(g.rows) {
			g.valid = false
			return FrameView{}, false
		}
		cols := int(g.cols)
		k := copy(g.cells, g.cells[sh.Rows*cols:])
		for i := k; i < n; i++ {
			g.cells[i] = sh.Fill
		}
		v.Shift = sh.Rows
	}
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
	// Links only ever ride full frames (and the frame after them is full too),
	// so a diff means none are on screen.
	g.cursor, g.scroll, g.links = f.Cursor, f.Scroll, nil
	return v, true
}

// FullView is the grid's current screen as a full frame's view: what a
// translator needs to hand a window the whole screen right now, without a
// round trip to the daemon. ok is false while the grid is invalid.
//
// The view shares the grid's cells, so it must be translated before the next
// Apply — the same rule every view from Apply already follows.
func (g *Grid) FullView() (FrameView, bool) {
	if !g.valid {
		return FrameView{}, false
	}
	f := &orchestration.Frame{Cols: g.cols, Rows: g.rows, Full: true,
		Cursor: g.cursor, Scroll: g.scroll, Hyperlinks: g.links}
	return FrameView{Frame: f, Cells: g.cells}, true
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
	if f.Full || !t.haveFull {
		return t.translateFull(v)
	}
	changed := len(v.Changed)
	if v.Shift > 0 {
		if !t.shift {
			// Changed is relative to a shifted grid this client will never
			// hold, so no diff can describe the update to it.
			return t.translateFull(v)
		}
		// The vacated rows may cost cells of their own (see translateDiff);
		// counted as a whole so the fallback errs toward the full frame.
		changed += v.Shift * int(f.Cols)
	}
	if changed*fullFallbackDen > len(v.Cells)*fullFallbackNum {
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
	if v.Shift == 0 {
		for _, i := range v.Changed {
			out.Cells = append(out.Cells, DiffCell{I: i, Cell: cellFrom(v.Cells[i], t.defFg, t.defBg)})
		}
		return out
	}
	// A shift blanks the vacated rows to THIS connection's blank — a space in
	// its def_fg/def_bg — which need not be the β fill (the terminal's default
	// colours; def_fg/def_bg are the frame's dominant ones). So the vacated
	// rows are not trusted to Changed: every cell there that differs from the
	// browser's blank is sent, and Changed covers the rows above them.
	out.Shift = v.Shift
	vacated := (int(f.Rows) - v.Shift) * int(f.Cols)
	for _, i := range v.Changed {
		if i >= vacated {
			break // ascending, so the rest are all in the vacated rows
		}
		out.Cells = append(out.Cells, DiffCell{I: i, Cell: cellFrom(v.Cells[i], t.defFg, t.defBg)})
	}
	for i := vacated; i < len(v.Cells); i++ {
		if c := cellFrom(v.Cells[i], t.defFg, t.defBg); c != blankCell {
			out.Cells = append(out.Cells, DiffCell{I: i, Cell: c})
		}
	}
	return out
}

// blankCell is what a browser fills a shift's vacated rows with.
var blankCell = Cell{S: " "}

// ViewEncoder encodes one FrameView for every connection showing the pane,
// doing the work once per distinct translator state rather than once per
// connection.
//
// Connections that have been streaming the pane together sit in the same
// state (same def_fg/def_bg from the same last full frame, same features), so
// in the common multi-window case — a desktop and a phone on one workspace —
// every one after the first is a cache hit that costs a comparison and a
// state copy. A connection in a different state (it just gained the pane and
// needs a full frame) gets its own entry. Build one per view; not safe for
// concurrent use.
type ViewEncoder struct {
	view    *FrameView
	entries []encoded
}

type encoded struct {
	before, after translatorState
	b             []byte
}

func NewViewEncoder(v *FrameView) *ViewEncoder { return &ViewEncoder{view: v} }

// Encode translates the view for t and returns the bytes to send, advancing t
// exactly as TranslateView would have. The bytes are shared between the
// connections that hit the same entry, so they must not be modified.
func (e *ViewEncoder) Encode(t *FrameTranslator) ([]byte, error) {
	for i := range e.entries {
		if e.entries[i].before == t.translatorState {
			t.translatorState = e.entries[i].after
			return e.entries[i].b, nil
		}
	}
	before := t.translatorState
	b, err := MarshalFrame(t.TranslateView(e.view))
	if err != nil {
		return nil, err
	}
	e.entries = append(e.entries, encoded{before: before, after: t.translatorState, b: b})
	return b, nil
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
