package main

// complete.go is the completion engine behind the hidden `__complete` verb —
// the one place that knows what may follow what on a catctl command line. The
// shell scripts emitted by `catctl completion <shell>` (completion.go) are
// deliberately thin: they collect the words typed so far, hand them here, and
// render whatever comes back. Every piece of vocabulary — verbs, the §7 method
// table, family subcommands, integration targets, live pane/tab/workspace ids,
// installed plugins and their actions — therefore stays in Go, next to the code
// that defines it, and can never drift from a hand-maintained shell script.
//
// # The protocol
//
//	catctl __complete [--for <binary>] [--] <word>...
//
// The words are the command line after the program name, the last one being the
// (possibly empty) word under the cursor. Output is one candidate per line as
//
//	value<TAB>description        (the description is optional)
//
// followed by a final directive line telling the shell what to do when there
// are no candidates: ":nofiles" (nothing — the default), ":files" or ":dirs".
// A directive always terminates the output, so a shell can parse it without
// knowing anything about catctl.
//
// # --for
//
// Plugins declare in their manifest that they know how to complete a command
// name ([[completions]], see internal/plugin). The generated script registers
// those names too and routes them back here with --for <binary>; catctl either
// serves the manifest's static lists or execs the plugin's own completer and
// passes its output through. One protocol in the shell, either style in the
// plugin.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/integration"
	"github.com/rohanthewiz/cats/internal/plugin"
)

// The directives a completer may end its output with.
const (
	dirNoFiles = ":nofiles"
	dirFiles   = ":files"
	dirDirs    = ":dirs"
)

// liveTimeout caps the control-socket round trips that fetch pane/tab/workspace
// /theme candidates. Completion runs between keystrokes, so a stopped or wedged
// server must cost a barely perceptible pause and then simply offer nothing —
// never a hang at the prompt. A dead socket fails far faster than this (connect
// refused); the budget is for a server that answers slowly.
const liveTimeout = 500 * time.Millisecond

// Bounds on a delegated plugin completer: how long it may take, and how much it
// may say. Neither is a policy about plugins so much as a promise to the user's
// shell, which is blocked on this process until it returns.
const (
	pluginTimeout   = 2 * time.Second
	pluginMaxOutput = 64 << 10
)

// candidate is one completion: the text inserted, plus an optional description
// that shells with rich menus (zsh, fish) show beside it.
type candidate struct {
	value string
	desc  string
}

// flagSpec describes one command-line flag for completion purposes: whether a
// value follows it (so slot counting can skip that value) and what the value
// itself completes to.
type flagSpec struct {
	name     string
	desc     string
	takesVal bool
	valueDir string // directive for the value word; dirNoFiles when it is free text
}

// globalFlags mirrors main.go's FlagSet. They are documented as going before
// the verb, but main re-parses the tail, so they are offered after it too.
var globalFlags = []flagSpec{
	{"--socket", "control socket path", true, dirFiles},
	{"--params", "command params as a JSON object", true, dirNoFiles},
	{"--timeout", "round-trip timeout", true, dirNoFiles},
	{"--id", "correlation id echoed in the response", true, dirNoFiles},
	{"--json", "print the full JSON response instead of the result", false, dirNoFiles},
}

// families are the verbs that own a subcommand vocabulary of their own rather
// than mapping to a single §7 method.
var families = []candidate{
	{"integration", "install/remove agent hooks (offline)"},
	{"plugin", "manage and launch plugins"},
	{"probe", "browser-protocol WebSocket probe"},
	{"cp", "copy a file to or from a cathost"},
	{"completion", "print a shell completion script"},
	{"shellinit", "cats shell setup: PATH + plugin shell hooks"},
	{"commands", "list the raw §7 method names"},
	{"pair", "mint a device-pairing code (QR)"},
	{"help", "show help for catctl or one verb"},
}

