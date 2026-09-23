//go:build ghostty

package terminal

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

// Snapshot reuses the rows libghostty reports clean. The property that makes
// that safe: after ANY sequence of terminal traffic, the snapshot built with
// the cache is identical to one read from scratch.
//
// The traffic is random but aimed at everything that moves rows without
// rewriting them in place — scrolling, scroll regions, line insert/delete,
// reverse index, the alternate screen, viewport scrolling into history,
// resizes — plus colours, wide glyphs and OSC 8 links, which change what a
// row holds without changing its text.
func TestSnapshotRowCacheMatchesAFreshRead(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	e, err := New(30, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	g := e.(*ghosttyEmulator)

	ops := []func() string{
		func() string { return string(rune('a' + rng.Intn(26))) },
		func() string { return "hello world " },
		func() string { return "\r\n" },
		func() string { return "界🍏" },
		func() string { return fmt.Sprintf("\x1b[%d;%dH", 1+rng.Intn(10), 1+rng.Intn(32)) },
		func() string { return fmt.Sprintf("\x1b[%dm", []int{0, 1, 7, 31, 32, 44, 90}[rng.Intn(7)]) },
		func() string { return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", rng.Intn(256), rng.Intn(256), rng.Intn(256)) },
		func() string { return fmt.Sprintf("\x1b[%dK", rng.Intn(3)) },
		func() string { return fmt.Sprintf("\x1b[%dJ", rng.Intn(3)) },
		func() string { return fmt.Sprintf("\x1b[%dL", 1+rng.Intn(3)) },
		func() string { return fmt.Sprintf("\x1b[%dM", 1+rng.Intn(3)) },
		func() string { return fmt.Sprintf("\x1b[%dS", 1+rng.Intn(3)) },
		func() string { return fmt.Sprintf("\x1b[%dT", 1+rng.Intn(3)) },
		func() string { return fmt.Sprintf("\x1b[%d@", 1+rng.Intn(4)) },
		func() string { return fmt.Sprintf("\x1b[%dP", 1+rng.Intn(4)) },
		func() string { return fmt.Sprintf("\x1b[%d;%dr", 1+rng.Intn(3), 4+rng.Intn(5)) },
		func() string { return "\x1b[r" },
		func() string { return "\x1bM" },
		func() string { return []string{"\x1b[?1049h", "\x1b[?1049l"}[rng.Intn(2)] },
		func() string {
			return fmt.Sprintf("\x1b]8;;https://example.com/%d\x1b\\link\x1b]8;;\x1b\\", rng.Intn(3))
		},
		func() string { return "line of output that wraps past the edge of the grid\r\n" },
	}

	for step := range 3000 {
		switch k := rng.Intn(40); {
		case k == 0:
			if err := e.Resize(uint16(20+rng.Intn(20)), uint16(5+rng.Intn(6))); err != nil {
				t.Fatal(err)
			}
		case k < 3:
			if err := e.Scroll(rng.Intn(9) - 4); err != nil {
				t.Fatal(err)
			}
		default:
			for range 1 + rng.Intn(4) {
				if _, err := e.Write([]byte(ops[rng.Intn(len(ops))]())); err != nil {
					t.Fatal(err)
				}
			}
		}
		// Not every step is snapshotted, so the cache also has to survive
		// several changes landing between two snapshots.
		if rng.Intn(3) != 0 {
			continue
		}
		cached, err := e.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		g.lastRows = nil // forces the next snapshot to read every row
		fresh, err := e.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cached.Cells, fresh.Cells) {
			for y := range fresh.Cells {
				if y >= len(cached.Cells) || !reflect.DeepEqual(cached.Cells[y], fresh.Cells[y]) {
					t.Fatalf("step %d: row %d differs from a fresh read\ncached %s\nfresh  %s",
						step, y, rowString(cached, y), rowString(fresh, y))
				}
			}
			t.Fatalf("step %d: %d rows cached, %d fresh", step, len(cached.Cells), len(fresh.Cells))
		}
		if cached.HasHyperlinks != fresh.HasHyperlinks {
			t.Fatalf("step %d: HasHyperlinks %v, fresh %v", step, cached.HasHyperlinks, fresh.HasHyperlinks)
		}
	}
}

func rowString(s *Snapshot, y int) string {
	if y >= len(s.Cells) {
		return "(missing)"
	}
	out := ""
	for _, c := range s.Cells[y] {
		r := c.Rune
		if r == "" {
			r = "·"
		}
		if c.Link != "" {
			r = "[" + r + "]"
		}
		out += r
	}
	return out
}
