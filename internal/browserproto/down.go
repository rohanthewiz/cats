package browserproto

import "encoding/json"

// --- Session (§2) -------------------------------------------------------------

// Welcome is the server's reply to Init. A version mismatch or rejection sets
// Error and the server closes the socket; otherwise the server immediately
// pushes initial full state (layout, per-visible-pane full frame + chrome,
// agents rollup, app title).
type Welcome struct {
	T     Type   `json:"t"`
	V     int    `json:"v"`
	Error string `json:"error,omitempty"`
}

func NewWelcome(errMsg string) Welcome {
	return Welcome{T: MsgWelcome, V: ProtocolVersion, Error: errMsg}
}

// --- Layout & chrome (§3) -----------------------------------------------------

// Rect is a cell rectangle on the wire: [x, y, w, h].
type Rect [4]uint16

// Layout is a full replacement of the connection's viewport structure, sent on
// connect and on any structural/focus change (D3: computed rects, never the
// BSP tree). Built by BuildLayout.
type Layout struct {
	T          Type            `json:"t"`
	Workspaces []WorkspaceInfo `json:"workspaces"`
	Tabs       []TabInfo       `json:"tabs"` // active workspace's tabs
	Panes      []PaneRectInfo  `json:"panes"` // active tab only
	Borders    []BorderInfo    `json:"borders"`
}

// WorkspaceInfo is one sidebar entry, in display order.
type WorkspaceInfo struct {
	ID           string `json:"id"` // stable public id, e.g. "w1"
	Name         string `json:"name"`
	Active       bool   `json:"active"`
	AgentSummary string `json:"agent_summary,omitempty"`
}

// TabInfo is one tab of the active workspace.
type TabInfo struct {
	Num    int    `json:"num"` // stable public tab number
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Zoomed bool   `json:"zoomed"`
}

// PaneRectInfo positions one pane of the active tab (layout.PaneInfo shape).
type PaneRectInfo struct {
	Pane      uint32 `json:"pane"`
	Pub       string `json:"pub"` // public handle, e.g. "w1:p3" — display only
	Rect      Rect   `json:"rect"`
	Inner     Rect   `json:"inner"`
	Scrollbar *Rect  `json:"scrollbar,omitempty"`
	Focused   bool   `json:"focused"`
}

// BorderInfo is one draggable split boundary (layout.SplitBorder shape). ID is
// the opaque server handle the browser echoes in pane.resize_border, so tree
// paths never cross the wire as structure (see BorderID).
type BorderInfo struct {
	ID    string  `json:"id"`
	Pos   uint16  `json:"pos"`
	Dir   uint8   `json:"dir"` // 0 = horizontal split (vertical divider line), 1 = vertical
	Ratio float32 `json:"ratio"`
	Area  Rect    `json:"area"`
}

// Agent states (β PaneAgent.State passthrough).
const (
	AgentIdle    = "idle"
	AgentWorking = "working"
	AgentBlocked = "blocked"
	AgentUnknown = "unknown"
)

// Agents is the full sidebar rollup across ALL workspaces (frames stream only
// for visible panes, but agent chrome is global).
type Agents struct {
	T     Type        `json:"t"`
	Items []AgentItem `json:"items"`
}

type AgentItem struct {
	Pane      uint32 `json:"pane"`
	Pub       string `json:"pub"`
	Workspace string `json:"workspace"`
	Agent     string `json:"agent"`
	State     string `json:"state"`
	Seen      bool   `json:"seen"` // false renders as "Done"
}

func NewAgents(items []AgentItem) Agents { return Agents{T: MsgAgents, Items: items} }

// PaneTitle reports a pane's window title (OSC 0/2); "" clears.
type PaneTitle struct {
	T     Type   `json:"t"`
	Pane  uint32 `json:"pane"`
	Title string `json:"title"`
}

func NewPaneTitle(pane uint32, title string) PaneTitle {
	return PaneTitle{T: MsgPaneTitle, Pane: pane, Title: title}
}

// PaneCwd reports a pane's working directory (OSC 7).
type PaneCwd struct {
	T    Type   `json:"t"`
	Pane uint32 `json:"pane"`
	Cwd  string `json:"cwd"`
}

func NewPaneCwd(pane uint32, cwd string) PaneCwd {
	return PaneCwd{T: MsgPaneCwd, Pane: pane, Cwd: cwd}
}

// PaneAgent reports one pane's agent identity + state change (also patches the
// Agents rollup client-side). Agent is "" for a plain shell.
type PaneAgent struct {
	T     Type   `json:"t"`
	Pane  uint32 `json:"pane"`
	Agent string `json:"agent"`
	State string `json:"state"`
	Seen  bool   `json:"seen"`
}

func NewPaneAgent(pane uint32, agent, state string, seen bool) PaneAgent {
	return PaneAgent{T: MsgPaneAgent, Pane: pane, Agent: agent, State: state, Seen: seen}
}

// PaneModes is the display-relevant subset of β PaneModes: Mouse gates pointer
// capture vs native text selection, AltScreen gates the scrollbar. The full
// mode state stays server-side where the input encoder (D4) consumes it.
type PaneModes struct {
	T         Type   `json:"t"`
	Pane      uint32 `json:"pane"`
	Mouse     bool   `json:"mouse"`
	AltScreen bool   `json:"alt_screen"`
}

