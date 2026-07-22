package browserproto

import (
	"math/rand"
	"testing"

	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/terminal"
)

// pack mirrors β's packRGB (0x02_RR_GG_BB) for expectations.
func pack(c terminal.Color) uint32 {
	return 0x02000000 | uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
}

var (
	testDefFg = terminal.Color{R: 200, G: 200, B: 200}
	testDefBg = terminal.Color{}
	red       = terminal.Color{R: 0xcc, G: 0x66, B: 0x66}
)

func mkSnap(cols, rows uint16, cells [][]terminal.Cell, cur terminal.Cursor) *terminal.Snapshot {
	return &terminal.Snapshot{
		Cols: cols, Rows: rows, Cells: cells, Cursor: cur,
		DefaultFg: testDefFg, DefaultBg: testDefBg,
	}
}

func TestTranslateFullFrame(t *testing.T) {
	cells := [][]terminal.Cell{{
		{Rune: "h", Fg: &red, Bold: true},
		{Rune: ""},
		{Rune: "x"},
	}}
	cur := terminal.Cursor{X: 1, Y: 0, Visible: true, Style: terminal.CursorBar}
	bf := orchestration.FrameFromSnapshot(mkSnap(3, 1, cells, cur), nil)

	msg := NewFrameTranslator(7).Translate(bf)
	frame, ok := msg.(*PaneFrame)
	if !ok {
		t.Fatalf("first frame should be a *PaneFrame, got %T", msg)
	}
	if frame.T != MsgPaneFrame || frame.Pane != 7 || frame.W != 3 || frame.H != 1 {
		t.Fatalf("frame header = %+v", frame)
	}
	// Two of three cells use the terminal defaults, so they dominate.
	if frame.DefFg != pack(testDefFg) || frame.DefBg != pack(testDefBg) {
		t.Fatalf("defaults = %#x/%#x, want %#x/%#x",
			frame.DefFg, frame.DefBg, pack(testDefFg), pack(testDefBg))
	}
	h := frame.Cells[0]
	if h.S != "h" || h.F != pack(red) || h.B != 0 || h.M == 0 {
		t.Errorf("styled cell = %+v (want explicit fg, omitted bg, bold bit)", h)
	}
	blank := frame.Cells[1]
	if blank.S != " " || blank.F != 0 || blank.B != 0 || blank.M != 0 {
		t.Errorf("default blank cell should omit everything, got %+v", blank)
	}
	if frame.Cur != (Cursor{X: 1, Y: 0, Vis: true, Shape: 6}) {
		t.Errorf("cursor = %+v, want bar shape 6 at 1,0", frame.Cur)
	}
	if frame.Scroll != nil {
		t.Errorf("no scrollback ⇒ no scroll, got %+v", frame.Scroll)
	}
}

func TestTranslateDiff(t *testing.T) {
	cur := terminal.Cursor{Visible: true}
	prev := mkSnap(3, 1, [][]terminal.Cell{{{Rune: "a"}, {Rune: "b"}, {Rune: "c"}}}, cur)
	next := mkSnap(3, 1, [][]terminal.Cell{{{Rune: "a"}, {Rune: "X", Fg: &red}, {Rune: "c"}}}, cur)

	tr := NewFrameTranslator(7)
	tr.Translate(orchestration.FrameFromSnapshot(prev, nil))
	msg := tr.Translate(orchestration.FrameFromSnapshot(next, prev))

	diff, ok := msg.(*PaneDiff)
	if !ok {
		t.Fatalf("small change should be a *PaneDiff, got %T", msg)
	}
	if diff.T != MsgPaneDiff || diff.Pane != 7 {
		t.Fatalf("diff header = %+v", diff)
	}
	if len(diff.Cells) != 1 || diff.Cells[0].I != 1 {
		t.Fatalf("diff cells = %+v, want the single changed index 1", diff.Cells)
	}
	c := diff.Cells[0]
	if c.S != "X" || c.F != pack(red) || c.B != 0 {
		t.Errorf("diff cell = %+v (bg equals remembered default ⇒ omitted)", c)
	}
	if diff.Cur == nil || !diff.Cur.Vis {
		t.Errorf("diff cursor = %+v", diff.Cur)
	}
}

