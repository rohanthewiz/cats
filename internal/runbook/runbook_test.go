package runbook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parse is the happy-path helper: a document that must load.
func parse(t *testing.T, src string) *Runbook {
	t.Helper()
	rb, err := Parse([]byte(src), "stem", "t.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return rb
}

// parseErr asserts a document is refused, and that the message says why in
// words the author can act on.
func parseErr(t *testing.T, src, want string) {
	t.Helper()
	_, err := Parse([]byte(src), "stem", "t.yaml")
	if err == nil {
		t.Fatalf("expected an error mentioning %q, got none", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not mention %q", err, want)
	}
}

func TestParseMinimal(t *testing.T) {
	rb := parse(t, `
steps:
  - run: pane.last
`)
	if rb.Name != "stem" {
		t.Errorf("name = %q, want the file stem", rb.Name)
	}
	if len(rb.Steps) != 1 || rb.Steps[0].Run != "pane.last" {
		t.Fatalf("steps = %+v", rb.Steps)
	}
}

func TestParseNameFromDocumentWins(t *testing.T) {
	rb := parse(t, `
name: morning
description: the three panes
steps:
  - run: pane.last
`)
	if rb.Name != "morning" || rb.Description != "the three panes" {
		t.Fatalf("got %q / %q", rb.Name, rb.Description)
	}
}

// The load-time checks are the point of the package: every one of these would
// otherwise be discovered after earlier steps had already changed the session.
func TestParseRejects(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"no steps", "name: x\n", "at least one step"},
		{"no run", "steps:\n  - id: a\n", "needs a `run:`"},
		{"unknown command", "steps:\n  - run: pane.explode\n", "is not a command"},
		{"nested runbook", "steps:\n  - run: runbook.run\n    params: {name: other}\n", "cannot run a runbook"},
		{"missing required params", "steps:\n  - run: pane.focus\n", "requires params"},
		{"duplicate id", `
steps:
  - run: tab.create
    id: a
  - run: tab.create
    id: a
`, "already used"},
		{"id on a command returning nothing", `
steps:
  - run: pane.last
    id: a
`, "returns no data"},
		{"forward reference", `
steps:
  - run: pane.focus
    params: {pane: "{{ later.pane }}"}
  - run: tab.create
    id: later
`, "no earlier step binds"},
		{"undeclared var", `
steps:
  - run: pane.focus
    params: {pane: "{{ vars.who }}"}
`, "no earlier step binds"},
		{"unclosed reference", `
steps:
  - run: pane.rename
    params: {pane: 1, name: "{{ vars.x"}
`, "unclosed reference"},
		{"reserved id", `
vars: {x: "1"}
steps:
  - run: tab.create
    id: vars
`, "reserved"},
		{"bad name", "name: 'has space'\nsteps:\n  - run: pane.last\n", "must be a non-empty run"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { parseErr(t, c.src, c.want) })
	}
}

// A declared var makes the reference legal; that is the whole reason vars are
// declared rather than free-form.
func TestParseDeclaredVarUnlocksReference(t *testing.T) {
	rb := parse(t, `
vars:
  who: "1"
steps:
  - run: pane.focus
    params: {pane: "{{ vars.who }}"}
`)
	if len(rb.Steps[0].refs) != 1 {
		t.Fatalf("refs = %+v", rb.Steps[0].refs)
	}
}

func TestParseBackwardReferenceIsFine(t *testing.T) {
	parse(t, `
steps:
  - run: tab.create
    id: api
  - run: pane.send_input
    params: {pane: "{{ api.pane }}", text: "ls\n"}
`)
}

func TestMaxSteps(t *testing.T) {
	var b strings.Builder
	b.WriteString("steps:\n")
	for range maxSteps + 1 {
		b.WriteString("  - run: pane.last\n")
	}
	parseErr(t, b.String(), "exceeds the")
}

// --- reference resolution --------------------------------------------------------

func TestResolveWholeStringKeepsType(t *testing.T) {
	b := Bindings{}
	if err := b.Bind("api", map[string]any{"pane": 3}); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(map[string]any{"pane": "{{ api.pane }}"}, b)
	if err != nil {
		t.Fatal(err)
	}
	// json.Unmarshal into `any` gives float64; what matters is that it is a
	// NUMBER, not the string "3" — Pane is a uint32 on the wire.
	if _, ok := got["pane"].(float64); !ok {
		t.Fatalf("pane = %#v (%T), want a number", got["pane"], got["pane"])
	}
}

func TestResolveEmbeddedInterpolates(t *testing.T) {
	b := Bindings{}
	if err := b.Bind("api", map[string]any{"pane": 3}); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(map[string]any{"text": "echo pane {{ api.pane }} ready"}, b)
	if err != nil {
		t.Fatal(err)
	}
	// "3", never "3.0": the value is going into a shell command line.
	if got["text"] != "echo pane 3 ready" {
		t.Fatalf("text = %q", got["text"])
	}
}

