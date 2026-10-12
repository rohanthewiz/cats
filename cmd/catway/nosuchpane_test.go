//go:build ghostty

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// "no such pane" (N-036). The daemon drops a pane from its map at PTY EOF, while
// catway keeps the exited pane on screen for the reaper's countdown. Every send
// below used to reach the daemon for such a pane and come back as a WARN in
// daemons.log plus an "error: no such pane" toast. These tests pin the gates
// that stop them at catway.

// noSuchPaneResponder captures one synchronous reply.
type noSuchPaneResponder struct {
	ok, fail bool
	errMsg   string
}

func (*noSuchPaneResponder) WantsReply() bool  { return true }
func (r *noSuchPaneResponder) OK(any)          { r.ok = true }
func (r *noSuchPaneResponder) Fail(msg string) { r.fail, r.errMsg = true, msg }

// A layout change reaches every pane on screen, the exited one included. The
// live pane is resized; the exited one keeps the new grid for a respawn but
// sends nothing.
func TestResizeSkipsExitedPane(t *testing.T) {
	o, focused, other := mcOrch(t)
	pd := newPipeDaemon(t, o)
	w := openWindow(o, o.session.ActiveWorkspace().ID, 200, 60)
	o.applyModel()
	if !o.panes[other].created {
		t.Fatalf("pane %d not created against the pipe daemon", other)
	}
	pd.collect(200 * time.Millisecond) // the create/resize burst of the setup

	code := 0
	o.panes[other].exited = &code
	colsBefore := o.panes[other].cols
	o.handleUp(w, &browserproto.Resize{T: browserproto.MsgResize, Cols: 120, Rows: 40})

	resized := map[uint32]bool{}
	deadline := time.After(300 * time.Millisecond)
collect:
	for {
		select {
		case m := <-pd.msgs:
			if m.mt != orchestration.MsgResize {
				continue
			}
			var rs orchestration.Resize
			if err := json.Unmarshal(m.payload, &rs); err != nil {
				t.Fatalf("decode resize: %v", err)
			}
			resized[rs.PaneID] = true
		case <-deadline:
			break collect
		}
	}
	if resized[other] {
		t.Fatalf("exited pane %d was sent a resize", other)
	}
	if !resized[focused] {
		t.Fatalf("live pane %d was not resized (got %v)", focused, resized)
	}
	if o.panes[other].cols == colsBefore {
		t.Fatalf("exited pane's grid not recorded: still %d cols", colsBefore)
	}
}

// A read or capture of an exited pane fails at once with the real reason,
// instead of waiting out reqTimeout for a daemon error that cannot resolve it.
// (The capture here has no screen to serve: the pane never had a frame. See
// TestCaptureOfExitedPaneServesItsLastScreen for the one that does.)
func TestReadAndCaptureOfExitedPaneFailFast(t *testing.T) {
	o, _, other := mcOrch(t)
	code := 0
	o.panes[other].exited = &code

	var capR noSuchPaneResponder
	o.StartCapture(&capR, app.CaptureParams{Pane: other})
	if !capR.fail || !strings.Contains(capR.errMsg, "has exited") {
		t.Fatalf("capture: fail=%v msg=%q, want an immediate \"has exited\"", capR.fail, capR.errMsg)
	}
	var readR noSuchPaneResponder
	o.StartRead(&readR, app.ReadParams{Pane: other})
	if !readR.fail || !strings.Contains(readR.errMsg, "has exited") {
		t.Fatalf("read: fail=%v msg=%q, want an immediate \"has exited\"", readR.fail, readR.errMsg)
	}
	if n := len(o.pendingReqs[paneKey(other, reqText)]) + len(o.pendingReqs[paneKey(other, reqSelection)]); n != 0 {
		t.Fatalf("%d requests left pending for an exited pane", n)
	}
	if err := o.ScrollPane(other, -5); err == nil || !strings.Contains(err.Error(), "has exited") {
		t.Fatalf("scroll: err=%v, want \"has exited\"", err)
	}
}

