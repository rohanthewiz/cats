package runbook

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/rohanthewiz/cats/internal/app"
)

// This file is the other direction: a sequence of §7 commands that were
// actually run, back into the document Load accepts. It is what record-a-macro
// is made of — "I did this once, do it again".
//
// Emitting YAML rather than a private blob is the whole design constraint, and
// it pays for itself twice: the product is a file a human can read and edit
// (which is what makes a recording a starting point rather than a black box),
// and the emitter has no format of its own to keep in step with the loader.
// Emit finishes by parsing what it produced, so a recording that would not load
// is an error at record time rather than a broken file discovered next week.
//
// Two things a faithful transcript gets wrong, and both are handled here:
//
//	1. Keystrokes. A browser sends pane.send_input per keypress, so a literal
//	   transcript is one step per character — technically exact and unreadable,
//	   and readability is the entire reason this emits YAML.
//	2. Handles. A recorded `pane.send_input {pane: 7}` replayed tomorrow types
//	   into whatever pane 7 is then, which is somebody else's terminal. Every
//	   handle is therefore rewritten into a reference to the step that produced
//	   it — exactly as a hand-written runbook would spell it — or, for the pane
//	   the recording started in, into the pane the runbook is started from. A
//	   handle that answers to neither is refused, because a runbook that loads
//	   and then does the wrong thing is the failure mode the load checks exist
//	   to prevent.

// Recorded is one captured command: what was run, the params the caller sent,
// and what it returned. Params and Result are the generic JSON trees, not the
// typed structs, for the reason Bindings are: the names here are the WIRE names
// a runbook step writes.
type Recorded struct {
	Run    string
	Params map[string]any
	Result map[string]any
}

// EmitOptions is what the recording knows that its steps do not.
//
// The two anchors are the pane and workspace that were focused when recording
// STARTED. They are what makes the common recording — "do the thing in the pane
// I am in" — emittable at all: a reference to the anchor becomes a reference to
// the pane the runbook is run from, which is the same relationship a second
// time round.
type EmitOptions struct {
	Name            string
	Description     string
	AnchorPane      uint32
	AnchorWorkspace string
}

// The ids the emitter gives the steps it adds. A recording's own steps are
// named after their position (see stepID), so these cannot collide.
const (
	anchorPaneID      = "start_pane"
	anchorWorkspaceID = "start_workspace"
)

// maxRecordingBytes bounds the emitted document. A runbook is a document, and
// the two ways one stops being readable are a file.put's base64 payload and a
// paste of a whole file through pane.send_input. Both are legitimate single
// commands and neither is a legitimate line in a file somebody is meant to
// read, so the recording is refused with the step named rather than silently
// truncated.
const maxRecordingBytes = 128 << 10

// emitDoc / emitStep are the write-side shape. They are separate from doc /
// docStep because the read side is deliberately forgiving (absent sections,
// several spellings of `on:`) and the write side must produce exactly one of
// those spellings, in a fixed field order.
type emitDoc struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description,omitempty"`
	Vars        yaml.MapSlice `yaml:"vars,omitempty"`
	Steps       []emitStep    `yaml:"steps"`
}

type emitStep struct {
	ID     string        `yaml:"id,omitempty"`
	Run    string        `yaml:"run"`
	Params yaml.MapSlice `yaml:"params,omitempty"`
}

