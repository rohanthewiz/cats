//go:build ghostty

package main

import (
	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/layout"
)

// Views: one session, many windows.
//
// A connection used to be a mirror. The session had one viewport — one active
// workspace, one active tab through it, one focused pane — and every browser
// got the same one: `workspace.focus` in one window switched the other,
// keystrokes went to *the* focused pane, and the last client to `resize` set
// pane sizes for everyone. That made the phone viewer possible without any
// notion of "which window", and it made the obvious desktop workflow — a
// window per project on a second monitor — impossible on every topology.
//
// A view is the fix: the per-connection half of the viewport.
//
//	                     ┌── client A ── view{ws:"w1", area:200x60} ──┐
//	 browser windows ────┤                                            ├──► one Session
//	                     └── client B ── view{ws:"w2", area:120x40} ──┘    (all panes live)
//
// Two windows on *different* workspaces are fully independent — own tab, own
// focus, own zoom, own grid. Two windows on the *same* workspace mirror, which
// is what every client does today: one active tab and one focused pane per
// workspace is the model, and splitting that would put two focuses in one
// layout.TileLayout for a workflow that does not need it.
//
// Nothing about a window is session state. Views live exactly as long as their
// WebSocket, nothing about them is persisted, and closing a window never
// mutates the session: a workspace no window shows keeps running exactly as a
// background workspace does. The session's own `active` workspace survives as
// the *default* for callers with no window (catctl, hooks, runbook steps) and
// as what persistence means by "where you were" — it tracks the primary view.

// view is what one connection is looking at.
type view struct {
	// ws is the public workspace id ("w2") this window shows. "" means "follow
	// the primary view", which is what a viewer (a phone) and a connection that
	// named no workspace both want.
	ws string
	// area is this window's pane-rendering grid, in cells. Zero for a viewer,
	// which declares no geometry and renders whatever the sizers settled on.
	area layout.Rect
	// visible is the pane set this window streams: its workspace's active tab's
	// leaves, or just the focused pane when that tab is zoomed. The orch's
	// o.visible is the union of these (decision 6) — "is anyone looking".
	visible map[uint32]bool
	// title is the browser-tab title last sent to this window. The title is a
	// per-window fact — its own focused pane, or the workspace it shows — so
	// the change detection that used to be one field on the orch is one per
	// view.
	title string
}

// --- resolving a view ---------------------------------------------------------

// viewWS is the workspace id a connection actually shows: its own when it named
// one that still exists, else the primary view's, else the session default.
//
// The fallback chain is what keeps a stale id harmless. A window's workspace can
// be closed by another window, and a bookmarked "?ws=w7" can outlive the
// workspace it named by a week; neither should produce an error or a blank
// window, so both land on whatever the session would have shown anyway.
func (o *orch) viewWS(c *client) string {
	if o.session == nil {
		return "" // a bare orch (test harnesses build them); no workspaces to name
	}
	if c != nil && c.view.ws != "" {
		if o.session.WorkspaceByID(c.view.ws) != nil {
			return c.view.ws
		}
	}
	// A "" view (a viewer, or a window that named nothing) follows the primary,
	// which is the phone's whole idea of "the desktop": whichever window the
	// user touched last.
	if p := o.primaryView(); p != nil && p != c && p.view.ws != "" {
		if o.session.WorkspaceByID(p.view.ws) != nil {
			return p.view.ws
		}
	}
	return o.session.ActiveWorkspaceID()
}

// viewOf is the app-layer View for a connection — what the dispatcher routes a
// browser command through. A nil connection is the view-less caller and gets
// the zero View, which every …In form reads as "the session's active
// workspace".
func (o *orch) viewOf(c *client) app.View {
	if c == nil {
		return app.View{}
	}
	return app.View{WorkspaceID: o.viewWS(c)}
}

