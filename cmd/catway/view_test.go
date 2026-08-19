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

// Multi-window: one session, several views.
//
// These are the invariants a second window exists to have. Like the
// multiclient tests they exist for silent wrongs rather than crashes — a frame
// landing in the window that is not showing that pane, or a resize in one
// window reflowing the panes of another — both of which look like a working
// system right up until they don't.

// twoWorkspaceOrch builds an orch with two workspaces, each holding one pane,
// and returns them. w1 is the session's original; w2 is created and left as the
// session default (CreateWorkspaceAtOn activates it), which the callers below
// immediately re-pin through the views they open.
func twoWorkspaceOrch(t *testing.T) (o *orch, ws1, ws2 string, pane1, pane2 uint32) {
	t.Helper()
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	ws1 = o.session.Workspaces()[0].ID
	pane1 = uint32(o.session.AllPaneIDs()[0])
	ws2, err = o.session.CreateWorkspace()
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	for _, id := range o.session.AllPaneIDs() {
		if uint32(id) != pane1 {
			pane2 = uint32(id)
		}
	}
	o.syncDaemon()
	o.refreshViewport()
	return o, ws1, ws2, pane1, pane2
}

// openWindow registers a sizer connection showing one workspace at a given grid
// — the Init a browser opened with "?ws=w2" sends.
func openWindow(o *orch, wsID string, cols, rows uint16) *client {
	c := &client{o: o, out: make(chan []byte, 256),
		trans: make(map[uint32]*browserproto.FrameTranslator),
		view:  view{ws: wsID, visible: map[uint32]bool{}}}
	o.registerConn(c, &browserproto.Init{Cols: cols, Rows: rows, Workspace: wsID})
	return c
}

// lastLayout drains a connection's queue and returns the last layout on it, or
// nil when none arrived — "did this window hear about it" in one call.
func lastLayout(t *testing.T, c *client) *browserproto.Layout {
	t.Helper()
	var last *browserproto.Layout
	for {
		select {
		case b := <-c.out:
			m, err := browserproto.DecodeDown(b)
			if err != nil {
				continue
			}
			if l, ok := m.(*browserproto.Layout); ok {
				last = l
			}
		default:
			return last
		}
	}
}

// activeWSOf reports which workspace a layout says is active — the field a
// window renders its sidebar selection from, and now a per-connection truth.
func activeWSOf(l *browserproto.Layout) string {
	for _, w := range l.Workspaces {
		if w.Active {
			return w.ID
		}
	}
	return ""
}

// --- independence -------------------------------------------------------------

// The headline: two windows, two workspaces, and a workspace switch in one is
// invisible to the other. Before views, workspace.focus was a session mutation
// and both windows moved.
func TestWorkspaceFocusMovesOnlyTheIssuingWindow(t *testing.T) {
	o, ws1, ws2, _, _ := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)
	drain(a)
	drain(b)

	o.handleCmd(a, cmd(t, "", browserproto.CmdWorkspaceFocus, browserproto.WorkspaceParams{ID: ws2}))

	if got := activeWSOf(lastLayout(t, a)); got != ws2 {
		t.Fatalf("issuing window active workspace = %q, want %q", got, ws2)
	}
	if l := lastLayout(t, b); l != nil && activeWSOf(l) != ws2 {
		t.Fatalf("the other window's layout changed to %q; it was already on %s and must not move",
			activeWSOf(l), ws2)
	}
	if b.view.ws != ws2 {
		t.Fatalf("the other window's view moved to %q", b.view.ws)
	}
}

// A frame reaches only the windows showing that pane. The union gate decides
// whether the frame is worth translating at all; the per-view sets decide who
// hears it.
func TestFrameReachesOnlyTheWindowShowingThePane(t *testing.T) {
	o, ws1, ws2, pane1, _ := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)
	drain(a)
	drain(b)

	if !a.view.visible[pane1] {
		t.Fatalf("window A on %s does not see its own pane %d", ws1, pane1)
	}
	if b.view.visible[pane1] {
		t.Fatalf("window B on %s sees %s's pane %d", ws2, ws1, pane1)
	}
	o.sendVisible(pane1, browserproto.NewPaneTitle(pane1, "hello"))

	if lastPaneTitle(t, a) != "hello" {
		t.Fatal("the window showing the pane did not get its chrome")
	}
	if got := lastPaneTitle(t, b); got != "" {
		t.Fatalf("a window on another workspace got the pane's chrome (%q)", got)
	}
}

