package app

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/rohanthewiz/herdr-web/internal/layout"
)

// errScroll stands in for a backend ScrollPane failure (e.g. unknown pane).
var errScroll = errors.New("unknown pane 7")

// These tests drive the protocol-neutral dispatcher directly against a real
// Session and fakes for the runtime seam — no libghostty, no daemon, no browser.
// A shared event log records the order of backend effects and responder replies
// so command flows can be asserted precisely (e.g. server.stop replies before it
// shuts down). This coverage did not exist below the ghostty-tagged, daemon-backed
// integration tests.

// fakeBackend records the runtime effects the dispatcher drives and returns
// canned answers for the gating queries.
type fakeBackend struct {
	log         *[]string
	area        layout.Rect
	paneExists  bool
	daemonUp    bool
	scrollErr   error
	reloadErr   error
	lastRead    Responder
	lastCapture Responder
	lastScroll  [2]int
	lastTitle   uint32
}

func (b *fakeBackend) rec(s string)                { *b.log = append(*b.log, s) }
func (b *fakeBackend) Area() layout.Rect           { return b.area }
func (b *fakeBackend) ApplyModel()                 { b.rec("applyModel") }
func (b *fakeBackend) BroadcastLayout()            { b.rec("broadcastLayout") }
func (b *fakeBackend) BroadcastPaneTitle(p uint32) { b.rec("title"); b.lastTitle = p }
func (b *fakeBackend) PaneExists(uint32) bool      { return b.paneExists }
func (b *fakeBackend) DaemonConnected() bool       { return b.daemonUp }
func (b *fakeBackend) ReloadConfig() error         { b.rec("reload"); return b.reloadErr }
func (b *fakeBackend) Shutdown()                   { b.rec("shutdown") }

func (b *fakeBackend) ScrollPane(pane uint32, delta int) error {
	b.rec("scroll")
	b.lastScroll = [2]int{int(pane), delta}
	return b.scrollErr
}
func (b *fakeBackend) StartRead(r Responder, _ ReadParams) { b.rec("startRead"); b.lastRead = r }
func (b *fakeBackend) StartCapture(r Responder, _ CaptureParams) {
	b.rec("startCapture")
	b.lastCapture = r
}

// fakeResponder records the terminal reply (and its data), writing "ok"/"fail" to
// the shared log so ordering against backend effects can be asserted.
type fakeResponder struct {
	log      *[]string
	wants    bool
	data     any
	errMsg   string
	okCall   bool
	failCall bool
}

func (r *fakeResponder) WantsReply() bool { return r.wants }
func (r *fakeResponder) OK(data any)      { *r.log = append(*r.log, "ok"); r.okCall = true; r.data = data }
func (r *fakeResponder) Fail(msg string) {
	*r.log = append(*r.log, "fail")
	r.failCall = true
	r.errMsg = msg
}

// jsonDec mirrors gateway2's browser param decoder: empty ⇒ ErrNoParams.
type jsonDec struct{ raw []byte }

func (d jsonDec) Decode(v any) error {
	if len(d.raw) == 0 {
		return ErrNoParams
	}
	return json.Unmarshal(d.raw, v)
}

func params(t *testing.T, v any) jsonDec {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return jsonDec{b}
}
func noParams() jsonDec { return jsonDec{} }
func badJSON() jsonDec  { return jsonDec{[]byte("{")} } // non-ErrNoParams decode error

// cmdHarness wires a real Session, a fakeBackend, and a shared log. daemonUp and
// paneExists default true (the common case); tests flip them.
type cmdHarness struct {
	d   *Dispatcher
	b   *fakeBackend
	s   *Session
	log *[]string
}

func newCmdHarness(t *testing.T) cmdHarness {
	t.Helper()
	log := &[]string{}
	s := newTestSession(t)
	b := &fakeBackend{log: log, area: layout.Rect{Width: 120, Height: 32}, paneExists: true, daemonUp: true}
	return cmdHarness{d: NewDispatcher(s, b), b: b, s: s, log: log}
}

func (h cmdHarness) resp() *fakeResponder { return &fakeResponder{log: h.log, wants: true} }

