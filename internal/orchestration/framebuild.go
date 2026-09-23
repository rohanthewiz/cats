package orchestration

import "github.com/rohanthewiz/cats/internal/terminal"

// FrameBuilder turns one pane's successive snapshots into frames, keeping the
// previous frame's resolved grid so each diff costs what changed rather than
// what is on screen.
//
// FrameFromSnapshot is the reference: stateless, it resolves both snapshots in
// full and compares every cell, which on a 200×50 pane was ~350 µs per flush
// tick even when one character changed. The builder produces the same frames
// (see TestFrameBuilderMatchesTheReference) with three shortcuts:
//
//  1. It keeps prev already RESOLVED (cells), so prev is never resolved again.
//  2. A row the emulator shared between the two snapshots (terminal's row
//     cache hands back the same slice for a row it did not touch) is equal by
//     construction: it is copied from prev's resolved row, not resolved and
//     not compared.
//  3. A sparse diff is gathered straight into runs, without first building the
//     dense grid with skip flags that Sparsify would then take apart.
//
// Shortcut 2, row by row:
//
//	snapshot rows   cur.Cells[y] and prev.Cells[y] the same backing array?
//	  y=0  shared  ──▶  copy prev's resolved row, nothing to compare
//	  y=1  shared  ──▶  copy
//	  y=2  new     ──▶  resolve 200 cells, compare against prev's row
//	  …
//
// The two buffers (cells, spare) swap on every diff, so a busy pane stops
// allocating a grid per tick. Neither is ever handed out: a full frame gets
// its own Cells, a dense diff its own copy, a sparse diff copies its runs.
//
// Not safe for concurrent use; the host serialises it under the pane's
// frameMu.
type FrameBuilder struct {
	prev  *terminal.Snapshot
	cells []Cell // prev resolved, row-major, no Skip — what the receiver holds
	spare []Cell // the buffer the next diff resolves into
}

// Full returns a full frame for cur and makes cur the base the next diff is
// taken against — the re-baseline a resync needs.
func (b *FrameBuilder) Full(cur *terminal.Snapshot) *Frame {
	f := FrameFromSnapshot(cur, nil)
	buf := b.buffer(len(f.Cells))
	copy(buf, f.Cells)
	b.prev, b.cells, b.spare = cur, buf, b.cells
	return f
}

// Diff returns the frame that takes a receiver holding the last frame to cur,
// and makes cur the new base. sparse and shift are the client's features
// (ClientFeatureSparseFrames, ClientFeatureShiftFrames); the frame is exactly
// what FrameFromSnapshot / ShiftedFrameFromSnapshot followed by Sparsify would
// have produced, run boundaries aside.
func (b *FrameBuilder) Diff(cur *terminal.Snapshot, sparse, shift bool) *Frame {
	prev := b.prev
	// Full for the same reasons FrameFromSnapshot is: nothing to diff against,
	// new dimensions, or links on screen now or on the frame before.
	if prev == nil || prev.Cols != cur.Cols || prev.Rows != cur.Rows ||
		cur.HasHyperlinks || prev.HasHyperlinks || len(b.cells) != int(cur.Cols)*int(cur.Rows) {
		return b.Full(cur)
	}
	cols, rows := int(cur.Cols), int(cur.Rows)
	cells := b.buffer(cols * rows)
	// A shared row is only equal if it resolves the same way, and a cell with
	// no colour of its own resolves to the snapshot's defaults — which an OSC
	// 10/11 can change without touching a single row.
	sameDefaults := cur.DefaultFg == prev.DefaultFg && cur.DefaultBg == prev.DefaultBg

	changed := 0                  // cells that differ from prev in place
	touched := make([]bool, rows) // rows resolved (not copied) this time
	for y := range rows {
		row, base := cells[y*cols:(y+1)*cols], b.cells[y*cols:(y+1)*cols]
		var src []terminal.Cell
		if y < len(cur.Cells) {
			src = cur.Cells[y]
		}
		if sameDefaults && y < len(prev.Cells) && sharedRow(src, prev.Cells[y]) {
			copy(row, base)
			continue
		}
		touched[y] = true
		for x := range row {
			var c terminal.Cell // out of range reads as blank, as Snapshot.At does
			if x < len(src) {
				c = src[x]
			}
			row[x] = resolveCell(cur, c)
			if row[x] != base[x] {
				changed++
			}
		}
	}

	f := &Frame{Cols: cur.Cols, Rows: cur.Rows, Cursor: frameCursor(cur), Scroll: frameScroll(cur)}
	base := b.cells
	if shift {
		fill := Cell{Symbol: " ", Fg: packRGB(cur.DefaultFg), Bg: packRGB(cur.DefaultBg)}
		if n, shifted := chooseShift(cells, b.cells, cols, rows, fill, changed); n > 0 {
			base = shifted
			f.Shift = &Shift{Rows: n, Fill: fill}
			// Against a shifted base every row can differ, copied or not.
			for y := range touched {
				touched[y] = true
			}
		}
	}
	if sparse {
		f.Sparse = true
		f.Runs = gatherRuns(cells, base, cols, touched)
	} else {
		f.Cells = make([]Cell, len(cells))
		copy(f.Cells, cells)
		for i := range f.Cells {
			if f.Cells[i] == base[i] {
				f.Cells[i].Skip = true
			}
		}
	}
	b.prev, b.cells, b.spare = cur, cells, b.cells
	return f
}

// buffer returns the spare buffer at length n, growing it when it is short.
func (b *FrameBuilder) buffer(n int) []Cell {
	if cap(b.spare) < n {
		b.spare = make([]Cell, n)
	}
	return b.spare[:n]
}

// sharedRow reports whether two snapshot rows are the same slice — the
// emulator's way of saying the row did not change (terminal.Snapshot: rows may
// be shared, and are never written).
func sharedRow(a, b []terminal.Cell) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// gatherRuns collects the cells that differ from base into runs, looking only
// at the touched rows: an untouched row was copied from base and cannot
// differ. A run is closed at an untouched row and otherwise continues across a
// row boundary, as Sparsify's do. Each run gets its own copy of its cells, since
// cur is a buffer the next diff will overwrite.
func gatherRuns(cur, base []Cell, cols int, touched []bool) []CellRun {
	var runs []CellRun
	start := -1 // first index of the open run, or -1
	closeRun := func(end int) {
		if start >= 0 {
			runs = append(runs, CellRun{At: start, Cells: append([]Cell(nil), cur[start:end]...)})
			start = -1
		}
	}
	for y, t := range touched {
		if !t {
			closeRun(y * cols)
			continue
		}
		for i := y * cols; i < (y+1)*cols; i++ {
			if cur[i] != base[i] {
				if start < 0 {
					start = i
				}
			} else {
				closeRun(i)
			}
		}
	}
	closeRun(len(cur))
	return runs
}
