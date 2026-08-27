package browserproto

import (
	"encoding/json"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
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
	// CapWindow: Init.Workspace is honoured — a connection is a view with its
	// own workspace, so workspace.focus moves only the window that sent it and
	// a resize reshapes only the workspace that window is showing. Without it,
	// opening a second window on another workspace fights the first, which is
	// exactly the failure a client cannot detect after the fact.
	CapWindow = "window"
)

// serverCaps is what this server advertises. Unexported so a caller cannot
// mutate the advertised set through the shared backing array.
var serverCaps = []string{CapViewer, CapKeyPane, CapClients, CapChat, CapWindow}

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
//
// Views answers the third question, the one multi-window introduced: "which
// workspace is each other window on?" A sidebar can then mark a workspace
// another window already shows (so "open in new window" does not silently make
// a duplicate), and a viewer can label the primary view it is following.
// Cols/Rows keep their old meaning — the primary view's grid — so a client that
// ignores Views is unchanged.
type Clients struct {
	T      Type   `json:"t"`
	Total  int    `json:"total"`
	Sizers int    `json:"sizers"`
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
	// Views is one entry per connection, in no meaningful order. Additive:
	// absent from an older server, and safely ignorable.
	Views []ClientView `json:"views,omitempty"`
}

// ClientView describes one connected window: the workspace it shows, the grid
// it declared (zero for a viewer, which declares none), whether its OS window
// is in the foreground, and whether it is the primary view — the most recently
// focused sizer, which is what a view-less caller (catctl, a hook, a runbook
// step) and every viewer resolve "the focused pane" through.
type ClientView struct {
	Workspace string `json:"workspace"`
	Cols      uint16 `json:"cols,omitempty"`
	Rows      uint16 `json:"rows,omitempty"`
	Focused   bool   `json:"focused,omitempty"`
	Viewer    bool   `json:"viewer,omitempty"`
	Primary   bool   `json:"primary,omitempty"`
}

func NewClients(total, sizers int, cols, rows uint16) Clients {
	return Clients{T: MsgClients, Total: total, Sizers: sizers, Cols: cols, Rows: rows}
}

// NewClientsWithViews is NewClients carrying the per-window breakdown.
func NewClientsWithViews(total, sizers int, cols, rows uint16, views []ClientView) Clients {
	c := NewClients(total, sizers, cols, rows)
	c.Views = views
	return c
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
	// Host is the cathost new panes in this workspace land on, resolved (never
	// the empty "means the default" form the model stores). The sidebar shows it
	// only while more than one host exists — see the hosts message.
	Host string `json:"host,omitempty"`
	// FlagInfo is the user's annotation on this workspace (workspace.flag): a
	// glyph with a meaning plus an optional note, drawn beside the name. Zero
	// when unflagged, which is the usual case.
	app.FlagInfo
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
	// Host is the id of the cathost this pane's terminal lives on, resolved
	// against the roster (so it names a host that exists, even for a pane
	// restored onto one that has since gone away). The pane header renders it as
	// a badge only while the session has more than one host — with one, the
	// answer is the same for every pane and says nothing.
	Host string `json:"host,omitempty"`
	// FlagInfo is the user's annotation on this pane (pane.flag), drawn as a
	// chip in the pane header. It rides the layout because the header is
	// redrawn from it; the sidebar's copies of the same fact come from the
	// agents rollup and pane.list, each of which reaches panes this message
	// does not (it carries the active tab only).
	app.FlagInfo
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
	// FlagInfo is the user's annotation on the pane this agent is running in
	// (pane.flag). Carried here as well as on the layout because this rollup is
	// the only message that spans every workspace: the AGENTS list is where a
	// "come back to this one" is most often set, and most of its rows are panes
	// the layout never mentions.
	app.FlagInfo
}

func NewAgents(items []AgentItem) Agents { return Agents{T: MsgAgents, Items: items} }

// Hosts is the cathost roster, pushed on connect and re-pushed whenever a host
// connects or drops. It is the client's answer to two different questions:
// "which machines is this session spread over" (the sidebar's HOSTS section)
// and "is there more than one" — the gate every host badge hangs on, since with
// a single host every pane's host is the same word and worth no pixels.
//
// It carries connectivity rather than leaving clients to infer it from error
// toasts: a host that is down still owns its panes, and a client that knows
// which ones can say so on the pane instead of blaming the whole session.
type Hosts struct {
	T     Type       `json:"t"`
	Items []HostItem `json:"items"`
}

