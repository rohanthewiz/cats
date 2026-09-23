package orchestration

// Scroll detection for diff frames (Frame.Shift).
//
// The question is "did the screen move up by n rows since the last frame?",
// answered cheaply enough to ask on every flush of a busy pane:
//
//  1. Count what changed in place. Fewer than minShiftGainRows rows' worth
//     cannot be saved by any shift, so typing, spinners and status clocks
//     — the overwhelming majority of frames — stop here, before any hashing.
//  2. Hash every row of both grids and let each new row vote for the shift
//     that would explain it: new row r matching old row j (j > r) votes for
//     n = j-r. Only old rows whose hash is unique vote, because blank rows
//     (the commonest row there is) match each other at every distance and
//     would drown the real signal.
//  3. Build the old grid moved up by the winning n, count what changes
//     against THAT, and keep the shift only when it saves at least
//     minShiftGainRows rows' worth of cells over the plain diff.
//
//	old          new (after `cat` printed two lines)
//	0 aaa        0 ccc   ← old 2   votes n=2
//	1 bbb        1 ddd   ← old 3   votes n=2
//	2 ccc        2 eee   ← old 4   votes n=2
//	3 ddd        3 fff   new
//	4 eee        4 ggg   new
//
// Step 3 is what keeps this honest: a vote only proposes, the cell count
// decides, so a wrong or coincidental winner costs one extra comparison pass
// and is then discarded.
//
// Only a whole-grid shift is detected. A program with a scroll region (a
// pinned status bar, vim's command line) shifts part of the screen; the rows
// outside the region then simply come out as changed cells, which is a few
// rows against the whole grid a plain diff would have sent.

// minShiftGainRows is how many rows' worth of cells a shift must save before
// it is used. More than a single row, because the receiver pays for a shift
// with a full repaint of the pane, where a small plain diff repaints only the
// rows it touches.
const minShiftGainRows = 2

// chooseShift reports the shift (rows moved up) that best explains cur given
// prev, and prev moved up by it with the vacated rows set to fill. n is 0 —
// and shifted nil — when no shift saves enough to be worth sending.
func chooseShift(cur, prev []Cell, cols, rows int, fill Cell) (n int, shifted []Cell) {
	if cols <= 0 || rows < 3 || len(cur) != cols*rows || len(prev) != cols*rows {
		return 0, nil
	}
	plain := 0
	for i := range cur {
		if cur[i] != prev[i] {
			plain++
		}
	}
	gain := minShiftGainRows * cols
	if plain < gain {
		return 0, nil
	}
	n = likelyShift(cur, prev, cols, rows)
	if n == 0 {
		return 0, nil
	}
	shifted = shiftCells(prev, cols, n, fill)
	changed := 0
	for i := range cur {
		if cur[i] != shifted[i] {
			changed++
		}
	}
	if changed+gain > plain {
		return 0, nil
	}
	return n, shifted
}

// shiftCells returns a copy of grid moved up by n rows, the bottom n filled.
// The same operation every receiver performs, so the diff is taken against
// exactly the grid the receiver will hold before the changed cells land.
func shiftCells(grid []Cell, cols, n int, fill Cell) []Cell {
	out := make([]Cell, len(grid))
	k := copy(out, grid[n*cols:])
	for i := k; i < len(out); i++ {
		out[i] = fill
	}
	return out
}

// likelyShift is step 2 above: the upward shift most new rows vote for, or 0
// when fewer than two do. Two, not one: a single matching row is as likely a
// repeated line (a separator, a blank-ish prompt) as a scroll.
func likelyShift(cur, prev []Cell, cols, rows int) int {
	oldAt := make(map[uint64]int, rows) // row hash → the old row, or -1 if not unique
	oldHash := make([]uint64, rows)
	for j := range rows {
		h := hashRow(prev[j*cols : (j+1)*cols])
		oldHash[j] = h
		if _, seen := oldAt[h]; seen {
			oldAt[h] = -1
		} else {
			oldAt[h] = j
		}
	}
	votes := make([]int, rows)
	for r := range rows {
		h := hashRow(cur[r*cols : (r+1)*cols])
		if h == oldHash[r] {
			continue // unchanged in place: says nothing about a shift
		}
		if j, ok := oldAt[h]; ok && j > r {
			votes[j-r]++
		}
	}
	best := 0
	for n := 1; n < rows; n++ {
		if votes[n] > votes[best] {
			best = n
		}
	}
	if votes[best] < 2 {
		return 0
	}
	return best
}

// hashRow is FNV-1a over everything that makes two cells equal for a diff
// (Skip and Hyperlink excluded: a diff's cells carry neither). A collision
// can only cost a wasted comparison pass, never a wrong frame — chooseShift
// re-compares cell by cell before it commits.
func hashRow(row []Cell) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	mix := func(v uint64) { h ^= v; h *= prime }
	for i := range row {
		c := &row[i]
		for k := 0; k < len(c.Symbol); k++ {
			mix(uint64(c.Symbol[k]))
		}
		// The separator keeps "ab"+"c" and "a"+"bc" apart across cells.
		mix(0xff)
		mix(uint64(c.Fg))
		mix(uint64(c.Bg))
		mix(uint64(c.Modifier))
	}
	return h
}
