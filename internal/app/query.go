package app

import (
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// This file assembles the read-only query results (the §7 *.list / *.get
// commands) from the Session domain model into the protocol-neutral result
// structs in command_vocab.go. It is the read counterpart to commands.go's
// mutations: no Backend, no effects — just a snapshot of session state for a
// CLI/control-API or browser to introspect. Every method here is a pure query
// over the same single-goroutine Session, so it needs no locking.

// Info returns a one-shot snapshot of the whole session (session.get).
func (s *Session) Info() SessionInfoResult {
	res := SessionInfoResult{
		ActiveWorkspace: s.ActiveWorkspace().ID,
		Workspaces:      len(s.workspaces),
		Panes:           s.totalPanes(),
		Cwd:             s.cwd,
	}
	if id, ok := s.FocusedPane(); ok {
		if pub, ok := s.PublicPaneID(id); ok {
			res.FocusedPane = pub
		}
	}
	return res
}

// ListWorkspaces describes every workspace in order (workspace.list).
func (s *Session) ListWorkspaces() []WorkspaceInfo {
	out := make([]WorkspaceInfo, 0, len(s.workspaces))
	for i, ws := range s.workspaces {
		out = append(out, WorkspaceInfo{
			ID:     ws.ID,
			Name:   ws.DisplayName(),
			Active: i == s.active,
			Tabs:   len(ws.Tabs),
		})
	}
	return out
}

// ListTabs describes the tabs of one workspace ("" = the active workspace),
// echoing the resolved workspace id. ok is false only when a non-empty id names
// no known workspace (tab.list).
func (s *Session) ListTabs(workspaceID string) (tabs []TabInfo, resolved string, ok bool) {
	idx := s.active
	if workspaceID != "" {
		i, found := s.workspaceIndexByID(workspaceID)
		if !found {
			return nil, "", false
		}
		idx = i
	}
	ws := s.workspaces[idx]
	out := make([]TabInfo, 0, len(ws.Tabs))
	for i, tab := range ws.Tabs {
		out = append(out, TabInfo{
			Num:    tab.Number,
			Name:   tab.DisplayName(),
			Active: i == ws.ActiveTabIndex(),
			Zoomed: tab.Zoomed,
			Panes:  tab.Layout.PaneCount(),
		})
	}
	return out, ws.ID, true
}

// ListPanes describes every pane across all workspaces and tabs (pane.list).
func (s *Session) ListPanes() []PaneInfo {
	visible := s.visibleSet()
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

// PaneInfoFor describes one pane addressed by internal id (nil target = the
// focused pane), reporting ok=false when the pane is unknown (pane.get).
func (s *Session) PaneInfoFor(target *layout.PaneID) (PaneInfo, bool) {
	id, err := s.resolvePaneTarget(target)
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
	return s.paneInfo(ws, id, focused, s.visibleSet()[id]), true
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
	return info
}

// visibleSet is the current viewport's panes as a lookup set.
func (s *Session) visibleSet() map[layout.PaneID]bool {
	ids := s.VisiblePaneIDs()
	set := make(map[layout.PaneID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}
