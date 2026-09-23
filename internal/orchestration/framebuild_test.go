//go:build ghostty

package orchestration

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/rohanthewiz/cats/internal/terminal"
)

// changedCells expands a diff into index → cell: the frame's content with its
// run (or skip) structure taken out, so two diffs that split the same changes
// into different runs compare equal.
func changedCells(t *testing.T, f *Frame) map[int]Cell {
	t.Helper()
	out := map[int]Cell{}
	if f.Sparse {
		for _, r := range f.Runs {
			for k, c := range r.Cells {
				out[r.At+k] = c
			}
		}
		return out
	}
	for i, c := range f.Cells {
		if !c.Skip {
			out[i] = c
		}
	}
	return out
}

// FrameBuilder is FrameFromSnapshot with shortcuts; this is the proof that the
// shortcuts change nothing. Real emulator snapshots, so rows really are shared
// between them (the terminal's row cache), driven by random traffic that
// scrolls, recolours, resizes and adds links; four builders, one per
// combination of client features, each checked against the stateless
// reference for its combination on every step.
func TestFrameBuilderMatchesTheReference(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	e, err := terminal.New(24, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	ops := []func() string{
		func() string { return string(rune('a' + rng.Intn(26))) },
		func() string { return "some output text " },
		func() string { return "\r\n" },
		func() string { return fmt.Sprintf("line %d of the log with words\r\n", rng.Intn(1000)) },
		func() string { return fmt.Sprintf("\x1b[%d;%dH", 1+rng.Intn(9), 1+rng.Intn(25)) },
		func() string { return fmt.Sprintf("\x1b[%dm", []int{0, 1, 7, 31, 42, 95}[rng.Intn(6)]) },
		func() string { return fmt.Sprintf("\x1b[%dK", rng.Intn(3)) },
		func() string { return fmt.Sprintf("\x1b[%dS", 1+rng.Intn(3)) },
		func() string { return fmt.Sprintf("\x1b[%dL", 1+rng.Intn(2)) },
		func() string { return "\x1b]8;;https://example.com\x1b\\L\x1b]8;;\x1b\\" },
		func() string { return []string{"\x1b]11;#102030\x1b\\", "\x1b]11;#000000\x1b\\"}[rng.Intn(2)] },
		func() string { return "界" },
	}

	type variant struct {
		sparse, shift bool
		b             FrameBuilder
	}
	vs := []*variant{{false, false, FrameBuilder{}}, {true, false, FrameBuilder{}},
		{false, true, FrameBuilder{}}, {true, true, FrameBuilder{}}}
	var prev *terminal.Snapshot
	shifts, sharedSteps, empties := 0, 0, 0

	for step := range 1500 {
		switch k := rng.Intn(50); {
		case k == 0:
			_ = e.Resize(uint16(16+rng.Intn(16)), uint16(4+rng.Intn(6)))
		case k == 1:
			_ = e.Scroll(rng.Intn(7) - 3)
		default:
			for range 1 + rng.Intn(3) {
				_, _ = e.Write([]byte(ops[rng.Intn(len(ops))]()))
			}
		}
		cur, err := e.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if prev != nil && len(prev.Cells) > 0 && len(cur.Cells) == len(prev.Cells) && sharedRow(cur.Cells[0], prev.Cells[0]) {
			sharedSteps++
		}
		resync := rng.Intn(60) == 0
		for _, v := range vs {
			var got, want *Frame
			switch {
			case resync:
				got, want = v.b.Full(cur), FrameFromSnapshot(cur, nil)
			case v.shift:
				got, want = v.b.Diff(cur, v.sparse, true), ShiftedFrameFromSnapshot(cur, prev)
			default:
				got, want = v.b.Diff(cur, v.sparse, false), FrameFromSnapshot(cur, prev)
			}
			if v.sparse && !resync {
				want.Sparsify()
			}
			name := fmt.Sprintf("step %d sparse=%v shift=%v", step, v.sparse, v.shift)
			if got == nil {
				// Suppressed as empty: the reference must agree there was
				// nothing to say — no cell, and the same cursor and scroll.
				if want.Full || want.Shift != nil || len(changedCells(t, want)) != 0 ||
					*frameCursor(prev) != *want.Cursor || !reflect.DeepEqual(frameScroll(prev), want.Scroll) {
					t.Fatalf("%s: builder sent nothing, the reference sent a change", name)
				}
				empties++
				continue
			}
			if !want.Full && want.Shift == nil && len(changedCells(t, want)) == 0 &&
				*frameCursor(prev) == *want.Cursor && reflect.DeepEqual(frameScroll(prev), want.Scroll) {
				t.Fatalf("%s: an empty diff was sent", name)
			}
			if got.Full != want.Full || got.Sparse != want.Sparse || got.Cols != want.Cols || got.Rows != want.Rows {
				t.Fatalf("%s: full=%v/%v sparse=%v/%v", name, got.Full, want.Full, got.Sparse, want.Sparse)
			}
			if !reflect.DeepEqual(got.Shift, want.Shift) {
				t.Fatalf("%s: shift %+v, want %+v", name, got.Shift, want.Shift)
			}
			if got.Shift != nil && v.sparse {
				shifts++
			}
			if !reflect.DeepEqual(got.Cursor, want.Cursor) || !reflect.DeepEqual(got.Scroll, want.Scroll) ||
				!reflect.DeepEqual(got.Hyperlinks, want.Hyperlinks) {
				t.Fatalf("%s: cursor/scroll/links differ", name)
			}
			if got.Full || !got.Sparse {
				// Dense and full frames carry every cell: exactly equal.
				if !reflect.DeepEqual(got.Cells, want.Cells) {
					t.Fatalf("%s: cells differ from the reference", name)
				}
				continue
			}
			if g, w := changedCells(t, got), changedCells(t, want); !reflect.DeepEqual(g, w) {
				t.Fatalf("%s: %d changed cells, reference %d", name, len(g), len(w))
			}
		}
		prev = cur
	}
	if shifts < 5 || sharedSteps < 100 || empties < 20 {
		t.Fatalf("weak coverage: %d shifted sparse diffs, %d steps with shared rows, %d empty diffs",
			shifts, sharedSteps, empties)
	}
}