// HostItem is one cathost in the roster.
type HostItem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Connected is the live link state. AddrKind is the transport ("unix",
	// "tcp", "tls") — not the address itself, which can carry a path or a
	// hostname an operator would rather not paint into a shared screen.
	Connected bool   `json:"connected"`
	AddrKind  string `json:"addr_kind,omitempty"`
	// Default marks where panes that name no host land. Spelled "is_default" for
	// the same reason app.HostInfo's is: `default` is reserved in Dart, and the
	// mobile client's types are generated from these keys.
	Default bool `json:"is_default,omitempty"`
	// Local marks this catway's own machine (the synthesized "local" host). The
	// UI gates every path-shaped affordance on it — the start-path picker, the
	// worktree dialogs — because those describe the catway machine's disk and a
	// unix address is no proof of localness (an ssh -L forward has one too).
	Local bool `json:"local,omitempty"`
	Panes int  `json:"panes"` // live panes currently on this host
	// Error is the last transport-level reason this host is unreachable, when
	// one is known ("dial: connection refused"). Empty while connected.
	Error string `json:"error,omitempty"`
	// LatencyMs is the last round trip measured to this cathost, in fractional
	// milliseconds; omitted when unknown. It is the one number that tells a
	// slow session from a slow machine — the same keystroke feels the same
	// whether the box is loaded or three thousand miles away, and only this
	// separates them.
	LatencyMs float64 `json:"latency_ms,omitempty"`
	// ListsDirs reports that the start-path picker works against this host: true
	// for the local machine, and for a remote one whose cathost can list its own
	// directories. Separate from Local because the two answers used to coincide
	// and no longer do.
	ListsDirs bool `json:"lists_dirs,omitempty"`
}

func NewHosts(items []HostItem) Hosts { return Hosts{T: MsgHosts, Items: items} }

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

// PaneBranch reports the git branch checked out in a pane's working directory;
// "" clears it (the pane left the repo, or never was in one).
//
// It rides its own message rather than a field on PaneCwd because the two move
// independently: a checkout in a pane that never cd's changes the branch with
// no cwd event behind it, and a cd within one repo changes the path with no
// branch change. Coupling them would mean re-sending the unchanged half on
// every update of the other, and — worse — would tie the branch's refresh
// cadence to OSC 7, which only fires when the shell moves.
type PaneBranch struct {
	T      Type   `json:"t"`
	Pane   uint32 `json:"pane"`
	Branch string `json:"branch"`
}