// runComplete implements `catctl __complete`. It always exits 0 with a
// well-formed (possibly empty) candidate list: a completer that fails loudly
// would put an error message where the user expects a menu.
func runComplete(argv []string) int {
	forBinary := ""
	i := 0
scan:
	for ; i < len(argv); i++ {
		switch {
		case argv[i] == "--":
			i++
			break scan
		case argv[i] == "--for":
			if i+1 >= len(argv) {
				fmt.Println(dirNoFiles)
				return 0
			}
			i++
			forBinary = argv[i]
		case strings.HasPrefix(argv[i], "--for="):
			forBinary = strings.TrimPrefix(argv[i], "--for=")
		default:
			break scan
		}
	}

	// No words at all still means "completing a fresh first word".
	words := argv[i:]
	if len(words) == 0 {
		words = []string{""}
	}

	if forBinary != "" {
		return completeForBinary(forBinary, words)
	}
	emit(completeCatctl(words))
	return 0
}

// completeCatctl resolves the candidates for a catctl command line.
func completeCatctl(words []string) ([]candidate, string) {
	cur := words[len(words)-1]
	prior := words[:len(words)-1]

	// The word under the cursor is a flag's value (`--socket <cursor>`): the
	// flag decides, and there is nothing catctl-specific left to offer.
	if len(prior) > 0 {
		if fs, ok := lookupFlag(globalFlags, prior[len(prior)-1]); ok && fs.takesVal {
			return nil, fs.valueDir
		}
	}

	vi, ok := firstPositional(prior, globalFlags)
	if !ok {
		if strings.HasPrefix(cur, "-") {
			return filter(flagCandidates(globalFlags), cur), dirNoFiles
		}
		return filter(topLevel(cur), cur), dirNoFiles
	}

	// rest is the *raw* tail after the verb, not a stripped one: a family owns
	// flags catctl's table has never heard of (--ref, --url), and stripping with
	// the global table would drop the flag while leaving its value behind to be
	// miscounted as an operand. Each family re-scans with its own table instead.
	verb, rest := prior[vi], prior[vi+1:]
	switch verb {
	case "integration":
		return completeIntegration(rest, cur)
	case "plugin":
		return completePlugin(rest, cur)
	case "probe":
		return completeProbe(rest, cur)
	case "cp":
		// Paths on both sides, one of which may be host:path — so the shell's own
		// file completion is what the user wants here, and a candidate list
		// would replace it with a worse one. The remote half gets no completion
		// at all, deliberately: completing it means a directory listing on
		// another machine between two keystrokes.
		return nil, dirFiles
	case "completion", "shellinit":
		if len(rest) == 0 && !strings.HasPrefix(cur, "-") {
			return filter(shellCandidates(), cur), dirNoFiles
		}
		return nil, dirNoFiles
	case "help":
		if len(rest) == 0 {
			return filter(topLevel(cur), cur), dirNoFiles
		}
		return nil, dirNoFiles
	case "commands", "pair":
		return nil, dirNoFiles // neither takes an operand
	}

	if strings.HasPrefix(cur, "-") {
		return filter(flagCandidates(globalFlags), cur), dirNoFiles
	}
	sc, found := lookupSubcommand(verb)
	if !found {
		return nil, dirNoFiles // a raw §7 method, or a typo: no positional vocabulary
	}
	slot := len(positionals(rest, globalFlags))
	return filter(argCandidates(sc.argAt(slot), words), cur), dirNoFiles
}

// topLevel is the first-word vocabulary: the ergonomic verbs, the families, and
// — only once the user has typed something — the raw §7 method names. The raw
// table is nearly as large as the verb table and is the escape hatch rather than
// the everyday path, so a bare <TAB> shows the curated list and typing any
// prefix opens the full one.
func topLevel(cur string) []candidate {
	out := make([]candidate, 0, len(subcommands)+len(families))
	for _, sc := range subcommands {
		out = append(out, candidate{sc.verb, sc.summary})
	}
	out = append(out, families...)
	if cur != "" {
		for _, n := range app.CommandNames() {
			out = append(out, candidate{n, "§7 command (raw; takes --params)"})
		}
	}
	return out
}