// A capture of an exited pane serves its last screen from catway's grid
// mirror (N-039): the daemon's emulator is gone, but the screen the pane died
// on usually holds the crash message. The visible scope gets the screen, the
// recent scope (what `catctl capture` sends) its tail; ansi, unwrap and a view
// scrolled into history fail at once, naming what is still available.
func TestCaptureOfExitedPaneServesItsLastScreen(t *testing.T) {
	o, _, other := mcOrch(t)
	var cells []orchestration.Cell
	for _, r := range "exit 3  $   " {
		cells = append(cells, orchestration.Cell{Symbol: string(r)})
	}
	if _, ok := o.panes[other].grid.Apply(&orchestration.Frame{Cols: 6, Rows: 2, Full: true, Cells: cells}); !ok {
		t.Fatal("seed frame not applied")
	}
	code := 3
	o.panes[other].exited = &code

	var ok captureResponder
	o.StartCapture(&ok, app.CaptureParams{Pane: other})
	if ok.fail || ok.text != "exit 3\n  $" {
		t.Fatalf("plain visible capture: fail=%v msg=%q text=%q", ok.fail, ok.errMsg, ok.text)
	}
	for name, c := range map[string]struct {
		p    app.CaptureParams
		want string
	}{
		"recent, whole buffer": {app.CaptureParams{Pane: other, Scope: 1}, "exit 3\n  $"},
		"recent, last line":    {app.CaptureParams{Pane: other, Scope: 1, Lines: 1}, "  $"},
		"recent, more lines":   {app.CaptureParams{Pane: other, Scope: 1, Lines: 50}, "exit 3\n  $"},
	} {
		var r captureResponder
		o.StartCapture(&r, c.p)
		if r.fail || r.text != c.want {
			t.Errorf("%s: fail=%v msg=%q text=%q, want %q", name, r.fail, r.errMsg, r.text, c.want)
		}
	}
	for name, p := range map[string]app.CaptureParams{
		"ansi":   {Pane: other, Ansi: true},
		"unwrap": {Pane: other, Scope: 1, Unwrap: true},
	} {
		var r captureResponder
		o.StartCapture(&r, p)
		if !r.fail || !strings.Contains(r.errMsg, "has exited") || !strings.Contains(r.errMsg, "last screen") {
			t.Errorf("%s: fail=%v msg=%q, want a \"has exited; only its last screen\" refusal", name, r.fail, r.errMsg)
		}
	}
	// Scrolled into history, the screen is not the buffer's tail, so recent
	// cannot be answered from it; visible still can.
	o.panes[other].grid.Apply(&orchestration.Frame{Cols: 6, Rows: 2, Full: true, Cells: cells,
		Scroll: &orchestration.ScrollInfo{OffsetFromBottom: 4, MaxOffsetFromBottom: 9, ViewportRows: 2}})
	var scrolled captureResponder
	o.StartCapture(&scrolled, app.CaptureParams{Pane: other, Scope: 1})
	if !scrolled.fail || !strings.Contains(scrolled.errMsg, "last screen") {
		t.Errorf("recent while scrolled back: fail=%v msg=%q, want a refusal", scrolled.fail, scrolled.errMsg)
	}
	var vis captureResponder
	o.StartCapture(&vis, app.CaptureParams{Pane: other})
	if vis.fail || vis.text != "exit 3\n  $" {
		t.Errorf("visible while scrolled back: fail=%v text=%q", vis.fail, vis.text)
	}
	if n := len(o.pendingReqs[paneKey(other, reqText)]); n != 0 {
		t.Fatalf("%d captures left pending for an exited pane", n)
	}
}

// captureResponder keeps the text of an OK capture as well as a failure.
type captureResponder struct {
	noSuchPaneResponder
	text string
}

func (r *captureResponder) OK(v any) {
	r.ok = true
	if c, isCap := v.(app.CaptureResult); isCap {
		r.text = c.Text
	}
}