// primaryView is the most recently OS-focused sizer connection, falling back to
// the most recent one that was, and to nil when no sizer is connected at all.
//
// It is the answer to "which window did the user touch last", and it is what
// every caller with no window of its own resolves through: catctl from a pane,
// a hook action, a runbook step, a ui.notify click-through, and every viewer.
// The bookkeeping is free — a Focus report already arrives for every foreground
// change (browserproto.Focus), so this reads a signal that was already on the
// wire.
//
// Viewers are excluded deliberately. A phone in the foreground is "somebody
// looking" (anyClientFocused) but it owns no geometry and follows a desktop
// window; making it primary would point catctl at whatever the phone happened
// to be showing and leave the phone following itself.
func (o *orch) primaryView() *client {
	for _, c := range o.focusOrder {
		if _, ok := o.conns[c]; ok && !c.viewer {
			return c
		}
	}
	return nil
}

// noteFocusOrder moves a connection to the front of the focus-recency list —
// the list primaryView reads. Called when a connection registers (a window that
// just opened is in front until it says otherwise) and on every Focus report
// that says the window came forward.
func (o *orch) noteFocusOrder(c *client) {
	for i, x := range o.focusOrder {
		if x == c {
			o.focusOrder = append(o.focusOrder[:i], o.focusOrder[i+1:]...)
			break
		}
	}
	o.focusOrder = append([]*client{c}, o.focusOrder...)
}

// forgetFocusOrder drops a connection from the recency list (dropConn).
func (o *orch) forgetFocusOrder(c *client) {
	for i, x := range o.focusOrder {
		if x == c {
			o.focusOrder = append(o.focusOrder[:i], o.focusOrder[i+1:]...)
			return
		}
	}
}

// syncPrimaryActive re-points the session's active workspace at the primary
// view's, so `active` keeps meaning "where you were" for persistence and for
// every view-less caller.
//
// It runs after every model mutation rather than only on a workspace switch,
// because several commands move s.active as a side effect of something else —
// CreateWorkspaceAtOn makes the new workspace active, dropWorkspace shifts the
// index — and a non-primary window doing any of those must not drag the
// session default (and with it catctl, and the cold-start workspace) along
// behind it.
//
// With no sizer connected there is no primary and s.active is left exactly as
// it was: a headless catway, or one driven only by catctl, behaves as before.
func (o *orch) syncPrimaryActive() {
	p := o.primaryView()
	if p == nil {
		return
	}
	wsID := o.viewWS(p)
	if wsID == "" || wsID == o.session.ActiveWorkspaceID() {
		return
	}
	_ = o.session.FocusWorkspace(wsID)
}

// setViewWorkspace moves one connection's view to a workspace — the
// per-connection meaning of workspace.focus. A nil connection is the view-less
// caller and moves the primary view instead, which is the session default and
// therefore exactly what catctl has always done.
//
// The window's grid follows it: a sizer arriving on a workspace is the most
// recent report for that workspace's area (decision 5, last reporter wins), so
// its panes reflow to the window that is now showing them.
func (o *orch) setViewWorkspace(c *client, wsID string) {
	if c == nil {
		c = o.primaryView()
	}
	if c == nil {
		// Nobody is connected: the session default is the only view there is.
		_ = o.session.FocusWorkspace(wsID)
		return
	}
	c.view.ws = wsID
	o.noteViewArea(c)
	o.connsDirty = true // the Clients census carries each view's workspace
	o.syncPrimaryActive()
}

// --- per-workspace areas ------------------------------------------------------
//
// desiredGrids used to size every tab of every workspace from one shared area,
// which is what made a second window unusable: window 2's 120x40 reflowed
// window 1's 200x60 the moment it connected. Each workspace now has its own
// area — the grid of the sizer view that most recently reported while showing
// it — so a resize in one window touches only the workspace that window shows.
//
// A workspace nobody shows keeps its last known area, so its panes keep their
// shape for the window that comes back to it.

// noteViewArea records a sizer view's grid as its workspace's area. Viewers and
// zero grids are ignored: a viewer declares no geometry (Init.Viewer), and a
// zero area would collapse every pane in the workspace to nothing.
func (o *orch) noteViewArea(c *client) {
	if c == nil || c.viewer || c.view.area.Width == 0 || c.view.area.Height == 0 {
		return
	}
	o.wsArea[o.viewWS(c)] = c.view.area
	if c == o.primaryView() {
		// o.area is the primary view's grid: the Backend.Area() contract, the
		// grid the Clients census reports, and the fallback for every workspace
		// no window has ever sized.
		o.area = c.view.area
	}
}

