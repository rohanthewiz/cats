// Package wire is the WS9 browser-facing protocol: the one versioned WebSocket
// contract between the Go server and every client of it, the browser and the
// phone alike. Layout + per-pane
// grid diffs + chrome state flow down; structured key/mouse/paste/resize and
// commands flow up. Full spec: ai_docs/phase-c-ws9-protocol.md.
//
// Transport is WebSocket text frames, one JSON message per frame, each shaped
// {"t": "<type>", ...}. Binary WS frames are reserved for a future packed cell
// encoding behind a version bump. Unknown "t" values must be ignored by both
// ends (DecodeUp/DecodeDown report them as ErrUnknownType so callers can).
//
// This package is the wire contract only, and it is deliberately a leaf: it
// imports nothing but the standard library, so an out-of-tree client (the
// phone, built with gomobile and for GOOS=js) can depend on it without the
// server behind it. Anything that needs internal/layout, internal/flags or the
// β orchestration seam lives on the server side: internal/browserproto's
// frame.go translates β frames into these messages and its layout.go builds
// the layout message; internal/app converts wire values onto model types.
package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// ProtocolVersion is bumped on any breaking change to the message shapes.
// Independent of orchestration.ProtocolVersion (the β seam).
const ProtocolVersion = 1

// Type is the JSON "t" discriminator.
type Type string

const (
	// Down (server → browser).
	MsgWelcome    Type = "welcome"
	MsgLayout     Type = "layout"
	MsgAgents     Type = "agents"
	MsgHosts      Type = "hosts"
	MsgPaneTitle  Type = "pane_title"
	MsgPaneCwd    Type = "pane_cwd"
	MsgPaneBranch Type = "pane_branch"
	MsgPaneAgent  Type = "pane_agent"
	MsgPaneModes  Type = "pane_modes"
	MsgPaneExited Type = "pane_exited"
	// MsgPaneRespawned is pane_exited's inverse: the pane's PTY came back
	// (cathost restart, or a move to another host), so the chrome a pane_exited
	// installed must come off. Added within protocol v1 — an old client ignores
	// it and shows the same stale red header it showed before this existed.
	MsgPaneRespawned Type = "pane_respawned"
	MsgPaneFrame     Type = "pane_frame"
	MsgPaneDiff      Type = "pane_diff"
	MsgClipboard     Type = "clipboard"
	MsgNotify        Type = "notify"
	MsgTitle         Type = "title"
	MsgError         Type = "error"
	MsgShutdown      Type = "shutdown"
	MsgUpdateReady   Type = "update_ready"
	MsgTheme         Type = "theme"
	MsgUsage         Type = "usage"
	MsgClients       Type = "clients"
	MsgCmdResult     Type = "cmd_result"
	MsgHistory       Type = "history"
	// MsgRecord is the macro recorder's state (runbook.record). Added within
	// protocol v1: an old client ignores the type and simply never draws the
	// indicator, which is exactly the UI it had before this existed.
	MsgRecord Type = "record"
	// MsgRunbookRuns is the set of runbook runs in flight (runbook.run and the
	// `on:` triggers). Added within protocol v1: an old client ignores the type
	// and marks only the runs it started itself, which is the UI it had before
	// this existed.
	MsgRunbookRuns Type = "runbook_runs"
	// Chat surface (the ACP side panel). Added within protocol v1: an old
	// client ignores unknown types, and a new client learns the server serves
	// chat from CapChat rather than by probing.
	MsgChatState    Type = "chat_state"
	MsgChatSnapshot Type = "chat_snapshot"
	MsgChatRow      Type = "chat_row"
	MsgChatDelta    Type = "chat_delta"
	MsgChatPerm     Type = "chat_perm"

	// Up (browser → server).
	MsgInit   Type = "init"
	MsgKey    Type = "key"
	MsgMouse  Type = "mouse"
	MsgPaste  Type = "paste"
	MsgImage  Type = "image"
	MsgResize Type = "resize"
	MsgFocus  Type = "focus"
	MsgRaw    Type = "raw"
	MsgCmd    Type = "cmd"
)

// ErrUnknownType is reported (wrapped) by DecodeUp/DecodeDown for an
// unrecognized "t". The spec requires unknown types be ignored, so callers
// should errors.Is-check and drop the message rather than fail the session.
var ErrUnknownType = errors.New("browserproto: unknown message type")