func TestDiffBeforeFullEmitsFull(t *testing.T) {
	cur := terminal.Cursor{}
	prev := mkSnap(2, 1, [][]terminal.Cell{{{Rune: "a"}, {Rune: "b"}}}, cur)
	next := mkSnap(2, 1, [][]terminal.Cell{{{Rune: "a"}, {Rune: "X"}}}, cur)
	bf := orchestration.FrameFromSnapshot(next, prev) // β diff

	// A fresh translator (this connection never saw a full) must upgrade it.
	if _, ok := NewFrameTranslator(1).Translate(bf).(*PaneFrame); !ok {
		t.Fatal("β diff before any full frame must be emitted full")
	}
}

func TestFullFallbackThreshold(t *testing.T) {
	row := func(runes string) [][]terminal.Cell {
		cells := make([]terminal.Cell, len(runes))
		for i, r := range runes {
			cells[i] = terminal.Cell{Rune: string(r)}
		}
		return [][]terminal.Cell{cells}
	}
	cur := terminal.Cursor{}
	base := mkSnap(10, 1, row("aaaaaaaaaa"), cur)

	// 6/10 changed: at the 60% boundary, not over ⇒ still a diff.
	tr := NewFrameTranslator(1)
	tr.Translate(orchestration.FrameFromSnapshot(base, nil))
	six := mkSnap(10, 1, row("XXXXXXaaaa"[0:10]), cur)
	if _, ok := tr.Translate(orchestration.FrameFromSnapshot(six, base)).(*PaneDiff); !ok {
		t.Error("60% changed should stay a diff (threshold is strict >)")
	}

	// 7/10 changed: over the threshold ⇒ upgraded to full.
	tr = NewFrameTranslator(1)
	tr.Translate(orchestration.FrameFromSnapshot(base, nil))
	seven := mkSnap(10, 1, row("XXXXXXXaaa"), cur)
	if _, ok := tr.Translate(orchestration.FrameFromSnapshot(seven, base)).(*PaneFrame); !ok {
		t.Error(">60% changed should be upgraded to a full frame")
	}
}

func TestResetForcesFull(t *testing.T) {
	cur := terminal.Cursor{}
	prev := mkSnap(2, 1, [][]terminal.Cell{{{Rune: "a"}, {Rune: "b"}}}, cur)
	next := mkSnap(2, 1, [][]terminal.Cell{{{Rune: "a"}, {Rune: "X"}}}, cur)

	tr := NewFrameTranslator(1)
	tr.Translate(orchestration.FrameFromSnapshot(prev, nil))
	tr.Reset() // pane left and re-entered the viewport
	if _, ok := tr.Translate(orchestration.FrameFromSnapshot(next, prev)).(*PaneFrame); !ok {
		t.Fatal("Reset must force the next frame full")
	}
}

func TestTranslateHyperlinksAndScroll(t *testing.T) {
	const url = "https://example.com/a"
	cells := [][]terminal.Cell{{{Rune: "l", Link: url}, {Rune: "x"}}}
	snap := mkSnap(2, 1, cells, terminal.Cursor{Visible: true})
	snap.HasHyperlinks = true
	snap.Scroll = terminal.ScrollMetrics{OffsetFromBottom: 3, MaxOffsetFromBottom: 90, ViewportRows: 1}

	msg := NewFrameTranslator(7).Translate(orchestration.FrameFromSnapshot(snap, nil))
	frame, ok := msg.(*PaneFrame)
	if !ok {
		t.Fatalf("link frames are always full, got %T", msg)
	}
	if len(frame.Links) != 1 || frame.Links[0] != url {
		t.Fatalf("links = %v", frame.Links)
	}
	if frame.Cells[0].H != 1 {
		t.Errorf("linked cell h = %d, want 1-based index 1", frame.Cells[0].H)
	}
	if frame.Cells[1].H != 0 {
		t.Errorf("unlinked cell h = %d, want 0 (omitted)", frame.Cells[1].H)
	}
	if frame.Scroll == nil || *frame.Scroll != (Scroll{Off: 3, Max: 90, Rows: 1}) {
		t.Errorf("scroll = %+v", frame.Scroll)
	}
}