func TestResolveNested(t *testing.T) {
	b := Bindings{"vars": map[string]any{"branch": "main"}}
	got, err := Resolve(map[string]any{
		"a": []any{"{{ vars.branch }}", "x"},
		"b": map[string]any{"c": "on {{ vars.branch }}"},
	}, b)
	if err != nil {
		t.Fatal(err)
	}
	if got["a"].([]any)[0] != "main" {
		t.Errorf("list = %#v", got["a"])
	}
	if got["b"].(map[string]any)["c"] != "on main" {
		t.Errorf("map = %#v", got["b"])
	}
}

func TestResolveMissingFieldNamesTheHop(t *testing.T) {
	b := Bindings{}
	_ = b.Bind("api", map[string]any{"pane": 3})
	_, err := Resolve(map[string]any{"x": "{{ api.tab }}"}, b)
	if err == nil || !strings.Contains(err.Error(), `has no field "tab"`) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "api") {
		t.Fatalf("error %q does not say which value was walked", err)
	}
}

func TestResolveListIndex(t *testing.T) {
	b := Bindings{}
	_ = b.Bind("l", map[string]any{"panes": []any{map[string]any{"id": 7}}})
	got, err := Resolve(map[string]any{"pane": "{{ l.panes.0.id }}"}, b)
	if err != nil {
		t.Fatal(err)
	}
	if got["pane"].(float64) != 7 {
		t.Fatalf("pane = %#v", got["pane"])
	}
}

func TestResolveListIndexOutOfRange(t *testing.T) {
	b := Bindings{}
	_ = b.Bind("l", map[string]any{"panes": []any{}})
	_, err := Resolve(map[string]any{"pane": "{{ l.panes.0 }}"}, b)
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("err = %v", err)
	}
}

// --- directory loading -----------------------------------------------------------

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSortsAndReportsBroken(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "zeta.yaml", "steps:\n  - run: pane.last\n")
	write(t, dir, "alpha.yaml", "steps:\n  - run: pane.last\n")
	write(t, dir, "bad.yaml", "steps:\n  - run: pane.explode\n")
	write(t, dir, "notes.txt", "ignored")

	set := Load(dir)
	if len(set.Books) != 2 || set.Books[0].Name != "alpha" || set.Books[1].Name != "zeta" {
		t.Fatalf("books = %+v", set.Books)
	}
	if len(set.Broken) != 1 || !strings.Contains(set.Broken[0].Err.Error(), "is not a command") {
		t.Fatalf("broken = %+v", set.Broken)
	}
}

// A broken file is reachable by name, and answers with its parse error rather
// than "no such runbook" — the user did write it.
func TestGetBrokenExplainsItself(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bad.yaml", "steps:\n  - run: pane.explode\n")
	_, err := Load(dir).Get("bad")
	if err == nil || !strings.Contains(err.Error(), "is not a command") {
		t.Fatalf("err = %v", err)
	}
}

func TestGetUnknown(t *testing.T) {
	_, err := Load(t.TempDir()).Get("nope")
	if err == nil || !strings.Contains(err.Error(), "no runbook named nope") {
		t.Fatalf("err = %v", err)
	}
}

// Two files claiming one name is a conflict, not a race: the loser is reported
// so `runbook run deploy` cannot quietly mean different things on two machines.
func TestDuplicateNameIsBroken(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", "name: deploy\nsteps:\n  - run: pane.last\n")
	write(t, dir, "b.yaml", "name: deploy\nsteps:\n  - run: pane.last\n")
	set := Load(dir)
	if len(set.Books) != 1 {
		t.Fatalf("books = %+v", set.Books)
	}
	if len(set.Broken) != 1 || !strings.Contains(set.Broken[0].Err.Error(), "already defined by") {
		t.Fatalf("broken = %+v", set.Broken)
	}
}

func TestLoadMissingDirIsEmpty(t *testing.T) {
	set := Load(filepath.Join(t.TempDir(), "absent"))
	if len(set.Books) != 0 || len(set.Broken) != 0 {
		t.Fatalf("got %+v", set)
	}
}

// --- unknown params --------------------------------------------------------------

// A key the params struct has no field for is DROPPED by encoding/json, so the
// command runs with that field at its zero value and reports success. That is
// the quietest way a runbook can do the wrong thing, so it is a load error.
func TestParseRejectsUnknownParamKey(t *testing.T) {
	parseErr(t, `
steps:
  - run: pane.wait_for_output
    params: {pane: 1, pattern: x, timeout_secs: 10}
`, `has no param "timeout_secs"`)
}