// Emit turns a recording into a runbook document.
func Emit(recs []Recorded, opt EmitOptions) ([]byte, error) {
	if !nameOK(opt.Name) {
		return nil, fmt.Errorf("runbook name %q must be a non-empty run of letters, digits, '-' or '_'", opt.Name)
	}
	recs = coalesceInput(recs)
	if len(recs) == 0 {
		return nil, fmt.Errorf("nothing was recorded: a runbook needs at least one step")
	}
	if len(recs) > maxSteps {
		return nil, fmt.Errorf("the recording is %d steps, past the %d-step limit a runbook document has",
			len(recs), maxSteps)
	}

	e := &emitter{
		opt:     opt,
		live:    map[string]handleSource{},
		used:    map[int]bool{},
		varSeen: map[string]bool{},
	}
	steps := make([]emitStep, 0, len(recs)+2)
	for i, rec := range recs {
		params, err := e.rewrite(i, rec)
		if err != nil {
			return nil, err
		}
		steps = append(steps, emitStep{Run: rec.Run, Params: params})
		// AFTER the step's own params, never before: a handle a step both
		// consumes and produces (there is none today, but tab.create returning
		// the pane it made is one edit away from being one) must not resolve to
		// the step that is using it.
		e.produce(i, rec)
	}
	for i := range steps {
		if e.used[i] {
			steps[i].ID = stepID(i)
		}
	}
	steps = append(e.anchorSteps(), steps...)

	d := emitDoc{
		Name:        opt.Name,
		Description: e.describe(),
		Vars:        e.varsSlice(),
		Steps:       steps,
	}
	out, err := yaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("cannot render the recording as YAML: %w", err)
	}
	if len(out) > maxRecordingBytes {
		return nil, fmt.Errorf("the recording renders to %d KiB of YAML, past the %d KiB a runbook file is meant to be%s",
			len(out)/1024, maxRecordingBytes/1024, e.biggest(steps))
	}
	// The last check is the one that matters: what was just written has to be
	// something Load accepts, and the only way to know that is to load it.
	if _, err := Parse(out, opt.Name, "the recording"); err != nil {
		return nil, fmt.Errorf("the recording did not produce a loadable runbook: %w", err)
	}
	return out, nil
}

// emitter is one Emit's working state.
type emitter struct {
	opt EmitOptions
	// live maps a handle's value to the step that produced it, updated as the
	// walk passes each producing step. It is keyed by kind AND value because a
	// pane id and a workspace id are different namespaces that both stringify
	// to short tokens.
	live map[string]handleSource
	// used marks the producing steps something actually referred to. Only those
	// are given an `id:`, because an id on a step nothing names is noise in a
	// file whose readability is the reason it is YAML.
	used map[int]bool
	// vars are the redacted secrets, in the order they were met, so a recording
	// with two of them declares them in the order they appear in the file.
	vars      []string
	varSeen   map[string]bool
	anchorPn  bool
	anchorWs  bool
	secretCnt int
}

// rewrite builds one step's emitted params: handles turned into references,
// secrets turned into vars, everything else through verbatim.
func (e *emitter) rewrite(i int, rec Recorded) (yaml.MapSlice, error) {
	if len(rec.Params) == 0 {
		return nil, nil
	}
	classes := map[string]string{}
	for _, f := range app.ParamFields(rec.Run) {
		classes[f.Name] = f.Class
	}

	keys := make([]string, 0, len(rec.Params))
	for k := range rec.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(yaml.MapSlice, 0, len(keys))
	for _, k := range keys {
		v := rec.Params[k]
		switch class := classes[k]; class {
		case app.ClassSecret:
			out = append(out, yaml.MapItem{Key: k, Value: e.secretVar(k)})
		case app.ClassPaneHandle, app.ClassWorkspaceHandle:
			ref, err := e.handleRef(i, rec.Run, k, class, v)
			if err != nil {
				return nil, err
			}
			out = append(out, yaml.MapItem{Key: k, Value: ref})
		default:
			out = append(out, yaml.MapItem{Key: k, Value: plainValue(v)})
		}
	}
	return out, nil
}

// handleRef resolves one handle-carrying param into a reference.
func (e *emitter) handleRef(i int, run, field, class string, v any) (any, error) {
	val, ok := canonHandle(v)
	if !ok {
		// Not a handle after all: an absent optional pane arrives as null, and
		// an empty workspace id means "the active one". Both are meaningful
		// values that survive a replay, so they travel unchanged.
		return v, nil
	}
	if src, ok := e.live[class+"\x00"+val]; ok {
		e.used[src.step] = true
		return src.ref, nil
	}
	switch {
	case class == app.ClassPaneHandle && e.opt.AnchorPane != 0 && val == strconv.FormatUint(uint64(e.opt.AnchorPane), 10):
		e.anchorPn = true
		return "{{ " + anchorPaneID + ".pane }}", nil
	case class == app.ClassWorkspaceHandle && e.opt.AnchorWorkspace != "" && val == e.opt.AnchorWorkspace:
		e.anchorWs = true
		return "{{ " + anchorWorkspaceID + ".active_workspace }}", nil
	}
	return nil, unboundHandle(i, run, field, class, val)
}

