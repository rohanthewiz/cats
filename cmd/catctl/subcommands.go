package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/ctlproto"
)

// This file is catctl's ergonomic subcommand layer: short verbs that take
// positional arguments and build the §7 command's JSON params for the caller,
// e.g. `catctl split h 2` in place of
// `catctl pane.split --params '{"direction":"h","pane":2}'`. Each verb maps to
// exactly one method and reuses the app.*Params structs so the wire shape can
// never drift from the server's. The raw `<method> [--params json]` path in
// main.go stays the full-coverage escape hatch (and the only way to reach the
// rarely-scripted options like read's rect or capture's ansi/unwrap).

// subcommand is one ergonomic verb. build turns the verb's positional args into
// the method's params (nil for a no-params command); a usageErr from build means
// the args were malformed and the synopsis should be shown.
type subcommand struct {
	verb     string
	method   string
	synopsis string    // argument shape, e.g. "split [h|v] [pane]"
	args     []argKind // what each positional slot completes (complete.go); nil = nothing to offer
	summary  string    // one-line description for help
	build    func(args []string) (json.RawMessage, error)
}

// argKind names the candidate source for one positional slot, so shell
// completion can offer live pane ids where the synopsis says <pane>. It sits
// beside synopsis deliberately: the two describe the same argument list, and an
// author who edits one has the other in view. Slots past the end of args — a
// variadic <name...> or <text...> tail, a number, a free-text pattern — offer
// nothing, which is also argNone's meaning.
type argKind int

const (
	argNone       argKind = iota // no candidates (free text, numbers)
	argPane                      // live pane ids, from pane.list
	argTab                       // live tab numbers, from tab.list
	argWorkspace                 // live workspace ids, from workspace.list
	argDirection                 // left|right|up|down
	argSplitDir                  // h|v
	argCycleDir                  // next|prev
	argTheme                     // installed theme names, from theme.list
	argDetachHost                // detachable cathost ids, from host.list
)

// usageErr reports malformed subcommand arguments; its message is the verb's
// synopsis so the CLI can point the user at the right shape.
type usageErr struct{ synopsis string }

func (e usageErr) Error() string { return "usage: catctl " + e.synopsis }

