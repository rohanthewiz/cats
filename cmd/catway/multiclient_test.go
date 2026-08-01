//go:build ghostty

package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// These cover the three additive protocol features that let a second client —
// a phone — share a session it does not own: Init.Viewer (watch without
// reshaping), Key.Pane/Paste.Pane (type somewhere without stealing focus), and
// the clients census (know who else is here and whose grid this is).
//
// The failure they exist to catch is not a crash but a silent wrong: a viewer
// that resizes the desktop's panes, or a keystroke landing in a pane the user
// is not looking at. Both look like a working system right up until they don't.

// mcOrch builds an orch with two panes side by side in the one visible tab,
// returning the focused pane and its neighbour. newOrch's constructor already
// runs syncDaemon/refreshViewport, so both panes have live encoders and are in
// the visible set.
func mcOrch(t *testing.T) (o *orch, focused, other uint32) {
	t.Helper()
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	first := o.session.AllPaneIDs()[0]
	second, err := o.session.SplitPane(nil, layout.Horizontal)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	o.syncDaemon()
	o.refreshViewport()
	// The split focuses the new pane; put focus back on the first so "focused"
	// and "addressed" are different panes in every test below.
	if err := o.session.FocusPane(first); err != nil {
		t.Fatalf("FocusPane: %v", err)
	}
	return o, uint32(first), uint32(second)
}

// newConn registers a connection the way serve would, minus the socket.
func newConn(o *orch, viewer bool, init browserproto.Init) *client {
	c := &client{o: o, out: make(chan []byte, 64), viewer: viewer,
		trans: make(map[uint32]*browserproto.FrameTranslator)}
	init.Viewer = viewer
	o.registerConn(c, &init)
	return c
}

// drain empties a connection's queue — registerConn pushes a pile of initial
// state that these tests are not about.
func drain(c *client) {
	for {
		select {
		case <-c.out:
		default:
			return
		}
	}
}

// nextInput reads β messages off the daemon pipe until an input arrives,
// returning the pane it was addressed to and its bytes.
func nextInput(t *testing.T, pd *pipeDaemon) (uint32, []byte) {
	t.Helper()
	var in orchestration.Input
	if err := json.Unmarshal(pd.expect(t, orchestration.MsgInput), &in); err != nil {
		t.Fatalf("decode input: %v", err)
	}
	return in.PaneID, in.Data
}

// --- Init.Viewer --------------------------------------------------------------

// A sizer's grid and cell metrics become the session's. This is the control for
// the viewer tests below: without it, "the viewer changed nothing" could just
// mean registerConn stopped applying anything at all.
func TestSizerDeclaresTheGrid(t *testing.T) {
	o, _, _ := mcOrch(t)

	newConn(o, false, browserproto.Init{Cols: 200, Rows: 60, CellWPx: 9, CellHPx: 19})

	if o.area.Width != 200 || o.area.Height != 60 {
		t.Fatalf("area = %dx%d, want 200x60", o.area.Width, o.area.Height)
	}
	if o.cellW != 9 || o.cellH != 19 {
		t.Fatalf("cell = %dx%d, want 9x19", o.cellW, o.cellH)
	}
}

// A viewer's declared geometry is ignored — grid *and* cell metrics. The
// metrics are the easy half to forget: they never touch the layout, they ride
// β create_pane/resize into every pane's environment, so a phone's cell size
// would reach panes whose rects never moved.
func TestViewerDeclaresNothing(t *testing.T) {
	o, _, _ := mcOrch(t)
	newConn(o, false, browserproto.Init{Cols: 200, Rows: 60, CellWPx: 9, CellHPx: 19})

	newConn(o, true, browserproto.Init{Cols: 40, Rows: 30, CellWPx: 3, CellHPx: 6})

	if o.area.Width != 200 || o.area.Height != 60 {
		t.Fatalf("viewer reshaped the session: area = %dx%d, want the sizer's 200x60",
			o.area.Width, o.area.Height)
	}
	if o.cellW != 9 || o.cellH != 19 {
		t.Fatalf("viewer changed cell metrics: %dx%d, want the sizer's 9x19", o.cellW, o.cellH)
	}
}

