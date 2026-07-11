package browserproto

import "github.com/rohanthewiz/herdr-web/internal/layout"

// Command names (§7): the control-API vocabulary. The WS2 orchestrator
// implements one command table serving both this protocol and the CLI/API.
const (
	CmdPaneSplit          = "pane.split"
	CmdPaneClose          = "pane.close"
	CmdPaneFocus          = "pane.focus"
	CmdPaneFocusDirection = "pane.focus_direction"
	CmdPaneCycle          = "pane.cycle"
	CmdPaneLast           = "pane.last"
	CmdPaneSwap           = "pane.swap"
	CmdPaneZoom           = "pane.zoom"
	CmdPaneRename         = "pane.rename"
	CmdPaneResizeBorder   = "pane.resize_border"
	CmdScroll             = "scroll"
	CmdRead               = "read"
	CmdTabCreate          = "tab.create"
	CmdTabClose           = "tab.close"
	CmdTabFocus           = "tab.focus"
	CmdTabRename          = "tab.rename"
	CmdWorkspaceCreate    = "workspace.create"
	CmdWorkspaceClose     = "workspace.close"
	CmdWorkspaceFocus     = "workspace.focus"
	CmdWorkspaceRename    = "workspace.rename"
	CmdAgentFocus         = "agent.focus"
	CmdServerReloadConfig = "server.reload_config"
	CmdServerStop         = "server.stop"
)

// Split direction wire values (pane.split).
const (
	SplitH = "h" // side-by-side (layout.Horizontal)
	SplitV = "v" // top/bottom (layout.Vertical)
)

// SplitDirection maps a wire direction value onto layout.Direction.
func SplitDirection(s string) (layout.Direction, bool) {
	switch s {
	case SplitH:
		return layout.Horizontal, true
	case SplitV:
		return layout.Vertical, true
	}
	return 0, false
}

// Cardinal direction wire values (pane.focus_direction, pane.swap).
const (
	DirLeft  = "left"
	DirRight = "right"
	DirUp    = "up"
	DirDown  = "down"
)

// NavDirection maps a wire cardinal value onto layout.NavDirection.
func NavDirection(s string) (layout.NavDirection, bool) {
	switch s {
	case DirLeft:
		return layout.Left, true
	case DirRight:
		return layout.Right, true
	case DirUp:
		return layout.Up, true
	case DirDown:
		return layout.Down, true
	}
	return 0, false
}

// SplitParams: pane.split. Pane nil = the focused pane.
type SplitParams struct {
	Pane      *uint32 `json:"pane,omitempty"`
	Direction string  `json:"direction"` // SplitH | SplitV
}

// PaneParams: pane.focus, agent.focus — commands addressing a specific pane.
type PaneParams struct {
	Pane uint32 `json:"pane"`
}

// OptPaneParams: pane.close, pane.zoom. Pane nil = the focused pane.
type OptPaneParams struct {
	Pane *uint32 `json:"pane,omitempty"`
}

// DirParams: pane.focus_direction, pane.swap.
type DirParams struct {
	Dir string `json:"dir"` // DirLeft | DirRight | DirUp | DirDown
}

// CycleParams: pane.cycle.
type CycleParams struct {
	Next bool `json:"next"`
}

// RenamePaneParams: pane.rename ("" clears the custom name).
type RenamePaneParams struct {
	Pane uint32 `json:"pane"`
	Name string `json:"name"`
}

// ResizeBorderParams: pane.resize_border. Border is the opaque id from the
// layout message's borders list; Ratio is the split's new first-child ratio.
type ResizeBorderParams struct {
	Border string  `json:"border"`
	Ratio  float32 `json:"ratio"`
}

// ScrollParams: scroll. Delta lines: negative scrolls up into history,
// positive back toward the live bottom (β ScrollViewport semantics).
type ScrollParams struct {
	Pane  uint32 `json:"pane"`
	Delta int    `json:"delta"`
}

// ReadParams: read — extract selection text. Anchor/Cursor are [row, col] in
// absolute screen-buffer coordinates (row from the top of scrollback, per
// β SelectionPoint; derive from the frame's Scroll). Rect selects a block
// region instead of a reading-order range.
type ReadParams struct {
	Pane   uint32    `json:"pane"`
	Anchor [2]uint32 `json:"anchor"`
	Cursor [2]uint32 `json:"cursor"`
	Rect   bool      `json:"rect,omitempty"`
}

// ReadResult is CmdResult.Data for a successful read.
type ReadResult struct {
	Text string `json:"text"`
}

// TabParams: tab.focus.
type TabParams struct {
	Num int `json:"num"`
}

// OptTabParams: tab.close. Num nil = the active tab.
type OptTabParams struct {
	Num *int `json:"num,omitempty"`
}

// RenameTabParams: tab.rename ("" clears the custom name).
type RenameTabParams struct {
	Num  int    `json:"num"`
	Name string `json:"name"`
}

// WorkspaceParams: workspace.focus, workspace.close.
type WorkspaceParams struct {
	ID string `json:"id"` // public workspace id, e.g. "w1"
}

// RenameWorkspaceParams: workspace.rename ("" reverts to auto-naming).
type RenameWorkspaceParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
