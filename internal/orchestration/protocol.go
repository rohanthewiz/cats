// Package orchestration defines the Phase B Go↔Rust seam: Rust orchestrates
// (workspace/pane tree, layout, detection, session, compositing) and Go is the
// terminal backend (PTY + VT emulation per pane). Rust sends commands
// (create/input/resize/close); Go sends events (pane frames, exit).
//
// This file is the wire contract and is pure Go (no CGO), so it compiles and is
// testable without the libghostty toolchain. The Host that actually runs panes
// lives in host.go behind the `ghostty` build tag.
//
// See ai_docs/phase-b-orchestration-seam.md for the full design.
package orchestration

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/rohanthewiz/cats/internal/hostmeter"
	"github.com/rohanthewiz/cats/internal/pathpick"
	"github.com/rohanthewiz/cats/internal/terminal"
)

// ProtocolVersion is bumped on any breaking change to the message shapes. v2
// added the raw pane-output stream (set_output_stream command + pane_output
// event) that pane.wait_for_output matches against, so a v1 daemon can't serve a
// v2 orchestrator's waits — the handshake rejects the mismatch rather than
// silently degrading (an old daemon would ignore set_output_stream and never
// stream, so waits would miss all post-registration output).
//
// v3 is the remote-host version: an authenticating hello (Hello.Token), a cwd
// fallback so a directory that exists only on the orchestrator's machine no
// longer yields a dead pane, and host-side branch resolution (pane_branch) —
// which is the only way a pane on another machine can carry a branch at all,
// since its cwd names a directory on *that* filesystem.
const ProtocolVersion = 3

// MinProtocolVersion is the oldest peer version this build still talks to.
//
// Exact-equality handshakes were affordable while both ends shipped in the same
// binary drop; they stop being affordable the moment a cathost lives on another
// machine, where "upgrade both at once" means an ssh session per host. Every v3
// addition is additive — a v2 peer ignores an unknown hello field and an unknown
// event type — so the two versions genuinely interoperate, with the v3-only
// behaviour (host-side branches) simply not happening.
const MinProtocolVersion = 2

// NegotiateVersion picks the version a session runs at: the newest both ends
// speak, or 0 when the peer's version is outside the supported range (the
// caller rejects the handshake).
//
// The daemon answers a hello with the *negotiated* version rather than its own,
// so an older orchestrator — which demands exact equality, because that is what
// every build before v3 did — still recognises a newer daemon's welcome. Without
// that, shipping v3 would break every not-yet-upgraded catway on contact.
func NegotiateVersion(peer int) int {
	if peer < MinProtocolVersion || peer > ProtocolVersion {
		return 0
	}
	return peer
}

// MaxFrameSize caps a single length-prefixed frame. A host frame is one pane
// (smaller than a full composited UI); 8 MiB leaves headroom for large grids.
const MaxFrameSize = 8 * 1024 * 1024

// MessageType is the JSON "type" discriminator.
type MessageType string

const (
	// Rust → Go (commands).
	MsgHello            MessageType = "hello"
	MsgCreatePane       MessageType = "create_pane"
	MsgInput            MessageType = "input"
	MsgResize           MessageType = "resize"
	MsgClosePane        MessageType = "close_pane"
	MsgScrollViewport   MessageType = "scroll_viewport"
	MsgRequestSelection MessageType = "request_selection"
	MsgRequestText      MessageType = "request_text"
	MsgRequestResync    MessageType = "request_resync"
	MsgSetOutputStream  MessageType = "set_output_stream"
	MsgShutdown         MessageType = "shutdown"
	MsgPing             MessageType = "ping"
	MsgRequestHostStats MessageType = "request_host_stats"
	MsgRequestListDir   MessageType = "request_list_dir"
	MsgHookReply        MessageType = "hook_reply"

	// Go → Rust (events).
	MsgWelcome       MessageType = "welcome"
	MsgPaneFrame     MessageType = "pane_frame"
	MsgPaneOutput    MessageType = "pane_output"
	MsgPaneCwd       MessageType = "pane_cwd"
	MsgPaneAgent     MessageType = "pane_agent"
	MsgPaneClipboard MessageType = "pane_clipboard"
	MsgPaneTitle     MessageType = "pane_title"
	MsgPaneSelection MessageType = "pane_selection"
	MsgPaneText      MessageType = "pane_text"
	MsgPaneModes     MessageType = "pane_modes"
	MsgPaneBranch    MessageType = "pane_branch"
	MsgPaneExited    MessageType = "pane_exited"
	MsgPong          MessageType = "pong"
	MsgHostStats     MessageType = "host_stats"
	MsgDirListing    MessageType = "dir_listing"
	MsgHookReport    MessageType = "hook_report"
	MsgError         MessageType = "error"
)

