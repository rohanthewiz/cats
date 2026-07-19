//go:build ghostty

package terminal

import (
	"fmt"
	"strings"
	"testing"
)

// TestScrollback drives the libghostty scroll API end to end: it builds history,
// scrolls up to reveal it, checks the reported metrics, and confirms scroll-lock —
// new output while scrolled up keeps the viewport pinned instead of snapping down.
func TestScrollback(t *testing.T) {
	e, _ := newEmu(t, 20, 3, "")

	var b strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "L%d\r\n", i)
	}
	if _, err := e.Write([]byte(b.String())); err != nil {
		t.Fatalf("Write: %v", err)
	}

	m, err := e.ScrollMetrics()
	if err != nil {
		t.Fatalf("ScrollMetrics: %v", err)
	}
	if m.ViewportRows != 3 {
		t.Errorf("viewport rows = %d, want 3", m.ViewportRows)
	}
	if m.OffsetFromBottom != 0 {
		t.Errorf("offset = %d, want 0 (pinned to bottom)", m.OffsetFromBottom)
	}
	if m.MaxOffsetFromBottom == 0 {
		t.Fatalf("expected scrollback history, got max offset 0")
	}

	bottom, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot (bottom): %v", err)
	}

	// Scroll all the way up; the very first line should be at the top.
	if err := e.Scroll(-m.MaxOffsetFromBottom); err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	up, err := e.ScrollMetrics()
	if err != nil {
		t.Fatalf("ScrollMetrics: %v", err)
	}
	if up.OffsetFromBottom != up.MaxOffsetFromBottom {
		t.Errorf("after scroll-to-top offset=%d, want max=%d", up.OffsetFromBottom, up.MaxOffsetFromBottom)
	}
	top, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot (top): %v", err)
	}
	t.Logf("metrics: viewport=%d max=%d; bottom row0=%q top row0=%q", m.ViewportRows, m.MaxOffsetFromBottom, rowText(bottom, 0), rowText(top, 0))
	if rowText(top, 0) == rowText(bottom, 0) {
		t.Errorf("scrolled view should differ from bottom (both row0 = %q)", rowText(top, 0))
	}
	if got := rowText(top, 0); got != "L1" {
		t.Errorf("top of scrollback row0 = %q, want L1", got)
	}

	// Scrolling back down past the bottom clamps to offset 0.
	if err := e.Scroll(up.MaxOffsetFromBottom + 5); err != nil {
		t.Fatalf("Scroll down: %v", err)
	}
	if down, _ := e.ScrollMetrics(); down.OffsetFromBottom != 0 {
		t.Errorf("after scroll-down offset = %d, want 0", down.OffsetFromBottom)
	}

	// Scroll-lock: new output while scrolled up keeps the viewport pinned to the
	// same content rather than snapping to the bottom. After scrolling to the top
	// (L1) and emitting a line, L1 must still be at the top and the offset must stay
	// non-zero (it grows by the line pushed into history).
	if err := e.Scroll(-up.MaxOffsetFromBottom); err != nil {
		t.Fatalf("Scroll up again: %v", err)
	}
	before, _ := e.ScrollMetrics()
	if _, err := e.Write([]byte("L11\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	after, _ := e.ScrollMetrics()
	if after.OffsetFromBottom == 0 {
		t.Errorf("after new output offset = 0 (snapped); want pinned (>0)")
	}
	if after.OffsetFromBottom != before.OffsetFromBottom+1 {
		t.Errorf("pinned offset = %d, want %d (grew by the pushed line)",
			after.OffsetFromBottom, before.OffsetFromBottom+1)
	}
	pinned, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot (pinned): %v", err)
	}
	if got := rowText(pinned, 0); got != "L1" {
		t.Errorf("pinned top row0 = %q, want L1 (viewport stayed put)", got)
	}

	// Scrolling all the way back down still snaps to the live bottom (L11 visible).
	if err := e.Scroll(after.MaxOffsetFromBottom + 5); err != nil {
		t.Fatalf("Scroll to bottom: %v", err)
	}
	if m2, _ := e.ScrollMetrics(); m2.OffsetFromBottom != 0 {
		t.Errorf("after scroll-to-bottom offset = %d, want 0", m2.OffsetFromBottom)
	}
}
