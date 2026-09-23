//go:build ghostty

package main

import (
	"testing"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// writeAll plays the connection's writer: everything queued leaves for the
// socket, reported through wrote exactly as writeLoop does. Returns the
// frame messages about pid, in order.
func writeAll(t *testing.T, o *orch, c *client, pid uint32) []any {
	t.Helper()
	var out []any
	for {
		select {
		case b := <-c.out:
			m, err := browserproto.DecodeDown(b)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			switch v := m.(type) {
			case *browserproto.PaneFrame:
				if v.Pane == pid {
					out = append(out, v)
				}
			case *browserproto.PaneDiff:
				if v.Pane == pid {
					out = append(out, v)
				}
			}
			c.wrote(len(b))
		default:
			// Run whatever the writes posted (the catch-up), and write out
			// whatever that queued, before calling the connection drained.
			if len(o.mailbox) == 0 {
				return out
			}
			waitFor(t, o, func() bool { return len(o.mailbox) == 0 })
		}
	}
}

// A connection that falls behind is not handed frames it could only receive
// stale; once it drains it gets the pane's CURRENT screen as one full frame,
// and diffs from there. A connection keeping up is unaffected throughout.
func TestSlowConnectionSkipsToTheCurrentScreen(t *testing.T) {
	hi, lo := congestHigh, congestLow
	t.Cleanup(func() { congestHigh, congestLow = hi, lo })

	o, pid, _ := mcOrch(t)
	fast := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	slow := newConn(o, true, browserproto.Init{})
	o.flushClients()
	for _, c := range []*client{fast, slow} {
		if !c.view.visible[pid] {
			t.Fatalf("pane %d is not on a window's screen", pid)
		}
		writeAll(t, o, c, pid)
	}
	congestHigh, congestLow = 64, 16

	// The base reaches both; the slow one does not write it out.
	dispatchFrame(t, o, pid, fullFrame("a", "b", "c", "d"))
	writeAll(t, o, fast, pid)
	if slow.queued.Load() <= congestHigh {
		t.Fatalf("slow connection has only %d bytes queued; the test needs it past %d", slow.queued.Load(), congestHigh)
	}

	for i, s := range []string{"X", "Y"} {
		dispatchFrame(t, o, pid, sparseDiff(4, i, s))
		msgs := writeAll(t, o, fast, pid)
		if len(msgs) != 1 {
			t.Fatalf("the connection keeping up got %d frame messages for %s, want 1", len(msgs), s)
		}
		if _, ok := msgs[0].(*browserproto.PaneDiff); !ok {
			t.Fatalf("the connection keeping up got %#v, want a diff", msgs[0])
		}
	}
	if !slow.stale[pid] {
		t.Fatal("the slow connection's pane was not marked for a catch-up")
	}

	// It drains: first the base it was already holding, then — posted by the
	// write that took it under the low watermark — the screen as it is now.
	msgs := writeAll(t, o, slow, pid)
	if len(msgs) != 2 {
		t.Fatalf("slow connection got %d frame messages, want the base and one catch-up: %#v", len(msgs), msgs)
	}
	if pf, ok := msgs[1].(*browserproto.PaneFrame); !ok || symbols(pf) != "XYcd" {
		t.Fatalf("catch-up was %#v, want a full frame reading XYcd", msgs[1])
	}
	if len(slow.stale) != 0 {
		t.Fatalf("stale panes left after the catch-up: %v", slow.stale)
	}

	// And it is back on diffs.
	dispatchFrame(t, o, pid, sparseDiff(4, 3, "Z"))
	msgs = writeAll(t, o, slow, pid)
	if len(msgs) != 1 {
		t.Fatalf("after catching up: %d messages, want 1", len(msgs))
	}
	if d, ok := msgs[0].(*browserproto.PaneDiff); !ok || len(d.Cells) != 1 || d.Cells[0].S != "Z" {
		t.Fatalf("after catching up the connection got %#v, want a one-cell diff", msgs[0])
	}
}

// A pane whose grid is invalid cannot be redrawn from catway's copy, so the
// catch-up asks its host for a full frame instead.
func TestCatchUpWithoutAGridAsksTheHost(t *testing.T) {
	o, pid, _ := mcOrch(t)
	c := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients()
	writeAll(t, o, c, pid)
	pd := newPipeDaemon(t, o)

	c.skipFrame(pid)
	o.panes[pid].grid.Invalidate()
	c.behind.Store(false)
	o.catchUp(c)
	pd.expect(t, orchestration.MsgRequestResync)
}