// --- families -----------------------------------------------------------------

func completeIntegration(rest []string, cur string) ([]candidate, string) {
	pos := positionals(rest, globalFlags)
	if len(pos) == 0 {
		return filter([]candidate{
			{"install", "install the cats hooks for an agent"},
			{"uninstall", "remove them"},
			{"status", "show install state per agent"},
			{"help", "list the integration commands"},
		}, cur), dirNoFiles
	}
	switch pos[0] {
	case "install", "uninstall":
		if len(pos) > 1 {
			return nil, dirNoFiles
		}
		return filter(integrationTargets(), cur), dirNoFiles
	case "status":
		if strings.HasPrefix(cur, "-") {
			return filter([]candidate{
				{"--outdated-only", "print a notice only if something is out of date"},
			}, cur), dirNoFiles
		}
	}
	return nil, dirNoFiles
}

// integrationTargets lists the agents, described by their current install
// state — the same information `integration status` prints, which is exactly
// what you want to see when choosing what to install or remove.
func integrationTargets() []candidate {
	states := map[string]string{}
	for _, s := range integration.InstalledIntegrationStatuses() {
		version := "legacy"
		if s.InstalledVersion >= 0 {
			version = fmt.Sprintf("v%d", s.InstalledVersion)
		}
		switch s.State {
		case integration.StatusNotInstalled:
			states[s.Target.Label()] = "not installed"
		case integration.StatusCurrent:
			states[s.Target.Label()] = "installed " + version
		case integration.StatusOutdated:
			states[s.Target.Label()] = fmt.Sprintf("outdated (%s < v%d)", version, s.ExpectedVersion)
		}
	}
	out := make([]candidate, 0)
	for _, t := range integration.AllTargets() {
		out = append(out, candidate{t.Label(), states[t.Label()]})
	}
	return out
}

// pluginFlags are the plugin family's own flags, kept apart from globalFlags so
// slot counting skips `--ref <value>` instead of mistaking the value for the
// plugin id — and so `--all`, which takes none, is skipped without eating the
// word after it.
//
// Each is also named on its own because they belong to different subcommands
// (--ref to install, --all to run) and only that subcommand should offer it.
// Naming them beats slicing pluginFlags by index, which silently offers the
// wrong flag the moment another one is added.
var (
	pluginRefFlag = flagSpec{"--ref", "branch or tag to install", true, dirNoFiles}
	pluginAllFlag = flagSpec{"--all", "start the action in every unlocked workspace", false, dirNoFiles}
)

var pluginFlags = append([]flagSpec{pluginRefFlag, pluginAllFlag}, globalFlags...)

// completePlugin completes the plugin family.
func completePlugin(rest []string, cur string) ([]candidate, string) {
	pos := positionals(rest, pluginFlags)
	if len(pos) == 0 {
		if strings.HasPrefix(cur, "-") {
			return nil, dirNoFiles
		}
		return filter([]candidate{
			{"install", "install from GitHub (owner/repo) or a git URL"},
			{"link", "register a local checkout (dev mode)"},
			{"update", "re-fetch the recorded source and rebuild"},
			{"uninstall", "remove an installed plugin"},
			{"list", "list installed plugins and their actions"},
			{"run", "launch an action in a new tab"},
			{"help", "list the plugin commands"},
		}, cur), dirNoFiles
	}
	switch pos[0] {
	case "install":
		if strings.HasPrefix(cur, "-") {
			return filter(flagCandidates([]flagSpec{pluginRefFlag}), cur), dirNoFiles
		}
		return nil, dirNoFiles // owner/repo or a URL — nothing local to offer
	case "link":
		if len(pos) == 1 {
			return nil, dirDirs
		}
	case "update", "uninstall":
		if len(pos) == 1 {
			return filter(installedPlugins(), cur), dirNoFiles
		}
	case "run":
		// --all is offered at every slot: it is positionless, so a user who
		// reaches for it after typing the id should still find it.
		if strings.HasPrefix(cur, "-") {
			return filter(flagCandidates([]flagSpec{pluginAllFlag}), cur), dirNoFiles
		}
		switch len(pos) {
		case 1:
			return filter(installedPlugins(), cur), dirNoFiles
		case 2:
			return filter(pluginActions(pos[1]), cur), dirNoFiles
		}
	}
	return nil, dirNoFiles
}

