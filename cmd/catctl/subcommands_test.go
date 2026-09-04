package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/ctlproto"
)

// buildOK runs a verb's builder and unmarshals its params into want's type,
// asserting the result equals want. A nil want means the builder must emit no
// params (a no-params command).
func buildOK[T any](t *testing.T, verb string, args []string, want T) {
	t.Helper()
	sc, ok := lookupSubcommand(verb)
	if !ok {
		t.Fatalf("no such verb %q", verb)
	}
	raw, err := sc.build(args)
	if err != nil {
		t.Fatalf("%s %v: unexpected error %v", verb, args, err)
	}
	// An optional command with all-default operands emits no params at all; the
	// dispatcher treats that as the zero value, so want must be the zero too.
	if len(raw) == 0 {
		var zero T
		if mustJSON(t, want) != mustJSON(t, zero) {
			t.Fatalf("%s %v: builder emitted no params, but want %s", verb, args, mustJSON(t, want))
		}
		return
	}
	var got T
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s %v: params %s not a %T: %v", verb, args, raw, got, err)
	}
	if b, _ := json.Marshal(got); string(b) != mustJSON(t, want) {
		t.Fatalf("%s %v: params = %s, want %s", verb, args, b, mustJSON(t, want))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	return string(b)
}

// buildErr asserts a verb's builder rejects the given args.
func buildErr(t *testing.T, verb string, args []string) {
	t.Helper()
	sc, ok := lookupSubcommand(verb)
	if !ok {
		t.Fatalf("no such verb %q", verb)
	}
	if raw, err := sc.build(args); err == nil {
		t.Fatalf("%s %v: expected error, got params %s", verb, args, raw)
	}
}

func u32(v uint32) *uint32 { return &v }

// Split: direction defaults to h, an optional pane becomes a pointer, bad
// direction and extra args are rejected.
func TestBuildSplit(t *testing.T) {
	buildOK(t, "split", nil, app.SplitParams{Direction: "h"})
	buildOK(t, "split", []string{"v"}, app.SplitParams{Direction: "v"})
	buildOK(t, "split", []string{"h", "2"}, app.SplitParams{Direction: "h", Pane: u32(2)})
	buildErr(t, "split", []string{"diagonal"})
	buildErr(t, "split", []string{"h", "notanumber"})
	buildErr(t, "split", []string{"h", "2", "3"})
}

// Required vs optional pane operands.
func TestBuildPaneOperands(t *testing.T) {
	buildOK(t, "focus", []string{"3"}, app.PaneParams{Pane: 3})
	buildErr(t, "focus", nil) // pane required
	buildErr(t, "focus", []string{"1", "2"})

	buildOK(t, "close", nil, app.OptPaneParams{}) // focused
	buildOK(t, "close", []string{"5"}, app.OptPaneParams{Pane: u32(5)})
	buildErr(t, "close", []string{"1", "2"})
}

// Direction verbs validate against the cardinal set.
func TestBuildDir(t *testing.T) {
	buildOK(t, "focus-dir", []string{"left"}, app.DirParams{Dir: "left"})
	buildOK(t, "swap", []string{"up"}, app.DirParams{Dir: "up"})
	buildErr(t, "swap", []string{"sideways"})
	buildErr(t, "focus-dir", nil)
}

// cycle: next by default, prev flips it.
func TestBuildCycle(t *testing.T) {
	buildOK(t, "cycle", nil, app.CycleParams{Next: true})
	buildOK(t, "cycle", []string{"next"}, app.CycleParams{Next: true})
	buildOK(t, "cycle", []string{"prev"}, app.CycleParams{Next: false})
	buildErr(t, "cycle", []string{"sideways"})
}

// Multi-word names join the remaining args; an empty name clears.
func TestBuildRename(t *testing.T) {
	buildOK(t, "rename-pane", []string{"2", "build", "server"},
		app.RenamePaneParams{Pane: 2, Name: "build server"})
	buildOK(t, "rename-pane", []string{"2", ""}, app.RenamePaneParams{Pane: 2, Name: ""})
	buildErr(t, "rename-pane", []string{"2"}) // name required
	buildOK(t, "rename-ws", []string{"w1", "front", "end"},
		app.RenameWorkspaceParams{ID: "w1", Name: "front end"})
}

