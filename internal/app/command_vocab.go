package app

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/layout"
)

// This file is the protocol-neutral §7 command vocabulary: the command names,
// their parameter/result structs, and the small string→enum mappings the
// dispatcher needs. It lives in internal/app (not browserproto) so the one
// command table can serve both the browser WebSocket protocol and a future
// CLI/control-API. browserproto re-exports these as aliases for wire use; the
// json tags here are inert unless a json ParamDecoder consults them.

// Command names (§7): the control-API vocabulary. The dispatcher implements one
// command table serving both this protocol and the CLI/API.
const (
	CmdPaneSplit          = "pane.split"
	CmdPaneClose          = "pane.close"
	CmdPaneFocus          = "pane.focus"
	CmdPaneFocusDirection = "pane.focus_direction"
	CmdPaneCycle          = "pane.cycle"
	CmdPaneLast           = "pane.last"
	CmdPaneSwap           = "pane.swap"
	CmdPaneSwapWith       = "pane.swap_with"
	CmdPaneZoom           = "pane.zoom"
	CmdPaneRename         = "pane.rename"
	CmdPaneResizeBorder   = "pane.resize_border"
	CmdScroll             = "scroll"
	CmdRead               = "read"
	CmdCapture            = "capture"
	CmdWaitForOutput      = "pane.wait_for_output"
	CmdPaneSendInput      = "pane.send_input"
	CmdTabCreate          = "tab.create"
	CmdTabClose           = "tab.close"
	CmdTabFocus           = "tab.focus"
	CmdTabRename          = "tab.rename"
	CmdTabMove            = "tab.move"
	CmdWorkspaceCreate    = "workspace.create"
	CmdWorkspaceClose     = "workspace.close"
	CmdWorkspaceFocus     = "workspace.focus"
	CmdWorkspaceRename    = "workspace.rename"
	CmdWorkspaceMove      = "workspace.move"
	CmdWorkspaceLock      = "workspace.lock"
	CmdAgentFocus         = "agent.focus"
	CmdServerReloadConfig = "server.reload_config"
	CmdServerStop         = "server.stop"

	// CmdUsageRefresh re-reads the account's rate-limit windows now instead of
	// at the poller's next tick. The reading is pushed as a `usage` message
	// rather than returned, so every client sees the fresh numbers, not just
	// the one that asked.
	CmdUsageRefresh = "usage.refresh"

	// Chat commands (the ACP side panel). Like usage.refresh, their product is
	// the chat_* broadcast stream, not the reply — every client renders the
	// conversation, so answering only the sender would desynchronise the rest.
	CmdChatSend       = "chat.send"
	CmdChatCancel     = "chat.cancel"
	CmdChatPermission = "chat.permission"
	CmdChatClear      = "chat.clear"

	// Git-worktree commands (WS8 dialogs): list/create/open/remove checkouts
	// anchored on a pane's repo. The git work runs off-loop (Backend Start*).
	CmdWorktreeList   = "worktree.list"
	CmdWorktreeCreate = "worktree.create"
	CmdWorktreeOpen   = "worktree.open"
	CmdWorktreeRemove = "worktree.remove"

	// Config commands (settings modal): read the live configuration and persist
	// the live-appliable sections (theme, copy-mode keys).
	CmdConfigGet = "config.get"
	CmdConfigSet = "config.set"

	// Theme commands (settings modal + catctl + theme plugins): enumerate the
	// available themes and manage the user's custom theme files. Switching the
	// active theme is config.set (it's a config choice); these manage the
	// theme *library*.
	CmdThemeList   = "theme.list"
	CmdThemeSave   = "theme.save"
	CmdThemeDelete = "theme.delete"

	// Plugin commands (plugins dialog): enumerate and remove installed plugins.
	// Deliberately only the instant verbs — install/update shell out to git and
	// a build, whose output a caller wants to *watch*, so the dialog launches
	// those as `catctl plugin …` in a fresh tab (tab.create spawn params) rather
	// than hiding minutes of subprocess work behind a single cmd_result.
	CmdPluginList      = "plugin.list"
	CmdPluginUninstall = "plugin.uninstall"

	// Path listing (the start-path picker in the new-workspace dialog): one
	// directory's subdirectories plus the user's frecency-ranked recent
	// directories, so a front-end can complete a start path against the
	// server's filesystem instead of asking the user to type it blind.
	CmdPathList = "path.list"

	// UI notifications. ui.notify lets anything holding the control socket —
	// a plugin, an agent hook, a runbook, an editor in a pane on another
	// machine — raise the same notification an agent state change raises, so
	// "the build finished" reaches the browser toast, the pane_notify event
	// stream and the phone by the one path that already carries all three.
	// It confers no new privilege: a caller that can pane.send_input can
	// already do everything a notification action does, by a longer route.
	//
	// ui.action is the answer half. It is a command rather than a browser-only
	// message because the button may be tapped somewhere the WebSocket does not
	// reach — a lock screen — and both routes must land on one implementation
	// that performs the effect exactly once.
	CmdUINotify = "ui.notify"
	CmdUIAction = "ui.action"

	// pane.open_file asks the session's editor to open a path — the inverse of
	// `ced --remote`, and the command behind "click a path anywhere in cats and
	// it opens in the editor".
	//
	// cats deliberately learns almost nothing about editors from this. It does
	// not run an editor CLI and does not know one exists: it works out WHICH
	// pane should hear the request and emits a pane_open_file event on the
	// control stream that pane's editor is already subscribed to. An editor on
	// another machine is then free by construction — the control relay carries
	// its subscription — which is the same reason path.list and the worktree
	// commands are shaped the way they are.
	CmdPaneOpenFile = "pane.open_file"

	// Host commands (the HOSTS section's buttons + catctl): attach a cathost to
	// the running session, or detach one. They are §7 commands rather than a
	// config-file-only setting for the same reason config.set is one — a browser
	// client is an authenticated owner of this catway, and making "add the
	// build box" a restart turns the roster into something only the terminal
	// can edit. Both persist to the config's hosts: block, so the roster
	// survives the restart they no longer require.
	CmdHostAttach = "host.attach"
	CmdHostDetach = "host.detach"

	// Read-only query commands (§7): they return a snapshot of session state
	// and mutate nothing, so the dispatcher answers them straight from the
	// Session with no Backend effects.
	CmdSessionGet    = "session.get"
	CmdWorkspaceList = "workspace.list"
	CmdTabList       = "tab.list"
	CmdPaneList      = "pane.list"
	CmdPaneGet       = "pane.get"
	CmdHostList      = "host.list"
)

// CommandSpec describes one §7 command as data: its name, the zero value of its
// params and result structs, and the two dispatch properties a caller cannot
// infer from the name.
//
// It exists because the name ↔ params ↔ result mapping otherwise lives only in
// the Dispatch switch and the doc comments below — readable, but not walkable by
// a program. As data it can generate a client: cmd/catgen-dart emits the mobile
// app's typed call sites from this table, so a command added here arrives on the
// phone as a typed method rather than a hand-written string and a map literal.
//
// Params and Result hold a ZERO VALUE of the struct (SplitParams{}), not a
// reflect.Type — the table stays readable at the call site and a generator takes
// reflect.TypeOf itself. nil is meaningful in both: "takes no params" and
// "returns no data" are distinct from "takes an empty struct", because a
// generator emits different signatures for them.
type CommandSpec struct {
	Name   string
	Params any // zero value of the params struct; nil when parameterless
	Result any // zero value of the CmdResult.Data struct; nil when nothing is returned

	// ReplyRequired marks the commands Dispatch SILENTLY DROPS when the caller
	// cannot receive a result — a browser `cmd` sent with no `id`, whose
	// Responder reports WantsReply false. They exist only to produce data, so
	// an answer with nowhere to go is no answer, and for the async ones
	// (read/capture/wait) registering a pending round-trip that can never
	// resolve would simply leak it.
	//
	// This is the rule most likely to bite a client author, because the failure
	// is silence rather than an error: it makes "I sent capture and nothing
	// happened" a stated property of the call instead of a bug hunt.
	ReplyRequired bool

	// ParamsRequired marks the commands that fail with "bad params" when the
	// caller supplies none. The rest decode optionally — absent params mean the
	// zero value, which is a meaningful call (pane.close closes the focused
	// pane; tab.create opens a default-shell tab).
	//
	// It is a property of the COMMAND, not of the params type: WorkspaceParams
	// is required by workspace.focus, which cannot guess an id, and optional for
	// workspace.close, where an empty id means the active workspace.
	ParamsRequired bool
}

