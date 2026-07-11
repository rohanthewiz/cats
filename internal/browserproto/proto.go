// Package browserproto is the WS9 browser-facing protocol: the one versioned
// WebSocket contract between the Go server and the browser. Layout + per-pane
// grid diffs + chrome state flow down; structured key/mouse/paste/resize and
// commands flow up. Full spec: ai_docs/phase-c-ws9-protocol.md.
//
// Transport is WebSocket text frames, one JSON message per frame, each shaped
// {"t": "<type>", ...}. Binary WS frames are reserved for a future packed cell
// encoding behind a version bump. Unknown "t" values must be ignored by both
// ends (DecodeUp/DecodeDown report them as ErrUnknownType so callers can).
//
// This package is the wire contract only. The β orchestration seam
// (internal/orchestration) is unchanged; frame.go translates β frames into
// browser messages, layout.go builds the layout message from
// internal/layout + internal/workspace state.
package browserproto

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ProtocolVersion is bumped on any breaking change to the message shapes.
// Independent of orchestration.ProtocolVersion (the β seam).
const ProtocolVersion = 1

// Type is the JSON "t" discriminator.
type Type string

const (
	// Down (server → browser).
	MsgWelcome     Type = "welcome"
	MsgLayout      Type = "layout"
	MsgAgents      Type = "agents"
	MsgPaneTitle   Type = "pane_title"
	MsgPaneCwd     Type = "pane_cwd"
	MsgPaneAgent   Type = "pane_agent"
	MsgPaneModes   Type = "pane_modes"
	MsgPaneExited  Type = "pane_exited"
	MsgPaneFrame   Type = "pane_frame"
	MsgPaneDiff    Type = "pane_diff"
	MsgClipboard   Type = "clipboard"
	MsgNotify      Type = "notify"
	MsgTitle       Type = "title"
	MsgError       Type = "error"
	MsgShutdown    Type = "shutdown"
	MsgUpdateReady Type = "update_ready"
	MsgCmdResult   Type = "cmd_result"

	// Up (browser → server).
	MsgInit   Type = "init"
	MsgKey    Type = "key"
	MsgMouse  Type = "mouse"
	MsgPaste  Type = "paste"
	MsgImage  Type = "image"
	MsgResize Type = "resize"
	MsgRaw    Type = "raw"
	MsgCmd    Type = "cmd"
)

// ErrUnknownType is reported (wrapped) by DecodeUp/DecodeDown for an
// unrecognized "t". The spec requires unknown types be ignored, so callers
// should errors.Is-check and drop the message rather than fail the session.
var ErrUnknownType = errors.New("browserproto: unknown message type")

// Marshal encodes one message for one WebSocket text frame.
func Marshal(m any) ([]byte, error) {
	return json.Marshal(m)
}

func decodeAs[T any](data []byte) (any, error) {
	v := new(T)
	if err := json.Unmarshal(data, v); err != nil {
		return nil, fmt.Errorf("browserproto: decode: %w", err)
	}
	return v, nil
}

func peekType(data []byte) (Type, error) {
	var env struct {
		T Type `json:"t"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return "", fmt.Errorf("browserproto: decode envelope: %w", err)
	}
	return env.T, nil
}

// DecodeUp decodes one browser → server message into a pointer to its
// concrete struct (*Init, *Key, *Mouse, *Paste, *Image, *Resize, *Raw, *Cmd).
func DecodeUp(data []byte) (any, error) {
	t, err := peekType(data)
	if err != nil {
		return nil, err
	}
	switch t {
	case MsgInit:
		return decodeAs[Init](data)
	case MsgKey:
		return decodeAs[Key](data)
	case MsgMouse:
		return decodeAs[Mouse](data)
	case MsgPaste:
		return decodeAs[Paste](data)
	case MsgImage:
		return decodeAs[Image](data)
	case MsgResize:
		return decodeAs[Resize](data)
	case MsgRaw:
		return decodeAs[Raw](data)
	case MsgCmd:
		return decodeAs[Cmd](data)
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownType, t)
}

// DecodeDown decodes one server → browser message into a pointer to its
// concrete struct. The browser's real decoder is JS; this is for Go-side
// tests and harness clients.
func DecodeDown(data []byte) (any, error) {
	t, err := peekType(data)
	if err != nil {
		return nil, err
	}
	switch t {
	case MsgWelcome:
		return decodeAs[Welcome](data)
	case MsgLayout:
		return decodeAs[Layout](data)
	case MsgAgents:
		return decodeAs[Agents](data)
	case MsgPaneTitle:
		return decodeAs[PaneTitle](data)
	case MsgPaneCwd:
		return decodeAs[PaneCwd](data)
	case MsgPaneAgent:
		return decodeAs[PaneAgent](data)
	case MsgPaneModes:
		return decodeAs[PaneModes](data)
	case MsgPaneExited:
		return decodeAs[PaneExited](data)
	case MsgPaneFrame:
		return decodeAs[PaneFrame](data)
	case MsgPaneDiff:
		return decodeAs[PaneDiff](data)
	case MsgClipboard:
		return decodeAs[Clipboard](data)
	case MsgNotify:
		return decodeAs[Notify](data)
	case MsgTitle:
		return decodeAs[Title](data)
	case MsgError:
		return decodeAs[Error](data)
	case MsgShutdown:
		return decodeAs[Shutdown](data)
	case MsgUpdateReady:
		return decodeAs[UpdateReady](data)
	case MsgCmdResult:
		return decodeAs[CmdResult](data)
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownType, t)
}
