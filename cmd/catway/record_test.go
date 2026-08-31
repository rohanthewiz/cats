//go:build ghostty

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/layout"
)

// record drives runbook.record through the real dispatcher, so the parameter
// checks in internal/app are on the path a client's call takes.
func record(t *testing.T, o *orch, p app.RunbookRecordParams) *capture {
	t.Helper()
	c := &capture{}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	app.NewDispatcher(o.session, o).Dispatch(app.CmdRunbookRecord, app.JSONParamDecoder{Raw: raw}, c)
	return c
}

// dispatch runs one ordinary command through the dispatcher, which is how a
// recording gets anything in it.
func dispatch(t *testing.T, o *orch, cmd string, params any) *capture {
	t.Helper()
	c := &capture{}
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		raw = b
	}
	app.NewDispatcher(o.session, o).Dispatch(cmd, app.JSONParamDecoder{Raw: raw}, c)
	return c
}

func recordResult(t *testing.T, c *capture) app.RunbookRecordResult {
	t.Helper()
	if c.errMsg != "" {
		t.Fatalf("runbook.record failed: %s", c.errMsg)
	}
	res, ok := c.data.(app.RunbookRecordResult)
	if !ok {
		t.Fatalf("result is %T, want RunbookRecordResult", c.data)
	}
	return res
}

// The whole loop: arm, do something, stop with a name, and the runbook is on
// disk and loadable — which the emitter proved before writing it.
func TestRecordWritesARunbook(t *testing.T) {
	o, dir := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStart}))
	dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: pane, Name: "build"})
	dispatch(t, o, app.CmdPaneList, nil) // a query: recorded by nothing

	status := recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStatus}))
	if !status.Recording || status.Steps != 1 {
		t.Fatalf("status = %+v, want one captured command while recording", status)
	}
	if len(status.Commands) != 1 || status.Commands[0] != app.CmdPaneRename {
		t.Fatalf("captured %v, want just the rename", status.Commands)
	}

	res := recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStop, Name: "tidy"}))
	if res.Recording {
		t.Errorf("still recording after stop")
	}
	want := filepath.Join(dir, "tidy.yaml")
	if res.Path != want {
		t.Fatalf("path = %q, want %q", res.Path, want)
	}
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("the runbook was not written: %v", err)
	}
	if !strings.Contains(string(body), app.CmdPaneRename) {
		t.Errorf("the recorded command is missing from the file:\n%s", body)
	}
	// And it is a runbook: runbook.list finds it with no error.
	list := &capture{}
	o.RunbookList(list)
	res2, ok := list.data.(app.RunbookListResult)
	if !ok || len(res2.Runbooks) != 1 || res2.Runbooks[0].Error != "" {
		t.Fatalf("runbook.list = %+v, want the recording listed and parsing", list.data)
	}
}

// What was recorded actually replays: running the emitted runbook does the
// thing again. This is the end the whole phase exists for.
func TestARecordedRunbookReplays(t *testing.T) {
	o, _ := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	record(t, o, app.RunbookRecordParams{Action: app.RecordStart})
	dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: pane, Name: "recorded-name"})
	recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStop, Name: "again"}))

	// Undo the effect, then replay it.
	dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: pane, Name: "something-else"})
	res := run(t, o, app.RunbookRunParams{Name: "again"}).result(t)
	if res.Failed {
		t.Fatalf("the replay failed: %+v", res.Steps)
	}
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name != "recorded-name" {
		t.Fatalf("pane name = %q, want the recording to have replayed", name)
	}
}

// A pane the recording created is referred to by the step that created it, so
// the replay acts on ITS pane rather than on the id that existed that day.
func TestRecordRewritesAPaneItCreated(t *testing.T) {
	o, dir := newRunbookOrch(t)

	record(t, o, app.RunbookRecordParams{Action: app.RecordStart})
	split := dispatch(t, o, app.CmdPaneSplit, app.SplitParams{Direction: app.SplitV})
	newPane := split.data.(app.SplitResult).Pane
	dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: newPane, Name: "worker"})
	recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStop, Name: "two"}))

	body, err := os.ReadFile(filepath.Join(dir, "two.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "pane: "+strconv.FormatUint(uint64(newPane), 10)) {
		t.Errorf("the literal pane id was written:\n%s", body)
	}
	if !strings.Contains(string(body), "{{ s1.pane }}") {
		t.Errorf("the rename does not refer to the split:\n%s", body)
	}
}