// subcommands is the ordered ergonomic verb table (ordering drives help output).
// Grouped queries → pane → tab → workspace → misc, mirroring §7.
var subcommands = []subcommand{
	// Liveness.
	{"ping", ctlproto.MethodPing, "ping", nil, "check the server is reachable", noParams},

	// Read-only queries.
	{"session", app.CmdSessionGet, "session", nil, "session summary", noParams},
	{"workspaces", app.CmdWorkspaceList, "workspaces", nil, "list workspaces", noParams},
	{"tabs", app.CmdTabList, "tabs [workspace]", []argKind{argWorkspace}, "list tabs (active workspace by default)", buildTabList},
	{"panes", app.CmdPaneList, "panes", nil, "list all panes", noParams},
	{"pane", app.CmdPaneGet, "pane [pane]", []argKind{argPane}, "describe one pane (focused by default)", buildOptPane},
	{"hosts", app.CmdHostList, "hosts", nil, "list the cathosts panes can run on", noParams},
	{"events", ctlproto.MethodEventsSubscribe, "events [pane]", []argKind{argPane}, "stream pane events until interrupted (Ctrl-C)", buildEvents},
	{"clipboard", ctlproto.MethodClipboardRead, "clipboard", nil, "print the host system clipboard's text", noParams},

	// Pane commands.
	{"split", app.CmdPaneSplit, "split [h|v] [pane]", []argKind{argSplitDir, argPane}, "split a pane (h by default)", buildSplit},
	{"close", app.CmdPaneClose, "close [pane]", []argKind{argPane}, "close a pane (focused by default)", buildOptPane},
	{"focus", app.CmdPaneFocus, "focus <pane>", []argKind{argPane}, "focus a pane", buildPane},
	{"focus-dir", app.CmdPaneFocusDirection, "focus-dir <left|right|up|down>", []argKind{argDirection}, "focus the neighbour in a direction", buildDir},
	{"cycle", app.CmdPaneCycle, "cycle [prev]", []argKind{argCycleDir}, "focus the next pane (prev for previous)", buildCycle},
	{"last", app.CmdPaneLast, "last", nil, "focus the previously focused pane", noParams},
	{"swap", app.CmdPaneSwap, "swap <left|right|up|down>", []argKind{argDirection}, "swap with the neighbour in a direction", buildDir},
	{"zoom", app.CmdPaneZoom, "zoom [pane]", []argKind{argPane}, "toggle pane zoom (focused by default)", buildOptPane},
	{"rename-pane", app.CmdPaneRename, "rename-pane <pane> <name...>", []argKind{argPane}, "rename a pane (empty name clears)", buildRenamePane},
	{"resize", app.CmdPaneResizeBorder, "resize <border> <ratio>", nil, "set a split border's ratio", buildResize},
	{"scroll", app.CmdScroll, "scroll <pane> <delta>", []argKind{argPane}, "scroll a pane by delta lines (negative = up)", buildScroll},
	{"capture", app.CmdCapture, "capture <pane> [lines]", []argKind{argPane}, "capture a pane's text (whole buffer, or last N lines)", buildCapture},
	{"read", app.CmdRead, "read <pane> <r0> <c0> <r1> <c1>", []argKind{argPane}, "read the text between two [row,col] points", buildRead},
	{"wait", app.CmdWaitForOutput, "wait <pane> <pattern> [timeout_secs]", []argKind{argPane}, "wait until a pane's output contains a pattern", buildWait},
	{"send", app.CmdPaneSendInput, "send <pane> <text...>", []argKind{argPane}, "type text into a pane without submitting it", buildSend},
	{"run", app.CmdPaneSendInput, "run <pane> [text...]", []argKind{argPane}, "type text into a pane and submit it with Enter", buildRun},

	// Tab commands.
	{"tab", app.CmdTabFocus, "tab <num>", []argKind{argTab}, "focus a tab", buildTabFocus},
	{"new-tab", app.CmdTabCreate, "new-tab", nil, "create a tab", noParams},
	{"close-tab", app.CmdTabClose, "close-tab [num]", []argKind{argTab}, "close a tab (active by default)", buildOptTab},
	{"rename-tab", app.CmdTabRename, "rename-tab <num> <name...>", []argKind{argTab}, "rename a tab (empty name clears)", buildRenameTab},

	// Workspace commands.
	{"ws", app.CmdWorkspaceFocus, "ws <id>", []argKind{argWorkspace}, "focus a workspace", buildWorkspace},
	{"new-ws", app.CmdWorkspaceCreate, "new-ws [name...]", nil, "create a workspace (auto-named when no name is given)", buildNewWorkspace},
	{"close-ws", app.CmdWorkspaceClose, "close-ws [id]", []argKind{argWorkspace}, "close a workspace (active by default)", buildOptWorkspace},
	{"rename-ws", app.CmdWorkspaceRename, "rename-ws <id> <name...>", []argKind{argWorkspace}, "rename a workspace (empty name clears)", buildRenameWorkspace},
	{"lock-ws", app.CmdWorkspaceLock, "lock-ws [id]", []argKind{argWorkspace}, "close a workspace to plugins and agents (active by default)", buildLockWorkspace},
	{"unlock-ws", app.CmdWorkspaceLock, "unlock-ws [id]", []argKind{argWorkspace}, "reopen a locked workspace (active by default)", buildUnlockWorkspace},

	// Host commands. They edit the roster of the RUNNING catway and its config
	// file together, so neither needs a restart. Only the two positional
	// essentials are exposed here — a token, a token file or a pinned
	// fingerprint goes through `catctl host.attach --params …`, which is also
	// the shape a script would rather write.
	{"attach-host", app.CmdHostAttach, "attach-host <id> <addr> [label...]", nil, "attach a cathost (addr: unix://path, tcp://host:port, tls://host:port)", buildAttachHost},
	{"detach-host", app.CmdHostDetach, "detach-host <id> [force]", []argKind{argDetachHost}, "detach a cathost (force also re-homes its panes)", buildDetachHost},

	// Notifications. The ergonomic verb is the plain one-liner a script wants
	// at the end of a long build; anything with buttons goes through
	// `catctl ui.notify --params …`, which is the shape a caller declaring
	// actions would rather write anyway.
	{"notify", app.CmdUINotify, "notify <title...>", nil, "raise a notification on every client (and the phone bridge)", buildNotify},

	// Editors. The path is positional and the line is the optional second
	// argument, because "open this file at this line" is what a linter, a stack
	// trace and a grep hit all have in hand.
	{"open", app.CmdPaneOpenFile, "open <path> [line]", nil, "open a file in the session's editor", buildOpenFile},

	// Misc.
	{"agent", app.CmdAgentFocus, "agent <pane>", []argKind{argPane}, "reveal an agent's pane", buildPane},
	{"themes", app.CmdThemeList, "themes", nil, "list available UI themes", noParams},
	{"theme", app.CmdConfigSet, "theme <name>", []argKind{argTheme}, "switch the UI theme (clears color overrides)", buildTheme},
	{"reload", app.CmdServerReloadConfig, "reload", nil, "reload server config", noParams},
	{"stop", app.CmdServerStop, "stop", nil, "stop the server (terminals survive)", noParams},
}