// PaneExited reports a pane's child exit.
type PaneExited struct {
	T    Type   `json:"t"`
	Pane uint32 `json:"pane"`
	Code int    `json:"code"`
}

func NewPaneExited(pane uint32, code int) PaneExited {
	return PaneExited{T: MsgPaneExited, Pane: pane, Code: code}
}

// --- Pane content (§4) ---------------------------------------------------------

// Cursor is the viewport cursor. Shape is the DECSCUSR param.
type Cursor struct {
	X     uint16 `json:"x"`
	Y     uint16 `json:"y"`
	Vis   bool   `json:"vis"`
	Shape uint8  `json:"shape"`
}

// Cell is one grid cell. F/B are packed u32 colors (0x02_RR_GG_BB, D2) and are
// 0/omitted when equal to the frame's def_fg/def_bg — the dominant case (a
// real packed color is never 0). M is the ratatui modifier bitmask (β's). H is
// a 1-based index into the frame's Links table; 0/omitted = no hyperlink.
type Cell struct {
	S string `json:"s"`
	F uint32 `json:"f,omitempty"`
	B uint32 `json:"b,omitempty"`
	M uint16 `json:"m,omitempty"`
	H uint32 `json:"h,omitempty"`
}

// Scroll is the pane's scrollback position (β ScrollInfo): Off lines up from
// the live bottom, Max history lines available, Rows visible.
type Scroll struct {
	Off  int `json:"off"`
	Max  int `json:"max"`
	Rows int `json:"rows"`
}

// PaneFrame is a full grid for one pane. Cells is row-major, len == W*H.
// Links, when present, is the frame's OSC 8 URI table (link-bearing frames are
// always full — β rule, so diffs never carry links).
type PaneFrame struct {
	T      Type     `json:"t"`
	Pane   uint32   `json:"pane"`
	W      uint16   `json:"w"`
	H      uint16   `json:"h"`
	Cur    Cursor   `json:"cur"`
	DefFg  uint32   `json:"def_fg"`
	DefBg  uint32   `json:"def_bg"`
	Links  []string `json:"links,omitempty"`
	Cells  []Cell   `json:"cells"`
	Scroll *Scroll  `json:"scroll,omitempty"`
}

// DiffCell is one changed cell: I is the row-major index into the pane grid.
type DiffCell struct {
	I int `json:"i"`
	Cell
}

// PaneDiff is a sparse-index patch (D1): only changed cells, addressed by
// row-major index. Omitted cell colors resolve against the def_fg/def_bg of
// the last full PaneFrame.
type PaneDiff struct {
	T      Type       `json:"t"`
	Pane   uint32     `json:"pane"`
	Cur    *Cursor    `json:"cur,omitempty"`
	Cells  []DiffCell `json:"cells"`
	Scroll *Scroll    `json:"scroll,omitempty"`
}

// --- App-level (§5) -------------------------------------------------------------

// Clipboard is an OSC 52 clipboard write from any pane (base64 on the wire);
// empty data is a clipboard-clear.
type Clipboard struct {
	T    Type   `json:"t"`
	Data []byte `json:"data"`
}

func NewClipboard(data []byte) Clipboard { return Clipboard{T: MsgClipboard, Data: data} }

// Notify renders a toast + (permission-gated) system notification.
type Notify struct {
	T       Type   `json:"t"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Body    string `json:"body,omitempty"`
}

func NewNotify(kind, message, body string) Notify {
	return Notify{T: MsgNotify, Kind: kind, Message: message, Body: body}
}

// Title sets the browser-tab title (app-level).
type Title struct {
	T     Type   `json:"t"`
	Title string `json:"title"`
}

func NewTitle(title string) Title { return Title{T: MsgTitle, Title: title} }

// Error is a non-fatal error, rendered as a toast.
type Error struct {
	T    Type   `json:"t"`
	Msg  string `json:"msg"`
	Pane uint32 `json:"pane,omitempty"`
}

func NewError(pane uint32, msg string) Error { return Error{T: MsgError, Pane: pane, Msg: msg} }

// Shutdown announces a clean server exit; the browser shows disconnected chrome.
type Shutdown struct {
	T Type `json:"t"`
}

func NewShutdown() Shutdown { return Shutdown{T: MsgShutdown} }

// UpdateReady announces an available self-update; chrome shows a banner.
type UpdateReady struct {
	T       Type   `json:"t"`
	Version string `json:"version"`
	Command string `json:"command"`
}

func NewUpdateReady(version, command string) UpdateReady {
	return UpdateReady{T: MsgUpdateReady, Version: version, Command: command}
}

// CmdResult is the reply to a Cmd, always sent when the command carried an id.
// Data is command-specific (e.g. ReadResult for "read").
type CmdResult struct {
	T     Type            `json:"t"`
	ID    string          `json:"id"`
	Ok    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// NewCmdResult builds a reply; data may be nil. Marshal errors surface here so
// command handlers can turn them into an error result instead.
func NewCmdResult(id string, ok bool, errMsg string, data any) (CmdResult, error) {
	r := CmdResult{T: MsgCmdResult, ID: id, Ok: ok, Error: errMsg}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return CmdResult{}, err
		}
		r.Data = raw
	}
	return r, nil
}
