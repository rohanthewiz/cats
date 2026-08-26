//go:build ghostty

package main

import "github.com/rohanthewiz/cats/internal/app"

// This file is the runtime half of cats-level navigation: which window's
// history a nav.back/forward walks, and the post-dispatch hook that records
// where focus landed. The stack itself (app.NavHistory) and the walk live in
// internal/app beside the dispatcher.
//
//	handleCmd / controlDispatch / runbook step
//	    │ Dispatch(name, …)          — may move focus as its effect
//	    ▼
//	 noteNav(c, name)                — snapshot the issuing view's focus
//	    │                              location, offer it to the history
//	    ▼
//	 view.nav.Note(loc, coalesce)    — dedup / coalesce / push
//
// Recording by post-dispatch snapshot rather than per-command instrumentation
// is the point: focus moves as a side effect of many commands (a split focuses
// the new pane, pane.close refocuses a survivor, tab.create, ledger.jump), and
// comparing "where is this view now" after every command catches all of them,
// while Note's dedup makes the commands that moved nothing free.

// navHistoryFor is the issuing window's history, lazily allocated. A nil
// connection is the view-less caller (catctl, hook, runbook) and walks the
// PRIMARY view's history — the same resolution setViewWorkspace(nil, …) uses,
// so `catctl back` moves the window the user touched last. With no window
// connected at all there is no history to walk, and nil means nav.* no-ops.
func (o *orch) navHistoryFor(c *client) *app.NavHistory {
	if c == nil {
		c = o.primaryView()
	}
	if c == nil {
		return nil
	}
	if c.view.nav == nil {
		c.view.nav = app.NewNavHistory()
	}
	return c.view.nav
}

// NavHistory satisfies app's optional navHistoryBackend seam for the plain
// *orch backend — the one view-less dispatches (control API, runbook steps)
// run against. viewBackend shadows it with the issuing window's stack; without
// that shadow the embedded *orch would answer for the primary view and a
// background window's ⌘[ would walk the front window's trail.
func (o *orch) NavHistory() *app.NavHistory { return o.navHistoryFor(nil) }

// noteNav records where the issuing view's focus is now, after a command ran.
// Called by every dispatch site (browser, control API, runbook) and once at
// connection registration so entry zero is "where the window opened".
//
// nav.back/forward are skipped by name: the walk already left the cursor on
// the entry it focused, and recording them would make the history observe
// itself. Directional moves and cycles coalesce — a burst of hjkl within one
// tab replaces the current entry instead of pushing, so walking back later
// jumps between places, not between every pane a hop passed through.
func (o *orch) noteNav(c *client, cmdName string) {
	if cmdName == app.CmdNavBack || cmdName == app.CmdNavForward {
		return
	}
	h := o.navHistoryFor(c)
	if h == nil || o.session == nil {
		return
	}
	wsID := o.viewWS(c)
	pid, ok := o.session.FocusedPaneIn(wsID)
	if !ok {
		return // a workspace with no focusable pane is not a place to return to
	}
	ws := o.session.WorkspaceByID(wsID)
	if ws == nil {
		return
	}
	num, _ := ws.PublicTabNumber(ws.ActiveTabIndex())
	coalesce := cmdName == app.CmdPaneFocusDirection || cmdName == app.CmdPaneCycle
	h.Note(app.NavLocation{Workspace: wsID, Tab: num, Pane: pid}, coalesce)
}

// NavHistory on viewBackend hands the dispatcher the issuing window's stack —
// see the shadow-warning on (*orch).NavHistory above.
func (b viewBackend) NavHistory() *app.NavHistory { return b.orch.navHistoryFor(b.c) }
