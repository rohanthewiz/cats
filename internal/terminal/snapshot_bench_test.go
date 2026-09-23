//go:build ghostty

package terminal

import (
	"fmt"
	"testing"
)

// fillScreen writes a full 200×50 screen of coloured text.
func fillScreen(b testing.TB, e Emulator) {
	b.Helper()
	for y := range 50 {
		line := fmt.Sprintf("\x1b[%d;1H\x1b[3%dm", y+1, y%8)
		for x := range 200 {
			line += string(rune('a' + (x+y)%26))
		}
		if _, err := e.Write([]byte(line)); err != nil {
			b.Fatal(err)
		}
	}
}

// One character typed per snapshot: the common case, and the one per-row
// dirty tracking exists for.
func BenchmarkSnapshotOneCellChanged(b *testing.B) {
	e, err := New(200, 50)
	if err != nil {
		b.Fatal(err)
	}
	defer e.Close()
	fillScreen(b, e)
	if _, err := e.Snapshot(); err != nil {
		b.Fatal(err)
	}
	i := 0
	for b.Loop() {
		i++
		if _, err := e.Write([]byte(fmt.Sprintf("\x1b[10;10H%c", 'a'+i%26))); err != nil {
			b.Fatal(err)
		}
		if _, err := e.Snapshot(); err != nil {
			b.Fatal(err)
		}
	}
}