// commandSpecs is the §7 command table: the single list both CommandSpecs and
// CommandNames derive from, in the order `catctl commands` prints. Grouped like
// the constants above.
//
// Adding a command means three edits that must agree — the constant, this entry,
// and the Dispatch case. TestCommandSpecsRouted checks all three against the
// dispatcher's own source, in both directions, so a command routed but not
// listed (invisible to `catctl commands`, and missing from every generated
// client) fails the build the same way a listed-but-unrouted one does.
var commandSpecs = []CommandSpec{
	// Panes. Only split/read/capture/wait return data — the rest are effects,
	// whose outcome a client sees in the layout/frame stream rather than in a
	// reply. pane.split is not reply-gated: like tab.create the split is worth
	// performing for a caller that never listens (the browser's own split button
	// sends no id), and its result is a handle, not the point of the call.
	{Name: CmdPaneSplit, Params: SplitParams{}, Result: SplitResult{}, ParamsRequired: true},
	{Name: CmdPaneClose, Params: OptPaneParams{}},
	{Name: CmdPaneFocus, Params: PaneParams{}, ParamsRequired: true},
	{Name: CmdPaneFocusDirection, Params: DirParams{}, ParamsRequired: true},
	{Name: CmdPaneCycle, Params: CycleParams{}, ParamsRequired: true},
	{Name: CmdPaneLast},
	{Name: CmdPaneSwap, Params: DirParams{}, ParamsRequired: true},
	{Name: CmdPaneSwapWith, Params: SwapWithParams{}, ParamsRequired: true},
	{Name: CmdPaneZoom, Params: OptPaneParams{}},
	{Name: CmdPaneRename, Params: RenamePaneParams{}, ParamsRequired: true},
	{Name: CmdPaneResizeBorder, Params: ResizeBorderParams{}, ParamsRequired: true},
	{Name: CmdScroll, Params: ScrollParams{}, ParamsRequired: true},
	{Name: CmdRead, Params: ReadParams{}, Result: ReadResult{}, ReplyRequired: true, ParamsRequired: true},
	{Name: CmdCapture, Params: CaptureParams{}, Result: CaptureResult{}, ReplyRequired: true, ParamsRequired: true},
	{Name: CmdWaitForOutput, Params: WaitForOutputParams{}, Result: WaitForOutputResult{}, ReplyRequired: true, ParamsRequired: true},
	{Name: CmdPaneSendInput, Params: SendInputParams{}, ParamsRequired: true},

	// Tabs. tab.create returns its new tab/pane so an automation client can
	// drive the fresh pane without diffing pane.list.
	{Name: CmdTabCreate, Params: TabCreateParams{}, Result: TabCreateResult{}},
	{Name: CmdTabClose, Params: OptTabParams{}},
	{Name: CmdTabFocus, Params: TabParams{}, ParamsRequired: true},
	{Name: CmdTabRename, Params: RenameTabParams{}, ParamsRequired: true},
	{Name: CmdTabMove, Params: MoveTabParams{}, ParamsRequired: true},

	// Workspaces.
	{Name: CmdWorkspaceCreate, Params: WorkspaceCreateParams{}, Result: WorkspaceCreateResult{}},
	{Name: CmdWorkspaceClose, Params: WorkspaceParams{}},
	{Name: CmdWorkspaceFocus, Params: WorkspaceParams{}, ParamsRequired: true},
	{Name: CmdWorkspaceRename, Params: RenameWorkspaceParams{}, ParamsRequired: true},
	{Name: CmdWorkspaceMove, Params: MoveWorkspaceParams{}, ParamsRequired: true},
	{Name: CmdWorkspaceLock, Params: LockWorkspaceParams{}, ParamsRequired: true},

	// Global focus + server lifecycle.
	{Name: CmdAgentFocus, Params: PaneParams{}, ParamsRequired: true},
	{Name: CmdServerReloadConfig},
	{Name: CmdServerStop},

	// Usage. Not reply-gated: the refresh is worth performing for a caller that
	// never listens, because its product is the broadcast, not the reply.
	{Name: CmdUsageRefresh},

	// Chat. None reply-gated for the same reason as usage.refresh: the effects
	// land as chat_* broadcasts on every client.
	{Name: CmdChatSend, Params: ChatSendParams{}, ParamsRequired: true},
	{Name: CmdChatCancel},
	{Name: CmdChatPermission, Params: ChatPermissionParams{}, ParamsRequired: true},
	{Name: CmdChatClear},

	// Git worktrees. Only the listing is reply-gated: the other three have
	// effects worth performing even when the caller stops listening.
	{Name: CmdWorktreeList, Params: WorktreeListParams{}, Result: WorktreeListResult{}, ReplyRequired: true},
	{Name: CmdWorktreeCreate, Params: WorktreeCreateParams{}, Result: WorktreeCreateResult{}},
	{Name: CmdWorktreeOpen, Params: WorktreeOpenParams{}, Result: WorktreeOpenResult{}},
	{Name: CmdWorktreeRemove, Params: WorktreeRemoveParams{}},

	// Config + themes. Every writer echoes the same ConfigGetResult snapshot a
	// read would return, so a client refreshes from the reply it already has.
	{Name: CmdConfigGet, Result: ConfigGetResult{}, ReplyRequired: true},
	{Name: CmdConfigSet, Params: ConfigSetParams{}, Result: ConfigGetResult{}},
	{Name: CmdThemeList, Result: ThemeListResult{}, ReplyRequired: true},
	{Name: CmdThemeSave, Params: ThemeSaveParams{}, Result: ConfigGetResult{}, ParamsRequired: true},
	{Name: CmdThemeDelete, Params: ThemeDeleteParams{}, Result: ConfigGetResult{}, ParamsRequired: true},

	// Plugins.
	{Name: CmdPluginList, Result: PluginListResult{}, ReplyRequired: true},
	{Name: CmdPluginUninstall, Params: PluginUninstallParams{}, Result: PluginUninstallResult{}, ParamsRequired: true},

	// Path listing.
	{Name: CmdPathList, Params: PathListParams{}, Result: PathListResult{}, ReplyRequired: true},

	// Notifications. ui.notify returns the id its actions are answered by, but
	// is not reply-gated: a hook script that fires and forgets still wants the
	// toast, and the id is only interesting to a caller that declared actions
	// and is still around to watch for them.
	{Name: CmdUINotify, Params: UINotifyParams{}, Result: UINotifyResult{}, ParamsRequired: true},
	{Name: CmdUIAction, Params: UIActionParams{}, ParamsRequired: true},

	// pane.open_file returns which editor it reached (and whether it had to
	// start one), but is not reply-gated: "open this file" is worth doing for a
	// caller that never listens, exactly like a split.
	{Name: CmdPaneOpenFile, Params: OpenFileParams{}, Result: OpenFileResult{}, ParamsRequired: true},

	// Hosts. Both writers echo the new roster, so a client repaints from the
	// reply instead of waiting for the hosts push that also follows.
	{Name: CmdHostAttach, Params: HostAttachParams{}, Result: HostListResult{}, ParamsRequired: true},
	{Name: CmdHostDetach, Params: HostDetachParams{}, Result: HostListResult{}, ParamsRequired: true},

	// Read-only queries. They answer straight from the Session, so they are not
	// reply-gated — a query with no reply channel is a cheap no-op rather than a
	// leaked round-trip.
	{Name: CmdSessionGet, Result: SessionInfoResult{}},
	{Name: CmdWorkspaceList, Result: WorkspaceListResult{}},
	{Name: CmdTabList, Params: TabListParams{}, Result: TabListResult{}},
	{Name: CmdPaneList, Result: PaneListResult{}},
	{Name: CmdPaneGet, Params: OptPaneParams{}, Result: PaneInfo{}},
	// host.list is the one query the session cannot answer — the roster is the
	// backend's — but it is a query all the same: no effects, no round trip.
	{Name: CmdHostList, Result: HostListResult{}},
}

