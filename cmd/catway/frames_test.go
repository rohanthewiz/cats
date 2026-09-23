//go:build ghostty

package main

import (
	"encoding/json"
	"testing"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// Sparse β diffs through catway: the pane's grid (paneRuntime.grid) is what
// turns a diff carrying only its changed cells back into whatever each window
// needs — a delta for a window that has the pane, a whole frame for one that
// just gained it.

// dispatchFrame feeds one β frame through the host's dispatch, exactly as the
// daemon reader would, and waits for the loop to have handled it.
func dispatchFrame(t *testing.T, o *orch, pid uint32, f *orchestration.Frame) {
	t.Helper()
	payload, err := json.Marshal(orchestration.NewPaneFrame(pid, f))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	o.hosts[o.defaultHost].dispatch(orchestration.MsgPaneFrame, payload)
	waitFor(t, o, func() bool { return len(o.mailbox) == 0 })
}

func frameCell(s string) orchestration.Cell {
	return orchestration.Cell{Symbol: s, Fg: 0x02c8c8c8, Bg: 0x02000000}
}

func fullFrame(syms ...string) *orchestration.Frame {
	f := &orchestration.Frame{Cols: uint16(len(syms)), Rows: 1, Full: true,
		Cursor: &orchestration.Cursor{Visible: true, Shape: 2}}
	for _, s := range syms {
		f.Cells = append(f.Cells, frameCell(s))
	}
	return f
}

func sparseDiff(cols int, at int, syms ...string) *orchestration.Frame {
	run := orchestration.CellRun{At: at}
	for _, s := range syms {
		run.Cells = append(run.Cells, frameCell(s))
	}
	return &orchestration.Frame{Cols: uint16(cols), Rows: 1, Sparse: true,
		Cursor: &orchestration.Cursor{Visible: true, Shape: 2},
		Runs:   []orchestration.CellRun{run}}
}

// frameMsgs returns the pane_frame / pane_diff messages queued for c about pid.
func frameMsgs(t *testing.T, c *client, pid uint32) []any {
	t.Helper()
	var out []any
	for _, m := range drainDown(t, c) {
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
	}
	return out
}

func symbols(f *browserproto.PaneFrame) string {
	s := ""
	for _, c := range f.Cells {
		s += c.S
	}
	return s
}

func TestSparseDiffsReachTheBrowser(t *testing.T) {
	o, pid, _ := mcOrch(t)
	c := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients()
	if !c.view.visible[pid] {
		t.Fatalf("pane %d is not on the window's screen", pid)
	}
	drain(c)

	// The base.
	dispatchFrame(t, o, pid, fullFrame("a", "b", "c", "d"))
	msgs := frameMsgs(t, c, pid)
	if len(msgs) != 1 {
		t.Fatalf("full frame: %d messages, want 1", len(msgs))
	}
	if pf, ok := msgs[0].(*browserproto.PaneFrame); !ok || symbols(pf) != "abcd" {
		t.Fatalf("full frame reached the window as %#v", msgs[0])
	}

	// A sparse diff becomes an ordinary delta.
	dispatchFrame(t, o, pid, sparseDiff(4, 2, "X"))
	msgs = frameMsgs(t, c, pid)
	if len(msgs) != 1 {
		t.Fatalf("sparse diff: %d messages, want 1", len(msgs))
	}
	d, ok := msgs[0].(*browserproto.PaneDiff)
	if !ok || len(d.Cells) != 1 || d.Cells[0].I != 2 || d.Cells[0].S != "X" {
		t.Fatalf("sparse diff reached the window as %#v", msgs[0])
	}

	// A window that needs the WHOLE screen — its translator was reset, as when
	// the pane enters its viewport — is served from the grid, which is the
	// thing a sparse diff cannot carry itself.
	c.translator(pid).Reset()
	dispatchFrame(t, o, pid, sparseDiff(4, 0, "Y"))
	msgs = frameMsgs(t, c, pid)
	if len(msgs) != 1 {
		t.Fatalf("after reset: %d messages, want 1", len(msgs))
	}
	if pf, ok := msgs[0].(*browserproto.PaneFrame); !ok || symbols(pf) != "YbXd" {
		t.Fatalf("after reset the window got %#v, want a full frame reading YbXd", msgs[0])
	}
}

// A frame missed by the grid makes every later sparse diff unusable until a
// full frame arrives: they are dropped, never applied to the wrong base.
func TestSparseDiffAfterAMissedFrameWaitsForFull(t *testing.T) {
	o, pid, _ := mcOrch(t)
	c := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients()
	drain(c)

	// Before any full frame at all.
	dispatchFrame(t, o, pid, sparseDiff(4, 0, "Z"))
	if msgs := frameMsgs(t, c, pid); len(msgs) != 0 {
		t.Fatalf("a sparse diff with no base reached the window: %#v", msgs)
	}

	dispatchFrame(t, o, pid, fullFrame("a", "b", "c", "d"))
	frameMsgs(t, c, pid)

	// A reconnect: the grid came from the previous connection.
	o.post(func() { o.panes[pid].grid.Invalidate() })
	dispatchFrame(t, o, pid, sparseDiff(4, 1, "Q"))
	if msgs := frameMsgs(t, c, pid); len(msgs) != 0 {
		t.Fatalf("a sparse diff on a stale grid reached the window: %#v", msgs)
	}

	// The replayed full frame repairs it, and diffs flow again.
	dispatchFrame(t, o, pid, fullFrame("e", "f", "g", "h"))
	dispatchFrame(t, o, pid, sparseDiff(4, 3, "R"))
	msgs := frameMsgs(t, c, pid)
	if len(msgs) != 2 {
		t.Fatalf("after the full frame: %d messages, want 2", len(msgs))
	}
	if d, ok := msgs[1].(*browserproto.PaneDiff); !ok || len(d.Cells) != 1 || d.Cells[0].S != "R" {
		t.Fatalf("the diff after recovery reached the window as %#v", msgs[1])
	}
}