func TestModesFrom(t *testing.T) {
	m := ModesFrom(orchestration.NewPaneModes(9, terminal.InputModes{
		AlternateScreen: true,
		MouseMode:       terminal.MouseButtonMotion,
	}))
	if m.T != MsgPaneModes || m.Pane != 9 || !m.Mouse || !m.AltScreen {
		t.Fatalf("modes = %+v", m)
	}
	m = ModesFrom(orchestration.NewPaneModes(9, terminal.InputModes{}))
	if m.Mouse || m.AltScreen {
		t.Fatalf("plain shell modes = %+v", m)
	}
}

// --- Property test (task 2.3): replay a β full+diff sequence and assert the
// browser-side reconstruction equals the β-side fold, frame by frame. ---------

// rcell is a fully resolved browser-side cell.
type rcell struct {
	s    string
	f, b uint32
	m    uint16
	link string
}

// recon is the JS client's grid fold, in Go: it applies pane_frame/pane_diff
// exactly as the spec tells the browser to (resolve omitted colors against the
// last full frame's defaults, 1-based link indices into the last Links table).
type recon struct {
	w, h         int
	defFg, defBg uint32
	links        []string
	cells        []rcell
	cur          Cursor
}

func (r *recon) resolve(c Cell) rcell {
	out := rcell{s: c.S, f: c.F, b: c.B, m: c.M}
	if c.F == 0 {
		out.f = r.defFg
	}
	if c.B == 0 {
		out.b = r.defBg
	}
	if c.H > 0 {
		out.link = r.links[c.H-1]
	}
	return out
}

func (r *recon) apply(t *testing.T, msg any) {
	t.Helper()
	switch m := msg.(type) {
	case *PaneFrame:
		if len(m.Cells) != int(m.W)*int(m.H) {
			t.Fatalf("full frame cells = %d, want %d*%d", len(m.Cells), m.W, m.H)
		}
		r.w, r.h = int(m.W), int(m.H)
		r.defFg, r.defBg = m.DefFg, m.DefBg
		r.links = m.Links
		r.cells = make([]rcell, len(m.Cells))
		for i, c := range m.Cells {
			r.cells[i] = r.resolve(c)
		}
		r.cur = m.Cur
	case *PaneDiff:
		prev := -1
		for _, dc := range m.Cells {
			if dc.I <= prev || dc.I >= len(r.cells) {
				t.Fatalf("diff index %d out of order or bounds (prev %d, grid %d)", dc.I, prev, len(r.cells))
			}
			prev = dc.I
			if dc.H != 0 {
				t.Fatalf("diff cell carries link index %d — link frames must be full", dc.H)
			}
			r.cells[dc.I] = r.resolve(dc.Cell)
		}
		if m.Cur != nil {
			r.cur = *m.Cur
		}
	default:
		t.Fatalf("unexpected message %T", msg)
	}
}

// betaFold is the β-side fold (what Rust's compositor holds): full frames
// replace the grid; diffs apply only non-skip cells. β diffs never carry
// links (link-bearing frames are always full), so an applied diff cell
// clears any prior link — same as the browser's resolve of h=0.
type betaFold []rcell

func (g *betaFold) apply(f *orchestration.Frame) {
	if f.Full {
		out := make([]rcell, len(f.Cells))
		for i, c := range f.Cells {
			out[i] = rcell{s: c.Symbol, f: c.Fg, b: c.Bg, m: c.Modifier}
			if c.Hyperlink != nil {
				out[i].link = f.Hyperlinks[*c.Hyperlink]
			}
		}
		*g = out
		return
	}
	for i, c := range f.Cells {
		if c.Skip {
			continue
		}
		(*g)[i] = rcell{s: c.Symbol, f: c.Fg, b: c.Bg, m: c.Modifier}
	}
}