// CommandSpecs returns the §7 command table, in a stable order. The returned
// slice is a copy — a caller that sorts or filters it in place would otherwise
// be rewriting the vocabulary for everyone else in the process. A shallow clone
// suffices: each spec's Params/Result is a struct value, so the only way to
// reach it is a type assertion, which hands back another copy.
func CommandSpecs() []CommandSpec {
	return slices.Clone(commandSpecs)
}

// CommandNames returns every §7 command name Dispatcher.Dispatch accepts, in a
// stable order. Front-ends enumerate/validate the vocabulary against it — a CLI's
// help text, a control-API client — without re-listing the commands. Derived
// from commandSpecs, so the names and their shapes cannot disagree.
func CommandNames() []string {
	names := make([]string, len(commandSpecs))
	for i, spec := range commandSpecs {
		names[i] = spec.Name
	}
	return names
}

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

// BorderPath decodes a border id ("r" + one '0'/'1' per split step, e.g. "r01",
// produced by browserproto.BorderID) back into a split path for
// layout.TileLayout.SetRatioAt. Reports false for malformed ids. The "r01"
// format is a contract shared with browserproto's BuildLayout emitter.
func BorderPath(id string) ([]bool, bool) {
	if len(id) == 0 || id[0] != 'r' {
		return nil, false
	}
	path := make([]bool, 0, len(id)-1)
	for _, c := range id[1:] {
		switch c {
		case '0':
			path = append(path, false)
		case '1':
			path = append(path, true)
		default:
			return nil, false
		}
	}
	return path, true
}