// lastPaneTitle drains a connection and returns the last pane_title text on it.
func lastPaneTitle(t *testing.T, c *client) string {
	t.Helper()
	out := ""
	for {
		select {
		case b := <-c.out:
			if m, err := browserproto.DecodeDown(b); err == nil {
				if pt, ok := m.(*browserproto.PaneTitle); ok {
					out = pt.Title
				}
			}
		default:
			return out
		}
	}
}

// The bug that made a second window unusable: window B's 120x40 reflowed window
// A's 200x60, because one area sized every workspace. Areas are per workspace
// now, so a resize touches only what its window is showing.
func TestResizeInOneWindowLeavesTheOtherWorkspaceAlone(t *testing.T) {
	o, ws1, ws2, pane1, pane2 := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)
	drain(a)
	drain(b)

	before := o.desiredGrids()[pane1]
	o.handleUp(b, &browserproto.Resize{T: browserproto.MsgResize, Cols: 80, Rows: 24})

	if got := o.desiredGrids()[pane1]; got != before {
		t.Fatalf("resizing window B reshaped %s's pane: %v → %v", ws1, before, got)
	}
	if got := o.desiredGrids()[pane2]; got[0] != 80 {
		t.Fatalf("window B's own workspace did not follow its resize: pane %d grid %v", pane2, got)
	}
	if o.areaFor(ws1).Width != 200 {
		t.Fatalf("%s area = %d cols, want the window showing it (200)", ws1, o.areaFor(ws1).Width)
	}
	_ = a
}

// Unaddressed input rides the FOCUS OF THE WINDOW THAT TYPED. With one shared
// focus, window B's keystrokes followed window A's cursor.
func TestUnaddressedInputFollowsTheTypingWindow(t *testing.T) {
	o, ws1, ws2, pane1, pane2 := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)
	drain(a)
	drain(b)

	if rt := o.inputTarget(a, 0); rt == nil || rt.id != pane1 {
		t.Fatalf("window A's unaddressed input went to %v, want pane %d in %s", rt, pane1, ws1)
	}
	if rt := o.inputTarget(b, 0); rt == nil || rt.id != pane2 {
		t.Fatalf("window B's unaddressed input went to %v, want pane %d in %s", rt, pane2, ws2)
	}
}

// Addressed input is gated on THIS window's viewport, not the session's union:
// a window may type where the server recently told *it* it was streaming.
func TestAddressedInputGatedPerWindow(t *testing.T) {
	o, ws1, ws2, pane1, _ := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)
	drain(a)
	drain(b)

	if rt := o.inputTarget(a, pane1); rt == nil {
		t.Fatal("a window was refused input to a pane it is showing")
	}
	if rt := o.inputTarget(b, pane1); rt != nil {
		t.Fatal("a window typed into a pane only another window is showing")
	}
}

// pane.focus on a pane in another workspace still reveals it — in the window
// that clicked, and only there.
func TestPaneFocusAcrossWorkspacesMovesOnlyTheIssuingWindow(t *testing.T) {
	o, ws1, ws2, pane1, pane2 := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)
	drain(a)
	drain(b)

	o.handleCmd(a, cmd(t, "", browserproto.CmdPaneFocus, browserproto.PaneParams{Pane: pane2}))

	if a.view.ws != ws2 {
		t.Fatalf("the clicking window stayed on %q, want %q", a.view.ws, ws2)
	}
	if b.view.ws != ws2 {
		t.Fatalf("the other window moved to %q", b.view.ws)
	}
	if !a.view.visible[pane2] {
		t.Fatalf("the clicking window is not streaming the pane it revealed")
	}
	if a.view.visible[pane1] {
		t.Fatalf("the clicking window still streams the workspace it left")
	}
}

// --- the primary view ----------------------------------------------------------

