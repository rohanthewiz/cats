//go:build ghostty

package orchestration

import (
	"fmt"
	"testing"

	"github.com/rohanthewiz/cats/internal/terminal"
)

// busyEmulator is a 200×50 screen of coloured text.
func busyEmulator(b *testing.B) terminal.Emulator {
	e, err := terminal.New(200, 50)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { e.Close() })
	for y := range 50 {
		line := fmt.Sprintf("\x1b[%d;1H\x1b[3%dm", y+1, y%8)
		for x := range 200 {
			line += string(rune('a' + (x+y)%26))
		}
		_, _ = e.Write([]byte(line))
	}
	return e
}

// The flusher's per-pane work for a one-character change on a busy 200×50
// screen — snapshot and sparse shifted diff — as cathost does it.
func BenchmarkTakeFrameOneCellChanged(b *testing.B) {
	e := busyEmulator(b)
	var fb FrameBuilder
	snap, _ := e.Snapshot()
	fb.Full(snap)
	i := 0
	for b.Loop() {
		i++
		_, _ = e.Write([]byte(fmt.Sprintf("\x1b[10;10H%c", 'a'+i%26)))
		snap, _ := e.Snapshot()
		fb.Diff(snap, true, true)
	}
}

// The same through the stateless reference, for comparison.
func BenchmarkTakeFrameOneCellChangedReference(b *testing.B) {
	e := busyEmulator(b)
	prev, _ := e.Snapshot()
	i := 0
	for b.Loop() {
		i++
		_, _ = e.Write([]byte(fmt.Sprintf("\x1b[10;10H%c", 'a'+i%26)))
		snap, _ := e.Snapshot()
		f := ShiftedFrameFromSnapshot(snap, prev)
		f.Sparsify()
		prev = snap
	}
}