// SplitParams: pane.split. Pane nil = the focused pane. With no spawn fields the
// new pane runs a shell in the split pane's live working directory
// (Dispatcher.inheritedSplitCwd).
//
// Cwd/Command/Env mirror TabCreateParams field for field, and for the same
// reason: a client that wants a program in the new pane says so in the one round
// trip that creates it, instead of splitting and then typing a command line at
// whatever shell happened to start there. That difference is not cosmetic —
// typing into a shell means quoting, a bracketed-paste assumption, and a race
// against the shell's own startup, while an argv is exec'd as the pane's process,
// so its exit closes the pane and no prompt noise precedes it.
//
//   - Cwd overrides the inherited spawn directory.
//   - Command is an argv to exec as the pane's process instead of a shell.
//   - Env adds environment variables to the spawned process.
//
// Cwd/Env without Command still apply to the default shell spawn.
// Host puts the new pane on a named cathost (host.list names them) instead of
// where it would otherwise go, which for a split is the machine of the pane
// being split — not the workspace's default. "Beside this pane" is what a split
// means, and a guest pane's split belongs next to it; the workspace's host is a
// policy for new *tabs* and workspaces, which have no neighbouring pane to
// answer the question. Host is the one field that decides which *machine* the
// spawn lands on, so everything else here — Cwd especially — is interpreted on
// that machine: a cwd from the pane being split means nothing on another host,
// which is why the inherited cwd is dropped when the split crosses hosts
// (Dispatcher.inheritedSplitCwd).
type SplitParams struct {
	Pane      *uint32           `json:"pane,omitempty"`
	Direction string            `json:"direction"` // SplitH | SplitV
	Cwd       string            `json:"cwd,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Host      string            `json:"host,omitempty"`
}

// Validate rejects a present-but-unusable Command, the same rule tab.create
// applies (see validateSpawnCommand).
func (p SplitParams) Validate() error { return validateSpawnCommand(p.Command) }

// spawnOverride extracts the runtime-relevant fields; the zero flag tells the
// dispatcher whether staging is needed at all.
func (p SplitParams) spawnOverride() (SpawnOverride, bool) {
	return newSpawnOverride(p.Cwd, p.Command, p.Env)
}

// SplitResult is CmdResult.Data for pane.split: the id of the pane the split
// created. SplitPane focuses the new pane, so a client can chain straight into
// pane.send_input / wait_for_output on it.
//
// It exists because the alternative is diffing pane.list before and after, which
// is racy by construction — the dispatcher has the id in hand and any other
// client's split landing in the same window makes the diff ambiguous, so a
// caller that guessed wrong would type into someone else's pane.
//
// Unlike TabCreateResult there is no tab number: a split happens inside the tab
// the caller is already in, so naming it would report something the caller told
// us.
type SplitResult struct {
	Pane uint32 `json:"pane"`
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

// SwapWithParams: pane.swap_with — exchange two panes' layout slots (the
// drag-reorder drop; pane.swap is the directional keyboard variant).
type SwapWithParams struct {
	Pane   uint32 `json:"pane"`
	Target uint32 `json:"target"`
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

// CaptureParams: capture — extract a pane's buffer text (β RequestText). Scope
// 0 = visible (the on-screen viewport), 1 = recent (the last Lines rows of
// scrollback+active, 0 = the whole buffer). Ansi keeps VT styling; Unwrap rejoins
// soft-wrapped lines. Unlike read, this needs no coordinates — it captures whole
// rows, e.g. for "copy scrollback" or feeding an agent the terminal contents.
type CaptureParams struct {
	Pane   uint32 `json:"pane"`
	Scope  uint8  `json:"scope,omitempty"`
	Lines  uint32 `json:"lines,omitempty"`
	Ansi   bool   `json:"ansi,omitempty"`
	Unwrap bool   `json:"unwrap,omitempty"`
}

// CaptureResult is CmdResult.Data for a successful capture.
type CaptureResult struct {
	Text string `json:"text"`
}

// WaitForOutputParams: pane.wait_for_output — block until the pane's output
// matches Pattern (a substring, or a regexp when Regex is set), or until TimeoutMs
// elapses. Unlike read/capture (one round-trip), this rides the unary envelope but
// resolves only when the match appears: the backend matches Pattern against the
// pane's live output as it streams (VT escapes stripped to plain text), so it
// never misses fast-scrolling transient output or the child's final pre-exit
// output. It is additionally seeded once with the output already on screen when
// the wait begins. Lines bounds only that initial-screen seed (recent rows,
// 0 = the whole buffer); the live stream is matched in full. TimeoutMs 0 uses the
// server default.
type WaitForOutputParams struct {
	Pane      uint32 `json:"pane"`
	Pattern   string `json:"pattern"`
	Regex     bool   `json:"regex,omitempty"`
	TimeoutMs uint32 `json:"timeout_ms,omitempty"`
	Lines     uint32 `json:"lines,omitempty"`
}

// WaitForOutputResult is CmdResult.Data for pane.wait_for_output. Matched reports
// whether Pattern appeared before the timeout (false = timed out or the pane
// exited first); Text is the buffer line the match landed on, for context.
type WaitForOutputResult struct {
	Matched bool   `json:"matched"`
	Text    string `json:"text,omitempty"`
}

// Matcher compiles Pattern into a predicate over a pane's captured text: it
// returns the matched line (trimmed, for the result's Text) and whether the
// pattern is present. It also validates the params — an empty pattern or an
// uncompilable regex is an error the dispatcher reports as bad params before it
// registers a waiter. The backend calls Matcher to get the live predicate.
func (p WaitForOutputParams) Matcher() (func(text string) (line string, ok bool), error) {
	if p.Pattern == "" {
		return nil, errors.New("wait_for_output: empty pattern")
	}
	if p.Regex {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return nil, fmt.Errorf("wait_for_output: bad regex %q: %w", p.Pattern, err)
		}
		return func(text string) (string, bool) {
			loc := re.FindStringIndex(text)
			if loc == nil {
				return "", false
			}
			return lineAround(text, loc[0]), true
		}, nil
	}
	return func(text string) (string, bool) {
		idx := strings.Index(text, p.Pattern)
		if idx < 0 {
			return "", false
		}
		return lineAround(text, idx), true
	}, nil
}

// lineAround returns the trimmed line of text containing byte index idx (the
// wait_for_output result's context line). idx is assumed in range.
func lineAround(text string, idx int) string {
	start := strings.LastIndexByte(text[:idx], '\n') + 1 // 0 if none
	end := idx + strings.IndexByte(text[idx:], '\n')
	if end < idx { // no trailing newline: run to the end
		end = len(text)
	}
	return strings.TrimSpace(text[start:end])
}

// Wait-timeout bounds shared by the backend waiter and any client sizing its own
// round-trip deadline, so both agree on how long a wait can run.
const (
	defaultWaitTimeout = 30 * time.Second
	// MaxWaitTimeout caps a single wait; the ctlproto server sizes its backstop
	// above this so a waiter always resolves on its own timer first.
	MaxWaitTimeout = 10 * time.Minute
)

// WaitTimeout resolves a wait's TimeoutMs into a duration: 0 ⇒ the default, and
// anything above MaxWaitTimeout is clamped.
func WaitTimeout(ms uint32) time.Duration {
	if ms == 0 {
		return defaultWaitTimeout
	}
	if d := time.Duration(ms) * time.Millisecond; d < MaxWaitTimeout {
		return d
	}
	return MaxWaitTimeout
}

// SendInputParams: pane.send_input — inject text into a pane's PTY as though
// typed locally, the outbound half of the automation API (capture/wait are the
// inbound half; together a client can drive an interactive program end-to-end).
// Text is paste-encoded against the pane's live mode state, so when the
// foreground app has bracketed paste on (readline, most TUIs) a multi-line
// prompt lands in its input intact instead of executing line-by-line. Submit
// follows the text with a real Enter keypress — separate from Text so a caller
// can stage input for the user to review (Submit false) or fire it (true), and
// an empty-Text Submit sends just the Enter. Encoding stays server-side, same
// as browser input: clients never pre-encode VT bytes.
type SendInputParams struct {
	Pane   uint32 `json:"pane"`
	Text   string `json:"text,omitempty"`
	Submit bool   `json:"submit,omitempty"`
}

// Validate rejects a send with nothing to deliver — no text and no Enter — so
// the dispatcher reports a bad call instead of silently acking a no-op.
func (p SendInputParams) Validate() error {
	if p.Text == "" && !p.Submit {
		return errors.New("send_input: empty text and no submit — nothing to send")
	}
	return nil
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

// MoveTabParams: tab.move — reorder the active workspace's tabs. Index is an
// insertion point (a gap position 0..=len; len means "to the end"), the same
// convention the drag-reorder UI computes from a drop location.
type MoveTabParams struct {
	Num   int `json:"num"`
	Index int `json:"index"`
}

// MoveWorkspaceParams: workspace.move — reorder the workspace list. Index is an
// insertion point (a gap position 0..=len).
type MoveWorkspaceParams struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
}

// WorkspaceParams: workspace.focus, workspace.close.
type WorkspaceParams struct {
	ID string `json:"id"` // public workspace id, e.g. "w1"
}

// WorkspaceCreateParams: workspace.create. Every field is optional — the whole
// params object may be absent, which is what a key binding or `catctl new-ws`
// sends. Name pins the new workspace's label (what workspace.rename would set);
// leaving it empty keeps auto-naming, where the label follows the workspace's
// identity cwd.
//
// Path is the directory the workspace's first pane starts in, and it is a
// pointer because its three states differ: absent (nil) inherits the session's
// default directory — the historical behaviour, and what `catctl new-ws` sends;
// present but empty starts at the user's home directory — an explicit "not
// here" from the new-workspace dialog; and a non-empty value is that directory,
// with "~"/"$VAR"/relative forms expanded and its existence verified (a bad
// path fails the command rather than silently landing somewhere else).
//
// Mkdir turns that last failure into consent: a Path that does not exist is
// created (parents included) instead of failing the command. It is a separate
// flag rather than the default because a typo must stay an error on the first
// attempt — the dialog offers creation only after the user confirms the missing
// path is intentional, then retries with Mkdir set.
//
// Host pins the workspace to a cathost: it becomes the workspace's default for
// every pane created in it, so "a workspace on devbox" is one field rather than
// a host choice repeated at each split. It also changes how Path is read —
// on a non-local host the path is passed through verbatim, because the
// directory being named lives on *that* machine and this process cannot expand
// "~", stat it, or create it (see workspaceStartDir). Mkdir is therefore
// ignored for a remote workspace: the cwd fallback that keeps a bad path from
// producing a dead pane belongs to cathost.
type WorkspaceCreateParams struct {
	Name  string  `json:"name,omitempty"`
	Path  *string `json:"path,omitempty"`
	Mkdir bool    `json:"mkdir,omitempty"`
	Host  string  `json:"host,omitempty"`
}

// WorkspaceCreateResult is CmdResult.Data for workspace.create: the new
// workspace's public id. Returned for the same reason tab.create returns its
// tab number — a scripted caller can address the workspace it just made without
// diffing workspace.list. The browser UI ignores it (the layout broadcast that
// follows already carries the new workspace).
type WorkspaceCreateResult struct {
	ID string `json:"id"`
}

// RenameWorkspaceParams: workspace.rename ("" reverts to auto-naming).
type RenameWorkspaceParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LockWorkspaceParams: workspace.lock — set (or clear, with Locked false) a
// workspace's automation lock. ID "" means the active workspace, the same
// default workspace.close takes, so a key binding or `catctl lock-ws` can send
// no id at all.
//
// A locked workspace refuses tab.create with a command and pane.send_input
// (see Session.SetWorkspaceLock). The lock is a guardrail, not a permission
// boundary: workspace.lock is an ordinary command, so anything holding the
// control API can lift it — what it stops is a plugin action or an agent launch
// landing somewhere by accident.
type LockWorkspaceParams struct {
	ID     string `json:"id,omitempty"`
	Locked bool   `json:"locked"`
}

// --- Query params & results (§7 read-only commands) --------------------------

// TabListParams: tab.list. Workspace "" = the active workspace.
type TabListParams struct {
	Workspace string `json:"workspace,omitempty"`
}

// SessionInfoResult is CmdResult.Data for session.get: a one-shot snapshot of the
// whole session. FocusedPane is the public handle of the globally focused pane
// (the active workspace's active tab's focused pane), empty if there is none.
type SessionInfoResult struct {
	ActiveWorkspace string `json:"active_workspace"`
	FocusedPane     string `json:"focused_pane,omitempty"`
	Workspaces      int    `json:"workspaces"`
	Panes           int    `json:"panes"` // total live panes across all workspaces/tabs
	Cwd             string `json:"cwd"`
}

// WorkspaceInfo describes one workspace for workspace.list.
type WorkspaceInfo struct {
	ID     string `json:"id"`   // stable public handle, e.g. "w1"
	Name   string `json:"name"` // display name (custom or auto)
	Active bool   `json:"active"`
	Tabs   int    `json:"tabs"`             // tab count
	Locked bool   `json:"locked,omitempty"` // closed to automation (workspace.lock)
	// Host is the cathost new panes in this workspace land on, as the MODEL
	// records it: empty means "whatever the default host is", which is what a
	// workspace created before hosts existed (or on the default) stores. It is a
	// policy, not a location — where a pane actually runs is PaneMeta.Host, which
	// the backend resolves, because a pane's process is somewhere whether or not
	// the workspace ever named a machine.
	Host string `json:"host,omitempty"`
}

// WorkspaceListResult is CmdResult.Data for workspace.list.
type WorkspaceListResult struct {
	Workspaces []WorkspaceInfo `json:"workspaces"`
}

// TabInfo describes one tab for tab.list.
type TabInfo struct {
	Num    int    `json:"num"`  // stable public tab number
	Name   string `json:"name"` // display name (custom or the number)
	Active bool   `json:"active"`
	Zoomed bool   `json:"zoomed"`
	Panes  int    `json:"panes"` // pane count in this tab
}

// TabListResult is CmdResult.Data for tab.list. Workspace echoes the resolved
// workspace id (useful when the request omitted it and got the active one).
type TabListResult struct {
	Workspace string    `json:"workspace"`
	Tabs      []TabInfo `json:"tabs"`
}

// PaneInfo describes one pane for pane.list / pane.get. Pane is the internal id
// used to address the pane in every other command; Handle is its human public
// label ("w1:p3"). Focused marks the pane focused within its own tab (each tab
// has one); Visible marks the panes in the current viewport.
//
// The session knows only the first five fields; the trailing PaneMeta block is
// runtime-side state (detected agent, live title/cwd) filled in by the
// dispatcher from Backend.PaneMeta, so automation clients (e.g. cats-todo's
// drop-target picker) can find agent panes and show where they live without a
// second protocol.
type PaneInfo struct {
	Pane    uint32 `json:"pane"`
	Handle  string `json:"handle,omitempty"`
	Name    string `json:"name,omitempty"` // custom name; empty if auto-named
	Focused bool   `json:"focused"`
	Visible bool   `json:"visible"`
	PaneMeta
}

// PaneMeta is the runtime-side metadata for one pane, supplied by the Backend
// (the session cannot know it): the arbitrated agent identity the sidebar shows
// (hook authority first, daemon detection second), the model that agent is
// running under, the pane's live title, and its current working directory. All
// fields may be empty — an unknown pane, no agent detected, or a
// title/cwd/model not yet reported.
type PaneMeta struct {
	Agent      string `json:"agent,omitempty"`       // detected agent label ("claude", "codex", …)
	AgentState string `json:"agent_state,omitempty"` // agent activity state ("working", "idle", …); only when Agent is set
	AgentModel string `json:"agent_model,omitempty"` // LLM the agent is running under; only some agents resolve one
	Title      string `json:"title,omitempty"`       // live terminal title
	Cwd        string `json:"cwd,omitempty"`         // live working directory
	// Host is the cathost the pane's terminal lives on, resolved against the
	// live roster — so it always names a host that exists, including for a pane
	// restored onto one that has since gone away. It sits here rather than
	// beside the session's own fields because the resolution is the runtime's:
	// the model stores an id that may be empty or stale, the backend knows which
	// machine is actually holding the PTY.
	Host string `json:"host,omitempty"`
}

// TabCreateParams is the optional params block for tab.create. The zero value
// (or no params at all — the historical wire shape) is a default-shell tab that
// inherits its left-hand neighbor tab's live cwd (the dispatcher's
// inheritedTabCwd), falling back to the workspace's identity cwd. The optional
// fields let an automation client (catctl plugin run, scripts) open a
// fully-formed tab in one round trip instead of tab.create → tab.rename →
// typing a command into the shell:
//
//   - Title pins the tab's display name (what tab.rename would set).
//   - Cwd overrides the spawn directory for the tab's root pane, beating the
//     inherited one.
//   - Command is an argv to exec as the pane's process instead of a shell —
//     the pane runs the program directly, so its exit closes the pane and no
//     shell history/prompt noise precedes it (same mechanism as agent resume).
//   - Env adds environment variables to the spawned process.
//   - Workspace puts the tab in a workspace other than the active one.
//
// Cwd/Env without Command still apply to the default shell spawn.
//
// Workspace ("w2") exists so a caller can open a tab where it is not looking.
// The browser's "start in all workspaces" plugin launch sends one tab.create
// per workspace with this field set; without it the only way there is to focus
// each workspace in turn, which moves the viewport as a side effect of an
// operation that is not about the viewport at all. Empty — the ordinary case —
// means the active workspace, and the viewport does not move either way: the
// new tab becomes its *own* workspace's active tab, nothing more.
// Host puts the tab's root pane on a named cathost instead of the target
// workspace's default (see SplitParams.Host — same field, same meaning, and the
// same reason the neighbor tab's cwd is not inherited across a host boundary).
type TabCreateParams struct {
	Title     string            `json:"title,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Workspace string            `json:"workspace,omitempty"`
	Host      string            `json:"host,omitempty"`
}