// --- Capabilities ------------------------------------------------------------
//
// Feature strings are how a client learns what a daemon can do beyond the
// version it negotiated, and they exist because the version ladder cannot carry
// an additive message safely in this direction.
//
// The problem is concrete: NegotiateVersion refuses a peer NEWER than this
// build, so a catway that bumped ProtocolVersion to announce one new request
// would be rejected outright by every already-deployed daemon one version
// behind — the exact fleet the range was widened for. And a request a daemon
// does not know is not silently ignored either: dispatch answers it with an
// "unknown message type" error event, which surfaces as a toast in somebody's
// browser.
//
// So a daemon lists what it can answer in its welcome, and a client sends a
// request only when it appears there. An empty list (every daemon built before
// this) means "the base protocol only", which is exactly right.
const (
	// FeaturePing: the daemon answers MsgPing with MsgPong. Carries the roster's
	// per-host latency, and doubles as liveness — a TCP connection to a machine
	// that slept stays "connected" until something is written to it.
	FeaturePing = "ping"
	// FeatureHostStats: the daemon can measure the machine it runs on and push
	// MsgHostStats on a subscription (MsgRequestHostStats). It is the only way a
	// pane's memory and disk pressure can be shown for a machine that is not
	// this one.
	FeatureHostStats = "host_stats"
	// FeatureListDir: the daemon can list a directory on its own filesystem
	// (MsgRequestListDir → MsgDirListing). It is what lets the start-path picker
	// complete a path for a pane on another machine, instead of being switched
	// off with an apology.
	FeatureListDir = "list_dir"
	// FeatureHookRelay: the daemon runs a hook-report socket on its own machine
	// and relays what arrives there to the client (MsgHookReport →
	// MsgHookReply). Advertised as a name AND as a path — Welcome.HookSocket is
	// what a client actually needs, since the value has to be injected into
	// every pane's environment — but the name is here so a client can reason
	// about the capability without special-casing an empty string.
	FeatureHookRelay = "hook_relay"
)

// --- Commands (Rust → Go) ---------------------------------------------------

type Hello struct {
	Type            MessageType `json:"type"`
	ProtocolVersion int         `json:"protocol_version"`
	// Token authenticates the orchestrator to a cathost started with
	// -token-file. Empty when the daemon requires none, which is every unix
	// socket session: filesystem permissions on the socket are the auth there,
	// and a token would add a secret to manage for no gain. A v2 daemon ignores
	// this field, so sending it costs nothing.
	Token string `json:"token,omitempty"`
}

func NewHello() Hello { return Hello{Type: MsgHello, ProtocolVersion: ProtocolVersion} }

// NewHelloWithToken is NewHello for a daemon that requires a bearer token.
func NewHelloWithToken(token string) Hello {
	h := NewHello()
	h.Token = token
	return h
}