func TestReplayReconstruction(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	palette := []*terminal.Color{
		nil, nil, nil, // weight toward default colors (the dominant real case)
		{R: 0xcc, G: 0x66, B: 0x66},
		{R: 0x66, G: 0xcc, B: 0x66},
		{R: 0x66, G: 0x66, B: 0xcc},
	}
	symbols := []string{"a", "b", "c", "x", " ", "▀", "世"}
	randCell := func(withLink bool) terminal.Cell {
		c := terminal.Cell{
			Rune: symbols[rng.Intn(len(symbols))],
			Fg:   palette[rng.Intn(len(palette))],
			Bg:   palette[rng.Intn(len(palette))],
			Bold: rng.Intn(4) == 0,
		}
		if withLink && rng.Intn(3) == 0 {
			c.Link = "https://example.com/" + symbols[rng.Intn(4)]
		}
		return c
	}

	cols, rows := uint16(8), uint16(4)
	newGrid := func(withLinks bool) [][]terminal.Cell {
		g := make([][]terminal.Cell, rows)
		for y := range g {
			g[y] = make([]terminal.Cell, cols)
			for x := range g[y] {
				g[y][x] = randCell(withLinks)
			}
		}
		return g
	}
	// copyGrid snapshots the mutable grid so prev keeps its own cells.
	copyGrid := func(g [][]terminal.Cell) [][]terminal.Cell {
		out := make([][]terminal.Cell, len(g))
		for y := range g {
			out[y] = append([]terminal.Cell(nil), g[y]...)
		}
		return out
	}

	grid := newGrid(false)
	var prev *terminal.Snapshot
	tr := NewFrameTranslator(9)
	var rc recon
	var fold betaFold
	fulls, diffs, linkSteps := 0, 0, 0

	for step := range 60 {
		withLinks := step%13 == 5 // periodic link-bearing frames
		switch {
		case step > 0 && rng.Intn(10) == 0: // resize
			cols = uint16(6 + rng.Intn(5))
			rows = uint16(3 + rng.Intn(3))
			grid = newGrid(withLinks)
		default:
			n := rng.Intn(int(cols)*int(rows) + 1) // 0..all cells, crossing the 60% fallback
			for range n {
				grid[rng.Intn(int(rows))][rng.Intn(int(cols))] = randCell(withLinks)
			}
			if !withLinks {
				// Clear leftover links, changing the rune with them: β's skip
				// comparison ignores Link (resolveCell), so a link removed with
				// no other change would never be propagated by a diff. Real
				// emulators drop links alongside content changes; "·" is not in
				// the symbol set, so the cell is guaranteed to differ.
				for y := range grid {
					for x := range grid[y] {
						if grid[y][x].Link != "" {
							grid[y][x].Link = ""
							grid[y][x].Rune = "·"
						}
					}
				}
			}
		}

		snap := mkSnap(cols, rows, copyGrid(grid), terminal.Cursor{
			X:       uint16(rng.Intn(int(cols))),
			Y:       uint16(rng.Intn(int(rows))),
			Visible: rng.Intn(4) != 0,
			Style:   terminal.CursorStyle(rng.Intn(4)),
		})
		for y := range snap.Cells {
			for x := range snap.Cells[y] {
				if snap.Cells[y][x].Link != "" {
					snap.HasHyperlinks = true
				}
			}
		}
		if maxOff := rng.Intn(6); maxOff > 0 {
			snap.Scroll = terminal.ScrollMetrics{
				OffsetFromBottom:    rng.Intn(maxOff + 1),
				MaxOffsetFromBottom: maxOff,
				ViewportRows:        int(rows),
			}
		}

		bf := orchestration.FrameFromSnapshot(snap, prev)
		msg := tr.Translate(bf)
		switch msg.(type) {
		case *PaneFrame:
			fulls++
		case *PaneDiff:
			diffs++
		}
		if snap.HasHyperlinks {
			linkSteps++
		}
		rc.apply(t, msg)
		fold.apply(bf)

		if rc.w != int(bf.Cols) || rc.h != int(bf.Rows) {
			t.Fatalf("step %d: recon dims %dx%d, β %dx%d", step, rc.w, rc.h, bf.Cols, bf.Rows)
		}
		if len(rc.cells) != len(fold) {
			t.Fatalf("step %d: recon %d cells, β fold %d", step, len(rc.cells), len(fold))
		}
		for i := range fold {
			if rc.cells[i] != fold[i] {
				t.Fatalf("step %d (%T): cell %d = %+v, want %+v", step, msg, i, rc.cells[i], fold[i])
			}
		}
		if wantCur := (Cursor{X: bf.Cursor.X, Y: bf.Cursor.Y, Vis: bf.Cursor.Visible, Shape: bf.Cursor.Shape}); rc.cur != wantCur {
			t.Fatalf("step %d: cursor = %+v, want %+v", step, rc.cur, wantCur)
		}

		prev = snap
	}

	// The run must exercise all paths, or the property proves nothing.
	if fulls < 3 || diffs < 3 || linkSteps < 2 {
		t.Fatalf("weak coverage: %d fulls, %d diffs, %d link steps", fulls, diffs, linkSteps)
	}
}