// The primary view is the most recently OS-focused sizer. Every caller with no
// window of its own — catctl, a hook action, a runbook step — resolves through
// it, and Session.active tracks it so persistence keeps meaning "where you
// were".
func TestPrimaryViewFollowsFocusReports(t *testing.T) {
	o, ws1, ws2, _, _ := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)

	// B registered last, so it is in front and primary.
	if o.primaryView() != b {
		t.Fatal("the most recently opened window should be primary")
	}
	if got := o.session.ActiveWorkspaceID(); got != ws2 {
		t.Fatalf("session active = %q, want the primary's %q", got, ws2)
	}

	o.handleUp(a, &browserproto.Focus{T: browserproto.MsgFocus, Focused: true})

	if o.primaryView() != a {
		t.Fatal("a focus report did not move the primary view")
	}
	if got := o.session.ActiveWorkspaceID(); got != ws1 {
		t.Fatalf("session active = %q, want the new primary's %q", got, ws1)
	}
}

// Dropping the primary hands it to the remaining sizer — and takes the session
// default with it, so a save landing after the close records a workspace some
// window is actually on.
func TestDroppingThePrimaryHandsItOver(t *testing.T) {
	o, ws1, ws2, _, _ := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)
	if o.primaryView() != b {
		t.Fatal("precondition: the last window opened is primary")
	}

	o.dropConn(b)

	if o.primaryView() != a {
		t.Fatal("the surviving sizer did not become primary")
	}
	if got := o.session.ActiveWorkspaceID(); got != ws1 {
		t.Fatalf("session active = %q, want the surviving window's %q", got, ws1)
	}
	// Closing a window must never mutate the session (decision 8): the
	// workspace it was showing is still there, with its panes and its area.
	if o.session.WorkspaceByID(ws2) == nil {
		t.Fatal("closing a window closed its workspace")
	}
	if o.areaFor(ws2).Width != 120 {
		t.Fatalf("%s forgot its area when its window closed: %d cols", ws2, o.areaFor(ws2).Width)
	}
}

// A viewer (a phone) owns no geometry and follows the primary view: whichever
// desktop window the user touched last, which is the phone's whole idea of
// "the desktop".
func TestViewerFollowsThePrimaryAcrossAChange(t *testing.T) {
	o, ws1, ws2, _, _ := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)
	phone := &client{o: o, out: make(chan []byte, 256), viewer: true,
		trans: make(map[uint32]*browserproto.FrameTranslator),
		view:  view{visible: map[uint32]bool{}}}
	o.registerConn(phone, &browserproto.Init{Viewer: true, Cols: 40, Rows: 30})

	if got := o.viewWS(phone); got != ws2 {
		t.Fatalf("viewer follows %q, want the primary's %q", got, ws2)
	}
	o.handleUp(a, &browserproto.Focus{T: browserproto.MsgFocus, Focused: true})
	o.applyModel()
	if got := o.viewWS(phone); got != ws1 {
		t.Fatalf("viewer follows %q after the primary moved, want %q", got, ws1)
	}
	// And it still declares nothing: a phone's 40x30 must not become a
	// workspace's area just because it is following one.
	if o.areaFor(ws1).Width != 200 {
		t.Fatalf("the viewer sized %s: %d cols", ws1, o.areaFor(ws1).Width)
	}
	_ = b
}

// A caller with no window (the control API path) acts on the primary view.
func TestViewLessCallerActsOnThePrimary(t *testing.T) {
	o, ws1, ws2, pane1, pane2 := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	openWindow(o, ws2, 120, 40)
	o.handleUp(a, &browserproto.Focus{T: browserproto.MsgFocus, Focused: true})

	// The dispatcher catctl builds: no view, so d.ws() is "" and every command
	// resolves through the session default, which tracks the primary.
	if got := o.focusedPaneID(); got != pane1 {
		t.Fatalf("view-less focused pane = %d, want the primary window's %d", got, pane1)
	}
	o.SetViewWorkspace(ws2)
	if a.view.ws != ws2 {
		t.Fatalf("a view-less workspace switch did not move the primary window (%q)", a.view.ws)
	}
	if got := o.focusedPaneID(); got != pane2 {
		t.Fatalf("after the switch the focused pane is %d, want %d", got, pane2)
	}
}

// --- union visibility ----------------------------------------------------------