func NewPaneBranch(pane uint32, branch string) PaneBranch {
	return PaneBranch{T: MsgPaneBranch, Pane: pane, Branch: branch}
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

	// Kitty is the pane's live kitty-keyboard-protocol flags (0 = legacy
	// keyboard). It is here for one decision the browser cannot make
	// without it: whether to hand a ⌘ chord to the pane or leave it to
	// the browser. A pane that asked for the protocol can RECEIVE super
	// chords, so forwarding one is giving an app its own keybinding; a
	// legacy pane cannot, so the same forward would swallow the user's
	// browser shortcut and send nothing (the encoder emits no bytes for
	// a super chord in legacy mode). Sent as the raw flags rather than a
	// bool because bit 2 (report-event-types) already decides elsewhere
	// whether key RELEASES are worth sending.
	//
	// omitempty keeps the legacy case off the wire entirely, which is
	// also what an older client sees: absent → 0 → nothing forwarded,
	// i.e. exactly today's behavior.
	Kitty uint16 `json:"kitty,omitempty"`
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

// PaneRespawned reports that a dead pane has a live child again — the inverse
// of PaneExited, and the only way a client learns to take the "exited (N)" off
// a header it already drew. There is no exit code to carry: the pane is alive.
//
// It exists because a pane's death is remembered by the client, not re-derived:
// the chrome a late joiner gets simply omits pane_exited for a live pane, so an
// already-connected window needs telling.
type PaneRespawned struct {
	T    Type   `json:"t"`
	Pane uint32 `json:"pane"`
}

func NewPaneRespawned(pane uint32) PaneRespawned {
	return PaneRespawned{T: MsgPaneRespawned, Pane: pane}
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
// "attention" (an agent hit a blocker), "finished" (a background agent run
// completed) or "info" (anything raised through ui.notify). Pane/Pub name the
// pane so a notification click can reveal it; the front-end suppresses the
// whole thing when that pane is visible and the page is focused (the user is
// already looking at it).
//
// ID and Actions arrive together or not at all: a notification that declared
// buttons carries the id they are answered by (ui.action) alongside them. The
// toast holding the buttons is therefore self-contained — it does not have to
// look the notification up to answer it, which matters because a toast can
// outlive the reconnect that would have invalidated any client-side handle.
type Notify struct {
	T       Type               `json:"t"`
	Kind    string             `json:"kind"`
	Message string             `json:"message"`
	Body    string             `json:"body,omitempty"`
	Pane    uint32             `json:"pane,omitempty"`
	Pub     string             `json:"pub,omitempty"`
	ID      string             `json:"id,omitempty"`
	Actions []app.NotifyAction `json:"actions,omitempty"`
}

func NewNotify(kind, message, body string) Notify {
	return Notify{T: MsgNotify, Kind: kind, Message: message, Body: body}
}

// History is the command ledger's recent entries, pushed rather than polled.
//
// A push because a command finishing is a moment only the server knows about:
// records come from the pane's own cathost, and a client polling for them would
// either lag a command it is looking at or ask on a timer for a section most
// sessions never open. Sent on client init and again whenever a command is
// recorded, carrying the whole recent list rather than a delta — the list is
// short, and one message that is always the complete answer costs less than a
// delta protocol the client could fall out of step with.
type History struct {
	T       Type              `json:"t"`
	Entries []app.LedgerEntry `json:"entries"`
}

func NewHistory(entries []app.LedgerEntry) History {
	return History{T: MsgHistory, Entries: entries}
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

// Usage is what the agents on this machine have spent, and against what — the
// sidebar's USAGE section, surfaced so "how much of the week have I spent?" is
// answerable without leaving the pane you are working in.
//
// Groups are the section's subsections, in the order they are drawn. One per
// provider that reports anything (claude, copilot), plus the host's own memory
// last. The message is a LIST rather than a fixed set of named windows because
// the providers do not meter alike: claude reports percentages of a 5-hour and
// a weekly allowance, copilot reports counts with no allowance at all, and the
// next one will do something else again. A struct with a field per window would
// have to be widened for every provider added; a list is widened by the reader
// that produces it.
//
// ReadAt is when the server took the reading (RFC 3339). It is the message's
// own age, and it is on the wire because the receiver cannot infer it: the
// stored reading is replayed to a browser that connects between polls, so
// "when did this arrive?" and "when was this true?" are different questions.
// A front-end shows it as "n ago" — a percentage with no date beside it looks
// equally current whether it was read a minute or an hour ago.
type Usage struct {
	T      Type         `json:"t"`
	Groups []UsageGroup `json:"groups"`
	ReadAt string       `json:"read_at,omitempty"`
}

// UsageGroup is one subsection: a heading and the rows drawn beneath it.
//
// ID is a detect.IdentifyAgent label ("claude", "copilot") for a provider, or
// the literal "host" for the one group the server synthesises rather than reads
// from a provider. That value is CLOSED, and a front-end may branch on it to
// pick a warning scale: the same percentage does not mean the same thing on a
// rate-limit window as on host memory, and only the group says which this is.
// Every other ID is opaque — nothing downstream should enumerate providers.
//
// Name is the heading text. Note is the caption under the rows, composed here
// because only the server knows why a group is showing an estimate rather than
// a reading. Note never carries a credential or a raw response body: the reader
// that fills it strips both first (see fetchAccountUsage).
//
// A group with neither Windows nor Note is never sent. An empty heading reads
// as a broken section rather than as an absent provider.
type UsageGroup struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Note    string        `json:"note,omitempty"`
	Windows []UsageWindow `json:"windows"`
}

// UsageWindow is one row. Name is its label ("5 hr", "Week · Fable"), supplied
// by the server for the same reason Groups is a list: the meters a provider
// reports are the provider's business, and the browser cannot enumerate them.
//
// Pct is the share of the window's allowance already spent (0–100), or -1 when
// there is no denominator to divide by — a local count fills Detail instead and
// leaves Pct at -1, so a missing number reads as missing rather than as zero.
// ResetsAt is when the window rolls over (RFC 3339), "" when unknown.
//
// Headline marks the one row that stands in for its whole group when the group
// is folded away. It is the server's call rather than the front-end's for the
// same reason the row names are: which of a provider's meters answers the
// question the section is scanned for — Claude's 5-hour window, not its week;
// the host's memory, not its disk — is knowledge about the provider, and the
// browser has none. At most one row per group carries it; a group that marks
// none leaves the front-end to fall back to whatever it showed before.
//
// SoonSecs is how long before ResetsAt the row's countdown deserves a warning
// colour — the point at which "when does this roll over" stops being background
// and starts being something to plan around. It scales with the window and not
// with the clock: half an hour left in a five-hour window is the last tenth of
// it, while half an hour left in a week is a rounding error nobody could act on,
// and by the time a week is worth mentioning at all there are a couple of hours
// left in it. Only the provider knows which of its rows is which, for the same
// reason it owns Name and Headline, so the number is sent rather than inferred
// from a name the browser would have to enumerate. 0 means "no opinion" and
// leaves the front-end its own default.
//
// Spark is the row's recent history, oldest first, in the same units as Pct: a
// front-end may draw it as a small chart beside the current reading. It is sent
// only for a row whose movement between polls is itself the information (host
// CPU), because a value that a two-minute poll captures faithfully — a weekly
// window, a disk — has nothing to plot that the number does not already say.
type UsageWindow struct {
	Name     string    `json:"name"`
	Pct      float64   `json:"pct"`
	ResetsAt string    `json:"resets_at,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	Headline bool      `json:"headline,omitempty"`
	SoonSecs int       `json:"soon_secs,omitempty"`
	Spark    []float64 `json:"spark,omitempty"`
}

// UsagePctUnknown is UsageWindow.Pct's "no denominator" value.
const UsagePctUnknown = -1

func NewUsage(groups []UsageGroup) Usage {
	return Usage{T: MsgUsage, Groups: groups}
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