// msgTypes maps each message struct to its "t" discriminator. It is the
// single source Marshal stamps from, and TestMarshalStampsEveryType pins it
// against DecodeUp/DecodeDown so a struct cannot be added to one and not the
// other. Keyed by the struct type, not the pointer: Marshal derefs first.
var msgTypes = map[reflect.Type]Type{
	// Up.
	reflect.TypeOf(Init{}):   MsgInit,
	reflect.TypeOf(Key{}):    MsgKey,
	reflect.TypeOf(Mouse{}):  MsgMouse,
	reflect.TypeOf(Paste{}):  MsgPaste,
	reflect.TypeOf(Image{}):  MsgImage,
	reflect.TypeOf(Resize{}): MsgResize,
	reflect.TypeOf(Focus{}):  MsgFocus,
	reflect.TypeOf(Raw{}):    MsgRaw,
	reflect.TypeOf(Cmd{}):    MsgCmd,
	// Down.
	reflect.TypeOf(Welcome{}):       MsgWelcome,
	reflect.TypeOf(Layout{}):        MsgLayout,
	reflect.TypeOf(Agents{}):        MsgAgents,
	reflect.TypeOf(Hosts{}):         MsgHosts,
	reflect.TypeOf(PaneTitle{}):     MsgPaneTitle,
	reflect.TypeOf(PaneCwd{}):       MsgPaneCwd,
	reflect.TypeOf(PaneBranch{}):    MsgPaneBranch,
	reflect.TypeOf(PaneAgent{}):     MsgPaneAgent,
	reflect.TypeOf(PaneModes{}):     MsgPaneModes,
	reflect.TypeOf(PaneExited{}):    MsgPaneExited,
	reflect.TypeOf(PaneRespawned{}): MsgPaneRespawned,
	reflect.TypeOf(PaneFrame{}):     MsgPaneFrame,
	reflect.TypeOf(PaneDiff{}):      MsgPaneDiff,
	reflect.TypeOf(Clipboard{}):     MsgClipboard,
	reflect.TypeOf(Notify{}):        MsgNotify,
	reflect.TypeOf(Title{}):         MsgTitle,
	reflect.TypeOf(Error{}):         MsgError,
	reflect.TypeOf(Shutdown{}):      MsgShutdown,
	reflect.TypeOf(UpdateReady{}):   MsgUpdateReady,
	reflect.TypeOf(Theme{}):         MsgTheme,
	reflect.TypeOf(Usage{}):         MsgUsage,
	reflect.TypeOf(Clients{}):       MsgClients,
	reflect.TypeOf(CmdResult{}):     MsgCmdResult,
	reflect.TypeOf(History{}):       MsgHistory,
	reflect.TypeOf(Record{}):        MsgRecord,
	reflect.TypeOf(RunbookRuns{}):   MsgRunbookRuns,
	reflect.TypeOf(ChatState{}):     MsgChatState,
	reflect.TypeOf(ChatSnapshot{}):  MsgChatSnapshot,
	reflect.TypeOf(ChatRowMsg{}):    MsgChatRow,
	reflect.TypeOf(ChatDelta{}):     MsgChatDelta,
	reflect.TypeOf(ChatPerm{}):      MsgChatPerm,
}

// ErrTypeMismatch is returned by Marshal when a message's T field disagrees
// with its Go type, e.g. a Key struct carrying "paste". That is always a
// caller bug, and letting it onto the wire would make the far end decode the
// wrong shape, so it is refused here rather than discovered there.
var ErrTypeMismatch = errors.New("browserproto: message T does not match its Go type")

// Marshal encodes one message for one WebSocket text frame.
//
// The "t" discriminator is derived from the Go type: a message struct passed
// with an empty T (by value or by pointer) goes out stamped, so no caller has
// to remember the constant. The stamp lands on a copy, never on the caller's
// value, which keeps Marshal side-effect free for values shared between
// goroutines. A T already set is left alone if it agrees with the type and is
// an ErrTypeMismatch if it does not.
//
// Anything not in msgTypes (a raw json.RawMessage, a map, a test fixture)
// is encoded as-is, exactly as before stamping existed.
func Marshal(m any) ([]byte, error) {
	v := reflect.ValueOf(m)
	for v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	// An untyped nil has no Type (reflect panics); a nil *Init stops the loop
	// above as a pointer, which is not in the table. Both fall through to
	// plain encoding, "null", as before.
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return json.Marshal(m)
	}
	want, known := msgTypes[v.Type()]
	if !known {
		return json.Marshal(m)
	}
	// Every struct in msgTypes has its discriminator as the exported field T.
	f := v.FieldByName("T")
	switch have := Type(f.String()); {
	case have == want:
		return json.Marshal(m)
	case have != "":
		return nil, fmt.Errorf("%w: %T has %q, want %q", ErrTypeMismatch, m, have, want)
	}
	// Copy so the caller's struct is untouched even when passed by pointer.
	cp := reflect.New(v.Type()).Elem()
	cp.Set(v)
	cp.FieldByName("T").SetString(string(want))
	return json.Marshal(cp.Interface())
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
// concrete struct (*Init, *Key, *Mouse, *Paste, *Image, *Resize, *Focus,
// *Raw, *Cmd).
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
	case MsgFocus:
		return decodeAs[Focus](data)
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
	case MsgHosts:
		return decodeAs[Hosts](data)
	case MsgPaneTitle:
		return decodeAs[PaneTitle](data)
	case MsgPaneCwd:
		return decodeAs[PaneCwd](data)
	case MsgPaneBranch:
		return decodeAs[PaneBranch](data)
	case MsgPaneAgent:
		return decodeAs[PaneAgent](data)
	case MsgPaneModes:
		return decodeAs[PaneModes](data)
	case MsgPaneExited:
		return decodeAs[PaneExited](data)
	case MsgPaneRespawned:
		return decodeAs[PaneRespawned](data)
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
	case MsgTheme:
		return decodeAs[Theme](data)
	case MsgUsage:
		return decodeAs[Usage](data)
	case MsgClients:
		return decodeAs[Clients](data)
	case MsgCmdResult:
		return decodeAs[CmdResult](data)
	case MsgHistory:
		return decodeAs[History](data)
	case MsgRecord:
		return decodeAs[Record](data)
	case MsgRunbookRuns:
		return decodeAs[RunbookRuns](data)
	case MsgChatState:
		return decodeAs[ChatState](data)
	case MsgChatSnapshot:
		return decodeAs[ChatSnapshot](data)
	case MsgChatRow:
		return decodeAs[ChatRowMsg](data)
	case MsgChatDelta:
		return decodeAs[ChatDelta](data)
	case MsgChatPerm:
		return decodeAs[ChatPerm](data)
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownType, t)
}
