package browserproto

import (
	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// BorderID encodes a split node's tree path as the opaque handle carried in
// layout.borders[].id: "r" for the root split, then one '0' (first child) or
// '1' (second child) per step, e.g. "r01". The browser echoes it verbatim in
// pane.resize_border; the server decodes it with BorderPath — no per-border
// state to keep.
func BorderID(path []bool) string {
	b := make([]byte, 0, len(path)+1)
	b = append(b, 'r')
	for _, second := range path {
		if second {
			b = append(b, '1')
		} else {
			b = append(b, '0')
		}
	}
	return string(b)
}

// BorderPath decodes a BorderID back into a split path (moved to internal/app
// with the command vocabulary, since the dispatcher decodes pane.resize_border).
// Re-exported here so the "r01" encode/decode pair stays discoverable together.
var BorderPath = app.BorderPath

// paneFlagInfo reads one pane's user flag off the workspace that owns it. A
// pane the workspace does not know yields the zero value, which is the
// unflagged encoding — the same answer as "no flag set", and the right one:
// this is chrome, not a place to fail a layout over.
func paneFlagInfo(ws *workspace.Workspace, id layout.PaneID) app.FlagInfo {
	st, ok := ws.PaneStateFor(id)
	if !ok {
		return app.FlagInfo{}
	}
	return app.NewFlagInfo(st.Flag)
}

func rectOf(r layout.Rect) Rect {
	return Rect{r.X, r.Y, r.Width, r.Height}
}

// PaneRectFrom translates one computed layout.PaneInfo; pub is the pane's
// public handle (workspace.PublicPaneID). The caller fills in the fields the
// geometry does not know — the pane's host (resolved against the roster) and
// its user flag.
func PaneRectFrom(info layout.PaneInfo, pub string) PaneRectInfo {
	p := PaneRectInfo{
		Pane:    uint32(info.ID),
		Pub:     pub,
		Rect:    rectOf(info.Rect),
		Inner:   rectOf(info.InnerRect),
		Focused: info.IsFocused,
	}
	if info.ScrollbarRect != nil {
		sb := rectOf(*info.ScrollbarRect)
		p.Scrollbar = &sb
	}
	return p
}

// BorderFrom translates one layout.SplitBorder.
func BorderFrom(sb layout.SplitBorder) BorderInfo {
	return BorderInfo{
		ID:    BorderID(sb.Path),
		Pos:   sb.Pos,
		Dir:   uint8(sb.Direction),
		Ratio: sb.Ratio,
		Area:  rectOf(sb.Area),
	}
}

// BuildLayout assembles the layout message for one connection's viewport:
// every workspace (sidebar order), the active workspace's tabs, and the
// active tab's pane rects + borders computed over area. A zoomed tab shows
// only its focused pane at the full area, with no draggable borders.
//
// AgentSummary is left empty — agent state lives outside the workspace
// package; the caller patches it in from its detection rollup.
func BuildLayout(workspaces []*workspace.Workspace, active int, area layout.Rect) Layout {
	msg := Layout{
		T:          MsgLayout,
		Workspaces: make([]WorkspaceInfo, 0, len(workspaces)),
		Tabs:       []TabInfo{},
		Panes:      []PaneRectInfo{},
		Borders:    []BorderInfo{},
	}
	for i, ws := range workspaces {
		msg.Workspaces = append(msg.Workspaces, WorkspaceInfo{
			ID:       ws.ID,
			Name:     ws.DisplayName(),
			Active:   i == active,
			Locked:   ws.Locked,
			Asleep:   ws.Asleep,
			Parked:   app.ParkedInfo(ws.ParkedAgents),
			FlagInfo: app.NewFlagInfo(ws.Flag),
		})
	}
	if active < 0 || active >= len(workspaces) {
		return msg
	}
	ws := workspaces[active]
	for i, tab := range ws.Tabs {
		msg.Tabs = append(msg.Tabs, TabInfo{
			Num:    tab.Number,
			Name:   tab.DisplayName(),
			Active: i == ws.ActiveTabIndex(),
			Zoomed: tab.Zoomed,
		})
	}
	tab := ws.ActiveTab()
	if tab == nil {
		return msg
	}
	if tab.Zoomed {
		focus := tab.Layout.Focused()
		pub, _ := ws.PublicPaneID(focus)
		msg.Panes = append(msg.Panes, PaneRectInfo{
			Pane:     uint32(focus),
			Pub:      pub,
			Rect:     rectOf(area),
			Inner:    rectOf(area),
			Focused:  true,
			FlagInfo: paneFlagInfo(ws, focus),
		})
		return msg
	}
	for _, info := range tab.Layout.Panes(area) {
		pub, _ := ws.PublicPaneID(info.ID)
		p := PaneRectFrom(info, pub)
		p.FlagInfo = paneFlagInfo(ws, info.ID)
		msg.Panes = append(msg.Panes, p)
	}
	for _, sb := range tab.Layout.Splits(area) {
		msg.Borders = append(msg.Borders, BorderFrom(sb))
	}
	return msg
}