// The declaration is made once at init, but resize is the message that keeps
// arriving — every time the phone rotates. Gating only at init would let the
// first rotation undo it.
func TestViewerResizeIsIgnored(t *testing.T) {
	o, _, _ := mcOrch(t)
	sizer := newConn(o, false, browserproto.Init{Cols: 200, Rows: 60})
	viewer := newConn(o, true, browserproto.Init{})

	o.handleUp(viewer, &browserproto.Resize{T: browserproto.MsgResize, Cols: 40, Rows: 30})
	if o.area.Width != 200 || o.area.Height != 60 {
		t.Fatalf("viewer resize applied: area = %dx%d, want 200x60", o.area.Width, o.area.Height)
	}

	// ... while the sizer's resize still lands, so the gate is about who asked.
	o.handleUp(sizer, &browserproto.Resize{T: browserproto.MsgResize, Cols: 100, Rows: 50})
	if o.area.Width != 100 || o.area.Height != 50 {
		t.Fatalf("sizer resize ignored: area = %dx%d, want 100x50", o.area.Width, o.area.Height)
	}
}

// --- Key.Pane / Paste.Pane ----------------------------------------------------

// The whole point of the field: bytes reach the named pane, not the focused
// one. A test that only checked "input was sent" would pass on the old code.
func TestAddressedKeyReachesTheNamedPane(t *testing.T) {
	o, focused, other := mcOrch(t)
	pd := newPipeDaemon(t, o)

	o.handleUp(nil, &browserproto.Key{T: browserproto.MsgKey, Pane: other,
		Code: "KeyA", Key: "a", Kind: browserproto.KeyDown})

	pane, data := nextInput(t, pd)
	if pane != other {
		t.Fatalf("input went to pane %d, want the addressed %d (focused is %d)", pane, other, focused)
	}
	if string(data) != "a" {
		t.Fatalf("input = %q, want %q", data, "a")
	}
}

// Pane 0 keeps the historical route byte for byte, which is what every browser
// sends and why this change is invisible to the desktop.
func TestUnaddressedKeyStillFollowsFocus(t *testing.T) {
	o, focused, _ := mcOrch(t)
	pd := newPipeDaemon(t, o)

	o.handleUp(nil, &browserproto.Key{T: browserproto.MsgKey,
		Code: "KeyA", Key: "a", Kind: browserproto.KeyDown})

	if pane, _ := nextInput(t, pd); pane != focused {
		t.Fatalf("input went to pane %d, want the focused %d", pane, focused)
	}
}

// Paste carries the same field and takes the same route — including the
// server-side bracketed-paste wrapping, which is why it is its own message.
func TestAddressedPasteReachesTheNamedPane(t *testing.T) {
	o, _, other := mcOrch(t)
	pd := newPipeDaemon(t, o)

	o.handleUp(nil, &browserproto.Paste{T: browserproto.MsgPaste, Pane: other, Data: "hi"})

	pane, data := nextInput(t, pd)
	if pane != other {
		t.Fatalf("paste went to pane %d, want %d", pane, other)
	}
	if string(data) != "hi" {
		t.Fatalf("paste = %q, want %q", data, "hi")
	}
}

// Visibility is the client's freshness proof: frames stream only for visible
// panes, so a pane the client cannot currently see is one it has no business
// typing into. Same rule Mouse already takes.
func TestAddressedInputRefusedForInvisiblePane(t *testing.T) {
	o, _, other := mcOrch(t)
	delete(o.visible, other) // as if the viewport had moved on

	if rt := o.inputTarget(other); rt != nil {
		t.Fatal("addressed input accepted for a pane outside the viewport")
	}
	// The runtime is still there and still reachable through focus — the gate is
	// about the addressing, not about the pane being gone.
	if rt := o.inputTarget(0); rt == nil {
		t.Fatal("focus-routed input refused; the visibility gate leaked onto pane 0")
	}
}

// workspace.lock is a stated guardrail, and pane.send_input honours it. If
// addressed keys did not, a client wanting past the lock would simply send
// key{pane:N} instead of pane.send_input — a bypass we would have built
// ourselves.
func TestAddressedInputRefusedInLockedWorkspace(t *testing.T) {
	o, focused, other := mcOrch(t)
	ws := o.session.ActiveWorkspace()
	if _, err := o.session.SetWorkspaceLock(ws.ID, true); err != nil {
		t.Fatalf("SetWorkspaceLock: %v", err)
	}

	if rt := o.inputTarget(other); rt != nil {
		t.Fatal("addressed input accepted into a locked workspace")
	}

	// Focus-routed input is untouched. The lock closes the workspace to
	// automation, not to the person whose keyboard owns the focused pane — the
	// same asymmetry pane.send_input has against someone typing in the terminal.
	rt := o.inputTarget(0)
	if rt == nil || rt.id != focused {
		t.Fatalf("focus-routed input refused by the lock: got %v, want pane %d", rt, focused)
	}
}