// flag / unflag and their -ws pair. The note is the variadic tail, so quoting it
// is optional; the kind is passed through unvalidated, because flags.ParseKind
// on the server is what refuses a typo — one vocabulary, one error message,
// identical for the CLI and the browser.
func TestBuildFlag(t *testing.T) {
	buildOK(t, "flag", []string{"2", "followup", "waiting", "on", "review"},
		app.FlagPaneParams{Pane: 2, Kind: "followup", Note: "waiting on review"})
	buildOK(t, "flag", []string{"2", "star"}, app.FlagPaneParams{Pane: 2, Kind: "star"})
	// A glyph is just another kind here; only the server knows the difference.
	buildOK(t, "flag", []string{"2", "🍕", "lunch"}, app.FlagPaneParams{Pane: 2, Kind: "🍕", Note: "lunch"})
	buildErr(t, "flag", []string{"2"})         // a kind is required
	buildErr(t, "flag", []string{"x", "star"}) // and the pane must parse

	buildOK(t, "unflag", []string{"2"}, app.FlagPaneParams{Pane: 2})
	buildErr(t, "unflag", nil)
	buildErr(t, "unflag", []string{"2", "3"})

	buildOK(t, "flag-ws", []string{"w1", "warn", "flaky", "tests"},
		app.FlagWorkspaceParams{ID: "w1", Kind: "warn", Note: "flaky tests"})
	buildErr(t, "flag-ws", []string{"w1"}) // the id does NOT stand in for the kind

	// Clearing defaults to the active workspace, the unlock-ws shape: one
	// argument, so there is no id/kind collision to disambiguate.
	buildOK(t, "unflag-ws", nil, app.FlagWorkspaceParams{})
	buildOK(t, "unflag-ws", []string{"w2"}, app.FlagWorkspaceParams{ID: "w2"})
	buildErr(t, "unflag-ws", []string{"w2", "extra"})
}

// clean-ws / sleep-ws: an optional id first, then the agent mode; wake-ws
// takes the id it cannot guess.
func TestBuildCleanWorkspace(t *testing.T) {
	for _, verb := range []string{"clean-ws", "sleep-ws"} {
		buildOK(t, verb, nil, app.CleanWorkspaceParams{})
		buildOK(t, verb, []string{"w2"}, app.CleanWorkspaceParams{ID: "w2"})
		buildOK(t, verb, []string{"park"}, app.CleanWorkspaceParams{Agents: "park"})
		buildOK(t, verb, []string{"w2", "park"}, app.CleanWorkspaceParams{ID: "w2", Agents: "park"})
		buildOK(t, verb, []string{"w2", "run", "/exit"}, app.CleanWorkspaceParams{ID: "w2", Agents: "command", Command: "/exit"})
		buildOK(t, verb, []string{"run", "/compact", "then", "rest"}, app.CleanWorkspaceParams{Agents: "command", Command: "/compact then rest"})
		buildErr(t, verb, []string{"w2", "run"})          // run needs its text
		buildErr(t, verb, []string{"w2", "park", "more"}) // park takes nothing
		buildErr(t, verb, []string{"w2", "w3"})           // one id
	}
	buildOK(t, "wake-ws", []string{"w2"}, app.WorkspaceParams{ID: "w2"})
	buildErr(t, "wake-ws", nil)
}

// new-ws names the workspace when asked and stays a bare no-params command
// otherwise (the shape a key binding sends).
func TestBuildNewWorkspace(t *testing.T) {
	sc, ok := lookupSubcommand("new-ws")
	if !ok {
		t.Fatal("no such verb new-ws")
	}
	raw, err := sc.build(nil)
	if err != nil || raw != nil {
		t.Errorf("new-ws with no args: want (nil, nil), got (%s, %v)", raw, err)
	}
	buildOK(t, "new-ws", []string{"api", "rewrite"}, app.WorkspaceCreateParams{Name: "api rewrite"})
}

// scroll / resize / read parse their numeric operands.
func TestBuildNumeric(t *testing.T) {
	buildOK(t, "scroll", []string{"1", "-10"}, app.ScrollParams{Pane: 1, Delta: -10})
	buildErr(t, "scroll", []string{"1", "down"})
	buildOK(t, "resize", []string{"r0", "0.6"}, app.ResizeBorderParams{Border: "r0", Ratio: 0.6})
	buildErr(t, "resize", []string{"r0", "wide"})
	buildOK(t, "read", []string{"1", "0", "0", "2", "5"},
		app.ReadParams{Pane: 1, Anchor: [2]uint32{0, 0}, Cursor: [2]uint32{2, 5}})
	buildErr(t, "read", []string{"1", "0", "0", "2"}) // needs 5
}

// capture defaults to the whole buffer (recent scope, 0 lines); a line count
// bounds it.
func TestBuildCapture(t *testing.T) {
	buildOK(t, "capture", []string{"1"}, app.CaptureParams{Pane: 1, Scope: 1})
	buildOK(t, "capture", []string{"1", "100"}, app.CaptureParams{Pane: 1, Scope: 1, Lines: 100})
	buildErr(t, "capture", nil)
	buildErr(t, "capture", []string{"1", "lots"})
}

// wait: pane + pattern required; an optional fractional timeout becomes ms.
func TestBuildWait(t *testing.T) {
	buildOK(t, "wait", []string{"1", "ready"}, app.WaitForOutputParams{Pane: 1, Pattern: "ready"})
	buildOK(t, "wait", []string{"2", "BUILD DONE", "30"},
		app.WaitForOutputParams{Pane: 2, Pattern: "BUILD DONE", TimeoutMs: 30000})
	buildOK(t, "wait", []string{"1", "x", "0.5"},
		app.WaitForOutputParams{Pane: 1, Pattern: "x", TimeoutMs: 500})
	buildErr(t, "wait", []string{"1"})              // pattern required
	buildErr(t, "wait", []string{"1", "x", "soon"}) // non-numeric timeout
	buildErr(t, "wait", []string{"1", "x", "2", "3"})
}