// argAt reports the candidate source for the n-th positional argument (0-based)
// of this verb. Past the declared slots — a <name...> tail, extra numbers —
// there is nothing to offer.
func (sc subcommand) argAt(n int) argKind {
	if n < 0 || n >= len(sc.args) {
		return argNone
	}
	return sc.args[n]
}

// lookupSubcommand finds an ergonomic verb by name.
func lookupSubcommand(verb string) (subcommand, bool) {
	for _, sc := range subcommands {
		if sc.verb == verb {
			return sc, true
		}
	}
	return subcommand{}, false
}

// --- param builders ----------------------------------------------------------

// noParams accepts a no-argument verb.
func noParams(args []string) (json.RawMessage, error) {
	if len(args) != 0 {
		return nil, usageErr{"<verb> (takes no arguments)"}
	}
	return nil, nil
}

// buildSplit: split [h|v] [pane].
func buildSplit(args []string) (json.RawMessage, error) {
	if len(args) > 2 {
		return nil, usageErr{"split [h|v] [pane]"}
	}
	p := app.SplitParams{Direction: app.SplitH}
	if len(args) >= 1 {
		if args[0] != app.SplitH && args[0] != app.SplitV {
			return nil, usageErr{"split [h|v] [pane]"}
		}
		p.Direction = args[0]
	}
	if len(args) == 2 {
		n, err := parsePane(args[1])
		if err != nil {
			return nil, err
		}
		p.Pane = &n
	}
	return marshal(p)
}

// buildPane: <verb> <pane> — a required pane id (focus, agent).
func buildPane(args []string) (json.RawMessage, error) {
	if len(args) != 1 {
		return nil, usageErr{"<verb> <pane>"}
	}
	n, err := parsePane(args[0])
	if err != nil {
		return nil, err
	}
	return marshal(app.PaneParams{Pane: n})
}

// buildOptPane: <verb> [pane] — an optional pane id (close, zoom, pane.get).
func buildOptPane(args []string) (json.RawMessage, error) {
	if len(args) > 1 {
		return nil, usageErr{"<verb> [pane]"}
	}
	if len(args) == 0 {
		return nil, nil
	}
	n, err := parsePane(args[0])
	if err != nil {
		return nil, err
	}
	return marshal(app.OptPaneParams{Pane: &n})
}

// buildDir: <verb> <direction> — focus-dir, swap.
func buildDir(args []string) (json.RawMessage, error) {
	if len(args) != 1 {
		return nil, usageErr{"<verb> <left|right|up|down>"}
	}
	if _, ok := app.NavDirection(args[0]); !ok {
		return nil, usageErr{"<verb> <left|right|up|down>"}
	}
	return marshal(app.DirParams{Dir: args[0]})
}

// buildCycle: cycle [prev].
func buildCycle(args []string) (json.RawMessage, error) {
	next := true
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "prev":
		next = false
	case len(args) == 1 && args[0] == "next":
	default:
		return nil, usageErr{"cycle [prev]"}
	}
	return marshal(app.CycleParams{Next: next})
}

// buildRenamePane: rename-pane <pane> <name...>.
func buildRenamePane(args []string) (json.RawMessage, error) {
	if len(args) < 2 {
		return nil, usageErr{"rename-pane <pane> <name...>"}
	}
	n, err := parsePane(args[0])
	if err != nil {
		return nil, err
	}
	return marshal(app.RenamePaneParams{Pane: n, Name: strings.Join(args[1:], " ")})
}

