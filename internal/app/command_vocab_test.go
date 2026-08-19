package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// These tests guard the §7 command table (commandSpecs) as the machine-readable
// truth about the vocabulary: cmd/catgen-dart emits a typed client from it, so a
// wrong or missing entry is not a stale comment — it is a client method that
// does not exist, or one whose signature lies about what comes back.
//
// Three kinds of check, deliberately separate:
//
//   - TestCommandSpecsRouted reads the DISPATCHER'S OWN SOURCE, so the table and
//     the switch cannot drift in either direction.
//   - TestCommandSpecsParamsRequired / …ReplyRequired drive the real dispatcher
//     and assert the two behavioural flags actually describe what it does.
//   - TestCommandSpecsResultTypes checks the declared result type against the
//     value that comes back, for every command the fakes can carry to completion.

// --- source-level: the table, the constants and the switch agree --------------

// Every Cmd* constant must be listed in commandSpecs AND routed by Dispatch, and
// nothing may be routed or specced that is not a declared constant. The existing
// TestCommandNamesAllRouted covers one direction by dispatching each name and
// watching for the "not supported yet" default; it cannot see the other one — a
// command added to the switch but never listed is invisible to `catctl commands`
// and absent from every generated client, with no test failing. Reading the
// switch's AST closes that side.
func TestCommandSpecsRouted(t *testing.T) {
	declared := parseCommandConsts(t, "command_vocab.go") // wire name -> Go constant name
	routed := parseDispatchCases(t, "commands.go", declared)

	specced := make(map[string]bool, len(commandSpecs))
	for _, spec := range commandSpecs {
		if specced[spec.Name] {
			t.Errorf("command %q is listed twice in commandSpecs", spec.Name)
		}
		specced[spec.Name] = true
	}

	for name, constName := range declared {
		if !specced[name] {
			t.Errorf("%s (%q) is declared but missing from commandSpecs — it will not reach `catctl commands` or any generated client", constName, name)
		}
		if !routed[name] {
			t.Errorf("%s (%q) is declared but has no case in Dispatch", constName, name)
		}
	}
	for name := range routed {
		if !specced[name] {
			t.Errorf("%q has a Dispatch case but no commandSpecs entry", name)
		}
	}
	for name := range specced {
		if _, ok := declared[name]; !ok {
			t.Errorf("commandSpecs lists %q, which is not a Cmd* constant", name)
		}
	}
}

// parseCommandConsts extracts the Cmd* command-name constants from a source file
// as wire name -> Go constant name. Only `CmdX = "some.name"` string constants
// qualify, which skips the neighbouring wire-value constants (SplitH, DirLeft)
// that share the same const blocks but are not command names.
func parseCommandConsts(t *testing.T, file string) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	out := map[string]string{}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, s := range gen.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			if !strings.HasPrefix(vs.Names[0].Name, "Cmd") {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", vs.Names[0].Name, err)
			}
			out[val] = vs.Names[0].Name
		}
	}
	if len(out) == 0 {
		t.Fatalf("no Cmd* constants found in %s — the parser is looking in the wrong place", file)
	}
	return out
}

// parseDispatchCases returns the set of wire names the Dispatch switch has a
// case for. It takes the first switch statement in Dispatch's body, which is the
// command switch (the function has no other), and resolves each case's
// identifier through the constant table so the test never re-lists a wire string.
func parseDispatchCases(t *testing.T, file string, consts map[string]string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	byConst := make(map[string]string, len(consts)) // Go constant name -> wire name
	for wire, name := range consts {
		byConst[name] = wire
	}

	var sw *ast.SwitchStmt
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		// The table is in `dispatch`, the unexported half of Dispatch — the
		// exported one is the recorder wrapper (record.go) and holds no cases.
		if !ok || fn.Name.Name != "dispatch" || fn.Recv == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if sw != nil {
				return false
			}
			if s, ok := n.(*ast.SwitchStmt); ok {
				sw = s
				return false
			}
			return true
		})
	}
	if sw == nil {
		t.Fatalf("no switch statement found in dispatch (%s)", file)
	}

	out := map[string]bool{}
	for _, stmt := range sw.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok || cc.List == nil {
			continue // the default clause
		}
		for _, expr := range cc.List {
			id, ok := expr.(*ast.Ident)
			if !ok {
				t.Errorf("Dispatch has a non-identifier case %T — the vocabulary must be reachable through a Cmd* constant", expr)
				continue
			}
			wire, ok := byConst[id.Name]
			if !ok {
				t.Errorf("Dispatch cases on %s, which is not a Cmd* command constant", id.Name)
				continue
			}
			out[wire] = true
		}
	}
	return out
}

// --- shape: what a generator will read off the table --------------------------

// A spec's Params/Result must be a plain struct value or nil. A pointer or a
// slice would still compile and still marshal, but a generator emitting a Dart
// class from reflect.TypeOf would produce nonsense from it, and the nil that
// means "no params" would become indistinguishable from a typed nil pointer.
func TestCommandSpecsShape(t *testing.T) {
	for _, spec := range commandSpecs {
		if spec.Name == "" {
			t.Fatalf("commandSpecs has an entry with no name")
		}
		for _, f := range []struct {
			what string
			v    any
		}{{"Params", spec.Params}, {"Result", spec.Result}} {
			if f.v == nil {
				continue
			}
			if k := reflect.TypeOf(f.v).Kind(); k != reflect.Struct {
				t.Errorf("%s: %s is a %s, want a struct value or nil", spec.Name, f.what, k)
			}
		}
		if spec.ReplyRequired && spec.Result == nil {
			t.Errorf("%s: ReplyRequired with no Result — a command dropped for want of a reply channel must have something to say", spec.Name)
		}
	}
}

