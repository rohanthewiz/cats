package app

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/layout"
)

// --- the classification checklist ---------------------------------------------

// recordedParamClasses is every wire field of every RECORDED command, with what
// the recorder does with it. It is exhaustive on purpose: an untagged field is
// recorded verbatim at run time (the only safe default — see record.go), so the
// thing that has to fail when somebody adds `Password string` to a params struct
// is this table, at build time, rather than a runbook file on somebody's disk.
//
// It covers the recorded commands only. A field on a query cannot reach a file,
// and the day a query becomes recorded, flipping its Recorded flag brings it in
// here — which is exactly when the question is worth asking.
var recordedParamClasses = map[string]map[string]string{
	"pane.split": {
		"command":   ClassPlain,
		"cwd":       ClassPlain,
		"direction": ClassPlain,
		"env":       ClassPlain,
		"host":      ClassPlain,
		"pane":      ClassPaneHandle,
	},
	"pane.close":           {"pane": ClassPaneHandle},
	"pane.focus":           {"pane": ClassPaneHandle},
	"pane.focus_direction": {"dir": ClassPlain},
	"pane.cycle":           {"next": ClassPlain},
	"pane.last":            {},
	"nav.back":             {},
	"nav.forward":          {},
	"pane.swap":            {"dir": ClassPlain},
	"pane.swap_with": {
		"pane":   ClassPaneHandle,
		"target": ClassPaneHandle,
	},
	"pane.zoom": {"pane": ClassPaneHandle},
	"pane.rename": {
		"name": ClassPlain,
		"pane": ClassPaneHandle,
	},
	"pane.resize_border": {
		"border": ClassPlain,
		"ratio":  ClassPlain,
	},
	"scroll": {
		"delta": ClassPlain,
		"pane":  ClassPaneHandle,
	},
	"pane.wait_for_output": {
		"lines":      ClassPlain,
		"pane":       ClassPaneHandle,
		"pattern":    ClassPlain,
		"regex":      ClassPlain,
		"timeout_ms": ClassPlain,
	},
	// text is the most private field in the vocabulary and is recorded all the
	// same: it is also the one a macro exists to replay, and the protection for
	// it is that recording is armed at all. See ClassSecret.
	"pane.send_input": {
		"pane":   ClassPaneHandle,
		"submit": ClassPlain,
		"text":   ClassPlain,
	},
	"tab.create": {
		"command":   ClassPlain,
		"cwd":       ClassPlain,
		"env":       ClassPlain,
		"host":      ClassPlain,
		"title":     ClassPlain,
		"workspace": ClassWorkspaceHandle,
	},
	"tab.close": {"num": ClassPlain},
	"tab.focus": {"num": ClassPlain},
	"tab.rename": {
		"name": ClassPlain,
		"num":  ClassPlain,
	},
	"tab.move": {
		"index": ClassPlain,
		"num":   ClassPlain,
	},
	// Workspace ids and tab numbers are model coordinates, not handles: a
	// replay against a session with the same shape means the same thing.
	"tab.move_to_workspace": {
		"workspace": ClassPlain,
		"num":       ClassPlain,
		"from":      ClassPlain,
	},
	"workspace.create": {
		"host":  ClassPlain,
		"mkdir": ClassPlain,
		"name":  ClassPlain,
		"path":  ClassPlain,
	},
	"workspace.close": {"id": ClassWorkspaceHandle},
	"workspace.focus": {"id": ClassWorkspaceHandle},
	"workspace.rename": {
		"id":   ClassWorkspaceHandle,
		"name": ClassPlain,
	},
	"workspace.move": {
		"id":    ClassWorkspaceHandle,
		"index": ClassPlain,
	},
	"workspace.lock": {
		"id":     ClassWorkspaceHandle,
		"locked": ClassPlain,
	},
	"agent.focus":          {"pane": ClassPaneHandle},
	"server.reload_config": {},
	"usage.refresh":        {},
	"chat.send":            {"text": ClassPlain},
	"chat.cancel":          {},
	"chat.clear":           {},
	"worktree.create": {
		"branch": ClassPlain,
		"pane":   ClassPaneHandle,
		"path":   ClassPlain,
	},
	"worktree.open": {
		"pane": ClassPaneHandle,
		"path": ClassPlain,
	},
	"worktree.remove": {
		"force":     ClassPlain,
		"workspace": ClassWorkspaceHandle,
	},
	"config.set": {
		"copy_mode": ClassPlain,
		"theme":     ClassPlain,
	},
	"theme.save": {
		"activate": ClassPlain,
		"colors":   ClassPlain,
		"dark":     ClassPlain,
		"font":     ClassPlain,
		"label":    ClassPlain,
		"name":     ClassPlain,
	},
	"theme.delete":     {"name": ClassPlain},
	"plugin.uninstall": {"id": ClassPlain},
	"ui.notify": {
		"actions": ClassPlain,
		"body":    ClassPlain,
		"kind":    ClassPlain,
		"pane":    ClassPaneHandle,
		"title":   ClassPlain,
	},
	"pane.open_file": {
		"column": ClassPlain,
		"editor": ClassPaneHandle,
		"host":   ClassPlain,
		"line":   ClassPlain,
		"pane":   ClassPaneHandle,
		"path":   ClassPlain,
		"spawn":  ClassPlain,
	},
	"file.put": {
		"data":      ClassPlain,
		"host":      ClassPlain,
		"mode":      ClassPlain,
		"more":      ClassPlain,
		"offset":    ClassPlain,
		"overwrite": ClassPlain,
		"pane":      ClassPaneHandle,
		"path":      ClassPlain,
	},
	// token is the one secret in the vocabulary: a credential is useless to a
	// replay and dangerous on disk, so its value is withheld and the emitted
	// runbook asks for it.
	"host.attach": {
		"addr":        ClassPlain,
		"fingerprint": ClassPlain,
		"id":          ClassPlain,
		"is_default":  ClassPlain,
		"label":       ClassPlain,
		"token":       ClassSecret,
		"token_file":  ClassPlain,
	},
	"host.detach": {
		"force": ClassPlain,
		"id":    ClassPlain,
	},
}

