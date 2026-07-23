package browserproto

import "github.com/rohanthewiz/cats/internal/app"

// The §7 command vocabulary (names, param/result structs, direction mappings)
// now lives in internal/app so one command table can serve both this browser
// protocol and a future CLI/control-API. These are thin re-exports for wire use
// — existing browserproto consumers keep spelling browserproto.Cmd*/*Params.

// Command names (§7).
const (
	CmdPaneSplit          = app.CmdPaneSplit
	CmdPaneClose          = app.CmdPaneClose
	CmdPaneFocus          = app.CmdPaneFocus
	CmdPaneFocusDirection = app.CmdPaneFocusDirection
	CmdPaneCycle          = app.CmdPaneCycle
	CmdPaneLast           = app.CmdPaneLast
	CmdPaneSwap           = app.CmdPaneSwap
	CmdPaneSwapWith       = app.CmdPaneSwapWith
	CmdPaneZoom           = app.CmdPaneZoom
	CmdPaneRename         = app.CmdPaneRename
	CmdPaneResizeBorder   = app.CmdPaneResizeBorder
	CmdScroll             = app.CmdScroll
	CmdRead               = app.CmdRead
	CmdCapture            = app.CmdCapture
	CmdWaitForOutput      = app.CmdWaitForOutput
	CmdPaneSendInput      = app.CmdPaneSendInput
	CmdTabCreate          = app.CmdTabCreate
	CmdTabClose           = app.CmdTabClose
	CmdTabFocus           = app.CmdTabFocus
	CmdTabRename          = app.CmdTabRename
	CmdTabMove            = app.CmdTabMove
	CmdWorkspaceCreate    = app.CmdWorkspaceCreate
	CmdWorkspaceClose     = app.CmdWorkspaceClose
	CmdWorkspaceFocus     = app.CmdWorkspaceFocus
	CmdWorkspaceRename    = app.CmdWorkspaceRename
	CmdWorkspaceMove      = app.CmdWorkspaceMove
	CmdAgentFocus         = app.CmdAgentFocus
	CmdServerReloadConfig = app.CmdServerReloadConfig
	CmdServerStop         = app.CmdServerStop
	CmdWorktreeList       = app.CmdWorktreeList
	CmdWorktreeCreate     = app.CmdWorktreeCreate
	CmdWorktreeOpen       = app.CmdWorktreeOpen
	CmdWorktreeRemove     = app.CmdWorktreeRemove
	CmdConfigGet          = app.CmdConfigGet
	CmdConfigSet          = app.CmdConfigSet
	CmdSessionGet         = app.CmdSessionGet
	CmdWorkspaceList      = app.CmdWorkspaceList
	CmdTabList            = app.CmdTabList
	CmdPaneList           = app.CmdPaneList
	CmdPaneGet            = app.CmdPaneGet
)

// Split / cardinal direction wire values.
const (
	SplitH = app.SplitH
	SplitV = app.SplitV

	DirLeft  = app.DirLeft
	DirRight = app.DirRight
	DirUp    = app.DirUp
	DirDown  = app.DirDown
)

// Direction / border mappings (bound to the app implementations).
var (
	SplitDirection = app.SplitDirection
	NavDirection   = app.NavDirection
)

// Command param + result types.
type (
	SplitParams           = app.SplitParams
	PaneParams            = app.PaneParams
	OptPaneParams         = app.OptPaneParams
	DirParams             = app.DirParams
	SwapWithParams        = app.SwapWithParams
	CycleParams           = app.CycleParams
	RenamePaneParams      = app.RenamePaneParams
	ResizeBorderParams    = app.ResizeBorderParams
	ScrollParams          = app.ScrollParams
	ReadParams            = app.ReadParams
	ReadResult            = app.ReadResult
	CaptureParams         = app.CaptureParams
	CaptureResult         = app.CaptureResult
	WaitForOutputParams   = app.WaitForOutputParams
	SendInputParams       = app.SendInputParams
	WaitForOutputResult   = app.WaitForOutputResult
	TabParams             = app.TabParams
	OptTabParams          = app.OptTabParams
	RenameTabParams       = app.RenameTabParams
	MoveTabParams         = app.MoveTabParams
	WorkspaceParams       = app.WorkspaceParams
	RenameWorkspaceParams = app.RenameWorkspaceParams
	MoveWorkspaceParams   = app.MoveWorkspaceParams
	WorktreeListParams    = app.WorktreeListParams
	WorktreeListResult    = app.WorktreeListResult
	WorktreeInfo          = app.WorktreeInfo
	WorktreeCreateParams  = app.WorktreeCreateParams
	WorktreeCreateResult  = app.WorktreeCreateResult
	WorktreeOpenParams    = app.WorktreeOpenParams
	WorktreeOpenResult    = app.WorktreeOpenResult
	WorktreeRemoveParams  = app.WorktreeRemoveParams
	ConfigTheme           = app.ConfigTheme
	ConfigServerInfo      = app.ConfigServerInfo
	ConfigGetResult       = app.ConfigGetResult
	ConfigSetParams       = app.ConfigSetParams
)
