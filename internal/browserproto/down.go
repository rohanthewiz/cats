package browserproto

import (
	"encoding/json"
	"time"
)

// --- Session (§2) -------------------------------------------------------------

// Optional server capabilities, advertised in Welcome.Caps. These name
// behaviours added within a protocol version — additive changes that an older
// server silently ignores rather than rejects.
//
// A capability is only listed once the server actually honours it, because the
// whole point is to let a client tell "the server obeyed my field" from "the
// server dropped it on the floor". Guessing wrong about CapKeyPane in
// particular means keystrokes going to the wrong pane, which is worse than
// keystrokes going nowhere.
const (
	// CapViewer: Init.Viewer is honoured — a viewer neither sizes the session
	// grid nor resizes it later.
	CapViewer = "viewer"
	// CapKeyPane: Key.Pane and Paste.Pane are honoured — input can be addressed
	// to a pane instead of riding the shared focus.
	CapKeyPane = "key.pane"
	// CapClients: the server pushes the Clients census on connect/disconnect.
	CapClients = "clients"
	// CapChat: the server serves the ACP chat surface — chat.* commands are
	// routed and chat_* messages flow. Without it a client should hide its
	// chat UI rather than let chat.send vanish into an unknown-command error.
	CapChat = "chat"
)

// serverCaps is what this server advertises. Unexported so a caller cannot
// mutate the advertised set through the shared backing array.
var serverCaps = []string{CapViewer, CapKeyPane, CapClients, CapChat}

// Welcome is the server's reply to Init. A version mismatch or rejection sets
// Error and the server closes the socket; otherwise the server immediately
// pushes initial full state (layout, per-visible-pane full frame + chrome,
// agents rollup, app title).
//
// Caps names the optional behaviours this server honours (Cap* above). It is
// absent on a rejection: that socket is about to close, and the only thing the
// client needs from it is the reason.
type Welcome struct {
	T     Type     `json:"t"`
	V     int      `json:"v"`
	Error string   `json:"error,omitempty"`
	Caps  []string `json:"caps,omitempty"`
}

func NewWelcome(errMsg string) Welcome {
	w := Welcome{T: MsgWelcome, V: ProtocolVersion, Error: errMsg}
	if errMsg == "" {
		w.Caps = serverCaps
	}
	return w
}

// Clients is the connected-client census, pushed on every connect and
// disconnect. Two questions it answers for a client that is not the only one
// looking:
//
//   - "Is anyone else here?" — Total. A phone can say "desktop connected —
//     viewing only" rather than pretending it is alone with the session.
//   - "Whose grid am I rendering?" — Sizers is how many connections declared a
//     grid (non-viewers), and Cols/Rows is the grid they settled on. Sizers == 0
//     means nobody is driving it and the layout is whatever the last sizer left
//     behind, which is worth rendering differently from a live desktop's.
type Clients struct {
	T      Type   `json:"t"`
	Total  int    `json:"total"`
	Sizers int    `json:"sizers"`
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
}

func NewClients(total, sizers int, cols, rows uint16) Clients {
	return Clients{T: MsgClients, Total: total, Sizers: sizers, Cols: cols, Rows: rows}
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
	Tabs       []TabInfo       `json:"tabs"`  // active workspace's tabs
	Panes      []PaneRectInfo  `json:"panes"` // active tab only
	Borders    []BorderInfo    `json:"borders"`
}

