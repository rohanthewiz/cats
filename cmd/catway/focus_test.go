//go:build ghostty

package main

import (
	"testing"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// These cover the window-focus relay: a browser reporting its window's OS
// focus (browserproto.Focus), folded into per-pane "is anyone looking" state
// and forwarded as CSI I/O to pane programs that enabled focus reporting
// (DEC 1004). The failure they exist to catch is quiet: a TUI's caret happily
// blinking away in a backgrounded window, or — worse — focus bytes leaking
// into a shell that never asked for them, where they type "I" at the prompt.

// enableFocusReporting flips DEC 1004 on for one pane's runtime, the state a
// β pane_modes event would have installed.
func enableFocusReporting(o *orch, pid uint32) {
	rt := o.panes[pid]
	rt.modes.FocusReporting = true
	rt.enc.SetModes(rt.modes)
}

// A window blur reaches the focused pane's program as CSI O, a refocus as
// CSI I — the whole point of the feature.
func TestWindowBlurReachesTheFocusedPane(t *testing.T) {
	o, focused, _ := mcOrch(t)
	enableFocusReporting(o, focused)
	pd := newPipeDaemon(t, o)

	c := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients() // what run() does between mailbox closures
	if pane, data := nextInput(t, pd); pane != focused || string(data) != "\x1b[I" {
		t.Fatalf("after connect: input %q to pane %d, want CSI I to %d", data, pane, focused)
	}

	o.handleUp(c, &browserproto.Focus{T: browserproto.MsgFocus, Focused: false})
	if pane, data := nextInput(t, pd); pane != focused || string(data) != "\x1b[O" {
		t.Fatalf("after blur: input %q to pane %d, want CSI O to %d", data, pane, focused)
	}

	o.handleUp(c, &browserproto.Focus{T: browserproto.MsgFocus, Focused: true})
	if pane, data := nextInput(t, pd); pane != focused || string(data) != "\x1b[I" {
		t.Fatalf("after refocus: input %q to pane %d, want CSI I to %d", data, pane, focused)
	}
}

// A pane whose program never enabled DEC 1004 hears nothing across the same
// transitions. Proven by ordering: the key pressed after the blur/refocus
// churn is the very next input on the wire.
func TestFocusSilentWithoutReporting(t *testing.T) {
	o, focused, _ := mcOrch(t)
	pd := newPipeDaemon(t, o)
	c := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients()

	o.handleUp(c, &browserproto.Focus{T: browserproto.MsgFocus, Focused: false})
	o.handleUp(c, &browserproto.Focus{T: browserproto.MsgFocus, Focused: true})
	o.handleUp(c, &browserproto.Key{T: browserproto.MsgKey,
		Code: "KeyA", Key: "a", Kind: browserproto.KeyDown})

	if pane, data := nextInput(t, pd); pane != focused || string(data) != "a" {
		t.Fatalf("input %q to pane %d, want the bare key %q to %d — focus bytes leaked",
			data, pane, "a", focused)
	}
}

// Moving pane focus within a focused window is a handoff: the pane losing it
// hears CSI O, the pane gaining it CSI I. Both programs have 1004 on, so both
// reports must arrive (in either order — the sync walks a map).
func TestPaneFocusMoveHandsReportsOver(t *testing.T) {
	o, focused, other := mcOrch(t)
	enableFocusReporting(o, focused)
	enableFocusReporting(o, other)
	pd := newPipeDaemon(t, o)
	newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients()
	if pane, data := nextInput(t, pd); pane != focused || string(data) != "\x1b[I" {
		t.Fatalf("seed: input %q to pane %d, want CSI I to %d", data, pane, focused)
	}

	if err := o.session.FocusPane(layout.PaneID(other)); err != nil {
		t.Fatalf("FocusPane: %v", err)
	}
	o.emitStructuralEvents() // the choke point every model mutation runs through

	got := map[uint32]string{}
	for range 2 {
		pane, data := nextInput(t, pd)
		got[pane] = string(data)
	}
	if got[focused] != "\x1b[O" || got[other] != "\x1b[I" {
		t.Fatalf("handoff = %v, want CSI O to %d and CSI I to %d", got, focused, other)
	}
}

// The last focused window disconnecting is a blur no Focus message will ever
// report — the sync has to fall out of the connection census instead.
func TestLastClientLeavingIsABlur(t *testing.T) {
	o, focused, _ := mcOrch(t)
	enableFocusReporting(o, focused)
	pd := newPipeDaemon(t, o)
	c := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients()
	if _, data := nextInput(t, pd); string(data) != "\x1b[I" {
		t.Fatalf("seed: input %q, want CSI I", data)
	}

	o.dropConn(c)
	o.flushClients()
	if pane, data := nextInput(t, pd); pane != focused || string(data) != "\x1b[O" {
		t.Fatalf("after drop: input %q to pane %d, want CSI O to %d", data, pane, focused)
	}
}

// A program enabling 1004 mid-session is seeded with the current state: one
// launched into a backgrounded window must hear CSI O immediately, not sit
// blinking on its focused-by-default assumption until the next transition.
func TestModeEnableSeedsCurrentState(t *testing.T) {
	o, focused, _ := mcOrch(t)
	pd := newPipeDaemon(t, o)
	c := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients()
	o.handleUp(c, &browserproto.Focus{T: browserproto.MsgFocus, Focused: false}) // silent: 1004 off

	o.applyPaneModes(orchestration.PaneModes{Type: orchestration.MsgPaneModes,
		PaneID: focused, FocusReporting: true})

	if pane, data := nextInput(t, pd); pane != focused || string(data) != "\x1b[O" {
		t.Fatalf("seed on enable: input %q to pane %d, want CSI O to %d", data, pane, focused)
	}
}