// unboundHandle is the refusal, and it carries the fix because the fix is not
// obvious: the handle names something that existed before the recording did.
//
// Refusing beats the alternatives. Emitting the literal id produces a runbook
// that loads and then acts on whatever holds that id at run time — a stranger's
// pane — and no comment in a file prevents that. Dropping the step produces a
// runbook that silently does less than what was recorded.
func unboundHandle(i int, run, field, class, val string) error {
	what := "pane"
	fix := "Record the step that creates it (a split or a new tab), or run the sequence from that pane so it is the one the recording starts in."
	if class == app.ClassWorkspaceHandle {
		what = "workspace"
		fix = "Record the workspace.create that makes it, or start the recording in it."
	}
	return fmt.Errorf("step %d (%s) names %s %s in %q, which nothing in the recording created and which is not the %s it started in. A %s id belongs to this session only, so replaying it would act on whatever holds it then. %s",
		i+1, run, what, val, field, what, what, fix)
}

// produce records the handles a completed step returned, so later steps can
// refer to them.
func (e *emitter) produce(i int, rec Recorded) {
	for field, class := range app.ResultHandles(rec.Run) {
		val, ok := canonHandle(rec.Result[field])
		if !ok {
			continue
		}
		// Last writer wins: an id reused after the pane holding it was closed
		// belongs to whichever step made it most recently, which is the one an
		// intervening step was talking to.
		e.live[class+"\x00"+val] = handleSource{
			ref:  fmt.Sprintf("{{ %s.%s }}", stepID(i), field),
			step: i,
		}
	}
}

// handleSource is where a live handle came from: the reference that resolves it
// and the step that has to be given the id that reference names.
type handleSource struct {
	ref  string
	step int
}

// secretVar redacts one field: the value never reaches the file, and the
// runbook asks for it instead.
func (e *emitter) secretVar(field string) string {
	name := field
	for i := 2; e.varSeen[name]; i++ {
		name = fmt.Sprintf("%s_%d", field, i)
	}
	e.varSeen[name] = true
	e.vars = append(e.vars, name)
	e.secretCnt++
	return "{{ vars." + name + " }}"
}

func (e *emitter) varsSlice() yaml.MapSlice {
	if len(e.vars) == 0 {
		return nil
	}
	out := make(yaml.MapSlice, 0, len(e.vars))
	for _, v := range e.vars {
		out = append(out, yaml.MapItem{Key: v, Value: ""})
	}
	return out
}

// anchorSteps prepends the queries that resolve the recording's starting point.
//
// They are ordinary §7 commands, which is the point: `pane.get` with no params
// is the focused pane and `session.get` reports the active workspace, so "the
// pane this runbook was started from" needs no runbook-only concept to express.
// Neither is added unless something referred to it.
func (e *emitter) anchorSteps() []emitStep {
	var out []emitStep
	if e.anchorPn {
		out = append(out, emitStep{ID: anchorPaneID, Run: app.CmdPaneGet})
	}
	if e.anchorWs {
		out = append(out, emitStep{ID: anchorWorkspaceID, Run: app.CmdSessionGet})
	}
	return out
}

// describe writes the runbook's description, folding in the one thing a reader
// has to know that the steps do not say: that a value was withheld and has to
// be supplied.
func (e *emitter) describe() string {
	d := strings.TrimSpace(e.opt.Description)
	if e.secretCnt == 0 {
		return d
	}
	note := fmt.Sprintf("%d value was redacted when this was recorded; set it when you run this", e.secretCnt)
	if e.secretCnt > 1 {
		note = fmt.Sprintf("%d values were redacted when this was recorded; set them when you run this", e.secretCnt)
	}
	if d == "" {
		return note
	}
	return d + " (" + note + ")"
}