// send/run: words join with spaces; send stages (text required), run submits
// with Enter (a bare `run <pane>` is just the Enter, firing staged input).
func TestBuildSendRun(t *testing.T) {
	buildOK(t, "send", []string{"1", "ls", "-la"}, app.SendInputParams{Pane: 1, Text: "ls -la"})
	buildErr(t, "send", nil)
	buildErr(t, "send", []string{"1"})         // text required: staging nothing is a no-op
	buildErr(t, "send", []string{"x", "text"}) // bad pane id

	buildOK(t, "run", []string{"2", "make", "test"},
		app.SendInputParams{Pane: 2, Text: "make test", Submit: true})
	buildOK(t, "run", []string{"3"}, app.SendInputParams{Pane: 3, Submit: true})
	buildErr(t, "run", nil)
	buildErr(t, "run", []string{"x"})
}

// events: optional pane filter.
func TestBuildEvents(t *testing.T) {
	buildOK(t, "events", nil, app.EventsSubscribeParams{})
	buildOK(t, "events", []string{"3"}, app.EventsSubscribeParams{Pane: u32(3)})
	buildErr(t, "events", []string{"1", "2"})
	buildErr(t, "events", []string{"notapane"})
}

// tab.list / tab operands.
func TestBuildTabs(t *testing.T) {
	buildOK(t, "tabs", nil, app.TabListParams{})
	buildOK(t, "tabs", []string{"w1"}, app.TabListParams{Workspace: "w1"})
	buildOK(t, "tab", []string{"2"}, app.TabParams{Num: 2})
	buildErr(t, "tab", []string{"two"})
	buildOK(t, "close-tab", nil, app.OptTabParams{})
	buildOK(t, "rename-tab", []string{"2", "logs"}, app.RenameTabParams{Num: 2, Name: "logs"})
}

// No-params verbs emit no params and reject any argument.
func TestBuildNoParams(t *testing.T) {
	for _, verb := range []string{"session", "panes", "workspaces", "last", "new-tab", "reload", "stop", "ping"} {
		sc, ok := lookupSubcommand(verb)
		if !ok {
			t.Fatalf("no such verb %q", verb)
		}
		raw, err := sc.build(nil)
		if err != nil || raw != nil {
			t.Errorf("%s: want (nil, nil), got (%s, %v)", verb, raw, err)
		}
		buildErr(t, verb, []string{"x"})
	}
}

// Every ergonomic verb maps to a real §7 method (or ping), has a builder, and a
// unique name — the registry can't advertise a verb the server would reject.
func TestSubcommandRegistryIntegrity(t *testing.T) {
	seen := map[string]bool{}
	names := app.CommandNames()
	for _, sc := range subcommands {
		if seen[sc.verb] {
			t.Errorf("duplicate verb %q", sc.verb)
		}
		seen[sc.verb] = true
		if sc.build == nil {
			t.Errorf("verb %q has no builder", sc.verb)
		}
		if !ctlproto.IsTransportMethod(sc.method) && !slices.Contains(names, sc.method) {
			t.Errorf("verb %q maps to unknown method %q", sc.verb, sc.method)
		}
		if sc.synopsis == "" || sc.summary == "" {
			t.Errorf("verb %q missing help text", sc.verb)
		}
	}
}

// A runbook that ran but whose steps failed is a successful command with an
// unsuccessful result. The shell has to be able to tell them apart, or
// `catctl runbook deploy && ./ship.sh` ships after a failed deploy.
func TestRunbookFailedDrivesTheExitCode(t *testing.T) {
	mk := func(v any) ctlproto.Response {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return ctlproto.Response{OK: true, Data: raw}
	}
	if runbookFailed(mk(app.RunbookRunResult{Name: "x"})) {
		t.Error("a clean run must exit 0")
	}
	if !runbookFailed(mk(app.RunbookRunResult{Name: "x", Failed: true})) {
		t.Error("a failed run must exit non-zero")
	}
	// An undecodable payload is not invented into a failure: a catctl-side bug
	// must not become a false alarm in somebody's deploy script.
	if runbookFailed(ctlproto.Response{OK: true, Data: []byte("not json")}) {
		t.Error("an undecodable result must not report failure")
	}
}

// buildRunbook turns the trailing words into vars, and refuses a bare one — a
// second word with no '=' is either a typo or a var whose '=' the shell ate,
// and guessing which would run the wrong runbook.
func TestBuildRunbookVars(t *testing.T) {
	raw, err := buildRunbook([]string{"deploy", "branch=main", "env=staging"})
	if err != nil {
		t.Fatalf("buildRunbook: %v", err)
	}
	var p app.RunbookRunParams
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "deploy" || p.Vars["branch"] != "main" || p.Vars["env"] != "staging" {
		t.Fatalf("params = %+v", p)
	}

	if _, err := buildRunbook([]string{"deploy", "main"}); err == nil {
		t.Error("a bare second word must be refused, not folded into the name")
	}
	if _, err := buildRunbook(nil); err == nil {
		t.Error("no name must be a usage error")
	}
}