// "Is anyone looking" is the union of every view. A completion in a pane some
// window shows clears its unseen badge; a pane nobody shows keeps it.
func TestUnseenClearsWhenAnyWindowShowsThePane(t *testing.T) {
	o, ws1, ws2, pane1, pane2 := twoWorkspaceOrch(t)
	openWindow(o, ws2, 120, 40) // only w2 is on screen

	if o.visible[pane1] {
		t.Fatalf("pane %d in %s is streamed by nobody but counts as visible", pane1, ws1)
	}
	if !o.visible[pane2] {
		t.Fatalf("pane %d in %s is on screen but the union says otherwise", pane2, ws2)
	}

	// Drive each pane working → idle, which is the "finished" transition that
	// decides the badge.
	for _, pid := range []uint32{pane1, pane2} {
		o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "working"})
		o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "idle"})
	}

	if !o.panes[pane1].unseen {
		t.Fatal("a completion in a pane no window shows should raise the unseen badge")
	}
	if o.panes[pane2].unseen {
		t.Fatal("a completion in a pane a window is showing should not raise the badge")
	}
}

// --- the census -----------------------------------------------------------------

// Clients grows a per-view breakdown so a page can mark a workspace another
// window already has open, and a viewer can label which view it follows.
func TestClientsCensusCarriesTheViews(t *testing.T) {
	o, ws1, ws2, _, _ := twoWorkspaceOrch(t)
	openWindow(o, ws1, 200, 60)
	openWindow(o, ws2, 120, 40)

	msg := o.clientsMsg()
	if msg.Total != 2 || msg.Sizers != 2 {
		t.Fatalf("census = %d clients / %d sizers, want 2/2", msg.Total, msg.Sizers)
	}
	if len(msg.Views) != 2 {
		t.Fatalf("census carried %d views, want 2", len(msg.Views))
	}
	seen := map[string][2]uint16{}
	primaries := 0
	for _, v := range msg.Views {
		seen[v.Workspace] = [2]uint16{v.Cols, v.Rows}
		if v.Primary {
			primaries++
		}
	}
	if seen[ws1] != [2]uint16{200, 60} || seen[ws2] != [2]uint16{120, 40} {
		t.Fatalf("census views = %v, want each window's own workspace and grid", seen)
	}
	if primaries != 1 {
		t.Fatalf("%d views claim to be primary, want exactly 1", primaries)
	}
}

// --- Init.Workspace --------------------------------------------------------------

// An unknown workspace id is never an error: it typically comes from a URL the
// user bookmarked before that workspace was closed, and the window opens on the
// primary view instead.
func TestUnknownInitWorkspaceFallsBackToThePrimary(t *testing.T) {
	o, ws1, _, _, _ := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, "w-does-not-exist", 100, 30)

	if got := o.viewWS(b); got != ws1 {
		t.Fatalf("a window with a stale ?ws= opened on %q, want the primary's %q", got, ws1)
	}
	if l := lastLayout(t, b); l == nil || activeWSOf(l) != ws1 {
		t.Fatalf("its first layout did not show %s", ws1)
	}
	_ = a
}

// A view whose workspace is closed by another window falls back rather than
// going blank or refusing commands.
func TestViewSurvivesItsWorkspaceBeingClosed(t *testing.T) {
	o, ws1, ws2, _, _ := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)
	drain(a)
	drain(b)

	o.handleCmd(a, cmd(t, "", browserproto.CmdWorkspaceClose, browserproto.WorkspaceParams{ID: ws2}))

	if o.session.WorkspaceByID(ws2) != nil {
		t.Fatal("workspace.close did not close the workspace")
	}
	if got := o.viewWS(b); got != ws1 {
		t.Fatalf("the orphaned window shows %q, want the fallback %q", got, ws1)
	}
	if len(b.view.visible) == 0 {
		t.Fatal("the orphaned window streams nothing")
	}
}

// --- daemon-side sizing ----------------------------------------------------------

