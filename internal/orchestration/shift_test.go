package orchestration

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/rohanthewiz/cats/internal/terminal"
)

// textGrid lays lines out one per row, space-padded, in default colours.
func textGrid(cols, rows int, lines []string) [][]terminal.Cell {
	g := make([][]terminal.Cell, rows)
	for y := range g {
		g[y] = make([]terminal.Cell, cols)
		if y < len(lines) {
			for x, r := range []rune(lines[y]) {
				if x < cols {
					g[y][x].Rune = string(r)
				}
			}
		}
	}
	return g
}

// logLines is n distinct lines of scrolling output. Neighbouring lines differ
// along most of their length, as real output does; lines that differ only in
// a counter would leave a plain diff cheap enough that no shift is chosen.
func logLines(from, n int) []string {
	words := []string{"compile", "link", "vet", "test", "fetch", "cache", "build", "ok",
		"github.com/rohanthewiz/cats/internal", "wire", "PASS", "0.231s", "(cached)"}
	out := make([]string, n)
	for i := range out {
		k := from + i
		line := fmt.Sprintf("%05d", k)
		for w := range 8 {
			line += " " + words[(k*5+w*3+k*k)%len(words)]
		}
		out[i] = line
	}
	return out
}

// The case shifts exist for: a full screen of output scrolls by one line.
// Without a shift every row changed; with one, only the new bottom line did.
func TestShiftedDiffIsSmall(t *testing.T) {
	const cols, rows = 200, 50
	cur := terminal.Cursor{Visible: true}
	prev := mkSnap(cols, rows, textGrid(cols, rows, logLines(0, rows)), cur)
	next := mkSnap(cols, rows, textGrid(cols, rows, logLines(1, rows)), cur)

	plain := FrameFromSnapshot(next, prev)
	plain.Sparsify()
	shifted := ShiftedFrameFromSnapshot(next, prev)
	shifted.Sparsify()
	if shifted.Shift == nil || shifted.Shift.Rows != 1 {
		t.Fatalf("shift = %+v, want 1 row", shifted.Shift)
	}
	pb, _ := json.Marshal(NewPaneFrame(1, plain))
	sb, _ := json.Marshal(NewPaneFrame(1, shifted))
	changed := 0
	for _, r := range shifted.Runs {
		changed += len(r.Cells)
	}
	if changed > cols {
		t.Errorf("a one-line scroll still sends %d cells (a row is %d)", changed, cols)
	}
	if len(pb) < 10*len(sb) {
		t.Errorf("plain %d B vs shifted %d B — expected a far larger gap", len(pb), len(sb))
	}
	t.Logf("one-line scroll on %dx%d: sparse %d B, sparse+shift %d B", cols, rows, len(pb), len(sb))
}

// Applying a shifted diff the way a receiver does (move up, fill, patch)
// reproduces the new screen exactly — including a pinned bottom row that did
// not scroll with the rest.
func TestShiftedDiffReproducesTheScreen(t *testing.T) {
	const cols, rows = 40, 8
	cur := terminal.Cursor{Visible: true}
	before := append(logLines(0, rows-1), "STATUS: building")
	after := append(logLines(3, rows-1), "STATUS: building")
	prev := mkSnap(cols, rows, textGrid(cols, rows, before), cur)
	next := mkSnap(cols, rows, textGrid(cols, rows, after), cur)

	f := ShiftedFrameFromSnapshot(next, prev)
	if f.Shift == nil || f.Shift.Rows != 3 {
		t.Fatalf("shift = %+v, want 3 rows", f.Shift)
	}
	grid := resolveCells(prev)
	k := copy(grid, grid[f.Shift.Rows*cols:])
	for i := k; i < len(grid); i++ {
		grid[i] = f.Shift.Fill
	}
	for i, c := range f.Cells {
		if !c.Skip {
			grid[i] = c
		}
	}
	want := resolveCells(next)
	for i := range want {
		if grid[i] != want[i] {
			t.Fatalf("cell %d (row %d) = %+v, want %+v", i, i/cols, grid[i], want[i])
		}
	}
}

// What must NOT shift: a screen that changed in place, and a mostly blank one
// whose blank rows match each other at every distance.
func TestNoShiftWithoutAScroll(t *testing.T) {
	const cols, rows = 40, 10
	cur := terminal.Cursor{Visible: true}
	cases := map[string][2][]string{
		"typing on one row": {{"$ ls"}, {"$ ls -la"}},
		"a redraw in place": {logLines(0, rows), logLines(100, rows)},
		"blank rows only":   {{"a", "", "", "", "b"}, {"", "", "", "", "", "", "", "c"}},
	}
	for name, c := range cases {
		prev := mkSnap(cols, rows, textGrid(cols, rows, c[0]), cur)
		next := mkSnap(cols, rows, textGrid(cols, rows, c[1]), cur)
		if f := ShiftedFrameFromSnapshot(next, prev); f.Shift != nil {
			t.Errorf("%s: shifted by %d", name, f.Shift.Rows)
		}
	}
}