type CreatePane struct {
	Type         MessageType       `json:"type"`
	PaneID       uint32            `json:"pane_id"`
	Cols         uint16            `json:"cols"`
	Rows         uint16            `json:"rows"`
	CellWidthPx  uint32            `json:"cell_width_px"`
	CellHeightPx uint32            `json:"cell_height_px"`
	Cwd          string            `json:"cwd,omitempty"`
	Command      string            `json:"command,omitempty"` // empty ⇒ default shell
	Args         []string          `json:"args,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	// InitialHistory is VT-encoded scrollback to seed the emulator with before the
	// child's first output, so a restored session shows its prior history above the
	// freshly spawned shell (the analogue of cats's seed_history_ansi).
	InitialHistory string `json:"initial_history,omitempty"`
}

func NewCreatePane(id uint32, cols, rows uint16) CreatePane {
	return CreatePane{Type: MsgCreatePane, PaneID: id, Cols: cols, Rows: rows}
}

// Input carries raw bytes to write to a pane's PTY. Data marshals as base64.
type Input struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
	Data   []byte      `json:"data"`
}

func NewInput(id uint32, data []byte) Input {
	return Input{Type: MsgInput, PaneID: id, Data: data}
}

type Resize struct {
	Type         MessageType `json:"type"`
	PaneID       uint32      `json:"pane_id"`
	Cols         uint16      `json:"cols"`
	Rows         uint16      `json:"rows"`
	CellWidthPx  uint32      `json:"cell_width_px"`
	CellHeightPx uint32      `json:"cell_height_px"`
}

func NewResize(id uint32, cols, rows uint16) Resize {
	return Resize{Type: MsgResize, PaneID: id, Cols: cols, Rows: rows}
}

type ClosePane struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
}

func NewClosePane(id uint32) ClosePane { return ClosePane{Type: MsgClosePane, PaneID: id} }

// ScrollViewport scrolls a pane's viewport by Delta lines: negative scrolls up
// into scrollback history, positive scrolls back down toward the live bottom. The
// Go side clamps to the available history, so a large positive Delta is a reliable
// "scroll to bottom". The resulting position is reported back via Frame.Scroll.
type ScrollViewport struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
	Delta  int32       `json:"delta"`
}

func NewScrollViewport(id uint32, delta int32) ScrollViewport {
	return ScrollViewport{Type: MsgScrollViewport, PaneID: id, Delta: delta}
}

// SelectionPoint is one endpoint of a selection in screen-buffer (absolute)
// coordinates: Row counts from the top of the scrollback buffer (so it is stable
// while the pane scrolls), Col is the 0-based column. This mirrors cats's
// Selection endpoints (row, col), which it tracks in screen-buffer space.
type SelectionPoint struct {
	Row uint32 `json:"row"`
	Col uint16 `json:"col"`
}

// RequestSelection asks the Go side to extract the text of the selection bounded
// by Anchor and Cursor (in screen-buffer coordinates). The orchestrator holds
// selection state and key/mouse handling; the Go daemon owns the emulator that
// can resolve those coordinates to text, so this is a request/response: the Host
// replies with a pane_selection event carrying the formatted text. The two
// endpoints may be in any order (the Host orders them top-left → bottom-right);
// Rectangle selects a block region rather than a linear (reading-order) range.
type RequestSelection struct {
	Type      MessageType    `json:"type"`
	PaneID    uint32         `json:"pane_id"`
	Anchor    SelectionPoint `json:"anchor"`
	Cursor    SelectionPoint `json:"cursor"`
	Rectangle bool           `json:"rectangle,omitempty"`
}

func NewRequestSelection(id uint32, anchor, cursor SelectionPoint, rectangle bool) RequestSelection {
	return RequestSelection{Type: MsgRequestSelection, PaneID: id, Anchor: anchor, Cursor: cursor, Rectangle: rectangle}
}

// RequestText asks the Go side to extract buffer text from a pane (the orchestrator
// holds an unfed local emulator for cathost panes, so it can't read text itself).
// The Host replies with a pane_text event. Scope is terminal.TextScope (0 visible,
// 1 recent); Lines bounds the recent scope (0 = whole buffer); Ansi selects VT vs
// plain; Unwrap rejoins soft-wrapped lines.
type RequestText struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
	Scope  uint8       `json:"scope"`
	Lines  uint32      `json:"lines,omitempty"`
	Ansi   bool        `json:"ansi,omitempty"`
	Unwrap bool        `json:"unwrap,omitempty"`
}

func NewRequestText(id uint32, scope uint8, lines uint32, ansi, unwrap bool) RequestText {
	return RequestText{Type: MsgRequestText, PaneID: id, Scope: scope, Lines: lines, Ansi: ansi, Unwrap: unwrap}
}

// RequestResync asks the daemon to replay a single pane's current state (full
// frame + modes + cwd + title + agent). A reconnecting client sends this after
// adopting a surviving pane reported in welcome.panes, so the pane repaints
// deterministically regardless of when the client registered it (it doesn't have
// to race the daemon's post-hello replay). Unknown pane IDs are ignored.
type RequestResync struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
}

func NewRequestResync(id uint32) RequestResync {
	return RequestResync{Type: MsgRequestResync, PaneID: id}
}

// SetOutputStream toggles a pane's raw-output stream: while Enabled, the Host
// emits a pane_output event carrying each chunk of raw PTY bytes it reads (on top
// of the usual diffed frames). The orchestrator turns this on for a pane only
// while a pane.wait_for_output waiter is active — matching against the byte stream
// catches fast-scrolling transient output the diffed frames coalesce away, and the
// final pre-exit output (emitted before pane_exited) that a post-exit capture
// can't reach. It is off by default, so a pane with no waiter never pays the
// raw-stream cost.
type SetOutputStream struct {
	Type    MessageType `json:"type"`
	PaneID  uint32      `json:"pane_id"`
	Enabled bool        `json:"enabled"`
}

func NewSetOutputStream(id uint32, enabled bool) SetOutputStream {
	return SetOutputStream{Type: MsgSetOutputStream, PaneID: id, Enabled: enabled}
}

// Shutdown asks a persistent daemon to exit and tear down all panes. The
// orchestrator sends this on a *clean* quit so the daemon doesn't linger; a
// crash or binary handoff instead just drops the connection (the daemon keeps
// its panes alive for the next cats to reconnect and resync).
// Ping asks the daemon for a pong carrying the same ID. The ID is the client's
// to choose and means nothing to the daemon — it exists so a reply can be
// matched to the request that caused it, which is what makes the round trip a
// measurement rather than a guess.
//
// Answered on the daemon's ordinary event queue, deliberately: a pong that
// jumped the queue would measure the network and not the thing the user feels,
// which is how long a keystroke takes to come back as a frame.
type Ping struct {
	Type MessageType `json:"type"`
	ID   uint64      `json:"id"`
}

func NewPing(id uint64) Ping { return Ping{Type: MsgPing, ID: id} }

// RequestHostStats subscribes to the daemon's readings of the machine it runs
// on, one MsgHostStats every IntervalMs. IntervalMs of 0 cancels: the daemon
// stops sampling and sends nothing more.
//
// A subscription rather than a request/reply pair because the expensive part of
// a CPU reading is not the answer, it is *having been measuring* — utilization
// is a rate, so it only exists as a difference between two readings taken an
// interval apart. A daemon nobody has subscribed to therefore samples nothing
// at all, which is the point: a cathost is not a monitoring agent, and starting
// an iostat on every box in the roster for a sidebar section nobody has opened
// would be a poor trade.
//
// The interval is the client's to choose because the client knows who is
// looking. Re-sending it re-paces an existing subscription rather than adding a
// second one — there is one per connection, and the connection is the
// subscription's lifetime.
// RequestListDir asks the daemon to list a directory on ITS filesystem.
//
// Dir is what the user has typed, unexpanded: "~", "$HOME/src" and a relative
// path all have to be resolved by the machine that owns the paths, because "~"
// is the daemon's user's home and "." is a directory only its kernel can
// resolve. Base is the anchor a relative Dir resolves against — the addressed
// pane's live cwd — and "" means the daemon's home directory, which is what a
// picker opened on a host this session has no pane on should start at.
//
// Live carries the client's own set of interesting directories (this session's
// live pane cwds on this host). They are merged behind the frecency ranking
// rather than displacing it, and are stat'ed by the daemon, since only its
// kernel can say whether they are still directories.
//
// PaneID is a correlation handle rather than a subject: the daemon echoes it in
// the reply, and the client matches replies to requests per pane in order, the
// same way request_text and pane_text are matched. It names the pane the picker
// is anchored on.
type RequestListDir struct {
	Type    MessageType `json:"type"`
	PaneID  uint32      `json:"pane_id"`
	Dir     string      `json:"dir,omitempty"`
	Base    string      `json:"base,omitempty"`
	Recents bool        `json:"recents,omitempty"`
	Live    []string    `json:"live,omitempty"`
}

func NewRequestListDir(paneID uint32, dir, base string, recents bool, live []string) RequestListDir {
	return RequestListDir{Type: MsgRequestListDir, PaneID: paneID, Dir: dir, Base: base, Recents: recents, Live: live}
}

// HookReply carries a hook reply back to the daemon, which writes it to the
// waiting hook client and closes. ID matches the report it answers.
type HookReply struct {
	Type    MessageType `json:"type"`
	ID      uint64      `json:"id"`
	Payload []byte      `json:"payload"`
}

func NewHookReply(id uint64, payload []byte) HookReply {
	return HookReply{Type: MsgHookReply, ID: id, Payload: payload}
}

type RequestHostStats struct {
	Type       MessageType `json:"type"`
	IntervalMs int         `json:"interval_ms"`
}

func NewRequestHostStats(interval time.Duration) RequestHostStats {
	return RequestHostStats{Type: MsgRequestHostStats, IntervalMs: int(interval.Milliseconds())}
}

type Shutdown struct {
	Type MessageType `json:"type"`
}

func NewShutdown() Shutdown { return Shutdown{Type: MsgShutdown} }

// --- Events (Go → Rust) -----------------------------------------------------

type Welcome struct {
	Type            MessageType `json:"type"`
	ProtocolVersion int         `json:"protocol_version"`
	Error           string      `json:"error,omitempty"`
	// Panes lists the pane IDs the daemon already has live when a client connects.
	// Empty on a fresh daemon; populated when a restarted/handed-off cats reconnects
	// to a persistent daemon, so it can reconcile its restored session against the
	// surviving panes (adopt the matches, expect a resync for each) instead of
	// re-creating them. The daemon replays each pane's current state (full frame +
	// modes + cwd + title + agent) right after this welcome.
	Panes []uint32 `json:"panes,omitempty"`
	// Features names the optional requests this daemon can answer beyond the
	// negotiated version's base set (see the Feature* constants). Absent from
	// every daemon built before capabilities existed, which is why the client's
	// rule is "send it only if it is listed" rather than "unless it is refused".
	Features []string `json:"features,omitempty"`
	// HookSocket is the path, ON THE DAEMON'S MACHINE, of the socket that
	// relays agent hook reports back to this client. Empty when the daemon
	// could not open one.
	//
	// It is in the welcome rather than behind a request because a client needs
	// it before it creates its first pane: the path is injected into every
	// pane's environment (CATS_SOCKET_PATH), and a pane spawned before the
	// answer arrived would have inert hooks until something respawned it.
	//
	// The path is stable for the daemon's lifetime, not the connection's. Panes
	// outlive a reconnect in persistent mode, and their environment cannot be
	// rewritten after the fact.
	HookSocket string `json:"hook_socket,omitempty"`
}

func NewWelcome(errMsg string, panes []uint32) Welcome {
	return NewWelcomeAt(ProtocolVersion, errMsg, panes)
}

// NewWelcomeAt is NewWelcome reporting a specific (negotiated) version — what
// the daemon answers a hello with, so an older orchestrator sees the version it
// asked for rather than one it would refuse. A rejection carries this build's
// own version instead, since there is no agreed one to report.
//
// The feature list is this build's own either way: it describes what this
// process can answer, which does not shrink because the peer asked for an older
// version. A client that does not understand the field ignores it.
func NewWelcomeAt(version int, errMsg string, panes []uint32) Welcome {
	w := Welcome{Type: MsgWelcome, ProtocolVersion: version, Error: errMsg, Panes: panes}
	if errMsg == "" {
		w.Features = Features()
	}
	return w
}

// Features is what this build can answer. Returned as a fresh slice so a caller
// cannot alter the daemon's advertisement by holding onto it.
func Features() []string {
	return []string{FeaturePing, FeatureHostStats, FeatureListDir, FeatureHookRelay}
}

type PaneFrame struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
	Frame  *Frame      `json:"frame"`
}

func NewPaneFrame(id uint32, f *Frame) PaneFrame {
	return PaneFrame{Type: MsgPaneFrame, PaneID: id, Frame: f}
}

// PaneOutput carries a chunk of raw PTY bytes exactly as the pane's child emitted
// them (Data marshals as base64), streamed only while the pane's output stream is
// enabled (SetOutputStream). Unlike a diffed frame this is the unmodified byte
// stream — VT escapes and all — so a consumer sees every byte the program wrote,
// including transient output that never lands on a rendered frame. The
// orchestrator uses it for pane.wait_for_output pattern matching; it is not a
// browser-facing message.
type PaneOutput struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
	Data   []byte      `json:"data"`
}

func NewPaneOutput(id uint32, data []byte) PaneOutput {
	return PaneOutput{Type: MsgPaneOutput, PaneID: id, Data: data}
}

// PaneCwd reports a pane's working directory (OSC 7) when it changes, so the
// orchestrator can track per-pane cwd (new-pane inheritance, worktree).
type PaneCwd struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
	Cwd    string      `json:"cwd"`
}

func NewPaneCwd(id uint32, cwd string) PaneCwd {
	return PaneCwd{Type: MsgPaneCwd, PaneID: id, Cwd: cwd}
}

// PaneAgent reports the detected agent identity and state for a pane. The Go
// daemon owns the PTY child, so it runs detection and reports results; the
// orchestrator maps this onto its screen-detection path. Agent is "" for a plain
// shell; State is one of idle|working|blocked|unknown.
type PaneAgent struct {
	Type           MessageType `json:"type"`
	PaneID         uint32      `json:"pane_id"`
	Agent          string      `json:"agent"`
	State          string      `json:"state"`
	VisibleBlocker bool        `json:"visible_blocker"`
	VisibleWorking bool        `json:"visible_working"`
}

func NewPaneAgent(id uint32, agent, state string, visibleBlocker, visibleWorking bool) PaneAgent {
	return PaneAgent{
		Type:           MsgPaneAgent,
		PaneID:         id,
		Agent:          agent,
		State:          state,
		VisibleBlocker: visibleBlocker,
		VisibleWorking: visibleWorking,
	}
}

// PaneClipboard forwards an OSC 52 clipboard-write the pane's child emitted.
// libghostty-vt drops OSC 52, so the Host reconstructs it from the raw PTY byte
// stream (as it does OSC 7 cwd) and the orchestrator re-emits it through its own
// clipboard writer. Data is the decoded clipboard bytes (base64 on the wire); an
// empty Data is a clipboard-clear. Only the "c"/default selection is forwarded;
// queries have no reply path and are dropped.
type PaneClipboard struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
	Data   []byte      `json:"data"`
}

func NewPaneClipboard(id uint32, data []byte) PaneClipboard {
	return PaneClipboard{Type: MsgPaneClipboard, PaneID: id, Data: data}
}

// PaneTitle reports a pane's window title (OSC 0/2) when it changes. libghostty
// surfaces the title to the emulator, but the seam otherwise carries none, so the
// orchestrator can show the running program's title on a cathost pane's border the
// way it does for in-process panes. An empty Title is a title-clear.
type PaneTitle struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
	Title  string      `json:"title"`
}

func NewPaneTitle(id uint32, title string) PaneTitle {
	return PaneTitle{Type: MsgPaneTitle, PaneID: id, Title: title}
}

// PaneBranch reports the git branch checked out in a pane's working directory
// ("" when it is not in a repository, "@<sha>" while detached). The daemon owns
// this because the pane's cwd is a path on the *daemon's* filesystem: resolving
// it on the orchestrator's machine answers a question about a different
// directory that merely shares a name — or, more often, about nothing at all.
// A v2 daemon never sends it and the orchestrator keeps resolving locally,
// which is correct there because a v2 daemon is always the local one.
type PaneBranch struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
	Branch string      `json:"branch"`
}

func NewPaneBranch(id uint32, branch string) PaneBranch {
	return PaneBranch{Type: MsgPaneBranch, PaneID: id, Branch: branch}
}

// PaneSelection is the reply to a RequestSelection: the plain text of the
// requested range, with soft-wrapped lines unwrapped and trailing whitespace
// trimmed (matching cats's own selection extraction). Text is "" when the range
// has no selectable content. The orchestrator hands this to its clipboard writer
// (AppEvent::ClipboardWrite). One pane_selection is emitted per request.
type PaneSelection struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
	Text   string      `json:"text"`
}

func NewPaneSelection(id uint32, text string) PaneSelection {
	return PaneSelection{Type: MsgPaneSelection, PaneID: id, Text: text}
}

// PaneText is the reply to a RequestText: the extracted buffer text (empty when the
// range has no content). One pane_text is emitted per request.
type PaneText struct {
	Type   MessageType `json:"type"`
	PaneID uint32      `json:"pane_id"`
	Text   string      `json:"text"`
}

func NewPaneText(id uint32, text string) PaneText {
	return PaneText{Type: MsgPaneText, PaneID: id, Text: text}
}

// PaneModes reports a pane's input-affecting DEC mode state (mouse tracking,
// bracketed paste, focus reporting, application cursor, alt-scroll, sync output,
// kitty keyboard) when it changes. The Go emulator owns these; the orchestrator
// mirrors them so its key/mouse encoders and its "is this event for the program or
// for my UI" decisions match what the running program actually requested. Without
// this the orchestrator would read its own unfed emulator and mis-encode input.
type PaneModes struct {
	Type                 MessageType `json:"type"`
	PaneID               uint32      `json:"pane_id"`
	AlternateScreen      bool        `json:"alternate_screen"`
	ApplicationCursor    bool        `json:"application_cursor"`
	BracketedPaste       bool        `json:"bracketed_paste"`
	FocusReporting       bool        `json:"focus_reporting"`
	MouseMode            uint8       `json:"mouse_mode"`     // terminal.MouseMode
	MouseEncoding        uint8       `json:"mouse_encoding"` // terminal.MouseEncoding
	MouseAlternateScroll bool        `json:"mouse_alternate_scroll"`
	SynchronizedOutput   bool        `json:"synchronized_output"`
	KittyKeyboardFlags   uint16      `json:"kitty_keyboard_flags"`
	ModifyOtherKeys      bool        `json:"modify_other_keys,omitempty"`
}

func NewPaneModes(id uint32, m terminal.InputModes) PaneModes {
	return PaneModes{
		Type:                 MsgPaneModes,
		PaneID:               id,
		AlternateScreen:      m.AlternateScreen,
		ApplicationCursor:    m.ApplicationCursor,
		BracketedPaste:       m.BracketedPaste,
		FocusReporting:       m.FocusReporting,
		MouseMode:            uint8(m.MouseMode),
		MouseEncoding:        uint8(m.MouseEncoding),
		MouseAlternateScroll: m.MouseAlternateScroll,
		SynchronizedOutput:   m.SynchronizedOutput,
		KittyKeyboardFlags:   m.KittyKeyboardFlags,
		ModifyOtherKeys:      m.ModifyOtherKeys,
	}
}

type PaneExited struct {
	Type     MessageType `json:"type"`
	PaneID   uint32      `json:"pane_id"`
	ExitCode int         `json:"exit_code"`
}

func NewPaneExited(id uint32, code int) PaneExited {
	return PaneExited{Type: MsgPaneExited, PaneID: id, ExitCode: code}
}

// Pong answers a Ping with the ID it carried. It is the only event the daemon
// sends that has nothing to do with a pane.
type Pong struct {
	Type MessageType `json:"type"`
	ID   uint64      `json:"id"`
}

func NewPong(id uint64) Pong { return Pong{Type: MsgPong, ID: id} }

// HostStats is one reading of the machine the daemon runs on: the memory, CPU
// and disk rows hostmeter produces, already named and formatted.
//
// The rows travel display-ready rather than as raw byte counts because both
// ends would otherwise have to agree on how to phrase them, and the local host's
// section is built from the same hostmeter.Rows call. Sending the numbers and
// re-deriving the captions on the far side is how the local and remote halves of
// one sidebar section start disagreeing about what "used" means.
//
// An empty Rows is a real answer: a machine whose meters cannot be read says so
// by reporting nothing, and the client draws no section for it.
type HostStats struct {
	Type MessageType     `json:"type"`
	Rows []hostmeter.Row `json:"rows,omitempty"`
}

func NewHostStats(rows []hostmeter.Row) HostStats {
	return HostStats{Type: MsgHostStats, Rows: rows}
}

// DirListing answers a RequestListDir, echoing its PaneID so the client can
// match it to the request. A directory that could not be read is not an error
// here — the Listing carries Exists false and the reason, because a half-typed
// path is the common case and the picker keeps taking keystrokes.
type DirListing struct {
	Type    MessageType      `json:"type"`
	PaneID  uint32           `json:"pane_id"`
	Listing pathpick.Listing `json:"listing"`
}

func NewDirListing(paneID uint32, l pathpick.Listing) DirListing {
	return DirListing{Type: MsgDirListing, PaneID: paneID, Listing: l}
}

// HookReport forwards one request that arrived on the daemon's hook socket,
// verbatim. The daemon does not parse it and does not act on it: the agent
// state it describes belongs to a pane the orchestrator owns, and the hook API
// is the orchestrator's to define. Relaying the bytes rather than a decoded
// shape is also what keeps the daemon out of the way of the next field added to
// that API.
//
// ID is the daemon's; it holds the hook client's connection open until the
// matching HookReply comes back.
type HookReport struct {
	Type    MessageType `json:"type"`
	ID      uint64      `json:"id"`
	Payload []byte      `json:"payload"`
}

func NewHookReport(id uint64, payload []byte) HookReport {
	return HookReport{Type: MsgHookReport, ID: id, Payload: payload}
}

type Error struct {
	Type    MessageType `json:"type"`
	PaneID  uint32      `json:"pane_id,omitempty"`
	Message string      `json:"message"`
}

func NewError(paneID uint32, msg string) Error {
	return Error{Type: MsgError, PaneID: paneID, Message: msg}
}

// --- Frame / cell wire types ------------------------------------------------
//
// Shaped to drop straight into Rust wire::FrameData / CellData compositing.

// Cell mirrors Rust wire::CellData.
type Cell struct {
	Symbol    string  `json:"symbol"`
	Fg        uint32  `json:"fg"`        // packed: 0x02_RR_GG_BB
	Bg        uint32  `json:"bg"`        // packed: 0x02_RR_GG_BB
	Modifier  uint16  `json:"modifier"`  // ratatui Modifier bitmask
	Skip      bool    `json:"skip"`      // true ⇒ unchanged since last frame (diff)
	Hyperlink *uint32 `json:"hyperlink"` // OSC 8 index (reserved; not yet populated)
}

// Cursor mirrors Rust wire::CursorState.
type Cursor struct {
	X       uint16 `json:"x"`
	Y       uint16 `json:"y"`
	Visible bool   `json:"visible"`
	Shape   uint8  `json:"shape"` // DECSCUSR param
}

// Frame is one pane's grid, full or diffed.
type Frame struct {
	Cols   uint16  `json:"cols"`
	Rows   uint16  `json:"rows"`
	Full   bool    `json:"full"`
	Cursor *Cursor `json:"cursor"`
	Cells  []Cell  `json:"cells"` // row-major, len == cols*rows
	// Hyperlinks is the frame's OSC 8 URI table; a cell's Hyperlink indexes into it.
	// Only populated on frames that carry links (which are always sent full).
	Hyperlinks []string `json:"hyperlinks,omitempty"`
	// Scroll is the pane's scrollback position, present only when the pane has
	// scrollback history (so non-scrollback panes' frames are unchanged).
	Scroll *ScrollInfo `json:"scroll,omitempty"`
}

// ScrollInfo mirrors terminal.ScrollMetrics on the wire (and cats's ScrollMetrics).
type ScrollInfo struct {
	OffsetFromBottom    int `json:"offset_from_bottom"`
	MaxOffsetFromBottom int `json:"max_offset_from_bottom"`
	ViewportRows        int `json:"viewport_rows"`
}

// ratatui Modifier bits (subset we map).
const (
	modBold       uint16 = 0b0000_0000_0001
	modDim        uint16 = 0b0000_0000_0010
	modItalic     uint16 = 0b0000_0000_0100
	modUnderlined uint16 = 0b0000_0000_1000
	modReversed   uint16 = 0b0000_0100_0000
	modCrossedOut uint16 = 0b0001_0000_0000
)

// packRGB encodes an RGB color like Rust wire::color_to_u32 (RGB variant).
func packRGB(c terminal.Color) uint32 {
	return 0x02000000 | uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
}

func modifierBits(c terminal.Cell) uint16 {
	var m uint16
	if c.Bold {
		m |= modBold
	}
	if c.Faint {
		m |= modDim
	}
	if c.Italic {
		m |= modItalic
	}
	if c.Underline {
		m |= modUnderlined
	}
	if c.Inverse {
		m |= modReversed
	}
	if c.Strikethrough {
		m |= modCrossedOut
	}
	return m
}

func cursorShape(s terminal.CursorStyle) uint8 {
	switch s {
	case terminal.CursorBar:
		return 6
	case terminal.CursorUnderline:
		return 4
	default: // block / block-hollow
		return 2
	}
}

// resolveCell turns a Snapshot cell into a wire Cell (without the skip flag),
// resolving nil fg/bg to the snapshot defaults so Rust receives concrete colors.
func resolveCell(snap *terminal.Snapshot, c terminal.Cell) Cell {
	fg := snap.DefaultFg
	if c.Fg != nil {
		fg = *c.Fg
	}
	bg := snap.DefaultBg
	if c.Bg != nil {
		bg = *c.Bg
	}
	sym := c.Rune
	if sym == "" {
		sym = " "
	}
	return Cell{
		Symbol:   sym,
		Fg:       packRGB(fg),
		Bg:       packRGB(bg),
		Modifier: modifierBits(c),
	}
}

// FrameFromSnapshot builds a Frame for cur. If prev is nil or its dimensions
// differ, the frame is full (all cells sent, skip=false). Otherwise it is a
// diff: cells unchanged from prev are marked skip=true.
func FrameFromSnapshot(cur, prev *terminal.Snapshot) *Frame {
	// A frame carrying OSC 8 links is always sent full: the per-cell hyperlink
	// index points into this frame's Hyperlinks table, and a skipped (diff) cell
	// would keep a stale index from the prior frame's table. Links are uncommon
	// and transient, so the lost diff savings while a link is on screen is fine.
	full := prev == nil || prev.Cols != cur.Cols || prev.Rows != cur.Rows || cur.HasHyperlinks

	f := &Frame{
		Cols:  cur.Cols,
		Rows:  cur.Rows,
		Full:  full,
		Cells: make([]Cell, 0, int(cur.Cols)*int(cur.Rows)),
		Cursor: &Cursor{
			X:       cur.Cursor.X,
			Y:       cur.Cursor.Y,
			Visible: cur.Cursor.Visible,
			Shape:   cursorShape(cur.Cursor.Style),
		},
	}

	var hlIndex map[string]uint32 // URI → table index, built only when links present
	if cur.HasHyperlinks {
		hlIndex = make(map[string]uint32)
	}

	for y := uint16(0); y < cur.Rows; y++ {
		for x := uint16(0); x < cur.Cols; x++ {
			src := cur.At(x, y)
			cell := resolveCell(cur, src)
			if !full {
				if prevCell := resolveCell(prev, prev.At(x, y)); prevCell == cell {
					cell.Skip = true
				}
			}
			if hlIndex != nil && src.Link != "" {
				idx, ok := hlIndex[src.Link]
				if !ok {
					idx = uint32(len(f.Hyperlinks))
					hlIndex[src.Link] = idx
					f.Hyperlinks = append(f.Hyperlinks, src.Link)
				}
				i := idx // stable address; &idx would alias the loop's reused var
				cell.Hyperlink = &i
			}
			f.Cells = append(f.Cells, cell)
		}
	}
	// Carry scrollback position only when the pane has history (or is scrolled),
	// leaving non-scrollback panes' frames byte-for-byte as before.
	if cur.Scroll.MaxOffsetFromBottom > 0 || cur.Scroll.OffsetFromBottom > 0 {
		f.Scroll = &ScrollInfo{
			OffsetFromBottom:    cur.Scroll.OffsetFromBottom,
			MaxOffsetFromBottom: cur.Scroll.MaxOffsetFromBottom,
			ViewportRows:        cur.Scroll.ViewportRows,
		}
	}
	return f
}

// --- Framing codec: [u32-LE length][JSON payload] ---------------------------

// WriteMessage marshals m to JSON and writes it as a length-prefixed frame.
func WriteMessage(w io.Writer, m any) error {
	payload, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("orchestration: marshal: %w", err)
	}
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("orchestration: message %d exceeds max %d", len(payload), MaxFrameSize)
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

// ReadMessage reads one frame and returns its type plus the raw JSON payload.
// Callers unmarshal the payload into the concrete message struct for that type.
func ReadMessage(r io.Reader) (MessageType, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n > MaxFrameSize {
		return "", nil, fmt.Errorf("orchestration: frame length %d exceeds max %d", n, MaxFrameSize)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return "", nil, err
	}
	var env struct {
		Type MessageType `json:"type"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return "", nil, fmt.Errorf("orchestration: decode type: %w", err)
	}
	return env.Type, payload, nil
}