// Every pane of every workspace stays live and sized — that has always been
// true, and it is what makes a second window need no PTY work at all. What
// changed is which area each workspace is sized against.
func TestEveryWorkspaceKeepsItsOwnGrid(t *testing.T) {
	o, ws1, ws2, pane1, pane2 := twoWorkspaceOrch(t)
	pd := newPipeDaemon(t, o)
	openWindow(o, ws1, 200, 60)
	openWindow(o, ws2, 120, 40)
	o.applyModel()

	grids := o.desiredGrids()
	if grids[pane1][0] != 200 {
		t.Fatalf("pane %d in %s sized %v, want its window's 200 cols", pane1, ws1, grids[pane1])
	}
	if grids[pane2][0] != 120 {
		t.Fatalf("pane %d in %s sized %v, want its window's 120 cols", pane2, ws2, grids[pane2])
	}
	// And the daemon was actually told: the grids above are the intent, the
	// resize on the wire is the effect.
	var rs orchestration.Resize
	if err := json.Unmarshal(pd.expect(t, orchestration.MsgResize), &rs); err != nil {
		t.Fatalf("decode resize: %v", err)
	}
	if rs.Cols != 200 && rs.Cols != 120 {
		t.Fatalf("daemon resize to %d cols matches neither window", rs.Cols)
	}
}

// A workspace no window shows keeps its last area, so its panes keep their
// shape for the window that comes back to it.
func TestUnshownWorkspaceKeepsItsArea(t *testing.T) {
	o, ws1, ws2, _, pane2 := twoWorkspaceOrch(t)
	b := openWindow(o, ws2, 120, 40)
	openWindow(o, ws1, 200, 60)
	before := o.desiredGrids()[pane2]

	o.dropConn(b)
	o.applyModel()

	if got := o.desiredGrids()[pane2]; got != before {
		t.Fatalf("an unshown workspace was reshaped: %v → %v", before, got)
	}
	if o.areaFor(ws2) != (layout.Rect{Width: 120, Height: 40}) {
		t.Fatalf("%s area = %v, want the 120x40 its window left behind", ws2, o.areaFor(ws2))
	}
}

// Persistence keeps meaning "where you were": the snapshot's active workspace
// is the primary view's, so a cold start opens the window the user was last in
// rather than whichever window happened to issue the last command.
func TestSnapshotRecordsThePrimaryViewsWorkspace(t *testing.T) {
	o, ws1, ws2, _, _ := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)

	// B is primary (opened last). A command from A must not move the default.
	o.handleCmd(a, cmd(t, "", browserproto.CmdPaneCycle, browserproto.CycleParams{Next: true}))
	if got := o.session.Workspaces()[o.session.Snapshot().Active].ID; got != ws2 {
		t.Fatalf("snapshot active = %q, want the primary's %q", got, ws2)
	}

	// Touch A: it becomes primary and the snapshot follows.
	o.handleUp(a, &browserproto.Focus{T: browserproto.MsgFocus, Focused: true})
	if got := o.session.Workspaces()[o.session.Snapshot().Active].ID; got != ws1 {
		t.Fatalf("snapshot active = %q after the primary moved, want %q", got, ws1)
	}
	_ = b
}

// --- phase 4: per-window title and focused-window-wins sizing ---------------------

// The browser tab title is a per-window fact: its own focused pane, or the
// workspace it is showing. One shared title is wrong in both windows when they
// are on two projects, and the tab strip is exactly where you look to tell them
// apart.
func TestTitleIsPerWindow(t *testing.T) {
	o, ws1, ws2, pane1, pane2 := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)
	o.panes[pane1].title = "build"
	o.panes[pane2].title = "logs"
	drain(a)
	drain(b)

	o.broadcastTitle()

	if got := lastTitle(t, a); got != "build" {
		t.Fatalf("window A title = %q, want its own focused pane's %q", got, "build")
	}
	if got := lastTitle(t, b); got != "logs" {
		t.Fatalf("window B title = %q, want its own focused pane's %q", got, "logs")
	}
}

// lastTitle drains a connection and returns the last app title on it.
func lastTitle(t *testing.T, c *client) string {
	t.Helper()
	out := ""
	for {
		select {
		case b := <-c.out:
			if m, err := browserproto.DecodeDown(b); err == nil {
				if ti, ok := m.(*browserproto.Title); ok {
					out = ti.Title
				}
			}
		default:
			return out
		}
	}
}