// installedPlugins lists plugin ids, described by name and version — or by the
// breakage, so a plugin you need to uninstall is still completable.
func installedPlugins() []candidate {
	plugins, err := plugin.List()
	if err != nil {
		return nil
	}
	out := make([]candidate, 0, len(plugins))
	for _, p := range plugins {
		switch {
		case p.Err != nil:
			out = append(out, candidate{p.ID, "broken"})
		case p.Linked:
			out = append(out, candidate{p.ID, fmt.Sprintf("%s v%s (linked)", p.Name, p.Version)})
		default:
			out = append(out, candidate{p.ID, fmt.Sprintf("%s v%s", p.Name, p.Version)})
		}
	}
	return out
}

// pluginActions lists one plugin's action ids, titles as descriptions.
func pluginActions(id string) []candidate {
	inst, err := plugin.Get(id)
	if err != nil || inst.Err != nil {
		return nil
	}
	out := make([]candidate, 0, len(inst.Actions))
	for _, a := range inst.Actions {
		out = append(out, candidate{a.ID, a.Title})
	}
	return out
}

// probeFlags is probe.go's FlagSet (it dials the browser WebSocket, so it shares
// none of the control-socket flags).
var probeFlags = []flagSpec{
	{"--url", "catway WebSocket URL", true, dirNoFiles},
	{"--cols", "grid columns", true, dirNoFiles},
	{"--rows", "grid rows", true, dirNoFiles},
	{"--script", "op script (see cmd/catctl/probe.go)", true, dirNoFiles},
	{"--timeout", "expect/modes poll timeout", true, dirNoFiles},
	{"--life", "connection lifetime limit", true, dirNoFiles},
	{"--token", "shared access token (WS10 auth)", true, dirNoFiles},
	{"--viewer", "connect as a viewer: declare no grid", false, dirNoFiles},
	{"--workspace", "open this connection on a workspace (e.g. w2)", true, dirNoFiles},
}

func completeProbe(rest []string, cur string) ([]candidate, string) {
	if len(rest) > 0 {
		if fs, ok := lookupFlag(probeFlags, rest[len(rest)-1]); ok && fs.takesVal {
			return nil, fs.valueDir
		}
	}
	return filter(flagCandidates(probeFlags), cur), dirNoFiles
}

func shellCandidates() []candidate {
	return []candidate{
		{"bash", "bash completion script"},
		{"zsh", "zsh completion script"},
		{"fish", "fish completion script"},
	}
}

// --- live values --------------------------------------------------------------

// argCandidates fetches the candidates for one positional slot. The live kinds
// dial the control socket; a server that is not running yields no candidates and
// no error, because a completion menu is not the place to learn that.
func argCandidates(kind argKind, words []string) []candidate {
	switch kind {
	case argPane:
		return livePanes(words)
	case argTab:
		return liveTabs(words)
	case argWorkspace:
		return liveWorkspaces(words)
	case argTheme:
		return liveThemes(words)
	case argDetachHost:
		return liveDetachableHosts(words)
	case argRunbook:
		return liveRunbooks(words)
	case argDirection:
		return []candidate{{"left", ""}, {"right", ""}, {"up", ""}, {"down", ""}}
	case argSplitDir:
		return []candidate{{"h", "split horizontally (side by side)"}, {"v", "split vertically (stacked)"}}
	case argCycleDir:
		return []candidate{{"next", "the next pane"}, {"prev", "the previous pane"}}
	case argRecordAction:
		return []candidate{
			{app.RecordStart, "start recording what you do"},
			{app.RecordStop, "save the recording as a runbook"},
			{app.RecordCancel, "throw the recording away"},
			{app.RecordStatus, "what has been captured so far"},
		}
	}
	return nil
}