func TestUnknownParamErrorListsTheRealOnes(t *testing.T) {
	_, err := Parse([]byte(`
steps:
  - run: pane.wait_for_output
    params: {pane: 1, pattern: x, timeout_secs: 10}
`), "stem", "t.yaml")
	if err == nil || !strings.Contains(err.Error(), "timeout_ms") {
		t.Fatalf("error %q does not offer the correct key", err)
	}
}

func TestParseAcceptsEveryRealParamKey(t *testing.T) {
	parse(t, `
steps:
  - run: pane.wait_for_output
    params: {pane: 1, pattern: x, regex: true, timeout_ms: 5000, lines: 40}
`)
}

// Nested objects are checked too, and the error says which one.
func TestParseRejectsUnknownNestedParamKey(t *testing.T) {
	parseErr(t, `
steps:
  - run: ui.notify
    params:
      title: hi
      actions:
        - {id: a, label: Go, sned: "y"}
`, `has no param "sned"`)
}

func TestParseAcceptsNestedParams(t *testing.T) {
	parse(t, `
steps:
  - run: ui.notify
    params:
      title: hi
      actions:
        - {id: a, label: Go, send: "y", submit: true}
`)
}

func TestParamsOnAParameterlessCommand(t *testing.T) {
	parseErr(t, "steps:\n  - run: pane.last\n    params: {x: 1}\n", "takes no params")
}

// --- expect ----------------------------------------------------------------------

func TestExpectRequiresAnID(t *testing.T) {
	parseErr(t, `
steps:
  - run: pane.list
    expect: "{{ p.panes }}"
`, "expect needs an `id:`")
}

func TestExpectMustBeOneReference(t *testing.T) {
	parseErr(t, `
steps:
  - run: pane.list
    id: p
    expect: "got {{ p.panes }} panes"
`, "exactly one reference")
}

func TestExpectMayNameItsOwnStep(t *testing.T) {
	rb := parse(t, `
steps:
  - run: pane.wait_for_output
    params: {pane: 1, pattern: done}
    id: w
    expect: "{{ w.matched }}"
`)
	if rb.Steps[0].expectRef == nil {
		t.Fatal("expect was not parsed")
	}
}

func TestExpectUnknownRoot(t *testing.T) {
	parseErr(t, `
steps:
  - run: pane.list
    id: p
    expect: "{{ other.x }}"
`, "neither this step's id nor anything bound earlier")
}

// The case the whole field exists for: a command that succeeded while saying
// the thing did not happen.
func TestCheckExpectFalseFails(t *testing.T) {
	rb := parse(t, `
steps:
  - run: pane.wait_for_output
    params: {pane: 1, pattern: done}
    id: w
    expect: "{{ w.matched }}"
`)
	b := Bindings{}
	if err := b.Bind("w", map[string]any{"matched": false}); err != nil {
		t.Fatal(err)
	}
	err := rb.Steps[0].CheckExpect(b)
	if err == nil || !strings.Contains(err.Error(), "was false") {
		t.Fatalf("err = %v", err)
	}

	_ = b.Bind("w", map[string]any{"matched": true})
	if err := rb.Steps[0].CheckExpect(b); err != nil {
		t.Fatalf("a matched wait must pass: %v", err)
	}
}

func TestCheckExpectTruthiness(t *testing.T) {
	rb := parse(t, `
steps:
  - run: pane.list
    id: p
    expect: "{{ p.panes }}"
`)
	cases := []struct {
		name string
		val  any
		ok   bool
	}{
		{"empty list", []any{}, false},
		{"one entry", []any{1}, true},
		{"zero", 0, false},
		{"number", 3, true},
		{"empty string", "", false},
		{"string", "x", true},
		{"null", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := Bindings{}
			if err := b.Bind("p", map[string]any{"panes": c.val}); err != nil {
				t.Fatal(err)
			}
			err := rb.Steps[0].CheckExpect(b)
			if (err == nil) != c.ok {
				t.Fatalf("value %#v gave err=%v, want ok=%v", c.val, err, c.ok)
			}
		})
	}
}

// Asserting on a field that does not exist is a mistake in the runbook, and
// says so rather than being read as "false".
func TestCheckExpectMissingFieldIsAnError(t *testing.T) {
	rb := parse(t, `
steps:
  - run: pane.list
    id: p
    expect: "{{ p.nope }}"
`)
	b := Bindings{}
	_ = b.Bind("p", map[string]any{"panes": []any{}})
	err := rb.Steps[0].CheckExpect(b)
	if err == nil || !strings.Contains(err.Error(), `has no field "nope"`) {
		t.Fatalf("err = %v", err)
	}
}