// Validate rejects a present-but-unusable Command (an empty argv slot cannot be
// exec'd; an absent Command is the normal shell case and always fine).
func (p TabCreateParams) Validate() error { return validateSpawnCommand(p.Command) }

// validateSpawnCommand is the Command rule tab.create and pane.split share. It
// is one function rather than two identical bodies because the two commands take
// the same argv to the same StageSpawn — a divergence here would mean one of them
// accepting a spawn the other rejects, which a caller would read as a bug in
// whichever one it hit second.
func validateSpawnCommand(command []string) error {
	if len(command) > 0 && strings.TrimSpace(command[0]) == "" {
		return errors.New("command[0] must be a program name")
	}
	return nil
}

// newSpawnOverride packs the spawn-shaping trio into the Backend's override,
// reporting false when all three are absent (nothing to stage). Shared by
// tab.create and pane.split for the same reason as validateSpawnCommand.
func newSpawnOverride(cwd string, command []string, env map[string]string) (SpawnOverride, bool) {
	if cwd == "" && len(command) == 0 && len(env) == 0 {
		return SpawnOverride{}, false
	}
	return SpawnOverride{Cwd: cwd, Command: command, Env: env}, true
}

// ChatSendParams is one user turn for the chat panel. Text is required — a
// blank prompt has no meaning to send to an agent.
type ChatSendParams struct {
	Text string `json:"text"`
}

// Validate rejects a whitespace-only prompt.
func (p ChatSendParams) Validate() error {
	if strings.TrimSpace(p.Text) == "" {
		return errors.New("text is required")
	}
	return nil
}

// ChatPermissionParams answers a pending permission prompt. Exactly one of
// OptionID (the chosen option) or Cancel should be set; ReqID names the
// prompt, because several can be open at once.
type ChatPermissionParams struct {
	ReqID    string `json:"req_id"`
	OptionID string `json:"option_id,omitempty"`
	Cancel   bool   `json:"cancel,omitempty"`
}

// Validate requires the prompt id and an answer of some kind.
func (p ChatPermissionParams) Validate() error {
	if p.ReqID == "" {
		return errors.New("req_id is required")
	}
	if p.OptionID == "" && !p.Cancel {
		return errors.New("option_id or cancel is required")
	}
	return nil
}

// SpawnOverride is the runtime-side slice of TabCreateParams: what the Backend
// must apply when it realizes the new pane's PTY (the dispatcher handles Title
// itself via the session). Staged per pane before ApplyModel and consumed
// exactly once by the pane's create.
type SpawnOverride struct {
	Cwd     string
	Command []string
	Env     map[string]string
}

// spawnOverride extracts the runtime-relevant fields; the zero flag tells the
// dispatcher whether staging is needed at all.
func (p TabCreateParams) spawnOverride() (SpawnOverride, bool) {
	return newSpawnOverride(p.Cwd, p.Command, p.Env)
}

// TabCreateResult is CmdResult.Data for tab.create: the new tab's public number
// and its root pane's id. CreateTab focuses the new tab (and its sole pane), so
// an automation client can chain straight into pane.send_input / wait_for_output
// on Pane without a follow-up query — the reason this command returns data at
// all (the browser UI ignores it).
type TabCreateResult struct {
	Num  int    `json:"num"`
	Pane uint32 `json:"pane"`
}

// PaneListResult is CmdResult.Data for pane.list.
type PaneListResult struct {
	Panes []PaneInfo `json:"panes"`
}

