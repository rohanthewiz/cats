//go:build ghostty

package terminal

import "testing"

// newEmu builds an emulator, writes the given VT input, and returns a snapshot.
func newEmu(t *testing.T, cols, rows uint16, input string) (*ghosttyEmulator, *Snapshot) {
	t.Helper()
	e, err := New(cols, rows)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	if input != "" {
		if _, err := e.Write([]byte(input)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	snap, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return e.(*ghosttyEmulator), snap
}

func rowText(s *Snapshot, row uint16) string {
	out := ""
	for _, c := range s.Cells[row] {
		if c.Rune == "" {
			out += " "
		} else {
			out += c.Rune
		}
	}
	// trim trailing blanks
	i := len(out)
	for i > 0 && out[i-1] == ' ' {
		i--
	}
	return out[:i]
}

func TestSnapshotDimensions(t *testing.T) {
	_, snap := newEmu(t, 80, 24, "")
	if snap.Cols != 80 || snap.Rows != 24 {
		t.Fatalf("dims = %dx%d, want 80x24", snap.Cols, snap.Rows)
	}
	if len(snap.Cells) != 24 {
		t.Fatalf("rows = %d, want 24", len(snap.Cells))
	}
	if len(snap.Cells[0]) != 80 {
		t.Fatalf("cols in row 0 = %d, want 80", len(snap.Cells[0]))
	}
}

func TestContent(t *testing.T) {
	_, snap := newEmu(t, 20, 3, "hello\r\nworld")
	if got := rowText(snap, 0); got != "hello" {
		t.Errorf("row 0 = %q, want %q", got, "hello")
	}
	if got := rowText(snap, 1); got != "world" {
		t.Errorf("row 1 = %q, want %q", got, "world")
	}
	if snap.At(0, 0).Rune != "h" {
		t.Errorf("cell(0,0) = %q, want h", snap.At(0, 0).Rune)
	}
}

func TestForegroundColor(t *testing.T) {
	// SGR 31 = red foreground.
	_, snap := newEmu(t, 10, 1, "\x1b[31mR\x1b[0mP")
	red := snap.At(0, 0)
	if red.Rune != "R" {
		t.Fatalf("cell(0,0) rune = %q, want R", red.Rune)
	}
	if red.Fg == nil {
		t.Fatal("red cell has nil Fg, want a resolved color")
	}
	plain := snap.At(1, 0)
	if plain.Fg != nil {
		t.Errorf("plain cell Fg = %+v, want nil (default)", *plain.Fg)
	}
}

func TestBackgroundColor(t *testing.T) {
	// SGR 44 = blue background.
	_, snap := newEmu(t, 10, 1, "\x1b[44mB")
	bcell := snap.At(0, 0)
	if bcell.Bg == nil {
		t.Fatal("cell has nil Bg, want a resolved color")
	}
}

func TestStyleFlags(t *testing.T) {
	// bold, italic, underline, inverse each on its own column.
	_, snap := newEmu(t, 10, 1, "\x1b[1mA\x1b[0m\x1b[3mB\x1b[0m\x1b[4mC\x1b[0m\x1b[7mD\x1b[0m")
	if !snap.At(0, 0).Bold {
		t.Error("cell A: Bold not set")
	}
	if !snap.At(1, 0).Italic {
		t.Error("cell B: Italic not set")
	}
	if !snap.At(2, 0).Underline {
		t.Error("cell C: Underline not set")
	}
	if !snap.At(3, 0).Inverse {
		t.Error("cell D: Inverse not set")
	}
}

func TestCursorPosition(t *testing.T) {
	_, snap := newEmu(t, 80, 24, "abc")
	if !snap.Cursor.Visible {
		t.Error("cursor not visible, want visible")
	}
	if snap.Cursor.X != 3 || snap.Cursor.Y != 0 {
		t.Errorf("cursor at (%d,%d), want (3,0)", snap.Cursor.X, snap.Cursor.Y)
	}
}

func TestResize(t *testing.T) {
	e, _ := newEmu(t, 80, 24, "hello")
	if err := e.Resize(40, 10); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	snap, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot after resize: %v", err)
	}
	if snap.Cols != 40 || snap.Rows != 10 {
		t.Fatalf("after resize dims = %dx%d, want 40x10", snap.Cols, snap.Rows)
	}
	// Content survives a widen-compatible resize.
	if got := rowText(snap, 0); got != "hello" {
		t.Errorf("row 0 after resize = %q, want %q", got, "hello")
	}
}