// Every params field of every recorded command is accounted for above, and
// nothing above describes a field or a command that no longer exists.
func TestRecordedParamsAreClassified(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range commandSpecs {
		if !spec.Recorded {
			if _, listed := recordedParamClasses[spec.Name]; listed {
				t.Errorf("%s is listed in recordedParamClasses but is not Recorded", spec.Name)
			}
			continue
		}
		seen[spec.Name] = true
		want, ok := recordedParamClasses[spec.Name]
		if !ok {
			t.Errorf("%s is recorded but unclassified: add its params to recordedParamClasses, "+
				"tagging any that carry a credential or a live handle", spec.Name)
			continue
		}
		got := map[string]string{}
		for _, f := range ParamFields(spec.Name) {
			got[f.Name] = f.Class
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s params classification = %v, want %v", spec.Name, got, want)
		}
	}
	for name := range recordedParamClasses {
		if !seen[name] {
			t.Errorf("recordedParamClasses lists %q, which is not a recorded command", name)
		}
	}
}

// Classification is top-level only (ParamField says so). A `cats` tag on a
// nested field would be read by nothing, so finding one means the walk has to
// grow rather than the tag being trusted.
func TestNoClassificationHidesInANestedStruct(t *testing.T) {
	for _, spec := range commandSpecs {
		for _, root := range []any{spec.Params, spec.Result} {
			if root == nil {
				continue
			}
			for name, f := range wireFields(reflect.TypeOf(root)) {
				findNestedTags(t, spec.Name+"."+name, f.Type, 0)
			}
		}
	}
}

func findNestedTags(t *testing.T, path string, rt reflect.Type, depth int) {
	t.Helper()
	if depth > 4 || rt == nil {
		return
	}
	for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return
	}
	for name, f := range wireFields(rt) {
		if f.Tag.Get("cats") != "" {
			t.Errorf("%s.%s carries a cats tag, but classification is top-level only; "+
				"extend the walk in record.go before relying on it", path, name)
		}
		findNestedTags(t, path+"."+name, f.Type, depth+1)
	}
}