// buildNotify: notify <title...>. Kind is left empty so the server applies its
// own default ("info") — spelling it here would freeze the default into every
// installed catctl.
func buildNotify(args []string) (json.RawMessage, error) {
	title := strings.TrimSpace(strings.Join(args, " "))
	if title == "" {
		return nil, usageErr{"notify <title...>"}
	}
	return marshal(app.UINotifyParams{Title: title})
}

// buildOpenFile: open <path> [line]. The path is passed through unexpanded —
// it names a file on the editor's machine, and "~" there is not "~" here.
func buildOpenFile(args []string) (json.RawMessage, error) {
	if len(args) < 1 || len(args) > 2 || strings.TrimSpace(args[0]) == "" {
		return nil, usageErr{"open <path> [line]"}
	}
	p := app.OpenFileParams{Path: args[0]}
	if len(args) == 2 {
		line, err := strconv.Atoi(args[1])
		if err != nil || line < 0 {
			return nil, fmt.Errorf("line %q is not a line number", args[1])
		}
		p.Line = line
	}
	return marshal(p)
}

// buildResize: resize <border> <ratio>.
func buildResize(args []string) (json.RawMessage, error) {
	if len(args) != 2 {
		return nil, usageErr{"resize <border> <ratio>"}
	}
	ratio, err := strconv.ParseFloat(args[1], 32)
	if err != nil {
		return nil, fmt.Errorf("ratio %q is not a number", args[1])
	}
	return marshal(app.ResizeBorderParams{Border: args[0], Ratio: float32(ratio)})
}

// buildScroll: scroll <pane> <delta>.
func buildScroll(args []string) (json.RawMessage, error) {
	if len(args) != 2 {
		return nil, usageErr{"scroll <pane> <delta>"}
	}
	pane, err := parsePane(args[0])
	if err != nil {
		return nil, err
	}
	delta, err := strconv.Atoi(args[1])
	if err != nil {
		return nil, fmt.Errorf("delta %q is not an integer", args[1])
	}
	return marshal(app.ScrollParams{Pane: pane, Delta: delta})
}

// buildCapture: capture <pane> [lines]. With lines, captures the last N rows of
// scrollback (scope "recent"); without, the whole buffer (recent, lines 0).
func buildCapture(args []string) (json.RawMessage, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, usageErr{"capture <pane> [lines]"}
	}
	pane, err := parsePane(args[0])
	if err != nil {
		return nil, err
	}
	p := app.CaptureParams{Pane: pane, Scope: 1} // 1 = recent; lines 0 = whole buffer
	if len(args) == 2 {
		lines, err := strconv.ParseUint(args[1], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("lines %q is not a number", args[1])
		}
		p.Lines = uint32(lines)
	}
	return marshal(p)
}

// buildRead: read <pane> <r0> <c0> <r1> <c1>.
func buildRead(args []string) (json.RawMessage, error) {
	if len(args) != 5 {
		return nil, usageErr{"read <pane> <r0> <c0> <r1> <c1>"}
	}
	nums := make([]uint32, 5)
	for i, a := range args {
		v, err := strconv.ParseUint(a, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", a)
		}
		nums[i] = uint32(v)
	}
	return marshal(app.ReadParams{
		Pane:   nums[0],
		Anchor: [2]uint32{nums[1], nums[2]},
		Cursor: [2]uint32{nums[3], nums[4]},
	})
}

// buildWait: wait <pane> <pattern> [timeout_secs]. The pattern is a plain
// substring; the raw path (pane.wait_for_output --params) reaches regex/lines.
// timeout_secs accepts fractions (e.g. 0.5); omitted uses the server default.
func buildWait(args []string) (json.RawMessage, error) {
	if len(args) < 2 || len(args) > 3 {
		return nil, usageErr{"wait <pane> <pattern> [timeout_secs]"}
	}
	pane, err := parsePane(args[0])
	if err != nil {
		return nil, err
	}
	p := app.WaitForOutputParams{Pane: pane, Pattern: args[1]}
	if len(args) == 3 {
		secs, err := strconv.ParseFloat(args[2], 64)
		if err != nil || secs < 0 {
			return nil, fmt.Errorf("timeout %q is not a non-negative number of seconds", args[2])
		}
		p.TimeoutMs = uint32(secs * 1000)
	}
	return marshal(p)
}