// HostInfo describes one cathost for host.list — the machines the session's
// panes are spread over. A single-host session answers with one entry, which is
// how a client tells "hosts aren't configured here" from "the remote one is
// down" without a capability flag.
//
// AddrKind names the transport ("unix", "tcp", "tls") rather than the address:
// the address is a socket path or a hostname, neither of which a listing needs
// and both of which are worth not printing by reflex. Error carries the last
// dial/session failure for a host that is not connected, so `catctl hosts` can
// say why rather than just "false".
type HostInfo struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Connected bool   `json:"connected"`
	AddrKind  string `json:"addr_kind,omitempty"`
	// Default is "is_default" on the wire, not "default": generated clients bind
	// each key to an identifier, and `default` is a reserved word in Dart (see
	// cmd/catgen-dart, which refuses it rather than silently mangling the name).
	Default bool `json:"is_default,omitempty"`
	// Local marks the host that is this catway's own machine — the synthesized
	// "local" host reached over server.cathost_socket. It is not derivable from
	// AddrKind: a unix address can be an ssh -L forward to another box, which is
	// exactly how the first real remote host is reached. Clients need it because
	// every path-shaped feature is local-only until cathost grows its own
	// filesystem commands: the directory picker, worktrees, and the branch/agent
	// readers all describe this machine's disk, so they are offered for a local
	// host and withheld for a remote one rather than silently answering about
	// the wrong filesystem.
	Local bool   `json:"local,omitempty"`
	Panes int    `json:"panes"`
	Error string `json:"error,omitempty"`
	// LatencyMs is the last measured round trip to this cathost, in
	// milliseconds; omitted when unknown — never measured, not connected, or a
	// daemon too old to answer a ping (see orchestration.FeaturePing).
	//
	// Fractional on purpose. The local host is the common case and a unix-socket
	// round trip is a fraction of a millisecond, so whole milliseconds would
	// report every healthy session as "0" — a number that reads as broken rather
	// than as instant.
	LatencyMs float64 `json:"latency_ms,omitempty"`
	// ListsDirs reports that path.list can complete a path on this host: always
	// true for the local machine, and true for a remote one whose cathost speaks
	// the list_dir capability.
	//
	// It is a separate flag from Local because the two used to be the same
	// answer and are not any more. A client gates its start-path picker on this:
	// with it, the picker works against the remote filesystem; without it, the
	// field takes a typed path that this session cannot verify, and saying so
	// beats offering a picker full of the wrong machine's directories.
	ListsDirs bool `json:"lists_dirs,omitempty"`
}

// HostListResult is CmdResult.Data for host.list.
type HostListResult struct {
	Hosts []HostInfo `json:"hosts"`
}

// HostAttachParams: host.attach — one cathost, in the same shape config.yaml's
// hosts: entries use (config.Host), because the command's other half is writing
// exactly that entry to the file. A client that can describe a host in the
// config can describe one here with the same keys.
//
// Addr is scheme://target — unix://path, tcp://host:port (loopback only), or
// tls://host:port. Token/TokenFile authenticate to a cathost started with
// -token-file, and Fingerprint pins its self-signed certificate; TokenFile is
// the better of the pair, since a literal token is written into the config file
// verbatim.
type HostAttachParams struct {
	ID          string `json:"id"`
	Label       string `json:"label,omitempty"`
	Addr        string `json:"addr"`
	Token       string `json:"token,omitempty"`
	TokenFile   string `json:"token_file,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// Default makes the new host the one unqualified panes land on. It is
	// "is_default" on the wire for the same reason HostInfo.Default is: `default`
	// is a reserved word in the generated Dart client.
	Default bool `json:"is_default,omitempty"`
}

// HostDetachParams: host.detach — drop a cathost from the running session and
// from the config's hosts: block.
//
// A host holding panes is refused unless Force, because detaching it abandons
// those terminals: the command cannot move a running process between machines,
// so Force re-homes the panes onto the default host, where they respawn as
// fresh shells. The refusal is the default so that "detach" never silently
// costs somebody a working session.
type HostDetachParams struct {
	ID    string `json:"id"`
	Force bool   `json:"force,omitempty"`
}

// --- Worktree params & results (§7, WS8 dialogs) ------------------------------

// WorktreeListParams: worktree.list. Pane nil = the focused pane; the repo is
// resolved from that pane's live cwd (Backend supplies pane cwds).
type WorktreeListParams struct {
	Pane *uint32 `json:"pane,omitempty"`
}

// WorktreeInfo describes one existing checkout for worktree.list. Current marks
// the checkout the anchoring pane is in; OpenWorkspace is the public id of a
// workspace already open on the checkout ("" when none).
type WorktreeInfo struct {
	Path          string `json:"path"`
	Branch        string `json:"branch,omitempty"`
	Detached      bool   `json:"detached,omitempty"`
	Prunable      bool   `json:"prunable,omitempty"`
	Current       bool   `json:"current,omitempty"`
	OpenWorkspace string `json:"open_workspace,omitempty"`
}

// WorktreeListResult is CmdResult.Data for worktree.list. WorktreeRoot is the
// directory new checkouts land under, tilde-expanded by the machine that will
// hold them, so the new-worktree dialog can preview the derived checkout path
// client-side.
//
// Host is the machine the whole answer describes — every path in it is a path
// on that filesystem. The dialogs name it when the session has more than one
// host, because "new worktree — cats" is otherwise the same sentence whichever
// machine's repository is about to grow a checkout.
type WorktreeListResult struct {
	RepoRoot     string         `json:"repo_root"`
	RepoName     string         `json:"repo_name"`
	WorktreeRoot string         `json:"worktree_root"`
	Host         string         `json:"host,omitempty"`
	Worktrees    []WorktreeInfo `json:"worktrees"`
}

// WorktreeCreateParams: worktree.create. Branch "" generates a slug; Path ""
// derives the default checkout path under the configured worktree root.
type WorktreeCreateParams struct {
	Pane   *uint32 `json:"pane,omitempty"`
	Branch string  `json:"branch,omitempty"`
	Path   string  `json:"path,omitempty"`
}

// WorktreeCreateResult is CmdResult.Data for worktree.create: the new
// workspace's public id and the resolved branch/checkout.
type WorktreeCreateResult struct {
	Workspace string `json:"workspace"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
}

// WorktreeOpenParams: worktree.open — focus the workspace already open on Path,
// or create a new one there.
type WorktreeOpenParams struct {
	Pane *uint32 `json:"pane,omitempty"`
	Path string  `json:"path"`
}

// WorktreeOpenResult is CmdResult.Data for worktree.open.
type WorktreeOpenResult struct {
	Workspace   string `json:"workspace"`
	AlreadyOpen bool   `json:"already_open,omitempty"`
}

// WorktreeRemoveParams: worktree.remove — delete the checkout behind a
// workspace (by public id) and close the workspace. Without Force, a dirty
// checkout fails with the "dirty_worktree_requires_force:" prefix so the
// front-end can escalate to the delete-anyway confirm. The branch is never
// deleted.
type WorktreeRemoveParams struct {
	Workspace string `json:"workspace"`
	Force     bool   `json:"force,omitempty"`
}

// --- Config params & results (§7, settings modal) -----------------------------

// ConfigTheme is the theme section on the wire. In config.get's result it is
// the EFFECTIVE appearance: Name is the active theme, Colors the fully
// resolved palette, Font the font actually in use (the user's per-key
// overrides ride separately in ConfigGetResult.ThemeOverrides). In config.set
// it is the user's new choices — see ConfigSetParams for the merge/replace
// semantics.
type ConfigTheme struct {
	Name   string            `json:"name,omitempty"`
	Colors map[string]string `json:"colors,omitempty"`
	Font   string            `json:"font,omitempty"`
}

// ThemeInfo is one available theme (config.get / theme.list). Colors is the
// NORMALIZED palette — every canonical key present — so a front-end can
// live-preview a theme switch without a round trip, and Font is resolved to a
// concrete stack the same way. Source is "builtin", "user", or "plugin:<id>";
// only "user" themes are deletable.
type ThemeInfo struct {
	Name   string            `json:"name"`
	Label  string            `json:"label"`
	Dark   bool              `json:"dark"`
	Source string            `json:"source"`
	Colors map[string]string `json:"colors"`
	Font   string            `json:"font"`
}