// Every handle a params field can name has to be producible by some recorded
// command, or the emitter could never rewrite it into anything.
func TestEveryHandleKindHasAProducer(t *testing.T) {
	produced := map[string]bool{}
	consumed := map[string]bool{}
	for _, spec := range commandSpecs {
		if !spec.Recorded {
			continue
		}
		for _, class := range ResultHandles(spec.Name) {
			produced[class] = true
		}
		for _, f := range ParamFields(spec.Name) {
			if f.Class == ClassPaneHandle || f.Class == ClassWorkspaceHandle {
				consumed[f.Class] = true
			}
		}
	}
	for class := range consumed {
		if !produced[class] {
			t.Errorf("params carry %s but no recorded command returns one, so a recording could never rewrite it", class)
		}
	}
}

// --- the hook in Dispatch ------------------------------------------------------

// fakeRecorder is the recorder seam as a list.
type fakeRecorder struct {
	on    bool
	seq   int64
	slots []fakeSlot
}

type fakeSlot struct {
	seq    int64
	cmd    string
	done   bool
	params json.RawMessage
	result any
}

func (r *fakeRecorder) Begin(cmd string) int64 {
	if !r.on {
		return 0
	}
	r.seq++
	r.slots = append(r.slots, fakeSlot{seq: r.seq, cmd: cmd})
	return r.seq
}

func (r *fakeRecorder) Commit(seq int64, params json.RawMessage, result any) {
	for i := range r.slots {
		if r.slots[i].seq == seq {
			r.slots[i].done, r.slots[i].params, r.slots[i].result = true, params, result
		}
	}
}

func (r *fakeRecorder) Abort(seq int64) {
	for i := range r.slots {
		if r.slots[i].seq == seq {
			r.slots = append(r.slots[:i], r.slots[i+1:]...)
			return
		}
	}
}

// captured names the completed slots in order.
func (r *fakeRecorder) captured() []string {
	var out []string
	for _, s := range r.slots {
		if s.done {
			out = append(out, s.cmd)
		}
	}
	return out
}

// recHarness is newCmdHarness with a recorder wired in.
type recHarness struct {
	cmdHarness
	rec *fakeRecorder
}

func newRecHarness(t *testing.T) recHarness {
	t.Helper()
	log := &[]string{}
	rec := &fakeRecorder{on: true}
	b := &fakeBackend{
		log: log, area: layout.Rect{Width: 120, Height: 32},
		paneExists: true, daemonUp: true, recorder: rec,
	}
	s := newTestSession(t)
	return recHarness{
		cmdHarness: cmdHarness{d: NewDispatcher(s, b), b: b, s: s, log: log},
		rec:        rec,
	}
}

func (h recHarness) dispatch(cmd string, params any) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			panic(err)
		}
		raw = b
	}
	h.d.Dispatch(cmd, JSONParamDecoder{Raw: raw}, h.resp())
}

// The recorder sees the effects and not the queries — the rule the Recorded
// flag exists to express.
func TestRecorderCapturesEffectsNotQueries(t *testing.T) {
	h := newRecHarness(t)
	pane, _ := h.s.FocusedPane()

	h.dispatch(CmdPaneSendInput, SendInputParams{Pane: uint32(pane), Text: "ls", Submit: true})
	h.dispatch(CmdPaneList, nil)
	h.dispatch(CmdSessionGet, nil)
	h.dispatch(CmdPaneRename, RenamePaneParams{Pane: uint32(pane), Name: "build"})

	want := []string{CmdPaneSendInput, CmdPaneRename}
	if got := h.rec.captured(); !reflect.DeepEqual(got, want) {
		t.Fatalf("captured %v, want %v", got, want)
	}
}