// The pane the recording started in becomes the pane the runbook is run from,
// which is what makes recording in an existing pane work at all.
func TestRecordAnchorsTheStartingPane(t *testing.T) {
	o, dir := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	record(t, o, app.RunbookRecordParams{Action: app.RecordStart})
	dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: pane, Name: "here"})
	recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStop, Name: "anchored"}))

	body, err := os.ReadFile(filepath.Join(dir, "anchored.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "run: "+app.CmdPaneGet) ||
		!strings.Contains(string(body), "{{ start_pane.pane }}") {
		t.Errorf("the starting pane was not anchored:\n%s", body)
	}
}

// A command that failed did not happen, so it is not in the recording.
func TestRecordSkipsAFailedCommand(t *testing.T) {
	o, _ := newRunbookOrch(t)

	record(t, o, app.RunbookRecordParams{Action: app.RecordStart})
	if c := dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: 9999, Name: "no"}); c.errMsg == "" {
		t.Fatal("renaming an unknown pane succeeded; the test needs a command that fails")
	}
	status := recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStatus}))
	if status.Steps != 0 {
		t.Fatalf("captured %v, want nothing", status.Commands)
	}
}

// Cancel throws the recording away and writes nothing.
func TestRecordCancelKeepsNothing(t *testing.T) {
	o, dir := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	record(t, o, app.RunbookRecordParams{Action: app.RecordStart})
	dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: pane, Name: "build"})
	res := recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordCancel}))
	if res.Recording || res.Steps != 0 {
		t.Fatalf("cancel = %+v, want a cleared recorder", res)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("cancel left %d files behind", len(entries))
	}
	if c := record(t, o, app.RunbookRecordParams{Action: app.RecordStop, Name: "x"}); c.errMsg == "" {
		t.Fatal("stop after cancel was accepted")
	}
}

// Nothing is captured before start or after stop: the recorder is armed, and
// that is the whole privacy story.
func TestRecordCapturesNothingWhileDisarmed(t *testing.T) {
	o, _ := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: pane, Name: "before"})
	status := recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStatus}))
	if status.Recording || status.Steps != 0 {
		t.Fatalf("status = %+v, want an idle recorder", status)
	}

	// And a recording that captured nothing says so rather than writing an
	// empty document: a file with no steps is not a runbook, and "I recorded
	// and got nothing" is the answer worth giving.
	record(t, o, app.RunbookRecordParams{Action: app.RecordStart})
	dispatch(t, o, app.CmdPaneList, nil) // a query, so still nothing captured
	c := record(t, o, app.RunbookRecordParams{Action: app.RecordStop, Name: "empty-ish"})
	if !strings.Contains(c.errMsg, "nothing was recorded") {
		t.Fatalf("stopping an empty recording = %q", c.errMsg)
	}
}

// The four refusals, each leaving the recorder as it was.
func TestRecordRefusals(t *testing.T) {
	o, dir := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	// stop and cancel with nothing running.
	for _, action := range []string{app.RecordStop, app.RecordCancel} {
		c := record(t, o, app.RunbookRecordParams{Action: action, Name: "n"})
		if !strings.Contains(c.errMsg, "nothing is being recorded") {
			t.Errorf("%s with no recording = %q", action, c.errMsg)
		}
	}
	// An unknown action never reaches the backend.
	if c := record(t, o, app.RunbookRecordParams{Action: "pause"}); !strings.Contains(c.errMsg, "unknown action") {
		t.Errorf("unknown action = %q", c.errMsg)
	}
	// A stop with no name is refused before anything is emitted.
	record(t, o, app.RunbookRecordParams{Action: app.RecordStart})
	if c := record(t, o, app.RunbookRecordParams{Action: app.RecordStop}); !strings.Contains(c.errMsg, "name is required") {
		t.Errorf("nameless stop = %q", c.errMsg)
	}
	// A second start is refused rather than silently restarting.
	dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: pane, Name: "build"})
	if c := record(t, o, app.RunbookRecordParams{Action: app.RecordStart}); !strings.Contains(c.errMsg, "already recording") {
		t.Errorf("second start = %q", c.errMsg)
	}
	if s := recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStatus})); s.Steps != 1 {
		t.Errorf("the refused start disturbed the recording: %+v", s)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a refusal wrote %d files", len(entries))
	}
}

