package app

import (
	"github.com/rohanthewiz/cats/internal/flags"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// This file assembles the read-only query results (the §7 *.list / *.get
// commands) from the Session domain model into the protocol-neutral result
// structs in command_vocab.go. It is the read counterpart to commands.go's
// mutations: no Backend, no effects — just a snapshot of session state for a
// CLI/control-API or browser to introspect. Every method here is a pure query
// over the same single-goroutine Session, so it needs no locking.

// Info returns a one-shot snapshot of the whole session (session.get) as the
// session-default view sees it.
func (s *Session) Info() SessionInfoResult { return s.InfoIn("") }

// InfoIn is Info answered for one view: "the active workspace" and "the focused
// pane" are per-window facts once several windows are open (view.go), and
// session.get issued from a window must describe that window — otherwise a
// script running in window B reads window A's focus and acts on the wrong pane.
func (s *Session) InfoIn(wsID string) SessionInfoResult {
	res := SessionInfoResult{
		ActiveWorkspace: s.viewWorkspace(wsID).ID,
		Workspaces:      len(s.workspaces),
		Panes:           s.totalPanes(),
		Cwd:             s.cwd,
	}
	if id, ok := s.FocusedPaneIn(wsID); ok {
		if pub, ok := s.PublicPaneID(id); ok {
			res.FocusedPane = pub
		}
	}
	return res
}

// ListWorkspaces describes every workspace in order (workspace.list).
func (s *Session) ListWorkspaces() []WorkspaceEntry { return s.ListWorkspacesIn("") }

// ListWorkspacesIn is ListWorkspaces with Active meaning "the one this view
// shows" rather than "the one the session defaults to".
func (s *Session) ListWorkspacesIn(wsID string) []WorkspaceEntry {
	active := s.viewWorkspaceIndex(wsID)
	out := make([]WorkspaceEntry, 0, len(s.workspaces))
	for i, ws := range s.workspaces {
		out = append(out, WorkspaceEntry{
			ID:     ws.ID,
			Name:   ws.DisplayName(),
			Active: i == active,
			Tabs:   len(ws.Tabs),
			Locked: ws.Locked,
			Asleep: ws.Asleep,
			Parked: ParkedInfo(ws.ParkedAgents),
			Host:   ws.HostID, // as stored: "" = whatever the default host is

			FlagInfo: NewFlagInfo(ws.Flag),
		})
	}
	return out
}

// ListTabs describes the tabs of one workspace ("" = the active workspace),
// echoing the resolved workspace id. ok is false only when a non-empty id names
// no known workspace (tab.list). meta feeds tab auto-naming (TabDisplayName);
// nil skips derivation and reports the plain custom-name-or-number.
func (s *Session) ListTabs(workspaceID string, meta func(uint32) PaneMeta) (tabs []TabEntry, resolved string, ok bool) {
	return s.ListTabsIn("", workspaceID, meta)
}

// ListTabsIn is ListTabs with the caller's view supplying the default: an
// unaddressed tab.list from a window lists *that window's* workspace.
func (s *Session) ListTabsIn(viewWS, workspaceID string, meta func(uint32) PaneMeta) (tabs []TabEntry, resolved string, ok bool) {
	idx := s.viewWorkspaceIndex(viewWS)
	if workspaceID != "" {
		i, found := s.workspaceIndexByID(workspaceID)
		if !found {
			return nil, "", false
		}
		idx = i
	}
	ws := s.workspaces[idx]
	out := make([]TabEntry, 0, len(ws.Tabs))
	for i, tab := range ws.Tabs {
		name := tab.DisplayName()
		if meta != nil {
			name = s.TabDisplayName(tab, meta)
		}
		out = append(out, TabEntry{
			Num:    tab.Number,
			Name:   name,
			Active: i == ws.ActiveTabIndex(),
			Zoomed: tab.Zoomed,
			Panes:  tab.Layout.PaneCount(),
		})
	}
	return out, ws.ID, true
}

// ListPanes describes every pane across all workspaces and tabs (pane.list).
func (s *Session) ListPanes() []PaneInfo { return s.ListPanesIn("") }

// ListPanesIn is ListPanes with Visible meaning "on screen in this view".
func (s *Session) ListPanesIn(wsID string) []PaneInfo {
	visible := s.visibleSetIn(wsID)
	var out []PaneInfo
	for _, ws := range s.workspaces {
		for _, tab := range ws.Tabs {
			focused := tab.Layout.Focused()
			for _, id := range tab.Layout.PaneIDs() {
				out = append(out, s.paneInfo(ws, id, id == focused, visible[id]))
			}
		}
	}
	return out
}

// ListFlagged is the flagged subset of ListWorkspaces and ListPanes
// (flag.list), for the session-default view.
func (s *Session) ListFlagged(kind flags.Kind) FlagListResult {
	return s.ListFlaggedIn("", kind)
}

// ListFlaggedIn is ListFlagged answered for one view, so Active and Visible mean
// what they mean everywhere else in this file.
//
// It filters the two full listings rather than walking the model again. That is
// deliberate and costs one pass over rows that are cheap to build: the rows a
// flag listing shows MUST be byte-identical to the ones `workspaces` and `panes`
// show, or a client would have to reconcile two descriptions of the same pane.
// Building them the one way there is only one way to get that.
//
// kind "" lists everything; anything else is an exact match on the stored kind,
// which is why a custom glyph filters exactly like a named kind — the two halves
// of the vocabulary are one string field (internal/flags).
func (s *Session) ListFlaggedIn(wsID string, kind flags.Kind) FlagListResult {
	match := func(f FlagInfo) bool {
		if f.Flag == "" {
			return false // unflagged: never in a flag listing
		}
		return kind == "" || f.Flag == string(kind)
	}
	// Non-nil empty slices: an empty listing is a normal answer, and a client
	// looping over `res.panes` should not have to tell null from [].
	out := FlagListResult{Workspaces: []WorkspaceEntry{}, Panes: []PaneInfo{}}
	for _, w := range s.ListWorkspacesIn(wsID) {
		if match(w.FlagInfo) {
			out.Workspaces = append(out.Workspaces, w)
		}
	}
	for _, p := range s.ListPanesIn(wsID) {
		if match(p.FlagInfo) {
			out.Panes = append(out.Panes, p)
		}
	}
	return out
}

// PaneInfoFor describes one pane addressed by internal id (nil target = the
// focused pane), reporting ok=false when the pane is unknown (pane.get).
func (s *Session) PaneInfoFor(target *layout.PaneID) (PaneInfo, bool) {
	return s.PaneInfoForIn("", target)
}

// PaneInfoForIn is PaneInfoFor resolved against a view: a nil target is that
// window's focused pane, and Visible is "on screen in that window".
func (s *Session) PaneInfoForIn(wsID string, target *layout.PaneID) (PaneInfo, bool) {
	id, err := s.ResolvePaneTargetIn(wsID, target)
	if err != nil {
		return PaneInfo{}, false
	}
	_, ws := s.workspaceIndexOf(id)
	if ws == nil {
		return PaneInfo{}, false
	}
	focused := false
	if tabIdx, ok := ws.FindTabIndexForPane(id); ok {
		focused = ws.Tabs[tabIdx].Layout.Focused() == id
	}
	return s.paneInfo(ws, id, focused, s.visibleSetIn(wsID)[id]), true
}

// paneInfo builds one PaneInfo, resolving the pane's public handle from its
// owning workspace and its custom name from the session.
func (s *Session) paneInfo(ws *workspace.Workspace, id layout.PaneID, focused, visible bool) PaneInfo {
	info := PaneInfo{Pane: uint32(id), Focused: focused, Visible: visible}
	if pub, ok := ws.PublicPaneID(id); ok {
		info.Handle = pub
	}
	if name, ok := s.PaneCustomName(id); ok {
		info.Name = name
	}
	// Read off the workspace's own pane state rather than through the session's
	// cross-workspace lookup: the caller already has the owning workspace in
	// hand, so re-scanning every tab of every workspace per row would make
	// pane.list quadratic in the session's size for a field that is usually
	// absent.
	if st, ok := ws.PaneStateFor(id); ok {
		info.FlagInfo = NewFlagInfo(st.Flag)
	}
	return info
}

// visibleSetIn is one view's viewport panes as a lookup set.
func (s *Session) visibleSetIn(wsID string) map[layout.PaneID]bool {
	ids := s.VisiblePaneIDsIn(wsID)
	set := make(map[layout.PaneID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}