// A pure focus command rebroadcasts the layout and acks, without reconciling the
// daemon or mutating the pane set.
func TestDispatchFocus(t *testing.T) {
	h := newCmdHarness(t)
	focused, _ := h.s.FocusedPane()
	r := h.resp()

	h.d.Dispatch(CmdPaneFocus, params(t, PaneParams{Pane: uint32(focused)}), r)

	if !r.okCall || r.failCall {
		t.Fatalf("focus should ack ok: ok=%v fail=%v (%q)", r.okCall, r.failCall, r.errMsg)
	}
	if got := *h.log; len(got) != 2 || got[0] != "broadcastLayout" || got[1] != "ok" {
		t.Fatalf("focus effects = %v, want [broadcastLayout ok]", got)
	}
	if len(h.s.VisiblePaneIDs()) != 1 {
		t.Fatalf("focus must not change the pane set")
	}
}

// A required-params command with no params fails in the historical wording.
func TestDispatchMissingParams(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdPaneFocus, noParams(), r)

	if !r.failCall || r.errMsg != "bad params: missing params" {
		t.Fatalf("missing params: fail=%v msg=%q, want bad params: missing params", r.failCall, r.errMsg)
	}
	if len(*h.log) != 1 || (*h.log)[0] != "fail" {
		t.Fatalf("a params failure must not run effects, log=%v", *h.log)
	}
}

// A bad split direction fails without mutating the session or reconciling.
func TestDispatchSplitBadDirection(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: "diagonal"}), r)

	if !r.failCall {
		t.Fatalf("bad direction should fail")
	}
	if len(h.s.VisiblePaneIDs()) != 1 {
		t.Fatalf("failed split must not mutate the session, panes=%d", len(h.s.VisiblePaneIDs()))
	}
	for _, e := range *h.log {
		if e == "applyModel" {
			t.Fatalf("failed split must not reconcile the daemon, log=%v", *h.log)
		}
	}
}

// A valid split mutates the session and reconciles exactly once, then acks.
func TestDispatchSplitOK(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH}), r)

	if !r.okCall || r.failCall {
		t.Fatalf("valid split should ack: ok=%v fail=%v (%q)", r.okCall, r.failCall, r.errMsg)
	}
	if len(h.s.VisiblePaneIDs()) != 2 {
		t.Fatalf("split should leave 2 panes, got %d", len(h.s.VisiblePaneIDs()))
	}
	if got := *h.log; len(got) != 2 || got[0] != "applyModel" || got[1] != "ok" {
		t.Fatalf("split effects = %v, want [applyModel ok]", got)
	}
}

// read with no reply channel (WantsReply false) does nothing — no orphan pending.
func TestDispatchReadNoReply(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()
	r.wants = false

	h.d.Dispatch(CmdRead, params(t, ReadParams{Pane: 1}), r)

	if r.okCall || r.failCall || len(*h.log) != 0 {
		t.Fatalf("id-less read should do nothing, log=%v ok=%v fail=%v", *h.log, r.okCall, r.failCall)
	}
}

// read on an unknown pane fails before starting a round-trip.
func TestDispatchReadUnknownPane(t *testing.T) {
	h := newCmdHarness(t)
	h.b.paneExists = false
	r := h.resp()

	h.d.Dispatch(CmdRead, params(t, ReadParams{Pane: 9999}), r)

	if !r.failCall || r.errMsg != "unknown pane 9999" {
		t.Fatalf("unknown-pane read: fail=%v msg=%q", r.failCall, r.errMsg)
	}
	if h.b.lastRead != nil {
		t.Fatalf("no round-trip should start for an unknown pane")
	}
}

// read with the daemon down fails with the connection message.
func TestDispatchReadDaemonDown(t *testing.T) {
	h := newCmdHarness(t)
	h.b.daemonUp = false
	r := h.resp()

	h.d.Dispatch(CmdRead, params(t, ReadParams{Pane: 1}), r)

	if !r.failCall || r.errMsg != "termhost daemon not connected" {
		t.Fatalf("daemon-down read: fail=%v msg=%q", r.failCall, r.errMsg)
	}
}