// areaFor is the grid a workspace's panes are laid out against: the last area
// reported for it, else the primary view's. Both viewportLayoutFor and
// desiredGrids resolve through it, which is what keeps the rects a window
// renders identical to the grids its panes were actually resized to.
func (o *orch) areaFor(wsID string) layout.Rect {
	// Focused window wins. Two windows on one workspace mirror (decision 2), so
	// exactly one grid can be right for both — and the one to pick is the window
	// the user is actually in front of. Without this the answer is "whichever
	// window resized or connected last", which is an accident of timing: alt-tab
	// to the small window, type in it, and the panes are still laid out for the
	// big one you left.
	//
	// focusOrder is most-recently-focused first, so the first match is the
	// front-most window showing this workspace.
	for _, c := range o.focusOrder {
		if _, ok := o.conns[c]; !ok || c.viewer || !c.focused {
			continue
		}
		if c.view.area.Width == 0 || c.view.area.Height == 0 || o.viewWS(c) != wsID {
			continue
		}
		return c.view.area
	}
	// Nobody showing it is in front — a workspace in a background window, or one
	// no window shows at all. Its last reported area stands, so its panes keep
	// their shape for the window that comes back to it.
	if r, ok := o.wsArea[wsID]; ok && r.Width > 0 && r.Height > 0 {
		return r
	}
	return o.area
}

// sharedSizerWorkspace reports whether more than one sizer window shows a
// workspace at different grids. It is the only case in which a window gaining
// or losing focus can change a pane's size, and therefore the only case worth
// reconciling the daemon for when a focus report arrives.
func (o *orch) sharedSizerWorkspace(wsID string) bool {
	var first layout.Rect
	seen := false
	for c := range o.conns {
		if c.viewer || o.viewWS(c) != wsID {
			continue
		}
		a := c.view.area
		if a.Width == 0 || a.Height == 0 {
			continue
		}
		if !seen {
			first, seen = a, true
			continue
		}
		if a != first {
			return true
		}
	}
	return false
}

// viewArea is the grid one connection's commands resolve against — directional
// pane navigation, which needs geometry to find "the pane to the left". A sizer
// uses its own; a viewer uses whatever its workspace settled on.
func (o *orch) viewArea(c *client) layout.Rect {
	if c != nil && !c.viewer && c.view.area.Width > 0 && c.view.area.Height > 0 {
		return c.view.area
	}
	return o.areaFor(o.viewWS(c))
}

// --- per-view visibility ------------------------------------------------------

// sendVisible delivers a pane-scoped message to the connections that are
// actually showing that pane, instead of broadcasting it to every window.
//
// Every call site used to read `if o.visible[pid] { o.broadcast(...) }`, which
// was correct when there was one viewport and is two wrongs with several: the
// windows not showing the pane get chrome for a pane they are not rendering,
// and — worse — the gate answers "is the *session* looking" rather than "is
// this window looking". Splitting it here means the union (o.visible) keeps its
// "is anyone looking" meaning for the scanners and badges, while delivery is
// per view.
func (o *orch) sendVisible(pid uint32, m any) {
	b, err := browserproto.Marshal(m)
	if err != nil {
		return
	}
	for c := range o.conns {
		if c.view.visible[pid] {
			o.enqueue(c, b)
		}
	}
}

// viewSees reports whether ONE window is showing a pane — the per-view half of
// the old o.visible gate, used to decide whether a client may address input or
// a mouse event at a pane. A nil connection, or one with no view yet (an
// internal caller, a test harness that never registered), falls back to the
// union, which is exactly what the gate meant before views existed.
func (o *orch) viewSees(c *client, pid uint32) bool {
	if c == nil || c.view.visible == nil {
		return o.visible[pid]
	}
	return c.view.visible[pid]
}