// A name that is already taken is refused, and the recording SURVIVES it: the
// alternative punishes a name collision with the loss of everything recorded.
func TestRecordRefusesAnExistingNameAndKeepsTheRecording(t *testing.T) {
	o, dir := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	writeRunbook(t, dir, "taken.yaml", "name: taken\nsteps:\n  - run: usage.refresh\n")

	record(t, o, app.RunbookRecordParams{Action: app.RecordStart})
	dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: pane, Name: "build"})

	c := record(t, o, app.RunbookRecordParams{Action: app.RecordStop, Name: "taken"})
	if !strings.Contains(c.errMsg, "already exists") || !strings.Contains(c.errMsg, "still running") {
		t.Fatalf("collision = %q", c.errMsg)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "taken.yaml")); !strings.Contains(string(body), "usage.refresh") {
		t.Fatalf("the existing runbook was overwritten anyway")
	}
	if s := recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStatus})); !s.Recording || s.Steps != 1 {
		t.Fatalf("the recording did not survive the refusal: %+v", s)
	}
	// overwrite is the word that says do it anyway.
	res := recordResult(t, record(t, o, app.RunbookRecordParams{
		Action: app.RecordStop, Name: "taken", Overwrite: true,
	}))
	if res.Path == "" {
		t.Fatalf("overwrite did not write: %+v", res)
	}
	if body, _ := os.ReadFile(res.Path); !strings.Contains(string(body), app.CmdPaneRename) {
		t.Fatalf("overwrite kept the old file:\n%s", body)
	}
}

// A recording the emitter refuses also survives, for the same reason — and the
// refusal is the emitter's own, so it names the step.
func TestRecordKeepsTheRecordingWhenTheEmitterRefuses(t *testing.T) {
	o, _ := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	record(t, o, app.RunbookRecordParams{Action: app.RecordStart})
	// A pane that is neither the anchor nor made by the recording: split first
	// (so the anchor is not the pane being renamed), then rename the ORIGINAL.
	o.macro.anchorPane = 0 // as if the recording had started with nothing focused
	dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: pane, Name: "orphan"})

	c := record(t, o, app.RunbookRecordParams{Action: app.RecordStop, Name: "orphaned"})
	if !strings.Contains(c.errMsg, "nothing in the recording created") {
		t.Fatalf("err = %q, want the unbound-handle refusal", c.errMsg)
	}
	if s := recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStatus})); !s.Recording {
		t.Fatalf("the recording was lost to a refusal")
	}
}

// The in-memory ceiling stops capture without failing the command that hit it,
// and says so.
func TestRecordCeilingStopsCaptureAndReportsIt(t *testing.T) {
	o, _ := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	record(t, o, app.RunbookRecordParams{Action: app.RecordStart})
	o.macro.bytes = maxRecordedBytes // as if a large transfer had been recorded

	c := dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: pane, Name: "build"})
	if c.errMsg != "" {
		t.Fatalf("the command was failed by the recorder's ceiling: %s", c.errMsg)
	}
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name != "build" {
		t.Fatalf("the command did not run")
	}
	s := recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStatus}))
	if s.Steps != 0 || !strings.Contains(s.Note, "ceiling") {
		t.Fatalf("status = %+v, want nothing captured and the ceiling reported", s)
	}
}

// Recording while a runbook runs records its STEPS, not the runbook.run that
// started them — otherwise the replay would do everything twice.
func TestRecordCapturesRunbookStepsNotTheRun(t *testing.T) {
	o, dir := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	writeRunbook(t, dir, "inner.yaml", `
name: inner
steps:
  - run: pane.rename
    params: {pane: `+strconv.FormatUint(uint64(pane), 10)+`, name: from-runbook}
`)
	record(t, o, app.RunbookRecordParams{Action: app.RecordStart})
	if res := run(t, o, app.RunbookRunParams{Name: "inner"}).result(t); res.Failed {
		t.Fatalf("inner run failed: %+v", res.Steps)
	}
	s := recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStatus}))
	if len(s.Commands) != 1 || s.Commands[0] != app.CmdPaneRename {
		t.Fatalf("captured %v, want the step and not the run", s.Commands)
	}
}

