package browserproto

import "encoding/json"

// --- Session (§2) -------------------------------------------------------------

// Init is the required first message on the socket: the browser's protocol
// version, the grid size of its pane-rendering area (it measures its own
// font), device pixel ratio, and cell pixel metrics (forwarded to β
// create_pane/resize for pixel-aware apps).
//
// Viewer declares a client that watches without owning the geometry. The
// session has one grid, shared by every connection, so an unqualified second
// client reshapes the first: a phone announcing 40x30 would resize the desktop's
// panes to fit a phone. A viewer's Cols/Rows/CellWPx/CellHPx are ignored and its
// Resize messages are dropped; it renders whatever grid the sizers established.
// Omitted (false) is the historical behaviour, so browsers need no change.
//
// Workspace picks which workspace this connection opens on — the "which window
// am I" field. Each connection is a view with its own workspace, active tab,
// focus and grid, so two windows can show two projects side by side without
// switching each other (server capability "window"). Omitted means "whatever
// the primary view is showing", which is exactly what a single window has
// always got, so today's clients are unchanged. An id that names no workspace
// is not an error and falls back the same way: it typically comes from a URL
// the user bookmarked before the workspace was closed.
type Init struct {
	T       Type    `json:"t"`
	V       int     `json:"v"`
	Cols    uint16  `json:"cols"`
	Rows    uint16  `json:"rows"`
	DPR     float64 `json:"dpr"`
	CellWPx uint32  `json:"cell_w_px"`
	CellHPx uint32  `json:"cell_h_px"`
	Viewer  bool    `json:"viewer,omitempty"`
	// Workspace is the public workspace id ("w2") this window opens on.
	Workspace string `json:"workspace,omitempty"`
}

// --- Input events (§6, D4: structured, encoded server-side) --------------------

// Key event kinds.
const (
	KeyDown   = "d"
	KeyRepeat = "r"
	KeyUp     = "u"
)

// Modifier bitmask values for Key.Mods / Mouse.Mods.
const (
	ModShift uint8 = 1
	ModAlt   uint8 = 2
	ModCtrl  uint8 = 4
	ModMeta  uint8 = 8
)

// Key is a structured keyboard event: W3C KeyboardEvent.code + .key. The
// server encodes VT bytes from the pane's live mode state and runs keybinding
// interception — the browser never pre-encodes.
//
// Pane addresses the event, following Mouse's precedent. 0 (omitted) routes to
// the session's focused pane, which is what a browser sends and what every
// client sent before this field existed; pane ids start at 1
// (internal/layout), so 0 is never a real pane. A non-zero pane is how a second
// client types somewhere without stealing the desktop's cursor — and without
// the race that follows it, where the desktop user clicks elsewhere and the
// phone's next keystroke lands there. Addressed input is gated: see the server's
// inputTarget.
type Key struct {
	T    Type   `json:"t"`
	Pane uint32 `json:"pane,omitempty"` // 0 = the focused pane
	Code string `json:"code"`           // e.g. "KeyA", "Enter", "ArrowLeft"
	Key  string `json:"key"`            // e.g. "a", "Enter", "ArrowLeft"
	Mods uint8  `json:"mods"`
	Kind string `json:"kind"` // KeyDown | KeyRepeat | KeyUp
}

// Mouse event kinds.
const (
	MouseDown  = "d"
	MouseUp    = "u"
	MouseMove  = "m"
	MouseWheel = "w"
)

// Mouse buttons.
const (
	BtnLeft   uint8 = 0
	BtnMiddle uint8 = 1
	BtnRight  uint8 = 2
	BtnNone   uint8 = 3
)

// Mouse is a pointer event in cell coordinates within a pane (the browser
// converts px → cell with its own metrics). DX/DY are wheel deltas in lines
// (MouseWheel only). The server applies the pane's reported mouse encoding.
type Mouse struct {
	T    Type   `json:"t"`
	Pane uint32 `json:"pane"`
	X    uint16 `json:"x"`
	Y    uint16 `json:"y"`
	Btn  uint8  `json:"btn"`
	Kind string `json:"kind"`
	Mods uint8  `json:"mods"`
	DX   int    `json:"dx,omitempty"`
	DY   int    `json:"dy,omitempty"`
}

// Paste is plain text; the server applies bracketed-paste wrapping per the
// target pane's mode. Pane addresses it exactly as Key.Pane does — the two
// travel together, since a client that can type into a pane can paste into it.
type Paste struct {
	T    Type   `json:"t"`
	Pane uint32 `json:"pane,omitempty"` // 0 = the focused pane
	Data string `json:"data"`
}

// Image is a clipboard image paste (base64 on the wire).
type Image struct {
	T    Type   `json:"t"`
	Data []byte `json:"data"`
	Ext  string `json:"ext"`
}

// Resize reports the browser window's new grid; the server relayouts (a new
// Layout follows) and resizes panes over β.
type Resize struct {
	T    Type   `json:"t"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// Focus reports the client *window's* focus — the OS-level "is this app in
// front" state, not which pane is focused (that is a session command). The
// server folds every connection's report into one "is anyone looking" bit and
// forwards its transitions to pane programs that enabled focus reporting (DEC
// mode 1004), which is how a TUI knows to park its caret blink while the user
// is in another app. A client that never sends this is treated as focused,
// which is the world every program assumed before the message existed.
type Focus struct {
	T       Type `json:"t"`
	Focused bool `json:"focused"`
}

// Raw is pre-encoded bytes to the focused pane.
//
// Deprecated: transition escape hatch only (α's "input"); removed before WS11.
type Raw struct {
	T    Type   `json:"t"`
	Data []byte `json:"data"`
}

// --- Commands (§7) --------------------------------------------------------------

// Cmd is the command envelope. Name uses the control-API vocabulary (Cmd*
// constants in cmd.go); Params is the command's typed params struct. ID is a
// client-chosen string echoed in the CmdResult; "" means no reply is wanted.
type Cmd struct {
	T      Type            `json:"t"`
	ID     string          `json:"id,omitempty"`
	Name   string          `json:"name"`
	Params json.RawMessage `json:"params,omitempty"`
}

// NewCmd builds a command; params may be nil for parameterless commands.
func NewCmd(id, name string, params any) (Cmd, error) {
	c := Cmd{T: MsgCmd, ID: id, Name: name}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return Cmd{}, err
		}
		c.Params = raw
	}
	return c, nil
}