// liveCall runs one read-only query against the server, honouring a --socket
// already typed on the line being completed. Any failure is "no candidates".
func liveCall(words []string, method string, out any) bool {
	resp, err := ctlproto.Call(ctlproto.ResolveSocket(socketFromWords(words)),
		ctlproto.Request{ID: "complete", Method: method}, liveTimeout)
	if err != nil || !resp.OK || len(resp.Data) == 0 {
		return false
	}
	return json.Unmarshal(resp.Data, out) == nil
}

// socketFromWords picks up a --socket the user has already typed, so completion
// queries the same server the command will reach.
func socketFromWords(words []string) string {
	for i, w := range words {
		for _, name := range []string{"--socket", "-socket"} {
			if w == name && i+1 < len(words) {
				return words[i+1]
			}
			if v, ok := strings.CutPrefix(w, name+"="); ok {
				return v
			}
		}
	}
	return ""
}

func livePanes(words []string) []candidate {
	var res app.PaneListResult
	if !liveCall(words, app.CmdPaneList, &res) {
		return nil
	}
	out := make([]candidate, 0, len(res.Panes))
	for _, p := range res.Panes {
		out = append(out, candidate{strconv.FormatUint(uint64(p.Pane), 10), paneDesc(p)})
	}
	return out
}

// paneDesc names a pane the way the sidebar would: where it lives, then the most
// specific label it has (custom name > agent > terminal title), then whether it
// is the one you are looking at. Pane ids are opaque numbers, so this
// description is what makes the menu usable.
//
// Focused alone marks the focused pane *within its own tab* — true of nearly
// every pane in a session of single-pane tabs, and therefore no help in a menu.
// Only Focused together with Visible (the current viewport) picks out the one
// globally focused pane, which is what the note is for.
func paneDesc(p app.PaneInfo) string {
	var parts []string
	if p.Handle != "" {
		parts = append(parts, p.Handle)
	}
	switch {
	case p.Name != "":
		parts = append(parts, p.Name)
	case p.Agent != "":
		agent := p.Agent
		if p.AgentState != "" {
			agent += " (" + p.AgentState + ")"
		}
		parts = append(parts, agent)
	case p.Title != "":
		parts = append(parts, p.Title)
	}
	if p.Focused && p.Visible {
		parts = append(parts, "focused")
	}
	return strings.Join(parts, " · ")
}

func liveTabs(words []string) []candidate {
	var res app.TabListResult
	if !liveCall(words, app.CmdTabList, &res) {
		return nil
	}
	out := make([]candidate, 0, len(res.Tabs))
	for _, t := range res.Tabs {
		desc := t.Name
		if t.Active {
			desc = strings.TrimPrefix(desc+" · active", " · ")
		}
		out = append(out, candidate{strconv.Itoa(t.Num), desc})
	}
	return out
}

func liveWorkspaces(words []string) []candidate {
	var res app.WorkspaceListResult
	if !liveCall(words, app.CmdWorkspaceList, &res) {
		return nil
	}
	out := make([]candidate, 0, len(res.Workspaces))
	for _, w := range res.Workspaces {
		desc := w.Name
		if w.Active {
			desc = strings.TrimPrefix(desc+" · active", " · ")
		}
		out = append(out, candidate{w.ID, desc})
	}
	return out
}

// liveRunbooks offers the runbooks on the server's disk. A broken file is
// offered too, described by its error: it is a name the user meant to be able
// to run, and completing it and being told why it will not run beats it silently
// not existing.
func liveRunbooks(words []string) []candidate {
	var res app.RunbookListResult
	if !liveCall(words, app.CmdRunbookList, &res) {
		return nil
	}
	out := make([]candidate, 0, len(res.Runbooks))
	for _, rb := range res.Runbooks {
		desc := rb.Description
		if rb.Error != "" {
			desc = "does not load: " + rb.Error
		}
		out = append(out, candidate{rb.Name, desc})
	}
	return out
}