// ConfigServerInfo is the read-only server section of config.get: informational
// only (these settings are flag/config driven and need a restart).
type ConfigServerInfo struct {
	Addr          string `json:"addr"`
	Auth          string `json:"auth"`
	TLS           bool   `json:"tls"`
	CathostSocket string `json:"cathost_socket"`
	ControlSocket string `json:"control_socket,omitempty"`
	HookSocket    string `json:"hook_socket,omitempty"`
	SessionTTL    string `json:"session_ttl"`
	// Hosts is the live roster (the same listing host.list returns), so the
	// settings modal can show which machines this catway attached to without a
	// second call. Like the rest of this struct it is read-only: hosts: is a
	// restart-time setting, and config.set never writes it.
	Hosts []HostInfo `json:"hosts,omitempty"`
}

// ConfigGetResult is CmdResult.Data for config.get (and config.set /
// theme.save / theme.delete, which echo the saved state). CopyMode's keys are
// the full known action set — the settings modal derives its rows (and
// validation) from them. Theme is the effective appearance; ThemeOverrides are
// the user's raw per-key color overrides from the config file (what sits on
// top of the named theme); Themes is the full available-theme registry.
type ConfigGetResult struct {
	Path           string              `json:"path"`
	Theme          ConfigTheme         `json:"theme"`
	ThemeOverrides map[string]string   `json:"theme_overrides,omitempty"`
	Themes         []ThemeInfo         `json:"themes,omitempty"`
	CopyMode       map[string][]string `json:"copy_mode"`
	Server         ConfigServerInfo    `json:"server"`
}

// ConfigSetParams: config.set — only the live-appliable sections. Absent fields
// keep their current values; copy-mode actions merge key-wise. The theme
// section has two modes, keyed on Name: with Name set ("switch to this theme"),
// Colors and Font REPLACE the stored overrides wholesale — an empty Colors
// means "the theme, clean" — because stale overrides from the previous theme
// are exactly what a switch must shed. With Name absent, Colors merge key-wise
// and a non-empty Font replaces, preserving the pre-themes contract for
// callers that just poke individual colors.
type ConfigSetParams struct {
	Theme    *ConfigTheme        `json:"theme,omitempty"`
	CopyMode map[string][]string `json:"copy_mode,omitempty"`
}

// ThemeListResult is CmdResult.Data for theme.list.
type ThemeListResult struct {
	Active string      `json:"active"` // effective theme name
	Themes []ThemeInfo `json:"themes"`
}

// ThemeSaveParams: theme.save — write (or overwrite) a user theme file. Colors
// may be sparse; only the eight core keys are required (the rest derive — see
// internal/theme). Dark is optional and auto-detected from the bg color when
// absent. Activate additionally switches the config to the saved theme,
// clearing any color overrides (they are presumed baked into what was saved).
type ThemeSaveParams struct {
	Name     string            `json:"name"`
	Label    string            `json:"label,omitempty"`
	Dark     *bool             `json:"dark,omitempty"`
	Colors   map[string]string `json:"colors"`
	Font     string            `json:"font,omitempty"`
	Activate bool              `json:"activate,omitempty"`
}

// ThemeDeleteParams: theme.delete — remove a user theme file. Built-in and
// plugin themes are not deletable through this command. Deleting the active
// theme falls the config back to the default theme.
type ThemeDeleteParams struct {
	Name string `json:"name"`
}

// --- Plugin params & results (§7, plugins dialog) ------------------------------

// PluginActionInfo describes one launchable action for plugin.list. Argv is the
// fully resolved argv (plugin-root-relative "./bin/tool" paths anchored to the
// install dir server-side), so a front-end can hand it straight to tab.create's
// Command without knowing the manifest's path conventions.
type PluginActionInfo struct {
	ID    string   `json:"id"`
	Title string   `json:"title,omitempty"`
	Argv  []string `json:"argv"`
}

// PluginInfo describes one installed plugin for plugin.list. Broken carries the
// manifest load/validate error for an entry that exists on disk but cannot run
// (the manifest fields are then empty apart from ID). Env is the identity
// environment a launch must carry (CATS_PLUGIN_ID / CATS_PLUGIN_DIR) — included
// per plugin so the front-end composes tab.create params without hard-coding
// the env var names.
type PluginInfo struct {
	ID          string             `json:"id"`
	Name        string             `json:"name,omitempty"`
	Version     string             `json:"version,omitempty"`
	Description string             `json:"description,omitempty"`
	Linked      bool               `json:"linked,omitempty"`
	Dir         string             `json:"dir"`
	Source      string             `json:"source,omitempty"`
	Ref         string             `json:"ref,omitempty"`
	Broken      string             `json:"broken,omitempty"`
	Actions     []PluginActionInfo `json:"actions,omitempty"`
	Env         map[string]string  `json:"env,omitempty"`
}

// PluginListResult is CmdResult.Data for plugin.list. Catctl is the server's
// best resolution of the catctl binary (PATH first, then a sibling of the
// server executable) — the dialog spawns `catctl plugin install/update` tabs
// and the browser has no way to resolve host paths itself.
type PluginListResult struct {
	Catctl  string       `json:"catctl"`
	Plugins []PluginInfo `json:"plugins"`
}

// PluginUninstallParams: plugin.uninstall — remove an installed plugin's
// directory (or unlink a linked one; the checkout itself is never touched).
type PluginUninstallParams struct {
	ID string `json:"id"`
}

// PluginUninstallResult is CmdResult.Data for plugin.uninstall: the same
// human-readable outcome line the CLI prints.
type PluginUninstallResult struct {
	Message string `json:"message"`
}

// --- Path listing params & results (§7, start-path picker) --------------------

// PathListParams: path.list. Dir is what the user has typed so far — a "~/",
// "$VAR/", relative or absolute path — and is resolved leniently (nothing is
// stat'ed to decide the answer). "" means the anchor directory itself: the
// addressed pane's live cwd, or the focused pane's when Pane is nil, which is
// the same neighbour inheritance new tabs and splits use.
//
// Recents asks for the frecency list too. A picker wants it once when it opens,
// not on every keystroke of directory navigation, so it is opt-in per request.
//
// Host names the machine to list on, overriding the anchor pane's. It exists
// because the picker in the new-workspace dialog chooses a host BEFORE anything
// exists there: with only a pane to go by, a path being typed for devbox would
// be completed against the local disk and every suggestion would be a directory
// that does not exist where the workspace is about to be created. "" keeps the
// anchor pane's host, which is what every other caller wants.
//
// A path is only ever completed by the machine that owns it. When Host is a
// remote cathost the listing is taken there — "~" is that user's home, "." is a
// directory only that kernel can resolve — and a host whose cathost is too old
// to list answers with an Error rather than with this machine's directories.
type PathListParams struct {
	Dir     string  `json:"dir,omitempty"`
	Pane    *uint32 `json:"pane,omitempty"`
	Recents bool    `json:"recents,omitempty"`
	Host    string  `json:"host,omitempty"`
}

// PathListResult is CmdResult.Data for path.list.
//
// The listing is deliberately unfiltered and unranked: Dirs is every
// subdirectory of Dir, and the caller matches it against what the user is
// typing. That keeps completion inside a directory a local, per-keystroke
// operation for a browser that may be a long way from the server, and it costs
// one round trip per directory the user walks into.
//
// Exists false (with Error explaining why) is the normal state of a path
// mid-typing, not a command failure: Dirs is empty and the picker shows nothing
// until the path resolves again.
type PathListResult struct {
	Dir       string   `json:"dir"`  // the resolved absolute directory Dirs is a listing of
	Cwd       string   `json:"cwd"`  // the anchor a relative Dir was resolved against
	Home      string   `json:"home"` // so a front-end can shorten/expand "~" the same way
	Exists    bool     `json:"exists"`
	Error     string   `json:"error,omitempty"`
	Dirs      []string `json:"dirs,omitempty"` // subdirectory names, sorted, hidden included
	Truncated bool     `json:"truncated,omitempty"`
	Recents   []string `json:"recents,omitempty"` // absolute directories, best-first
}

