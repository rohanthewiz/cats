package runbook

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
)

// rec builds one recorded step the way the recorder does: params and result as
// the generic JSON trees, so a pane id is a float64 here exactly as it is there.
func rec(t *testing.T, run string, params, result any) Recorded {
	t.Helper()
	return Recorded{Run: run, Params: tree(t, params), Result: tree(t, result)}
}

func tree(t *testing.T, v any) map[string]any {
	t.Helper()
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// emit runs Emit and loads what it produced, which is the property the whole
// file rests on: the emitter's output has to be a document the loader accepts.
func emit(t *testing.T, recs []Recorded, opt EmitOptions) (string, *Runbook) {
	t.Helper()
	if opt.Name == "" {
		opt.Name = "recorded"
	}
	out, err := Emit(recs, opt)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rb, err := Parse(out, opt.Name, "emitted.yaml")
	if err != nil {
		t.Fatalf("the emitted document does not load: %v\n%s", err, out)
	}
	return string(out), rb
}

// A pane made inside the recording is referred to by the step that made it, and
// only that step gets an id — an id on a step nothing names is noise.
func TestEmitRewritesAProducedPaneIntoAStepRef(t *testing.T) {
	recs := []Recorded{
		rec(t, app.CmdPaneSplit, app.SplitParams{Direction: app.SplitV}, app.SplitResult{Pane: 7}),
		rec(t, app.CmdPaneSendInput, app.SendInputParams{Pane: 7, Text: "make", Submit: true}, nil),
	}
	out, rb := emit(t, recs, EmitOptions{})

	if len(rb.Steps) != 2 {
		t.Fatalf("steps = %d, want 2\n%s", len(rb.Steps), out)
	}
	if rb.Steps[0].ID != "s1" {
		t.Errorf("the producing step has id %q, want s1\n%s", rb.Steps[0].ID, out)
	}
	if got := rb.Steps[1].Params["pane"]; got != "{{ s1.pane }}" {
		t.Errorf("pane = %v, want the reference to the split\n%s", got, out)
	}
	if strings.Contains(out, "pane: 7") {
		t.Errorf("the literal pane id survived:\n%s", out)
	}
}

// A step nothing refers to keeps no id.
func TestEmitLeavesUnreferencedStepsUnnamed(t *testing.T) {
	recs := []Recorded{
		rec(t, app.CmdPaneSplit, app.SplitParams{Direction: app.SplitV}, app.SplitResult{Pane: 7}),
		rec(t, app.CmdUsageRefresh, nil, nil),
	}
	_, rb := emit(t, recs, EmitOptions{})
	for i, st := range rb.Steps {
		if st.ID != "" {
			t.Errorf("step %d (%s) has id %q, want none", i+1, st.Run, st.ID)
		}
	}
}

// The pane the recording STARTED in becomes the pane the runbook is started
// from, through an ordinary pane.get. This is the common recording — do the
// thing in the pane I am already in — and without it it would be unemittable.
func TestEmitAnchorsThePaneTheRecordingStartedIn(t *testing.T) {
	recs := []Recorded{
		rec(t, app.CmdPaneSendInput, app.SendInputParams{Pane: 3, Text: "make", Submit: true}, nil),
	}
	out, rb := emit(t, recs, EmitOptions{AnchorPane: 3})

	if len(rb.Steps) != 2 {
		t.Fatalf("steps = %d, want the anchor plus the input\n%s", len(rb.Steps), out)
	}
	if rb.Steps[0].Run != app.CmdPaneGet || rb.Steps[0].ID != anchorPaneID {
		t.Fatalf("step 1 = %s/%q, want %s/%s\n%s", rb.Steps[0].Run, rb.Steps[0].ID, app.CmdPaneGet, anchorPaneID, out)
	}
	if got := rb.Steps[1].Params["pane"]; got != "{{ "+anchorPaneID+".pane }}" {
		t.Errorf("pane = %v, want the anchor reference\n%s", got, out)
	}
	// The reference has to name a field pane.get actually returns, since
	// nothing else checks a step-result reference at load time.
	if _, ok := tree(t, app.PaneInfo{Pane: 3})["pane"]; !ok {
		t.Errorf("pane.get's result has no `pane` field for the anchor to name")
	}
}

// The same rule for workspaces, through session.get.
func TestEmitAnchorsTheWorkspaceTheRecordingStartedIn(t *testing.T) {
	recs := []Recorded{
		rec(t, app.CmdWorkspaceRename, app.RenameWorkspaceParams{ID: "w2", Name: "api"}, nil),
	}
	out, rb := emit(t, recs, EmitOptions{AnchorWorkspace: "w2"})

	if rb.Steps[0].Run != app.CmdSessionGet || rb.Steps[0].ID != anchorWorkspaceID {
		t.Fatalf("step 1 = %s/%q, want the session.get anchor\n%s", rb.Steps[0].Run, rb.Steps[0].ID, out)
	}
	if got := rb.Steps[1].Params["id"]; got != "{{ "+anchorWorkspaceID+".active_workspace }}" {
		t.Errorf("id = %v, want the anchor reference\n%s", got, out)
	}
	if _, ok := tree(t, app.SessionInfoResult{ActiveWorkspace: "w2"})["active_workspace"]; !ok {
		t.Errorf("session.get's result has no `active_workspace` field for the anchor to name")
	}
}

// An anchor that nothing referred to adds no step.
func TestEmitAddsNoAnchorWhenNothingNeedsOne(t *testing.T) {
	recs := []Recorded{rec(t, app.CmdUsageRefresh, nil, nil)}
	out, rb := emit(t, recs, EmitOptions{AnchorPane: 3, AnchorWorkspace: "w1"})
	if len(rb.Steps) != 1 {
		t.Fatalf("steps = %d, want just the one recorded\n%s", len(rb.Steps), out)
	}
}

// A handle that answers to neither a producing step nor the anchor is refused,
// and the refusal names the step, the pane and the fix.
func TestEmitRefusesAHandleItCannotRewrite(t *testing.T) {
	recs := []Recorded{
		rec(t, app.CmdPaneSplit, app.SplitParams{Direction: app.SplitV}, app.SplitResult{Pane: 7}),
		rec(t, app.CmdPaneSendInput, app.SendInputParams{Pane: 9, Text: "make", Submit: true}, nil),
	}
	_, err := Emit(recs, EmitOptions{Name: "recorded", AnchorPane: 3})
	if err == nil {
		t.Fatal("a pane from before the recording was accepted")
	}
	for _, want := range []string{"step 2", app.CmdPaneSendInput, "pane 9", "split"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
}

// The same for a workspace, whose fix is a different sentence.
func TestEmitRefusesAnUnknownWorkspace(t *testing.T) {
	recs := []Recorded{rec(t, app.CmdWorkspaceFocus, app.WorkspaceParams{ID: "w9"}, nil)}
	_, err := Emit(recs, EmitOptions{Name: "recorded", AnchorWorkspace: "w1"})
	if err == nil || !strings.Contains(err.Error(), "workspace w9") {
		t.Fatalf("err = %v, want a refusal naming workspace w9", err)
	}
}

// An absent optional handle is not a handle: `pane.close` with no pane means
// the focused one, which survives a replay unchanged.
func TestEmitLeavesAnAbsentOptionalPaneAlone(t *testing.T) {
	recs := []Recorded{
		{Run: app.CmdPaneClose, Params: map[string]any{}},
		{Run: app.CmdWorkspaceClose, Params: map[string]any{"id": ""}},
	}
	out, rb := emit(t, recs, EmitOptions{})
	if len(rb.Steps) != 2 {
		t.Fatalf("steps = %d, want 2\n%s", len(rb.Steps), out)
	}
	if got := rb.Steps[1].Params["id"]; got != "" {
		t.Errorf("id = %v, want the empty id through unchanged\n%s", got, out)
	}
}

// Typing is one command per keypress. A literal transcript is unreadable, so a
// run of send_input to one pane becomes one step — up to and including the
// keypress that submitted, since submit is an Enter encoded against the pane's
// live modes rather than a newline in the text.
func TestEmitCoalescesTypingUpToTheSubmit(t *testing.T) {
	var recs []Recorded
	recs = append(recs, rec(t, app.CmdPaneSplit, app.SplitParams{Direction: app.SplitV}, app.SplitResult{Pane: 7}))
	for _, ch := range []string{"m", "a", "k", "e"} {
		recs = append(recs, rec(t, app.CmdPaneSendInput, app.SendInputParams{Pane: 7, Text: ch}, nil))
	}
	recs = append(recs, rec(t, app.CmdPaneSendInput, app.SendInputParams{Pane: 7, Submit: true}, nil))
	recs = append(recs, rec(t, app.CmdPaneSendInput, app.SendInputParams{Pane: 7, Text: "q", Submit: true}, nil))

	out, rb := emit(t, recs, EmitOptions{})
	if len(rb.Steps) != 3 {
		t.Fatalf("steps = %d, want split + one typed line + the next line\n%s", len(rb.Steps), out)
	}
	if got := rb.Steps[1].Params["text"]; got != "make" {
		t.Errorf("text = %v, want the whole line in one step\n%s", got, out)
	}
	if got := rb.Steps[1].Params["submit"]; got != true {
		t.Errorf("submit = %v, want the Enter that ended the run\n%s", got, out)
	}
	if got := rb.Steps[2].Params["text"]; got != "q" {
		t.Errorf("the step after a submit merged into it: %v\n%s", got, out)
	}
}

// Typing into two panes does not merge, however interleaved.
func TestEmitDoesNotCoalesceAcrossPanes(t *testing.T) {
	recs := []Recorded{
		rec(t, app.CmdPaneSplit, app.SplitParams{Direction: app.SplitV}, app.SplitResult{Pane: 7}),
		rec(t, app.CmdPaneSplit, app.SplitParams{Direction: app.SplitH}, app.SplitResult{Pane: 8}),
		rec(t, app.CmdPaneSendInput, app.SendInputParams{Pane: 7, Text: "a"}, nil),
		rec(t, app.CmdPaneSendInput, app.SendInputParams{Pane: 8, Text: "b"}, nil),
		rec(t, app.CmdPaneSendInput, app.SendInputParams{Pane: 7, Text: "c"}, nil),
	}
	out, rb := emit(t, recs, EmitOptions{})
	if len(rb.Steps) != 5 {
		t.Fatalf("steps = %d, want the two splits and three separate inputs\n%s", len(rb.Steps), out)
	}
}

// A secret never reaches the file; the runbook declares a var and asks for it.
func TestEmitRedactsASecretIntoAVar(t *testing.T) {
	recs := []Recorded{
		rec(t, app.CmdHostAttach, app.HostAttachParams{
			ID: "devbox", Addr: "tls://devbox:8422", Token: "hunter2",
		}, nil),
	}
	out, rb := emit(t, recs, EmitOptions{})

	if strings.Contains(out, "hunter2") {
		t.Fatalf("the token was written to the file:\n%s", out)
	}
	if _, declared := rb.Vars["token"]; !declared {
		t.Fatalf("no var was declared for the redacted token\n%s", out)
	}
	if got := rb.Steps[0].Params["token"]; got != "{{ vars.token }}" {
		t.Errorf("token = %v, want the var reference\n%s", got, out)
	}
	if !strings.Contains(rb.Description, "redacted") {
		t.Errorf("description %q does not say a value was withheld", rb.Description)
	}
	// The address, which is not a credential, is still there — redaction is per
	// field, not per command.
	if got := rb.Steps[0].Params["addr"]; got != "tls://devbox:8422" {
		t.Errorf("addr = %v, want it recorded", got)
	}
}

// A zero value the caller sent explicitly survives to the file. This is the
// omitempty trap one layer out: `submit: false` dropped would turn a step that
// typed a line into one that pressed Enter on it.
func TestEmitKeepsAnExplicitZero(t *testing.T) {
	recs := []Recorded{{
		Run:    app.CmdPaneSendInput,
		Params: map[string]any{"pane": float64(3), "text": "y", "submit": false},
	}}
	out, rb := emit(t, recs, EmitOptions{AnchorPane: 3})
	if _, ok := rb.Steps[1].Params["submit"]; !ok {
		t.Fatalf("submit:false was dropped\n%s", out)
	}
}

// Nothing recorded is an error rather than an empty file: a runbook needs a
// step, and a recording that captured nothing is worth saying out loud.
func TestEmitRefusesAnEmptyRecording(t *testing.T) {
	if _, err := Emit(nil, EmitOptions{Name: "recorded"}); err == nil ||
		!strings.Contains(err.Error(), "Nothing was recorded") && !strings.Contains(err.Error(), "nothing was recorded") {
		t.Fatalf("err = %v, want a refusal saying nothing was recorded", err)
	}
}

// A name that could not address a runbook is refused before anything is built.
func TestEmitRefusesABadName(t *testing.T) {
	recs := []Recorded{rec(t, app.CmdUsageRefresh, nil, nil)}
	if _, err := Emit(recs, EmitOptions{Name: "not a name"}); err == nil {
		t.Fatal("a name with a space was accepted")
	}
}

// One command carrying a payload — a file.put, a pasted file — is refused with
// the step named, rather than written into a document nobody can read.
func TestEmitRefusesAnOverlongDocument(t *testing.T) {
	recs := []Recorded{{
		Run: app.CmdPaneSendInput,
		Params: map[string]any{
			"pane": float64(3),
			"text": strings.Repeat("x", maxRecordingBytes+1),
		},
	}}
	_, err := Emit(recs, EmitOptions{Name: "recorded", AnchorPane: 3})
	if err == nil {
		t.Fatal("a document past the size limit was accepted")
	}
	if !strings.Contains(err.Error(), app.CmdPaneSendInput) {
		t.Errorf("refusal %q does not name the step responsible", err)
	}
}

// Emission is deterministic: the same recording twice is the same bytes, or a
// recording committed to git would churn on every re-record.
func TestEmitIsDeterministic(t *testing.T) {
	recs := []Recorded{
		rec(t, app.CmdPaneSplit, app.SplitParams{
			Direction: app.SplitV,
			Cwd:       "/tmp",
			Env:       map[string]string{"B": "2", "A": "1"},
		}, app.SplitResult{Pane: 7}),
		rec(t, app.CmdPaneSendInput, app.SendInputParams{Pane: 7, Text: "make", Submit: true}, nil),
	}
	first, err := Emit(recs, EmitOptions{Name: "recorded"})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	for range 5 {
		again, err := Emit(recs, EmitOptions{Name: "recorded"})
		if err != nil {
			t.Fatalf("Emit: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("emission is not stable:\n%s\n---\n%s", first, again)
		}
	}
}

// The whole point, end to end: a recorded sequence loads, and the references it
// was rewritten with resolve against the results the steps return.
func TestEmittedRunbookResolvesItsOwnReferences(t *testing.T) {
	recs := []Recorded{
		rec(t, app.CmdPaneSplit, app.SplitParams{Direction: app.SplitV}, app.SplitResult{Pane: 7}),
		rec(t, app.CmdPaneSendInput, app.SendInputParams{Pane: 7, Text: "make", Submit: true}, nil),
	}
	_, rb := emit(t, recs, EmitOptions{})

	binds := Bindings{"vars": map[string]any{}, "event": map[string]any{}}
	if err := binds.Bind(rb.Steps[0].ID, app.SplitResult{Pane: 42}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	params, err := Resolve(rb.Steps[1].Params, binds)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The reference resolves to the NEW pane, with its type intact — a string
	// there would be a decode error at the command.
	if got := params["pane"]; got != float64(42) {
		t.Fatalf("pane resolved to %#v, want the pane this run's split made", got)
	}
}
