package browserproto

import "encoding/json"

// --- Session (§2) -------------------------------------------------------------

// Init is the required first message on the socket: the browser's protocol
// version, the grid size of its pane-rendering area (it measures its own
// font), device pixel ratio, and cell pixel metrics (forwarded to β
// create_pane/resize for pixel-aware apps).
type Init struct {
	T       Type    `json:"t"`
	V       int     `json:"v"`
	Cols    uint16  `json:"cols"`
	Rows    uint16  `json:"rows"`
	DPR     float64 `json:"dpr"`
	CellWPx uint32  `json:"cell_w_px"`
	CellHPx uint32  `json:"cell_h_px"`
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
// server routes it to the focused pane, runs keybinding interception, and
// encodes VT bytes from the pane's live mode state — the browser never
// pre-encodes.
type Key struct {
	T    Type   `json:"t"`
	Code string `json:"code"` // e.g. "KeyA", "Enter", "ArrowLeft"
	Key  string `json:"key"`  // e.g. "a", "Enter", "ArrowLeft"
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
// focused pane's mode.
type Paste struct {
	T    Type   `json:"t"`
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