func liveThemes(words []string) []candidate {
	var res app.ThemeListResult
	if !liveCall(words, app.CmdThemeList, &res) {
		return nil
	}
	out := make([]candidate, 0, len(res.Themes))
	for _, t := range res.Themes {
		desc := t.Label
		if t.Name == res.Active {
			desc = strings.TrimPrefix(desc+" · active", " · ")
		}
		out = append(out, candidate{t.Name, desc})
	}
	return out
}

// liveDetachableHosts offers the cathost ids detach-host will accept: the roster
// minus the synthesized local host, which is always refused (it is this
// machine's own cathost and the fallback every other host's panes land on).
//
// The kind is named for that filter rather than for the roster, because a
// completion that offers a value the command cannot take is worse than one that
// offers nothing. A future slot that wants the WHOLE roster — a --host argument,
// say — should add its own kind rather than widen this one.
//
// Each candidate is described the way the roster reads: the label when it says
// something the id does not, then how many panes would have to be re-homed —
// the one fact that decides whether the command needs "force".
func liveDetachableHosts(words []string) []candidate {
	var res app.HostListResult
	if !liveCall(words, app.CmdHostList, &res) {
		return nil
	}
	out := make([]candidate, 0, len(res.Hosts))
	for _, h := range res.Hosts {
		if h.Local {
			continue
		}
		desc := h.Label
		if desc == h.ID {
			desc = ""
		}
		parts := []string{}
		if desc != "" {
			parts = append(parts, desc)
		}
		if h.Panes > 0 {
			parts = append(parts, fmt.Sprintf("%d panes", h.Panes))
		}
		if !h.Connected {
			parts = append(parts, "offline")
		}
		out = append(out, candidate{h.ID, strings.Join(parts, " · ")})
	}
	return out
}

// --- plugin-declared binaries --------------------------------------------------

// completeForBinary answers for a command name a plugin claims (--for). Static
// entries are served from the manifest; dynamic ones exec the plugin's own
// completer and pass its output through.
func completeForBinary(binary string, words []string) int {
	bc, ok := plugin.LookupCompletion(binary)
	if !ok {
		fmt.Println(dirNoFiles) // the plugin was uninstalled since the shell started
		return 0
	}
	if bc.Completion.Dynamic() {
		emitLines(delegateCompletion(bc, words))
		return 0
	}

	cur := words[len(words)-1]
	pos := positionals(words[:len(words)-1], nil)
	var cands []candidate
	switch {
	case strings.HasPrefix(cur, "-"):
		for _, f := range bc.Completion.Flags {
			cands = append(cands, candidate{value: f})
		}
	case len(pos) == 0:
		for _, s := range bc.Completion.Subcommands {
			cands = append(cands, candidate{value: s})
		}
	}
	emit(filter(cands, cur), dirNoFiles)
	return 0
}

// delegateCompletion runs a plugin's own completer and returns its output lines,
// normalized: truncated to whole lines within the byte cap, and guaranteed to
// end in exactly one directive. The shell is blocked on us while this runs, so a
// plugin that hangs, crashes, or floods costs a bounded pause and an empty menu
// rather than a broken prompt.
func delegateCompletion(bc plugin.BinaryCompletion, words []string) []string {
	argv := plugin.CompletionArgv(bc.Plugin, bc.Completion)
	args := append(append([]string{}, argv[1:]...), words...)

	ctx, cancel := context.WithTimeout(context.Background(), pluginTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], args...)
	// The same identity a launched action gets, so a completer can find its own
	// assets. The cwd is inherited from the shell — a plugin like cats-todo
	// completes against the project the user is standing in.
	cmd.Env = append(os.Environ(),
		plugin.IDEnvVar+"="+bc.Plugin.ID,
		plugin.DirPathEnvVar+"="+bc.Plugin.Dir)
	out := &cappedWriter{max: pluginMaxOutput}
	cmd.Stdout = out
	err := cmd.Run()

	lines := strings.Split(out.buf.String(), "\n")
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		lines = lines[:len(lines)-1] // a partial final line: truncated, or unterminated
	}
	if err != nil && len(lines) == 0 {
		return []string{dirNoFiles}
	}
	kept := make([]string, 0, len(lines)+1)
	directive := dirNoFiles
	for _, l := range lines {
		if strings.HasPrefix(l, ":") {
			directive = l
			continue
		}
		if l != "" {
			kept = append(kept, l)
		}
	}
	return append(kept, directive)
}

