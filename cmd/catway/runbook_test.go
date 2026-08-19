//go:build ghostty

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/layout"
)

// newRunbookOrch builds an orch whose runbook directory is a throwaway one, and
// returns that directory. XDG_CONFIG_HOME is what runbook.UserDir consults, so
// pointing it at a temp dir is enough to isolate the whole feature.
func newRunbookOrch(t *testing.T) (*orch, string) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := filepath.Join(cfg, "cats", "runbooks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	return o, dir
}

func writeRunbook(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// capture is an app.Responder that records the single answer a command gives.
type capture struct {
	data   any
	errMsg string
	calls  int
}

func (c *capture) WantsReply() bool { return true }
func (c *capture) OK(data any)      { c.calls++; c.data = data }
func (c *capture) Fail(msg string)  { c.calls++; c.errMsg = msg }

// result asserts the run answered OK exactly once and returns the result.
func (c *capture) result(t *testing.T) app.RunbookRunResult {
	t.Helper()
	if c.calls != 1 {
		t.Fatalf("responder called %d times, want exactly 1", c.calls)
	}
	if c.errMsg != "" {
		t.Fatalf("run failed outright: %s", c.errMsg)
	}
	res, ok := c.data.(app.RunbookRunResult)
	if !ok {
		t.Fatalf("result is %T, want RunbookRunResult", c.data)
	}
	return res
}

func run(t *testing.T, o *orch, p app.RunbookRunParams) *capture {
	t.Helper()
	c := &capture{}
	o.RunbookRun(c, p)
	return c
}

// The ordinary case: every step dispatches through the same app.Dispatcher a
// client command does, in order, and the run resolves once at the end.
func TestRunbookRunsStepsInOrder(t *testing.T) {
	o, dir := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	writeRunbook(t, dir, "m.yaml", `
name: m
steps:
  - run: pane.focus
    params: {pane: `+strconv.FormatUint(uint64(pane), 10)+`}
  - run: pane.rename
    params: {pane: `+strconv.FormatUint(uint64(pane), 10)+`, name: renamed}
`)
	res := run(t, o, app.RunbookRunParams{Name: "m"}).result(t)
	if res.Failed {
		t.Fatalf("run failed: %+v", res.Steps)
	}
	if len(res.Steps) != 2 {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if res.Steps[0].Index != 1 || res.Steps[1].Index != 2 {
		t.Errorf("indices = %d,%d, want 1,2", res.Steps[0].Index, res.Steps[1].Index)
	}
	// The effect actually landed — a runbook step is the command, not a
	// simulation of it.
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name != "renamed" {
		t.Errorf("pane name = %q, want the runbook's rename to have happened", name)
	}
}

// A step's result binds under its id, and a later step reads a WIRE field name
// out of it. This is the property that makes runbooks composable at all.
func TestRunbookBindsAResultForALaterStep(t *testing.T) {
	o, dir := newRunbookOrch(t)
	writeRunbook(t, dir, "b.yaml", `
name: b
steps:
  - run: pane.list
    id: p
  - run: pane.rename
    params: {pane: "{{ p.panes.0.pane }}", name: "via-binding"}
`)
	res := run(t, o, app.RunbookRunParams{Name: "b"}).result(t)
	if res.Failed {
		t.Fatalf("run failed: %+v", res.Steps)
	}
	pane := uint32(o.session.AllPaneIDs()[0])
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name != "via-binding" {
		t.Errorf("pane name = %q; the bound pane id did not reach the rename", name)
	}
}

// A failed step stops the run, and every step after it is reported as Skipped
// rather than silently absent — "did not run" and "ran fine" must not look the
// same to whoever is working out what state the session is in.
func TestRunbookStopsAtAFailureAndMarksTheRest(t *testing.T) {
	o, dir := newRunbookOrch(t)
	writeRunbook(t, dir, "f.yaml", `
name: f
steps:
  - run: pane.focus
    params: {pane: 99999}
  - run: pane.last
  - run: pane.last
`)
	res := run(t, o, app.RunbookRunParams{Name: "f"}).result(t)
	if !res.Failed {
		t.Fatal("run reported success despite a failing step")
	}
	if len(res.Steps) != 3 {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if res.Steps[0].Error == "" {
		t.Error("step 1 has no error")
	}
	for _, s := range res.Steps[1:] {
		if !s.Skipped || s.Error != "" {
			t.Errorf("step %d = %+v, want Skipped with no error", s.Index, s)
		}
	}
}

func TestRunbookContinueOnError(t *testing.T) {
	o, dir := newRunbookOrch(t)
	writeRunbook(t, dir, "c.yaml", `
name: c
steps:
  - run: pane.focus
    params: {pane: 99999}
    continue_on_error: true
  - run: pane.last
`)
	res := run(t, o, app.RunbookRunParams{Name: "c"}).result(t)
	if res.Failed {
		t.Error("a tolerated failure must not fail the run")
	}
	if len(res.Steps) != 2 || res.Steps[1].Skipped {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if res.Steps[0].Error == "" {
		t.Error("the tolerated failure is still recorded as one")
	}
}

// Vars override declared defaults; an undeclared one is refused rather than
// ignored, because a silently-dropped var runs the wrong thing successfully.
func TestRunbookVars(t *testing.T) {
	o, dir := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	writeRunbook(t, dir, "v.yaml", `
name: v
vars:
  label: fallback
steps:
  - run: pane.rename
    params: {pane: `+strconv.FormatUint(uint64(pane), 10)+`, name: "{{ vars.label }}"}
`)

	res := run(t, o, app.RunbookRunParams{Name: "v"}).result(t)
	if res.Failed {
		t.Fatalf("default run failed: %+v", res.Steps)
	}
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name != "fallback" {
		t.Errorf("name = %q, want the declared default", name)
	}

	res = run(t, o, app.RunbookRunParams{Name: "v", Vars: map[string]string{"label": "override"}}).result(t)
	if res.Failed {
		t.Fatalf("override run failed: %+v", res.Steps)
	}
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name != "override" {
		t.Errorf("name = %q, want the override", name)
	}

	c := run(t, o, app.RunbookRunParams{Name: "v", Vars: map[string]string{"nope": "x"}})
	if !strings.Contains(c.errMsg, `declares no var "nope"`) {
		t.Errorf("undeclared var was not refused: %q / %+v", c.errMsg, c.data)
	}
}

func TestRunbookUnknownName(t *testing.T) {
	o, _ := newRunbookOrch(t)
	c := run(t, o, app.RunbookRunParams{Name: "absent"})
	if !strings.Contains(c.errMsg, "no runbook named absent") {
		t.Errorf("errMsg = %q", c.errMsg)
	}
}

// A file that does not parse never runs a step: the refusal comes from the load,
// with the parse error, not from step 1 failing for a mysterious reason.
func TestRunbookBrokenFileRefusesBeforeAnyStep(t *testing.T) {
	o, dir := newRunbookOrch(t)
	writeRunbook(t, dir, "x.yaml", "steps:\n  - run: pane.explode\n")
	c := run(t, o, app.RunbookRunParams{Name: "x"})
	if !strings.Contains(c.errMsg, "is not a command") {
		t.Errorf("errMsg = %q", c.errMsg)
	}
}

// Editing a runbook and immediately running it must run the NEW steps. The
// directory is re-scanned per call precisely so this cannot go stale.
func TestRunbookIsRereadPerRun(t *testing.T) {
	o, dir := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	body := `
name: r
steps:
  - run: pane.rename
    params: {pane: ` + strconv.FormatUint(uint64(pane), 10) + `, name: "%s"}
`
	writeRunbook(t, dir, "r.yaml", strings.Replace(body, "%s", "first", 1))
	run(t, o, app.RunbookRunParams{Name: "r"}).result(t)

	writeRunbook(t, dir, "r.yaml", strings.Replace(body, "%s", "second", 1))
	run(t, o, app.RunbookRunParams{Name: "r"}).result(t)

	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name != "second" {
		t.Errorf("name = %q; the edited runbook was not re-read", name)
	}
}

func TestRunbookList(t *testing.T) {
	o, dir := newRunbookOrch(t)
	writeRunbook(t, dir, "good.yaml", "description: does a thing\nvars: {a: '1'}\nsteps:\n  - run: pane.last\n")
	writeRunbook(t, dir, "bad.yaml", "steps:\n  - run: pane.explode\n")

	c := &capture{}
	o.RunbookList(c)
	res, ok := c.data.(app.RunbookListResult)
	if !ok {
		t.Fatalf("data = %T (%s)", c.data, c.errMsg)
	}
	if len(res.Runbooks) != 2 {
		t.Fatalf("runbooks = %+v", res.Runbooks)
	}
	var good, bad *app.RunbookInfo
	for i := range res.Runbooks {
		switch res.Runbooks[i].Name {
		case "good":
			good = &res.Runbooks[i]
		case "bad":
			bad = &res.Runbooks[i]
		}
	}
	if good == nil || good.Steps != 1 || good.Description != "does a thing" || len(good.Vars) != 1 {
		t.Errorf("good = %+v", good)
	}
	// The broken file is listed WITH its reason: absent-from-the-list and
	// never-written look identical, and need different fixes.
	if bad == nil || bad.Error == "" {
		t.Errorf("bad = %+v", bad)
	}
}

// A step whose command answered OK but whose expectation did not hold fails the
// run. Without this, "wait for the build, then deploy" would deploy after a
// wait that timed out — pane.wait_for_output reports a timeout as a successful
// call returning matched:false.
func TestRunbookExpectFailsTheStep(t *testing.T) {
	o, dir := newRunbookOrch(t)
	writeRunbook(t, dir, "e.yaml", `
name: e
steps:
  - run: pane.list
    id: p
    expect: "{{ p.panes.0.focused }}"
  - run: pane.last
`)
	// The one pane in a fresh session is focused, so the expectation holds.
	res := run(t, o, app.RunbookRunParams{Name: "e"}).result(t)
	if res.Failed {
		t.Fatalf("a holding expectation must not fail the run: %+v", res.Steps)
	}

	// Now assert on something that is false: the same pane is not visible-and-
	// named, so use a field that is genuinely absent-as-false.
	writeRunbook(t, dir, "e.yaml", `
name: e
steps:
  - run: pane.list
    id: p
    expect: "{{ p.panes.0.name }}"
  - run: pane.last
`)
	res = run(t, o, app.RunbookRunParams{Name: "e"}).result(t)
	if !res.Failed {
		t.Fatalf("an unmet expectation must fail the run: %+v", res.Steps)
	}
	if res.Steps[0].Error == "" || !strings.Contains(res.Steps[0].Error, "expect") {
		t.Errorf("step 1 error = %q, want it to name the expectation", res.Steps[0].Error)
	}
	if !res.Steps[1].Skipped {
		t.Error("the step after an unmet expectation must not run")
	}
}

// An unmet expectation is tolerable like any other step failure.
func TestRunbookExpectRespectsContinueOnError(t *testing.T) {
	o, dir := newRunbookOrch(t)
	writeRunbook(t, dir, "e2.yaml", `
name: e2
steps:
  - run: pane.list
    id: p
    expect: "{{ p.panes.0.name }}"
    continue_on_error: true
  - run: pane.last
`)
	res := run(t, o, app.RunbookRunParams{Name: "e2"}).result(t)
	if res.Failed {
		t.Errorf("tolerated expectation failure must not fail the run: %+v", res.Steps)
	}
	if res.Steps[0].Error == "" {
		t.Error("the tolerated failure is still recorded")
	}
	if res.Steps[1].Skipped {
		t.Error("the next step must still run")
	}
}