// Two windows on ONE workspace mirror, so exactly one grid can be right for
// both — and the one to pick is the window the user is in front of. "Last
// reporter wins" is an accident of timing; this is something a user can predict.
func TestFocusedWindowWinsSizing(t *testing.T) {
	o, ws1, _, pane1, _ := twoWorkspaceOrch(t)
	big := openWindow(o, ws1, 200, 60)
	small := openWindow(o, ws1, 100, 30)

	// small connected last, so it is in front and owns the grid.
	if got := o.areaFor(ws1).Width; got != 100 {
		t.Fatalf("area = %d cols with the small window in front, want 100", got)
	}

	// The user alt-tabs to the big window: it blurs the small one and focuses
	// the big one, which is the pair of reports a real desktop sends.
	o.handleUp(small, &browserproto.Focus{T: browserproto.MsgFocus, Focused: false})
	o.handleUp(big, &browserproto.Focus{T: browserproto.MsgFocus, Focused: true})

	if got := o.areaFor(ws1).Width; got != 200 {
		t.Fatalf("area = %d cols after focusing the big window, want 200", got)
	}
	if got := o.desiredGrids()[pane1][0]; got != 200 {
		t.Fatalf("pane grid = %d cols, want the focused window's 200", got)
	}

	// With neither in front, the last reported area stands — a background
	// workspace keeps its shape rather than snapping to some other window's.
	o.handleUp(big, &browserproto.Focus{T: browserproto.MsgFocus, Focused: false})
	if got := o.areaFor(ws1).Width; got != 200 {
		t.Fatalf("area = %d cols with nobody in front, want the last reported 200", got)
	}
}

// A window on another workspace coming forward must not reflow a workspace it
// is not showing.
func TestFocusChangeDoesNotReflowAnotherWorkspace(t *testing.T) {
	o, ws1, ws2, pane1, _ := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 100, 30)
	before := o.desiredGrids()[pane1]

	o.handleUp(a, &browserproto.Focus{T: browserproto.MsgFocus, Focused: false})
	o.handleUp(b, &browserproto.Focus{T: browserproto.MsgFocus, Focused: true})

	if got := o.desiredGrids()[pane1]; got != before {
		t.Fatalf("focusing the %s window reshaped %s: %v → %v", ws2, ws1, before, got)
	}
}

// Moving a tab to another workspace moves it between WINDOWS: it leaves one
// window's viewport and joins the other's, and both are re-sized against their
// own workspace's grid.
func TestMoveTabBetweenWindows(t *testing.T) {
	o, ws1, ws2, pane1, _ := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 100, 30)
	// A second tab in ws1 so moving one does not empty the workspace.
	if _, _, err := o.session.CreateTabIn(ws1); err != nil {
		t.Fatalf("CreateTabIn: %v", err)
	}
	o.applyModel()
	drain(a)
	drain(b)
	num, _ := o.session.WorkspaceByID(ws1).PublicTabNumber(0)

	o.handleCmd(a, cmd(t, "m1", browserproto.CmdTabMoveToWorkspace,
		browserproto.MoveTabToWorkspaceParams{Workspace: ws2, Num: num}))
	expectCmdOK(t, a)

	if o.session.PaneWorkspace(layout.PaneID(pane1)).ID != ws2 {
		t.Fatalf("the tab's pane did not travel to %s", ws2)
	}
	if a.view.visible[pane1] {
		t.Fatal("the window it left is still streaming the moved pane")
	}
	if !b.view.visible[pane1] {
		t.Fatal("the window it arrived in is not streaming the moved pane")
	}
	// And it is sized against its NEW window's grid, not the one it left.
	if got := o.desiredGrids()[pane1][0]; got != 100 {
		t.Fatalf("moved pane grid = %d cols, want the destination window's 100", got)
	}
}

// expectCmdOK drains a connection until a cmd_result arrives and fails on a
// refusal — a command that quietly fails otherwise shows up as a model
// assertion three lines later, saying nothing about why.
func expectCmdOK(t *testing.T, c *client) {
	t.Helper()
	for {
		select {
		case b := <-c.out:
			m, err := browserproto.DecodeDown(b)
			if err != nil {
				continue
			}
			if res, ok := m.(*browserproto.CmdResult); ok {
				if !res.Ok {
					t.Fatalf("command failed: %s", res.Error)
				}
				return
			}
		default:
			t.Fatal("no cmd_result arrived")
		}
	}
}