// biggest names the step most responsible for an over-long document, so the
// refusal points at the paste or the payload rather than at the total.
func (e *emitter) biggest(steps []emitStep) string {
	worst, size := -1, 0
	for i, st := range steps {
		b, err := yaml.Marshal(st)
		if err != nil {
			continue
		}
		if len(b) > size {
			worst, size = i, len(b)
		}
	}
	if worst < 0 {
		return ""
	}
	return fmt.Sprintf("; step %d (%s) alone is %d KiB", worst+1, steps[worst].Run, size/1024)
}

// stepID names a recorded step after its position, which is the one name that
// is unique without asking anything of the recording and that a reader can
// trace back to the step it refers to.
func stepID(i int) string { return "s" + strconv.Itoa(i+1) }

// coalesceInput merges a run of pane.send_input calls into one step.
//
// A browser sends one command per keypress, so the literal transcript of typing
// `make test` and pressing Enter is ten steps. Merging is safe exactly while
// the earlier calls did not submit: submit sends an Enter encoded against the
// pane's live mode state, which is not the same byte as a newline inside text,
// so a run is merged up to AND INCLUDING the first submitting call and a new
// one starts after it. What replays is the same text with the same single
// Enter at the end.
func coalesceInput(recs []Recorded) []Recorded {
	out := make([]Recorded, 0, len(recs))
	for _, rec := range recs {
		if rec.Run != app.CmdPaneSendInput || len(out) == 0 {
			out = append(out, rec)
			continue
		}
		prev := &out[len(out)-1]
		if prev.Run != app.CmdPaneSendInput || submitted(prev.Params) || !samePane(prev.Params, rec.Params) {
			out = append(out, rec)
			continue
		}
		merged := map[string]any{}
		for k, v := range prev.Params {
			merged[k] = v
		}
		for k, v := range rec.Params {
			merged[k] = v
		}
		merged["text"] = text(prev.Params) + text(rec.Params)
		prev.Params = merged
	}
	return out
}

func samePane(a, b map[string]any) bool {
	av, aok := canonHandle(a["pane"])
	bv, bok := canonHandle(b["pane"])
	return aok == bok && av == bv
}

// submitted reports whether a send_input step pressed Enter, which is what
// ends a coalescing run.
func submitted(p map[string]any) bool {
	b, ok := p["submit"].(bool)
	return ok && b
}

func text(p map[string]any) string {
	s, _ := p["text"].(string)
	return s
}

// plainValue renders a recorded value for the file.
//
// The one transformation is numeric: params arrive as a JSON tree, where every
// number is a float64, and a YAML encoder writes float64(60000) as "60000.0".
// That reloads as the same number, so this is not a correctness fix for small
// values — it is one for large ones (a file offset past 2^53 written as a float
// is no longer the offset) and a readability fix for every one, in a file whose
// readability is why it is YAML.
func plainValue(v any) any {
	switch t := v.(type) {
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = plainValue(e)
		}
		return out
	case map[string]any:
		// Sorted, so a map in the params (an env block, a theme's colors) does
		// not reshuffle between two recordings of the same thing.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(yaml.MapSlice, 0, len(keys))
		for _, k := range keys {
			out = append(out, yaml.MapItem{Key: k, Value: plainValue(t[k])})
		}
		return out
	default:
		return v
	}
}

// canonHandle renders a handle value as the string the live map is keyed by,
// and reports whether the value is a handle at all.
//
// The numeric cases are the interesting ones: params arrive as a JSON tree, so
// every pane id is a float64, while the anchors and any Go-side value are
// integers. Comparing them as text is what keeps the two from silently never
// matching.
func canonHandle(v any) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		return t, t != ""
	case bool:
		return "", false
	case float64:
		if t != float64(int64(t)) {
			return "", false
		}
		return strconv.FormatInt(int64(t), 10), t != 0
	case float32:
		return canonHandle(float64(t))
	case int:
		return strconv.Itoa(t), t != 0
	case int64:
		return strconv.FormatInt(t, 10), t != 0
	case uint32:
		return strconv.FormatUint(uint64(t), 10), t != 0
	case uint64:
		return strconv.FormatUint(t, 10), t != 0
	default:
		return "", false
	}
}