// Nothing is captured while the recorder is disarmed, and the commands still
// run — the wrapping is invisible to the switch either way.
func TestDisarmedRecorderCapturesNothing(t *testing.T) {
	h := newRecHarness(t)
	h.rec.on = false
	pane, _ := h.s.FocusedPane()

	h.dispatch(CmdPaneRename, RenamePaneParams{Pane: uint32(pane), Name: "build"})

	if got := h.rec.captured(); len(got) != 0 {
		t.Fatalf("captured %v while disarmed", got)
	}
	if name, _ := h.s.PaneCustomName(pane); name != "build" {
		t.Fatalf("the command did not run: pane name = %q", name)
	}
}

// A command that failed did not happen, so its slot is released rather than
// left in the recording.
func TestRecorderDropsAFailedCommand(t *testing.T) {
	h := newRecHarness(t)
	pane, _ := h.s.FocusedPane()

	h.dispatch(CmdPaneRename, RenamePaneParams{Pane: 9999, Name: "nope"}) // unknown pane
	h.dispatch(CmdPaneRename, RenamePaneParams{Pane: uint32(pane), Name: "build"})

	want := []string{CmdPaneRename}
	if got := h.rec.captured(); !reflect.DeepEqual(got, want) {
		t.Fatalf("captured %v, want just the one that worked (%v)", got, want)
	}
	if len(h.rec.slots) != 1 {
		t.Fatalf("the failed command left %d slots behind", len(h.rec.slots))
	}
}

// The slot is taken on the way IN, so a command that resolves late keeps the
// position it was called in. Recording on completion alone would put the wait
// after the command the user ran while waiting for it.
func TestRecorderKeepsCallOrderAcrossALateResult(t *testing.T) {
	h := newRecHarness(t)
	pane, _ := h.s.FocusedPane()

	h.dispatch(CmdWaitForOutput, WaitForOutputParams{Pane: uint32(pane), Pattern: "done"})
	h.dispatch(CmdPaneRename, RenamePaneParams{Pane: uint32(pane), Name: "build"})
	h.b.lastWait.OK(WaitForOutputResult{Matched: true}) // the daemon answers now

	want := []string{CmdWaitForOutput, CmdPaneRename}
	if got := h.rec.captured(); !reflect.DeepEqual(got, want) {
		t.Fatalf("captured %v, want %v", got, want)
	}
}

// The params kept are the CALLER'S, not the decoded struct: an omitempty field
// sent explicitly as its zero value has to survive, or the macro replays a
// different command.
func TestRecorderKeepsAnExplicitZeroValue(t *testing.T) {
	h := newRecHarness(t)
	pane, _ := h.s.FocusedPane()

	raw := json.RawMessage(`{"pane":` + strconv.FormatUint(uint64(pane), 10) + `,"text":"ls","submit":false}`)
	h.d.Dispatch(CmdPaneSendInput, JSONParamDecoder{Raw: raw}, h.resp())

	if len(h.rec.slots) != 1 || !h.rec.slots[0].done {
		t.Fatalf("nothing was captured")
	}
	if got := string(h.rec.slots[0].params); !strings.Contains(got, `"submit":false`) {
		t.Fatalf("captured params %s, want the explicit submit:false to survive", got)
	}
}

// A command's result reaches the recorder, which is what makes a handle
// rewritable: pane.split's new pane is only referable because the split's own
// answer was kept.
func TestRecorderKeepsTheResult(t *testing.T) {
	h := newRecHarness(t)
	h.dispatch(CmdPaneSplit, SplitParams{Direction: SplitV})

	if len(h.rec.slots) != 1 {
		t.Fatalf("captured %d slots, want 1", len(h.rec.slots))
	}
	res, ok := h.rec.slots[0].result.(SplitResult)
	if !ok || res.Pane == 0 {
		t.Fatalf("result = %#v, want a SplitResult naming the new pane", h.rec.slots[0].result)
	}
	if ResultHandles(CmdPaneSplit)["pane"] != ClassPaneHandle {
		t.Fatalf("pane.split's result field is not declared a pane handle, so nothing could rewrite it")
	}
}