// buildSend / buildRun: the two faces of pane.send_input. Both join their text
// words with single spaces (shell-friendly; quote the text to keep exact
// whitespace as one arg). `send` only types the text — tmux-send-keys-style
// staging, so the user can review before running — while `run` follows it with
// a real Enter. They are separate verbs (not one verb with a flag) because
// main.go re-parses post-verb args through the global FlagSet, so a leading
// dash operand like -r would be eaten as an unknown flag.
func buildSend(args []string) (json.RawMessage, error) {
	if len(args) < 2 {
		return nil, usageErr{"send <pane> <text...>"}
	}
	pane, err := parsePane(args[0])
	if err != nil {
		return nil, err
	}
	return marshal(app.SendInputParams{Pane: pane, Text: strings.Join(args[1:], " ")})
}

// buildRun: run <pane> [text...]. Text is optional — a bare `run <pane>` sends
// just the Enter, submitting input already staged by an earlier `send`.
func buildRun(args []string) (json.RawMessage, error) {
	if len(args) == 0 {
		return nil, usageErr{"run <pane> [text...]"}
	}
	pane, err := parsePane(args[0])
	if err != nil {
		return nil, err
	}
	return marshal(app.SendInputParams{Pane: pane, Text: strings.Join(args[1:], " "), Submit: true})
}

// buildEvents: events [pane] — an optional pane filter for the event stream. The
// raw path reaches the events filter (`events.subscribe --params '{"events":[…]}'`).
func buildEvents(args []string) (json.RawMessage, error) {
	if len(args) > 1 {
		return nil, usageErr{"events [pane]"}
	}
	if len(args) == 0 {
		return nil, nil
	}
	pane, err := parsePane(args[0])
	if err != nil {
		return nil, err
	}
	return marshal(app.EventsSubscribeParams{Pane: &pane})
}

// buildTabFocus: tab <num>.
func buildTabFocus(args []string) (json.RawMessage, error) {
	if len(args) != 1 {
		return nil, usageErr{"tab <num>"}
	}
	num, err := strconv.Atoi(args[0])
	if err != nil {
		return nil, fmt.Errorf("tab number %q is not an integer", args[0])
	}
	return marshal(app.TabParams{Num: num})
}

// buildOptTab: close-tab [num].
func buildOptTab(args []string) (json.RawMessage, error) {
	if len(args) > 1 {
		return nil, usageErr{"close-tab [num]"}
	}
	if len(args) == 0 {
		return nil, nil
	}
	num, err := strconv.Atoi(args[0])
	if err != nil {
		return nil, fmt.Errorf("tab number %q is not an integer", args[0])
	}
	return marshal(app.OptTabParams{Num: &num})
}

// buildRenameTab: rename-tab <num> <name...>.
func buildRenameTab(args []string) (json.RawMessage, error) {
	if len(args) < 2 {
		return nil, usageErr{"rename-tab <num> <name...>"}
	}
	num, err := strconv.Atoi(args[0])
	if err != nil {
		return nil, fmt.Errorf("tab number %q is not an integer", args[0])
	}
	return marshal(app.RenameTabParams{Num: num, Name: strings.Join(args[1:], " ")})
}

// buildTabList: tabs [workspace].
func buildTabList(args []string) (json.RawMessage, error) {
	if len(args) > 1 {
		return nil, usageErr{"tabs [workspace]"}
	}
	if len(args) == 0 {
		return nil, nil
	}
	return marshal(app.TabListParams{Workspace: args[0]})
}

// buildTheme: theme <name>. Naming a theme in config.set is the switch form —
// the server replaces any per-key color overrides with the theme's clean
// palette (see app.ConfigSetParams).
func buildTheme(args []string) (json.RawMessage, error) {
	if len(args) != 1 {
		return nil, usageErr{"theme <name>"}
	}
	return marshal(app.ConfigSetParams{Theme: &app.ConfigTheme{Name: args[0]}})
}

// buildAttachHost: attach-host <id> <addr> [label...].
func buildAttachHost(args []string) (json.RawMessage, error) {
	if len(args) < 2 {
		return nil, usageErr{"attach-host <id> <addr> [label...]"}
	}
	p := app.HostAttachParams{ID: args[0], Addr: args[1]}
	if len(args) > 2 {
		p.Label = strings.Join(args[2:], " ")
	}
	return marshal(p)
}