// An exited pane takes no input by either route — the pre-existing rule, kept
// while both cases collapsed onto one helper.
func TestInputRefusedForExitedPane(t *testing.T) {
	o, focused, other := mcOrch(t)
	code := 0
	o.panes[other].exited = &code
	o.panes[focused].exited = &code

	if rt := o.inputTarget(other); rt != nil {
		t.Fatal("addressed input accepted for an exited pane")
	}
	if rt := o.inputTarget(0); rt != nil {
		t.Fatal("focus-routed input accepted for an exited pane")
	}
}

// --- Clients census -----------------------------------------------------------

// recvClients pulls the census off a connection's queue, skipping the rest.
func recvClients(t *testing.T, c *client) *browserproto.Clients {
	t.Helper()
	for {
		select {
		case b := <-c.out:
			msg, err := browserproto.DecodeDown(b)
			if err != nil {
				t.Fatalf("decode down: %v", err)
			}
			if cl, ok := msg.(*browserproto.Clients); ok {
				return cl
			}
		default:
			t.Fatal("no clients census queued")
			return nil
		}
	}
}

// The census counts heads and names whose grid is in force. Two connections and
// one sizer is the shape the phone case produces, and the one a client needs to
// render "desktop connected — viewing only".
func TestClientsCensusCountsSizersSeparately(t *testing.T) {
	o, _, _ := mcOrch(t)
	sizer := newConn(o, false, browserproto.Init{Cols: 200, Rows: 60})
	newConn(o, true, browserproto.Init{})
	drain(sizer)

	o.flushClients() // what run() does between mailbox closures

	got := recvClients(t, sizer)
	if got.Total != 2 || got.Sizers != 1 {
		t.Fatalf("census total=%d sizers=%d, want 2/1", got.Total, got.Sizers)
	}
	if got.Cols != 200 || got.Rows != 60 {
		t.Fatalf("census grid = %dx%d, want the sizer's 200x60", got.Cols, got.Rows)
	}
}

// dropConn must not broadcast: its hottest caller is enqueue, itself running
// inside a broadcast's range over o.conns. So the census is flagged there and
// sent later — and this pins the "later", because a census that never flushes
// leaves every remaining client believing in a peer that has gone.
func TestDropConnDefersTheCensusRatherThanSkippingIt(t *testing.T) {
	o, _, _ := mcOrch(t)
	stays := newConn(o, false, browserproto.Init{Cols: 200, Rows: 60})
	leaves := newConn(o, true, browserproto.Init{})
	drain(stays)

	o.dropConn(leaves)
	select {
	case b := <-stays.out:
		msg, _ := browserproto.DecodeDown(b)
		if _, ok := msg.(*browserproto.Clients); ok {
			t.Fatal("dropConn broadcast the census inline; it must only flag it")
		}
	default:
	}
	if !o.connsDirty {
		t.Fatal("dropConn left the census unflagged: the departure would never be announced")
	}

	o.flushClients()
	if got := recvClients(t, stays); got.Total != 1 || got.Sizers != 1 {
		t.Fatalf("census total=%d sizers=%d after one left, want 1/1", got.Total, got.Sizers)
	}
}

// One flush per settled state, not one per change: a closure that drops three
// connections sends one census, and a flush with nothing to say sends none.
func TestFlushClientsCoalescesAndStaysQuiet(t *testing.T) {
	o, _, _ := mcOrch(t)
	stays := newConn(o, false, browserproto.Init{Cols: 200, Rows: 60})
	a := newConn(o, true, browserproto.Init{})
	b := newConn(o, true, browserproto.Init{})
	drain(stays)

	o.dropConn(a)
	o.dropConn(b)
	o.flushClients()

	if got := recvClients(t, stays); got.Total != 1 {
		t.Fatalf("census total=%d, want 1", got.Total)
	}
	if len(stays.out) != 0 {
		t.Fatalf("%d extra messages: two departures in one turn must coalesce to one census",
			len(stays.out))
	}

	o.flushClients() // nothing changed since
	if len(stays.out) != 0 {
		t.Fatal("flushClients spoke with nothing to report")
	}
}