// The recorder announces itself. Every window's indicator is driven by this one
// hook, so both halves of it are asserted here: that each transition and each
// captured step fires it, and that reset does not quietly drop the notifier —
// the failure mode that would freeze the indicator in every connected browser
// with nothing anywhere reporting an error.
func TestRecordAnnouncesEveryChange(t *testing.T) {
	o, _ := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	m, ok := o.Recorder().(*macroRecorder)
	if !ok {
		t.Fatalf("recorder is %T, want *macroRecorder", o.Recorder())
	}
	// Stand a counter on the notifier orch wired in. It is installed BEFORE the
	// first start, so every reset on the way through has a chance to lose it.
	fired := 0
	m.notify = func() { fired++ }

	if msg := o.recordMsg(); msg.Recording || msg.Steps != 0 {
		t.Fatalf("idle recordMsg = %+v, want a not-recording message", msg)
	}

	recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStart}))
	if fired != 1 {
		t.Fatalf("start fired the notifier %d times, want 1 (reset dropped it?)", fired)
	}
	if msg := o.recordMsg(); !msg.Recording || msg.Steps != 0 || msg.StartedAt == "" {
		t.Fatalf("armed recordMsg = %+v, want recording, no steps, and a start time", msg)
	}

	dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: pane, Name: "build"})
	if fired != 2 {
		t.Errorf("a captured command fired the notifier %d times total, want 2", fired)
	}
	if msg := o.recordMsg(); msg.Steps != 1 {
		t.Errorf("recordMsg.Steps = %d after one captured command, want 1", msg.Steps)
	}

	// A query is captured by nothing, so it must move nothing — including the
	// indicator. A recorder that blinked on every pane.list would report the
	// UI's own polling as work the user did.
	dispatch(t, o, app.CmdPaneList, nil)
	if fired != 2 {
		t.Errorf("a query fired the notifier (%d total), want it left alone at 2", fired)
	}

	recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStop, Name: "tidy"}))
	if fired != 3 {
		t.Errorf("stop fired the notifier %d times total, want 3", fired)
	}
	if msg := o.recordMsg(); msg.Recording || msg.Steps != 0 || msg.StartedAt != "" {
		t.Errorf("recordMsg after stop = %+v, want the idle message", msg)
	}

	// Cancel resets through the same path stop does, and has its own call site.
	recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStart}))
	recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordCancel}))
	if fired != 5 {
		t.Errorf("start+cancel fired the notifier %d times total, want 5", fired)
	}
	if msg := o.recordMsg(); msg.Recording {
		t.Errorf("recordMsg after cancel = %+v, want the idle message", msg)
	}
}

// recvRecord pulls the first record message off a connection's queue.
func recvRecord(t *testing.T, c *client) *browserproto.Record {
	t.Helper()
	for {
		select {
		case b := <-c.out:
			msg, err := browserproto.DecodeDown(b)
			if err != nil {
				t.Fatalf("decode down: %v", err)
			}
			if r, ok := msg.(*browserproto.Record); ok {
				return r
			}
		default:
			t.Fatal("no record message queued")
			return nil
		}
	}
}

// A window learns the recorder's state on connect and on every change after —
// the two halves that let a toolbar indicator be right without polling.
//
// The connect half is asserted with the recorder IDLE on purpose. A window that
// reconnects mid-session was never reloaded and is still drawing whatever it
// last saw, so "nothing to say" is not a state the server may stay silent in:
// it is exactly the case where the indicator would stay lit over a recording
// that has since been stopped from another window or from catctl.
func TestRecordReachesEveryWindow(t *testing.T) {
	o, _ := newRunbookOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	c := newConn(o, false, browserproto.Init{Cols: 80, Rows: 24, CellWPx: 8, CellHPx: 16})
	if msg := recvRecord(t, c); msg.Recording {
		t.Errorf("connect burst said recording=%v, want the idle state", msg.Recording)
	}
	drain(c)

	// Armed from somewhere that is not this window — the dispatcher is the same
	// path catctl and a plugin take.
	recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordStart}))
	if msg := recvRecord(t, c); !msg.Recording || msg.Steps != 0 {
		t.Errorf("after start the window was told %+v, want recording with no steps", msg)
	}
	drain(c)

	dispatch(t, o, app.CmdPaneRename, app.RenamePaneParams{Pane: pane, Name: "build"})
	if msg := recvRecord(t, c); !msg.Recording || msg.Steps != 1 {
		t.Errorf("after one captured command the window was told %+v, want steps=1", msg)
	}
	drain(c)

	recordResult(t, record(t, o, app.RunbookRecordParams{Action: app.RecordCancel}))
	if msg := recvRecord(t, c); msg.Recording {
		t.Errorf("after cancel the window was told %+v, want the idle state", msg)
	}
}