// buildDetachHost: detach-host <id> [force].
//
// "force" is spelled out as a word rather than offered as a flag because it is
// the argument that decides whether running terminals are thrown away; a script
// that types it has said so, and a fat-fingered `-f` cannot mean it by accident.
func buildDetachHost(args []string) (json.RawMessage, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, usageErr{"detach-host <id> [force]"}
	}
	p := app.HostDetachParams{ID: args[0]}
	if len(args) == 2 {
		if args[1] != "force" {
			return nil, usageErr{"detach-host <id> [force]"}
		}
		p.Force = true
	}
	return marshal(p)
}

// buildWorkspace: ws <id>.
func buildWorkspace(args []string) (json.RawMessage, error) {
	if len(args) != 1 {
		return nil, usageErr{"ws <id>"}
	}
	return marshal(app.WorkspaceParams{ID: args[0]})
}

// buildOptWorkspace: close-ws [id].
func buildOptWorkspace(args []string) (json.RawMessage, error) {
	if len(args) > 1 {
		return nil, usageErr{"close-ws [id]"}
	}
	if len(args) == 0 {
		return nil, nil
	}
	return marshal(app.WorkspaceParams{ID: args[0]})
}

// buildNewWorkspace: new-ws [name...]. The trailing words join into one name, so
// `catctl new-ws api rewrite` needs no quoting (same convention as rename-ws).
// No arguments sends no params at all, keeping the historical wire shape — and
// with no path the server roots the workspace in the session's own directory.
// A start directory needs the raw escape hatch, since main re-parses flags after
// the verb and an ergonomic verb only ever sees positional args:
//
//	catctl workspace.create --params '{"name":"api","path":"~/src/api"}'
func buildNewWorkspace(args []string) (json.RawMessage, error) {
	if len(args) == 0 {
		return nil, nil
	}
	return marshal(app.WorkspaceCreateParams{Name: strings.Join(args, " ")})
}

// buildRenameWorkspace: rename-ws <id> <name...>.
func buildRenameWorkspace(args []string) (json.RawMessage, error) {
	if len(args) < 2 {
		return nil, usageErr{"rename-ws <id> <name...>"}
	}
	return marshal(app.RenameWorkspaceParams{ID: args[0], Name: strings.Join(args[1:], " ")})
}

// buildLockWorkspace / buildUnlockWorkspace: lock-ws [id] / unlock-ws [id]. Two
// verbs over one command, the way send/run both reach pane.send_input — the
// difference is the flag, and "unlock-ws w2" is what a user would go looking for
// rather than a --locked=false on a lock verb. No id locks the active
// workspace (the close-ws default).
func buildLockWorkspace(args []string) (json.RawMessage, error) {
	return buildWorkspaceLock(args, true, "lock-ws [id]")
}

func buildUnlockWorkspace(args []string) (json.RawMessage, error) {
	return buildWorkspaceLock(args, false, "unlock-ws [id]")
}

func buildWorkspaceLock(args []string, locked bool, synopsis string) (json.RawMessage, error) {
	if len(args) > 1 {
		return nil, usageErr{synopsis}
	}
	p := app.LockWorkspaceParams{Locked: locked}
	if len(args) == 1 {
		p.ID = args[0]
	}
	return marshal(p)
}

// --- helpers -----------------------------------------------------------------

// parsePane parses a pane id (the internal uint32 used to address panes; get it
// from `catctl panes`).
func parsePane(s string) (uint32, error) {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("pane %q is not a valid id (see `catctl panes`)", s)
	}
	return uint32(n), nil
}

// marshal encodes a params struct, wrapping the (practically impossible) error.
func marshal(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode params: %w", err)
	}
	return b, nil
}

// subcommandsHelp renders the ergonomic verb table as aligned help lines.
func subcommandsHelp() string {
	width := 0
	for _, sc := range subcommands {
		if len(sc.synopsis) > width {
			width = len(sc.synopsis)
		}
	}
	var b strings.Builder
	for _, sc := range subcommands {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, sc.synopsis, sc.summary)
	}
	return b.String()
}