// A valid read starts the async round-trip carrying the caller's responder, and
// does not reply yet (the daemon reply will).
func TestDispatchReadStarts(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdRead, params(t, ReadParams{Pane: 1}), r)

	if r.okCall || r.failCall {
		t.Fatalf("read must not reply synchronously")
	}
	if got := *h.log; len(got) != 1 || got[0] != "startRead" {
		t.Fatalf("read effects = %v, want [startRead]", got)
	}
	if h.b.lastRead != Responder(r) {
		t.Fatalf("StartRead should receive the caller's responder")
	}
}

// capture on an unknown pane fails (same gate as read).
func TestDispatchCaptureUnknownPane(t *testing.T) {
	h := newCmdHarness(t)
	h.b.paneExists = false
	r := h.resp()

	h.d.Dispatch(CmdCapture, params(t, CaptureParams{Pane: 42}), r)

	if !r.failCall || r.errMsg != "unknown pane 42" || h.b.lastCapture != nil {
		t.Fatalf("unknown-pane capture: fail=%v msg=%q lastCapture=%v", r.failCall, r.errMsg, h.b.lastCapture)
	}
}

// scroll surfaces the backend's error (e.g. unknown pane) as a failure.
func TestDispatchScrollError(t *testing.T) {
	h := newCmdHarness(t)
	h.b.scrollErr = errScroll
	r := h.resp()

	h.d.Dispatch(CmdScroll, params(t, ScrollParams{Pane: 7, Delta: -3}), r)

	if !r.failCall || r.errMsg != errScroll.Error() {
		t.Fatalf("scroll error: fail=%v msg=%q", r.failCall, r.errMsg)
	}
	if h.b.lastScroll != [2]int{7, -3} {
		t.Fatalf("scroll should pass pane/delta through, got %v", h.b.lastScroll)
	}
}

// An all-optional command with no params decodes to the zero value (focused pane)
// rather than failing.
func TestDispatchZoomOptionalNoParams(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdPaneZoom, noParams(), r)

	if !r.okCall || r.failCall {
		t.Fatalf("zoom with no params should ack: ok=%v fail=%v (%q)", r.okCall, r.failCall, r.errMsg)
	}
	if got := *h.log; len(got) != 2 || got[0] != "applyModel" || got[1] != "ok" {
		t.Fatalf("zoom effects = %v, want [applyModel ok]", got)
	}
}

// workspace.close ignores ALL decode errors (not just ErrNoParams): malformed
// params still close the active workspace.
func TestDispatchWorkspaceCloseIgnoresBadParams(t *testing.T) {
	h := newCmdHarness(t)
	if _, err := h.s.CreateWorkspace(); err != nil { // need a 2nd so close is legal
		t.Fatalf("CreateWorkspace: %v", err)
	}
	r := h.resp()

	h.d.Dispatch(CmdWorkspaceClose, badJSON(), r)

	if !r.okCall || r.failCall {
		t.Fatalf("workspace.close should ignore a decode error and ack: ok=%v fail=%v (%q)", r.okCall, r.failCall, r.errMsg)
	}
	if len(h.s.Workspaces()) != 1 {
		t.Fatalf("workspace.close should have closed one workspace, have %d", len(h.s.Workspaces()))
	}
}

// server.stop replies BEFORE it shuts down, so the caller receives its result.
func TestDispatchServerStopOrder(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdServerStop, noParams(), r)

	if got := *h.log; len(got) != 2 || got[0] != "ok" || got[1] != "shutdown" {
		t.Fatalf("server.stop order = %v, want [ok shutdown]", got)
	}
}

// server.reload_config acks after the backend's (no-op) reload.
func TestDispatchReloadConfig(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdServerReloadConfig, noParams(), r)

	if got := *h.log; len(got) != 2 || got[0] != "reload" || got[1] != "ok" {
		t.Fatalf("reload_config effects = %v, want [reload ok]", got)
	}
}

// An unknown command name fails with the not-supported message.
func TestDispatchUnknownCommand(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch("pane.teleport", noParams(), r)

	if !r.failCall || r.errMsg != `command "pane.teleport" not supported yet (WS2 in progress)` {
		t.Fatalf("unknown command: fail=%v msg=%q", r.failCall, r.errMsg)
	}
}