// WorkspaceInfo is one sidebar entry, in display order.
type WorkspaceInfo struct {
	ID           string `json:"id"` // stable public id, e.g. "w1"
	Name         string `json:"name"`
	Active       bool   `json:"active"`
	AgentSummary string `json:"agent_summary,omitempty"`
	// Locked: closed to automation (workspace.lock). The sidebar draws the row
	// dimmed with a lock beside the name; the refusal itself is the server's.
	Locked bool `json:"locked,omitempty"`
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
	Tab       int    `json:"tab"` // owning tab's stable number (tab-bar activity markers)
	Agent     string `json:"agent"`
	State     string `json:"state"`
	// Model is the LLM the agent is running under, in the same display spelling
	// PaneAgent carries ("claude-opus-5 · high") — the sidebar's agent rows name
	// the model rather than repeat the agent's own name, which every row shares.
	// Omitted when unresolved, so a row falls back to Agent.
	Model string `json:"model,omitempty"`
	Seen  bool   `json:"seen"` // false renders as "Done"
	// SinceMs is how long the pane has held this state, as of the moment the
	// rollup was built. The rollup only goes out on a change, so the browser
	// converts it to an absolute instant on arrival and ticks the label itself.
	// -1 when the state has never been published (age unknown) — 0 is a real
	// value, since the rollup ships in the same breath as the change.
	SinceMs int64 `json:"since_ms"`
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
// Agents rollup client-side). Agent is "" for a plain shell. Model is the LLM
// the agent is currently running under — display text, so it may carry the
// reasoning effort alongside it ("claude-opus-5 · high") — omitted when unknown,
// which covers both an agent whose history catway cannot read and one that has
// not answered yet (see catway's agentmodel.go for which agents are readable).
type PaneAgent struct {
	T     Type   `json:"t"`
	Pane  uint32 `json:"pane"`
	Agent string `json:"agent"`
	State string `json:"state"`
	Model string `json:"model,omitempty"`
	Seen  bool   `json:"seen"`
}

func NewPaneAgent(pane uint32, agent, state, model string, seen bool) PaneAgent {
	return PaneAgent{T: MsgPaneAgent, Pane: pane, Agent: agent, State: state, Model: model, Seen: seen}
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

// Notify renders a toast + (permission-gated) system notification. Kind is
// "attention" (an agent hit a blocker) or "finished" (a background agent run
// completed). Pane/Pub name the pane so a notification click can reveal it;
// the front-end suppresses the whole thing when that pane is visible and the
// page is focused (the user is already looking at it).
type Notify struct {
	T       Type   `json:"t"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Body    string `json:"body,omitempty"`
	Pane    uint32 `json:"pane,omitempty"`
	Pub     string `json:"pub,omitempty"`
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

// Theme pushes the effective UI theme to every connected browser, so a
// config.set / theme switch / server.reload_config lands live everywhere —
// not just on the issuing page and not only at the next page load. Colors is
// the full resolved palette (CSS custom-property names without the "--"); the
// front end applies them as inline :root properties and re-reads its canvas
// colors. Name rides along for display only.
type Theme struct {
	T      Type              `json:"t"`
	Name   string            `json:"name,omitempty"`
	Colors map[string]string `json:"colors"`
	Font   string            `json:"font,omitempty"`
}

func NewTheme(name string, colors map[string]string, font string) Theme {
	return Theme{T: MsgTheme, Name: name, Colors: colors, Font: font}
}

// Usage is the account's standing against Claude's rate-limit windows — the
// same two numbers claude's own /usage screen shows, surfaced in the sidebar so
// "how much of the week have I spent?" is answerable without leaving the pane
// you are working in.
//
// Source names where the numbers came from, because the two answers are not the
// same kind of answer: "account" is the authoritative per-window utilization
// read from the account itself, "local" is the fallback estimate summed from
// claude's transcripts on this machine. A local estimate knows how many tokens
// were spent but not what they were spent against, so it carries no percentage
// (see UsageWindow.Pct) — the sidebar renders it as a figure rather than a bar,
// which is the honest rendering of a number with no denominator.
//
// Err is a short reason the account read failed, shown beside whatever the
// fallback managed. It never carries the credential or the raw response.
//
// WeeklyModel is the weekly window of whichever model has its own allowance on
// top of the all-models week — Fable on a Max plan, for instance. It is the
// window that actually bites first on a heavy week, so it earns a row of its
// own rather than being folded into Weekly. WeeklyModelName is what to call it
// ("Fable"), taken from the account rather than guessed: the set of separately
// metered models is the plan's business and changes without us. An empty name
// means the account reports no such window and the sidebar shows no row.
//
// Memory is the share of the host machine's RAM in use — the one window here
// that has nothing to do with the account. It shares the section because it
// answers the same question the others do ("is something about to stop?") on
// the same glance, and it shares the poll because it is read on the same tick.
// Source does not describe it: it is always local, whichever source the
// rate-limit numbers came from. A host whose memory could not be read leaves
// Pct at UsagePctUnknown and the row is not drawn.
//
// ReadAt is when the server took the reading (RFC 3339). It is the message's
// own age, and it is on the wire because the receiver cannot infer it: the
// stored reading is replayed to a browser that connects between polls, so
// "when did this arrive?" and "when was this true?" are different questions.
// A front-end shows it as "n ago" — a percentage with no date beside it looks
// equally current whether it was read a minute or an hour ago.
type Usage struct {
	T               Type        `json:"t"`
	Source          string      `json:"source"` // "account" | "local"
	FiveHour        UsageWindow `json:"five_hour"`
	Weekly          UsageWindow `json:"weekly"`
	WeeklyModel     UsageWindow `json:"weekly_model"`
	WeeklyModelName string      `json:"weekly_model_name,omitempty"`
	Memory          UsageWindow `json:"memory"`
	ReadAt          string      `json:"read_at,omitempty"`
	Err             string      `json:"err,omitempty"`
}

// UsageWindow is one rate-limit window. Pct is the share of the window's
// allowance already spent (0–100), or -1 when there is no denominator to divide
// by — the local fallback fills Detail instead and leaves Pct at -1, so a
// missing number reads as missing rather than as zero. ResetsAt is when the
// window rolls over (RFC 3339), "" when unknown.
type UsageWindow struct {
	Pct      float64 `json:"pct"`
	ResetsAt string  `json:"resets_at,omitempty"`
	Detail   string  `json:"detail,omitempty"`
}

// UsagePctUnknown is UsageWindow.Pct's "no denominator" value.
const UsagePctUnknown = -1

func NewUsage(source string, fiveHour, weekly UsageWindow, errMsg string) Usage {
	return Usage{T: MsgUsage, Source: source, FiveHour: fiveHour, Weekly: weekly,
		WeeklyModel: UsageWindow{Pct: UsagePctUnknown},
		Memory:      UsageWindow{Pct: UsagePctUnknown}, Err: errMsg}
}

// WithWeeklyModel attaches the per-model weekly window. It is a separate step
// rather than another NewUsage parameter because only the account read has one:
// the local estimate counts tokens without knowing which model spent them, so
// it leaves this alone and the row stays hidden.
func (u Usage) WithWeeklyModel(name string, w UsageWindow) Usage {
	if name == "" {
		return u
	}
	u.WeeklyModelName, u.WeeklyModel = name, w
	return u
}

// WithMemory attaches the host's memory window. Chained rather than passed to
// NewUsage because it is orthogonal to both rate-limit sources: the same host
// reading rides an account reply and a transcript estimate alike, and a machine
// that will not report its memory simply leaves the field as NewUsage left it.
func (u Usage) WithMemory(w UsageWindow) Usage {
	u.Memory = w
	return u
}

// WithReadAt stamps the reading with the instant it was taken. Chained by the
// poller rather than folded into NewUsage so that building a Usage stays
// clock-free: the readers are pure functions over an endpoint reply or a pile
// of transcripts, and only the caller that owns the poll knows what "now" is.
func (u Usage) WithReadAt(t time.Time) Usage {
	u.ReadAt = t.UTC().Format(time.RFC3339)
	return u
}

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

// --- Chat (the ACP side panel) ------------------------------------------------
//
// The chat surface streams an external ACP agent's conversation to every
// connected client. Its message design mirrors the agents rollup: broadcasts
// keep all clients converged, and a full snapshot on connect (or clear)
// replaces everything, so a client can join mid-conversation without any
// request/reply choreography.

// ChatStateInfo describes the chat engine, shared by ChatState and
// ChatSnapshot. Status is a small closed set (idle, starting, ready, turn,
// dead) — closed because clients gate the composer and stop button on it.
type ChatStateInfo struct {
	Status  string `json:"status"`
	Backend string `json:"backend"`          // display name of the agent ("Copilot")
	Model   string `json:"model,omitempty"`  // agent-reported model id, when known
	Cwd     string `json:"cwd,omitempty"`    // the session's working directory
	Detail  string `json:"detail,omitempty"` // last error or note, for the status line
}

// ChatState announces a state transition.
type ChatState struct {
	T Type `json:"t"`
	ChatStateInfo
}

func NewChatState(info ChatStateInfo) ChatState {
	return ChatState{T: MsgChatState, ChatStateInfo: info}
}

// ChatAction is an optional button on an info row: an argv the client runs in
// a fresh tab via tab.create (e.g. `copilot login`). Carrying the argv rather
// than a semantic verb keeps the client dumb — the server decides what the
// remedy is, the client only offers it.
type ChatAction struct {
	Label string   `json:"label"`
	Argv  []string `json:"argv"`
}

// ChatRow is one transcript entry. Role is an open enum — user, agent, tool,
// info today — and clients must render unknown roles as plain rows, which is
// what lets new row kinds (collapsed thoughts, say) ship without a protocol
// change.
type ChatRow struct {
	ID     int64       `json:"id"`
	Role   string      `json:"role"`
	Text   string      `json:"text"`
	Kind   string      `json:"kind,omitempty"`   // tool rows: the ACP tool kind (execute, edit, …)
	Status string      `json:"status,omitempty"` // tool rows: pending|in_progress|completed|failed
	Action *ChatAction `json:"action,omitempty"`
}

// ChatRowMsg appends a row to the transcript — or replaces it when the client
// already has the ID (tool rows update status in place this way).
type ChatRowMsg struct {
	T   Type    `json:"t"`
	Row ChatRow `json:"row"`
}

func NewChatRow(row ChatRow) ChatRowMsg {
	return ChatRowMsg{T: MsgChatRow, Row: row}
}

// ChatDelta appends streamed text to the row with ID. Deltas are coalesced
// server-side (time- and size-bounded) so a fast token stream cannot flood
// the per-connection send buffer.
type ChatDelta struct {
	T    Type   `json:"t"`
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

func NewChatDelta(id int64, text string) ChatDelta {
	return ChatDelta{T: MsgChatDelta, ID: id, Text: text}
}

// ChatPermOption is one choice of a permission request; Kind is the ACP
// option kind (allow_once, allow_always, reject_once, reject_always), which
// clients may use for styling but must not gate on.
type ChatPermOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// ChatPerm is a permission request's lifecycle, both halves: options open the
// prompt, Resolved closes it everywhere. Broadcasting the resolution matters
// because any client may answer — the others must see their buttons collapse
// into the outcome rather than sit on a stale prompt.
type ChatPerm struct {
	T        Type             `json:"t"`
	ReqID    string           `json:"req_id"`
	Title    string           `json:"title,omitempty"` // the tool call being authorised
	Kind     string           `json:"kind,omitempty"`  // the ACP tool kind
	Options  []ChatPermOption `json:"options,omitempty"`
	Resolved bool             `json:"resolved,omitempty"`
	Outcome  string           `json:"outcome,omitempty"` // display verdict: allowed|rejected|cancelled
}

func NewChatPerm(reqID, title, kind string, options []ChatPermOption) ChatPerm {
	return ChatPerm{T: MsgChatPerm, ReqID: reqID, Title: title, Kind: kind, Options: options}
}

func NewChatPermResolved(reqID, outcome string) ChatPerm {
	return ChatPerm{T: MsgChatPerm, ReqID: reqID, Resolved: true, Outcome: outcome}
}

// ChatSnapshot replaces a client's entire chat model: sent to each client on
// connect, and broadcast (empty) on chat.clear. Perms carries the still-open
// permission prompts so a client that joins mid-question can answer it.
type ChatSnapshot struct {
	T     Type          `json:"t"`
	State ChatStateInfo `json:"state"`
	Rows  []ChatRow     `json:"rows"`
	Perms []ChatPerm    `json:"perms,omitempty"`
}

func NewChatSnapshot(state ChatStateInfo, rows []ChatRow, perms []ChatPerm) ChatSnapshot {
	return ChatSnapshot{T: MsgChatSnapshot, State: state, Rows: rows, Perms: perms}
}