// --- Notification params & results (§7, ui.notify / ui.action) ---------------

// NotifyAction is one button on a notification, and it is deliberately a
// DECLARED EFFECT rather than a callback.
//
// The caller this exists for is a hook script: it reports that its agent is
// blocked and exits, milliseconds before anybody sees the notification it
// caused. An action meaning "call me back" would therefore be dead on arrival
// in the case the feature is for — a phone, minutes later, with nothing left
// running to call. So an action says what to do and catway does it: Send is
// injected into Pane (falling back to the notification's own pane) exactly as
// pane.send_input would inject it.
//
// Send may be empty, and then the action is announcement-only: a live
// subscriber sees the ui_action event and acts on it itself. Both halves
// always happen in that order — perform, then announce — so a subscriber
// watching a prompt being answered from a phone sees the answer after the fact
// rather than racing it.
type NotifyAction struct {
	// ID is the caller's handle for this action, echoed in the ui_action event.
	// Generated from the index when empty, so a caller that only wants buttons
	// never has to invent names.
	ID    string `json:"id,omitempty"`
	Label string `json:"label"` // the button text; required
	// Send is the literal text injected into the pane when the action is taken.
	Send string `json:"send,omitempty"`
	// Submit appends the pane's Enter, exactly as pane.send_input's submit does.
	// Separate from Send because "1" and "1\n" are different answers to a
	// prompt that filters as you type.
	Submit bool `json:"submit,omitempty"`
	// Pane overrides the notification's pane for this action's Send. Nil is the
	// common case; it exists so one notification can offer "answer it" and
	// "look at the log over there".
	Pane *uint32 `json:"pane,omitempty"`
}

// UINotifyParams: ui.notify.
//
// Kind decides who hears about it, and the three values are the browser's
// existing notify kinds plus "info". "info" is new here and is deliberately
// NOT in the default push.kinds: a plugin that narrates its own progress must
// not be able to start vibrating a phone merely by existing, and an operator
// who wants that adds one word to the config.
//
// Pane attributes the notification to a pane — the deep link a tap follows,
// the client-side "is it already on screen" suppression, and the default
// target of an action's Send. Omitting it yields a session-level notification,
// which is right for "the nightly build finished" and wrong for anything a
// button could answer.
type UINotifyParams struct {
	Title   string         `json:"title"`
	Body    string         `json:"body,omitempty"`
	Kind    string         `json:"kind,omitempty"` // attention | finished | info (default info)
	Pane    *uint32        `json:"pane,omitempty"`
	Actions []NotifyAction `json:"actions,omitempty"`
}

// UINotifyResult is CmdResult.Data for ui.notify: the id ui.action answers by.
type UINotifyResult struct {
	ID string `json:"id"`
}

// UIActionParams: ui.action — take action Action on notification ID.
//
// A notification is answered ONCE. The registry drops it on the first
// successful action, so a prompt cannot be answered twice by a browser toast
// and a phone that both showed the same buttons, and a second tap is refused
// by name rather than silently re-sending "yes" into a shell that has since
// moved on.
type UIActionParams struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

// Notify kinds accepted by ui.notify. Duplicated as plain strings rather than
// imported from browserproto or internal/push, matching how config.go carries
// its own copies: the vocabulary package describes the wire, and taking a
// dependency on either renderer to name three constants would invert that.
const (
	NotifyKindAttention = "attention"
	NotifyKindFinished  = "finished"
	NotifyKindInfo      = "info"
)

// NotifyKindOK reports whether kind is one ui.notify accepts.
func NotifyKindOK(kind string) bool {
	switch kind {
	case NotifyKindAttention, NotifyKindFinished, NotifyKindInfo:
		return true
	}
	return false
}

// --- Editor params & results (§7, pane.open_file) ----------------------------

// OpenFileParams: pane.open_file.
//
// Path is NOT expanded or resolved here, for the reason every other path in
// this vocabulary travels raw since the multi-host slice: it names a file on
// the machine the editor is on. "~" is that user's home and a relative path is
// relative to that editor's own root, neither of which this side can answer
// about a disk it may not be able to see.
//
// Pane is the ANCHOR — where the request came from, usually the pane whose
// output the path was clicked in. It decides three things: which host the file
// is on, which tab and workspace to look for an editor in first, and where a
// freshly spawned editor is split. Nil means the focused pane, the same
// neighbour rule new tabs and splits use.
//
// Editor names an editor pane explicitly, skipping resolution. Use it when the
// caller already knows (an editor asking cats to open a file beside itself);
// leave it out and cats finds one.
//
// Host overrides the anchor's machine. It exists for the same reason
// PathListParams.Host does — a caller may be naming a file on a machine no
// current pane is anchored to — and the editor found must be on it, because a
// path is only half an identity: the same string on two machines is two files.
type OpenFileParams struct {
	Path   string  `json:"path"`
	Line   int     `json:"line,omitempty"`
	Column int     `json:"column,omitempty"`
	Pane   *uint32 `json:"pane,omitempty"`
	Editor *uint32 `json:"editor,omitempty"`
	Host   string  `json:"host,omitempty"`
	// Spawn allows starting an editor when none is running. Nil means the
	// configured default (editor.spawn, on). Set it false for a caller that
	// wants "reveal it if the editor is open" and nothing more — a linter
	// walking twenty findings should not open twenty editors.
	Spawn *bool `json:"spawn,omitempty"`
}

// OpenFileResult is CmdResult.Data for pane.open_file: which pane was asked,
// and whether it had to be started. Spawned is worth reporting rather than
// inferring, because a spawned editor opens the file from its ARGV and has not
// seen the line number — see the CmdPaneOpenFile comment.
type OpenFileResult struct {
	Pane    uint32 `json:"pane"`
	Host    string `json:"host"`
	Spawned bool   `json:"spawned,omitempty"`
}

// EditorInfo is the backend's editor policy, as the dispatcher needs it: which
// agent labels mark a pane as an editor, how to start one, and whether starting
// one is allowed at all.
//
// It comes over the Backend seam rather than being read from config here for
// the same reason the host roster does: the dispatcher is protocol-neutral and
// holds no configuration, and a fake in a test wants to answer this question
// without a config file.
type EditorInfo struct {
	Agents  []string
	Command []string
	Spawn   bool
}

// IsEditorAgent reports whether an agent label marks its pane as an editor.
// Case-insensitive: an agent label is a name a human typed into a config or a
// hook asset, and "CEd" and "ced" are the same editor.
func (e EditorInfo) IsEditorAgent(agent string) bool {
	if agent == "" {
		return false
	}
	for _, a := range e.Agents {
		if strings.EqualFold(a, agent) {
			return true
		}
	}
	return false
}

// optPaneID converts an optional wire pane id into an optional layout.PaneID
// (nil = the focused pane).
func optPaneID(p *uint32) *layout.PaneID {
	if p == nil {
		return nil
	}
	id := layout.PaneID(*p)
	return &id
}

// uniqueActionIDs fills in an empty action id from its index and reports a
// collision. Two buttons sharing an id is not a cosmetic problem: the id is the
// whole address of an action, so the second one would be unreachable and the
// first would answer for both — silently, and only for the caller unlucky
// enough to have named two buttons the same. Fail the notification instead.
//
// The generated ids are "1", "2", … rather than the labels, because a label is
// display text (it gets translated, it gets emoji, it collides) and an id is an
// address.
func uniqueActionIDs(actions []NotifyAction) error {
	seen := make(map[string]bool, len(actions))
	for i := range actions {
		if actions[i].ID == "" {
			actions[i].ID = strconv.Itoa(i + 1)
		}
		if seen[actions[i].ID] {
			return fmt.Errorf("actions: duplicate id %q", actions[i].ID)
		}
		seen[actions[i].ID] = true
	}
	return nil
}