// cappedWriter buffers at most max bytes and silently drops the rest. Write
// always reports full consumption: the overflow is our policy, not the child's
// error, and reporting a short write would only make it die noisily.
type cappedWriter struct {
	buf bytes.Buffer
	max int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	n := len(p) // the count to report, before any truncation below
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		w.buf.Write(p)
	}
	return n, nil
}

// --- shared helpers -------------------------------------------------------------

// positionals drops flags (and the values of the value-taking ones it knows)
// from a word list, leaving the operands. Slot counting runs on the result, so
// `catctl --json focus <TAB>` and `catctl focus <TAB>` complete alike.
func positionals(words []string, flags []flagSpec) []string {
	var out []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") || w == "-" {
			out = append(out, w)
			continue
		}
		if fs, ok := lookupFlag(flags, w); ok && fs.takesVal && !strings.Contains(w, "=") {
			i++ // skip the value
		}
	}
	return out
}

// firstPositional reports where the first operand sits in a word list — the
// verb's own index, which the caller needs in order to hand the family that
// follows an untouched tail.
func firstPositional(words []string, flags []flagSpec) (int, bool) {
	for i := 0; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") || w == "-" {
			return i, true
		}
		if fs, ok := lookupFlag(flags, w); ok && fs.takesVal && !strings.Contains(w, "=") {
			i++
		}
	}
	return 0, false
}

// lookupFlag matches a word against a flag table, accepting Go's single- and
// double-dash spellings and the --flag=value form.
func lookupFlag(flags []flagSpec, word string) (flagSpec, bool) {
	name, _, _ := strings.Cut(word, "=")
	name = "--" + strings.TrimLeft(name, "-")
	for _, f := range flags {
		if f.name == name {
			return f, true
		}
	}
	return flagSpec{}, false
}

func flagCandidates(flags []flagSpec) []candidate {
	out := make([]candidate, 0, len(flags))
	for _, f := range flags {
		out = append(out, candidate{f.name, f.desc})
	}
	return out
}

// filter keeps the candidates the typed prefix allows. Doing it here rather than
// in the shell keeps every shell's behaviour identical and lets bash insert our
// list verbatim.
func filter(cands []candidate, cur string) []candidate {
	if cur == "" {
		return cands
	}
	out := make([]candidate, 0, len(cands))
	for _, c := range cands {
		if strings.HasPrefix(c.value, cur) {
			out = append(out, c)
		}
	}
	return out
}

// emit writes the candidate list and its terminating directive.
func emit(cands []candidate, directive string) {
	var b strings.Builder
	for _, c := range cands {
		b.WriteString(c.value)
		if d := sanitizeDesc(c.desc); d != "" {
			b.WriteByte('\t')
			b.WriteString(d)
		}
		b.WriteByte('\n')
	}
	b.WriteString(directive)
	b.WriteByte('\n')
	os.Stdout.WriteString(b.String())
}

// emitLines writes already-formatted protocol lines (a delegated completer's).
func emitLines(lines []string) {
	os.Stdout.WriteString(strings.Join(lines, "\n") + "\n")
}

// sanitizeDesc flattens a description to one short field: a live terminal title
// or cwd is arbitrary text, and a stray tab or newline in it would be read as a
// new candidate.
func sanitizeDesc(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 72 {
		s = string(r[:71]) + "…"
	}
	return s
}