// CommandNames is derived from the table, and CommandSpecs hands out a copy:
// a caller reordering what it got back must not reorder the vocabulary.
func TestCommandSpecsCopyAndNames(t *testing.T) {
	names := CommandNames()
	specs := CommandSpecs()
	if len(names) != len(specs) {
		t.Fatalf("CommandNames has %d entries, CommandSpecs %d", len(names), len(specs))
	}
	for i := range specs {
		if names[i] != specs[i].Name {
			t.Fatalf("order drift at %d: name %q, spec %q", i, names[i], specs[i].Name)
		}
	}

	first := specs[0].Name
	specs[0] = CommandSpec{Name: "clobbered"}
	if got := CommandSpecs()[0].Name; got != first {
		t.Fatalf("CommandSpecs shares its backing array: first entry is now %q, want %q", got, first)
	}
}

// --- behaviour: the two flags describe what Dispatch actually does ------------

// specParams supplies valid params for the commands that need them before the
// dispatcher will get far enough to exercise a flag. Only the reply-gated ones
// matter: everything else either takes optional params or is expected to fail on
// the missing ones, which is itself the assertion.
var specParams = map[string]any{
	CmdRead:          ReadParams{Pane: 1, Cursor: [2]uint32{0, 4}},
	CmdCapture:       CaptureParams{Pane: 1},
	CmdWaitForOutput: WaitForOutputParams{Pane: 1, Pattern: "ok"},
	CmdFileStat:      FileStatParams{Path: "/tmp/x"},
	CmdFileGet:       FileGetParams{Path: "/tmp/x"},
	CmdFilePut:       FilePutParams{Path: "/tmp/x", Data: []byte("hi")},
}

func specDecoder(t *testing.T, name string) jsonDec {
	t.Helper()
	if p, ok := specParams[name]; ok {
		return params(t, p)
	}
	return noParams()
}

// Handed no params, exactly the ParamsRequired commands must fail with the
// "bad params" wording. The others decode optionally — the zero value is a real
// call for them (pane.close closes the focused pane) — so whatever they do next,
// it is not a params complaint.
func TestCommandSpecsParamsRequired(t *testing.T) {
	const badPrefix = "bad params"
	for _, spec := range commandSpecs {
		t.Run(spec.Name, func(t *testing.T) {
			h := newCmdHarness(t) // fresh session per command; order-independent
			r := h.resp()
			h.d.Dispatch(spec.Name, noParams(), r)

			complained := r.failCall && strings.HasPrefix(r.errMsg, badPrefix)
			if spec.ParamsRequired && !complained {
				t.Errorf("ParamsRequired, but no-params dispatch did not fail with %q (fail=%v msg=%q)", badPrefix, r.failCall, r.errMsg)
			}
			if !spec.ParamsRequired && complained {
				t.Errorf("not ParamsRequired, but no-params dispatch failed with %q", r.errMsg)
			}
		})
	}
}

// The silently-dropped rule, from the outside: given a caller that cannot
// receive a result, exactly the ReplyRequired commands do NOTHING — no backend
// effect, no reply, no pending round-trip registered. Every other command still
// runs, which is what makes the flag a distinction rather than a label.
//
// The shared log carries both halves (backend effects and replies), so "nothing
// happened" is one assertion over it.
func TestCommandSpecsReplyRequired(t *testing.T) {
	for _, spec := range commandSpecs {
		t.Run(spec.Name, func(t *testing.T) {
			h := newCmdHarness(t)
			r := &fakeResponder{log: h.log, wants: false}
			h.d.Dispatch(spec.Name, specDecoder(t, spec.Name), r)

			switch {
			case spec.ReplyRequired && len(*h.log) != 0:
				t.Errorf("ReplyRequired, but a reply-less dispatch still did %v", *h.log)
			case !spec.ReplyRequired && len(*h.log) == 0:
				t.Errorf("not ReplyRequired, but a reply-less dispatch did nothing at all")
			}
		})
	}
}

// A result that comes back must have the type the spec advertises. This reaches
// only the commands the fakes carry to completion — the queries, the creates,
// and the config/theme reads the fakeBackend answers inline. The results the
// real backend produces asynchronously (read/capture/wait, worktree.*,
// plugin.*, path.list) are dispatched into the fake and never resolved, so they
// are skipped here rather than asserted against a value that does not exist.
func TestCommandSpecsResultTypes(t *testing.T) {
	checked := 0
	for _, spec := range commandSpecs {
		t.Run(spec.Name, func(t *testing.T) {
			h := newCmdHarness(t)
			r := h.resp()
			h.d.Dispatch(spec.Name, specDecoder(t, spec.Name), r)
			if !r.okCall || r.data == nil {
				return // failed, or resolved with no data — nothing to compare
			}
			if spec.Result == nil {
				t.Fatalf("returned %T, but the spec declares no Result", r.data)
			}
			if got, want := reflect.TypeOf(r.data), reflect.TypeOf(spec.Result); got != want {
				t.Fatalf("returned %s, spec declares %s", got, want)
			}
			checked++
		})
	}
	// A guard on the guard: if the harness stopped carrying commands to
	// completion this test would pass by checking nothing at all.
	if checked < 8 {
		t.Errorf("only %d result types were actually compared — the harness is no longer resolving commands", checked)
	}
}
