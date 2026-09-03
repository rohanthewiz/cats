package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/layout"
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
	log            *[]string
	area           layout.Rect
	paneExists     bool
	daemonUp       bool
	scrollErr      error
	reloadErr      error
	lastRead       Responder
	lastNotify     UINotifyParams
	editor         EditorInfo
	lastLedger     LedgerListParams
	ledgerEntries  []LedgerEntry
	lastRunbook    RunbookRunParams
	lastRecord     RunbookRecordParams
	recorder       Recorder
	lastOpen       OpenFileParams
	lastOpenPane   uint32
	lastCapture    Responder
	lastWait       Responder
	lastWaitP      WaitForOutputParams
	lastScroll     [2]int
	sendErr        error
	lastSend       SendInputParams
	lastTitle      uint32
	lastWtList     Responder
	lastWtCreate   Responder
	lastWtCreP     WorktreeCreateParams
	lastWtOpen     Responder
	lastWtOpenP    WorktreeOpenParams
	lastWtRemove   Responder
	lastWtRemP     WorktreeRemoveParams
	lastCfgSetP    ConfigSetParams
	lastThemeSaveP ThemeSaveParams
	lastThemeDelP  ThemeDeleteParams
	lastPlgList    Responder
	lastPlgUnins   Responder
	lastPlgUninP   PluginUninstallParams
	lastPathList   Responder
	lastPathP      PathListParams
	lastFileStatP  FileStatParams
	lastFileGetP   FileGetParams
	lastFilePutP   FilePutParams
	lastFileGet    Responder
	// paneMeta is the canned per-pane metadata PaneMeta answers with (nil ⇒ all
	// zero values), letting pane.list/pane.get tests assert the merge.
	paneMeta map[uint32]PaneMeta
	// hosts is the canned roster host.list answers with (nil ⇒ a single
	// connected local host, the shape of a session with no hosts: block).
	hosts []HostInfo
	// lastHostAttach / lastHostDetach are the params of the last roster edit the
	// dispatcher forwarded, so a test can prove the shape checks ran on the
	// dispatcher side and the rest arrived intact.
	lastHostAttach HostAttachParams
	lastHostDetach HostDetachParams
	// staged records StageSpawn calls so tab.create tests can assert the
	// override reached the backend (and, via the log, before applyModel).
	staged map[uint32]SpawnOverride
	// view is the window this fake stands in for, and viewMoves records every
	// SetViewWorkspace the dispatcher asked for — the two halves of the
	// per-window viewport (view.go). The fake applies a move to its own view,
	// the way catway applies it to the issuing connection, so a test can drive
	// a sequence of commands as one window.
	view      View
	viewMoves []string
}

// SetViewWorkspace moves the fake's view, standing in for catway moving the
// issuing connection's. The session's active workspace is deliberately NOT
// touched: that is the runtime's primary-view bookkeeping, not the
// dispatcher's, and a test that asserted it here would be asserting the
// old shared-viewport behaviour under a new name.
func (b *fakeBackend) SetViewWorkspace(wsID string) {
	b.view.WorkspaceID = wsID
	b.viewMoves = append(b.viewMoves, wsID)
	b.rec("setViewWorkspace")
}

// Recorder makes the fake a recording backend (record.go). It is nil in every
// harness but the recorder's own, which is the point: the optional interface
// means a backend that does not record is a backend that returns nil here.
func (b *fakeBackend) Recorder() Recorder { return b.recorder }

func (b *fakeBackend) rec(s string)                { *b.log = append(*b.log, s) }
func (b *fakeBackend) Area() layout.Rect           { return b.area }
func (b *fakeBackend) ApplyModel()                 { b.rec("applyModel") }
func (b *fakeBackend) BroadcastLayout()            { b.rec("broadcastLayout") }
func (b *fakeBackend) BroadcastPaneTitle(p uint32) { b.rec("title"); b.lastTitle = p }
func (b *fakeBackend) BroadcastFlags()             { b.rec("broadcastFlags") }
func (b *fakeBackend) KeepPane(pane uint32) bool   { b.rec("keepPane"); return true }
func (b *fakeBackend) PaneExists(uint32) bool      { return b.paneExists }
func (b *fakeBackend) DaemonConnected() bool       { return b.daemonUp }

// PaneHostConnected tracks daemonUp: the fake session is single-host, so every
// pane's host is the default one.
func (b *fakeBackend) PaneHostConnected(uint32) bool { return b.daemonUp }

func (b *fakeBackend) PaneMeta(p uint32) PaneMeta { return b.paneMeta[p] }

// Hosts answers with whatever the test installed, defaulting to the shape a
// single-host session has: one connected local host.
func (b *fakeBackend) Hosts() []HostInfo {
	if b.hosts == nil {
		return []HostInfo{{
			ID: "local", Label: "local", Connected: b.daemonUp,
			AddrKind: "unix", Default: true, Local: true,
		}}
	}
	return b.hosts
}

// HostAttach / HostDetach record the call and echo the roster: the dispatcher's
// share of host.attach/host.detach is the shape and existence checks, and these
// let a test see which of them let a request through.
func (b *fakeBackend) HostAttach(r Responder, p HostAttachParams) {
	b.rec("hostAttach")
	b.lastHostAttach = p
	r.OK(HostListResult{Hosts: b.Hosts()})
}

func (b *fakeBackend) HostDetach(r Responder, p HostDetachParams) {
	b.rec("hostDetach")
	b.lastHostDetach = p
	r.OK(HostListResult{Hosts: b.Hosts()})
}

func (b *fakeBackend) UINotify(r Responder, p UINotifyParams) {
	b.rec("uiNotify:" + p.Kind + ":" + p.Title)
	b.lastNotify = p
	r.OK(UINotifyResult{ID: "n1"})
}

func (b *fakeBackend) UIAction(r Responder, p UIActionParams) {
	b.rec("uiAction:" + p.ID + ":" + p.Action)
	r.OK(nil)
}

func (b *fakeBackend) EditorConfig() EditorInfo { return b.editor }

func (b *fakeBackend) LedgerOutput(r Responder, p LedgerBlockParams) {
	b.rec("ledgerOutput:" + strconv.FormatUint(uint64(p.Pane), 10) + ":" + strconv.FormatUint(p.Block, 10))
	r.OK(LedgerOutputResult{Found: true, Text: "output"})
}

func (b *fakeBackend) LedgerJump(r Responder, p LedgerBlockParams) {
	b.rec("ledgerJump:" + strconv.FormatUint(uint64(p.Pane), 10) + ":" + strconv.FormatUint(p.Block, 10))
	r.OK(nil)
}

func (b *fakeBackend) LedgerList(r Responder, p LedgerListParams) {
	b.rec("ledgerList:" + p.Host + ":" + p.Contains)
	b.lastLedger = p
	r.OK(LedgerListResult{Entries: b.ledgerEntries})
}

func (b *fakeBackend) RunbookList(r Responder) {
	b.rec("runbookList")
	r.OK(RunbookListResult{})
}

func (b *fakeBackend) RunbookRun(r Responder, p RunbookRunParams) {
	b.rec("runbookRun:" + p.Name)
	b.lastRunbook = p
	r.OK(RunbookRunResult{Name: p.Name})
}

func (b *fakeBackend) RunbookRecord(r Responder, p RunbookRecordParams) {
	b.rec("runbookRecord:" + p.Action + ":" + p.Name)
	b.lastRecord = p
	r.OK(RunbookRecordResult{Action: p.Action, Name: p.Name})
}

func (b *fakeBackend) OpenFileIn(pane uint32, p OpenFileParams) {
	b.rec("openFileIn:" + strconv.FormatUint(uint64(pane), 10) + ":" + p.Path)
	b.lastOpen = p
	b.lastOpenPane = pane
}

func (b *fakeBackend) RefreshUsage()       { b.rec("refreshUsage") }
func (b *fakeBackend) ReloadConfig() error { b.rec("reload"); return b.reloadErr }
func (b *fakeBackend) Shutdown()           { b.rec("shutdown") }

func (b *fakeBackend) ChatSend(r Responder, p ChatSendParams) { b.rec("chatSend"); r.OK(nil) }
func (b *fakeBackend) ChatCancel(r Responder)                 { b.rec("chatCancel"); r.OK(nil) }
func (b *fakeBackend) ChatPermission(r Responder, p ChatPermissionParams) {
	b.rec("chatPerm")
	r.OK(nil)
}
func (b *fakeBackend) ChatClear(r Responder) { b.rec("chatClear"); r.OK(nil) }

func (b *fakeBackend) ScrollPane(pane uint32, delta int) error {
	b.rec("scroll")
	b.lastScroll = [2]int{int(pane), delta}
	return b.scrollErr
}
func (b *fakeBackend) SendInput(pane uint32, text string, submit bool) error {
	b.rec("sendInput")
	b.lastSend = SendInputParams{Pane: pane, Text: text, Submit: submit}
	return b.sendErr
}
func (b *fakeBackend) StageSpawn(pane uint32, ov SpawnOverride) {
	b.rec("stageSpawn")
	if b.staged == nil {
		b.staged = make(map[uint32]SpawnOverride)
	}
	b.staged[pane] = ov
}
func (b *fakeBackend) StartRead(r Responder, _ ReadParams) { b.rec("startRead"); b.lastRead = r }
func (b *fakeBackend) StartCapture(r Responder, _ CaptureParams) {
	b.rec("startCapture")
	b.lastCapture = r
}
func (b *fakeBackend) StartWaitForOutput(r Responder, p WaitForOutputParams) {
	b.rec("startWait")
	b.lastWait = r
	b.lastWaitP = p
}
func (b *fakeBackend) StartWorktreeList(r Responder, _ WorktreeListParams) {
	b.rec("wtList")
	b.lastWtList = r
}
func (b *fakeBackend) StartWorktreeCreate(r Responder, p WorktreeCreateParams) {
	b.rec("wtCreate")
	b.lastWtCreate = r
	b.lastWtCreP = p
}
func (b *fakeBackend) StartWorktreeOpen(r Responder, p WorktreeOpenParams) {
	b.rec("wtOpen")
	b.lastWtOpen = r
	b.lastWtOpenP = p
}
func (b *fakeBackend) StartWorktreeRemove(r Responder, p WorktreeRemoveParams) {
	b.rec("wtRemove")
	b.lastWtRemove = r
	b.lastWtRemP = p
}
func (b *fakeBackend) StartPluginList(r Responder) {
	b.rec("plgList")
	b.lastPlgList = r
}
func (b *fakeBackend) StartPluginUninstall(r Responder, p PluginUninstallParams) {
	b.rec("plgUninstall")
	b.lastPlgUnins = r
	b.lastPlgUninP = p
}
func (b *fakeBackend) StartPathList(r Responder, p PathListParams) {
	b.rec("pathList")
	b.lastPathList = r
	b.lastPathP = p
}
func (b *fakeBackend) StartFileStat(r Responder, p FileStatParams) {
	b.rec("fileStat")
	b.lastFileStatP = p
	r.OK(FileStatResult{Path: p.Path})
}
func (b *fakeBackend) StartFileGet(r Responder, p FileGetParams) {
	b.rec("fileGet")
	b.lastFileGetP = p
	b.lastFileGet = r
}
func (b *fakeBackend) StartFilePut(r Responder, p FilePutParams) {
	b.rec("filePut")
	b.lastFilePutP = p
	r.OK(FilePutResult{Path: p.Path, Written: len(p.Data), Complete: !p.More})
}
func (b *fakeBackend) ConfigGet(r Responder) { b.rec("cfgGet"); r.OK(ConfigGetResult{Path: "/cfg"}) }
func (b *fakeBackend) ConfigSet(r Responder, p ConfigSetParams) {
	b.rec("cfgSet")
	b.lastCfgSetP = p
	r.OK(nil)
}
func (b *fakeBackend) ThemeList(r Responder) { b.rec("themeList"); r.OK(ThemeListResult{}) }
func (b *fakeBackend) ThemeSave(r Responder, p ThemeSaveParams) {
	b.rec("themeSave")
	b.lastThemeSaveP = p
	r.OK(nil)
}
func (b *fakeBackend) ThemeDelete(r Responder, p ThemeDeleteParams) {
	b.rec("themeDelete")
	b.lastThemeDelP = p
	r.OK(nil)
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

// jsonDec mirrors catway's browser param decoder: empty ⇒ ErrNoParams.
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
	// setViewWorkspace first: pane.focus reveals the pane in the *issuing*
	// window, so the view moves to the pane's workspace before the layout that
	// window is about to be sent is built.
	if got := *h.log; len(got) != 3 || got[0] != "setViewWorkspace" || got[1] != "broadcastLayout" || got[2] != "ok" {
		t.Fatalf("focus effects = %v, want [setViewWorkspace broadcastLayout ok]", got)
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

// pane.swap_with exchanges two panes' layout slots and reconciles; same-pane
// or unknown ids fail without effects.
func TestDispatchSwapWith(t *testing.T) {
	h := newCmdHarness(t)
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH}), h.resp())
	ids := h.s.VisiblePaneIDs()
	if len(ids) != 2 {
		t.Fatalf("setup: want 2 panes, got %d", len(ids))
	}
	before := make([]uint32, len(ids))
	for i, id := range ids {
		before[i] = uint32(id)
	}

	r := h.resp()
	h.d.Dispatch(CmdPaneSwapWith, params(t, SwapWithParams{Pane: before[0], Target: before[1]}), r)
	if !r.okCall || r.failCall {
		t.Fatalf("swap_with should ack: ok=%v fail=%v (%q)", r.okCall, r.failCall, r.errMsg)
	}
	after := h.s.VisiblePaneIDs()
	if uint32(after[0]) != before[1] || uint32(after[1]) != before[0] {
		t.Fatalf("swap_with should exchange slots: before=%v after=%v", before, after)
	}

	r = h.resp()
	h.d.Dispatch(CmdPaneSwapWith, params(t, SwapWithParams{Pane: before[0], Target: before[0]}), r)
	if !r.failCall {
		t.Fatal("same-pane swap should fail")
	}
	r = h.resp()
	h.d.Dispatch(CmdPaneSwapWith, params(t, SwapWithParams{Pane: before[0], Target: 9999}), r)
	if !r.failCall {
		t.Fatal("unknown target swap should fail")
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

	if !r.failCall || r.errMsg != "cathost daemon not connected" {
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

// usage.refresh nudges the poller and acks the ask — the reading itself lands
// later as a broadcast, so the command has nothing to return.
func TestDispatchUsageRefresh(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdUsageRefresh, noParams(), r)

	if got := *h.log; len(got) != 2 || got[0] != "refreshUsage" || got[1] != "ok" {
		t.Fatalf("usage.refresh effects = %v, want [refreshUsage ok]", got)
	}
	if r.data != nil {
		t.Errorf("usage.refresh returned data %v, want none", r.data)
	}
}

// Every name CommandNames() advertises must actually be routed by Dispatch —
// none may fall through to the unknown-command default. This guards the
// enumeration (which CLI/control-API clients trust) against drifting from the
// switch. A command may still fail for a domain/params reason on empty input;
// we only reject the "not supported yet" fall-through.
//
// This is the runtime half of the drift guard; TestCommandSpecsRouted reads the
// switch's AST for the direction a dispatch cannot show — a command routed but
// never enumerated. The unrouted case below keeps this half honest: without it,
// a default clause that stopped saying "not supported yet" would make the whole
// sweep vacuous.
func TestCommandNamesAllRouted(t *testing.T) {
	const unknown = "not supported yet"
	for _, name := range CommandNames() {
		h := newCmdHarness(t) // fresh session per command; order-independent
		r := h.resp()
		h.d.Dispatch(name, noParams(), r)
		if r.failCall && strings.Contains(r.errMsg, unknown) {
			t.Errorf("command %q is enumerated but not routed by Dispatch (%q)", name, r.errMsg)
		}
	}

	h := newCmdHarness(t)
	r := h.resp()
	h.d.Dispatch("pane.definitely_not_a_command", noParams(), r)
	if !r.failCall || !strings.Contains(r.errMsg, unknown) {
		t.Fatalf("an unknown command must fail with %q, got fail=%v %q", unknown, r.failCall, r.errMsg)
	}
}

// --- pane.wait_for_output ----------------------------------------------------

// With no reply channel a wait yields nothing to await, so it short-circuits
// before registering a waiter (no backend effect, no reply).
func TestDispatchWaitNoReply(t *testing.T) {
	h := newCmdHarness(t)
	r := &fakeResponder{log: h.log, wants: false}
	h.d.Dispatch(CmdWaitForOutput, params(t, WaitForOutputParams{Pane: 1, Pattern: "x"}), r)
	if r.okCall || r.failCall {
		t.Fatalf("no-reply wait should not resolve: ok=%v fail=%v", r.okCall, r.failCall)
	}
	if len(*h.log) != 0 {
		t.Fatalf("no-reply wait should not start a waiter: log=%v", *h.log)
	}
}

// An empty pattern / bad regex is rejected as bad params before a waiter starts.
func TestDispatchWaitBadPattern(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    WaitForOutputParams
		want string
	}{
		{"empty", WaitForOutputParams{Pane: 1}, "empty pattern"},
		{"badRegex", WaitForOutputParams{Pane: 1, Pattern: "(", Regex: true}, "bad regex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCmdHarness(t)
			r := h.resp()
			h.d.Dispatch(CmdWaitForOutput, params(t, tc.p), r)
			if !r.failCall || !strings.Contains(r.errMsg, tc.want) {
				t.Fatalf("fail=%v msg=%q, want %q", r.failCall, r.errMsg, tc.want)
			}
			if h.b.lastWait != nil {
				t.Fatalf("bad pattern should not start a waiter")
			}
		})
	}
}

// The daemon-round-trip gates (unknown pane, daemon down) fail before starting.
func TestDispatchWaitGated(t *testing.T) {
	unknown := newCmdHarness(t)
	unknown.b.paneExists = false
	r := unknown.resp()
	unknown.d.Dispatch(CmdWaitForOutput, params(t, WaitForOutputParams{Pane: 9, Pattern: "x"}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "unknown pane") {
		t.Fatalf("unknown pane: fail=%v msg=%q", r.failCall, r.errMsg)
	}

	down := newCmdHarness(t)
	down.b.daemonUp = false
	r = down.resp()
	down.d.Dispatch(CmdWaitForOutput, params(t, WaitForOutputParams{Pane: 1, Pattern: "x"}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "not connected") {
		t.Fatalf("daemon down: fail=%v msg=%q", r.failCall, r.errMsg)
	}
}

// A valid wait registers with the backend and does not resolve synchronously; the
// params are forwarded intact.
func TestDispatchWaitStarts(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()
	p := WaitForOutputParams{Pane: 1, Pattern: "ready", TimeoutMs: 5000}
	h.d.Dispatch(CmdWaitForOutput, params(t, p), r)
	if r.okCall || r.failCall {
		t.Fatalf("wait should not resolve synchronously: ok=%v fail=%v", r.okCall, r.failCall)
	}
	if len(*h.log) != 1 || (*h.log)[0] != "startWait" {
		t.Fatalf("expected a single startWait, log=%v", *h.log)
	}
	if h.b.lastWait != r || h.b.lastWaitP != p {
		t.Fatalf("wait not forwarded: resp=%v params=%+v", h.b.lastWait == r, h.b.lastWaitP)
	}
}

// send_input forwards pane/text/submit to the backend and acks synchronously.
func TestDispatchSendInput(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()
	p := SendInputParams{Pane: 3, Text: "make test", Submit: true}

	h.d.Dispatch(CmdPaneSendInput, params(t, p), r)

	if !r.okCall {
		t.Fatalf("send_input should ack: fail=%v msg=%q", r.failCall, r.errMsg)
	}
	if got := *h.log; len(got) != 2 || got[0] != "sendInput" || got[1] != "ok" {
		t.Fatalf("send_input effects = %v, want [sendInput ok]", got)
	}
	if h.b.lastSend != p {
		t.Fatalf("send_input params = %+v, want %+v", h.b.lastSend, p)
	}
}

// A submit-only send (bare Enter) is valid; an empty send (no text, no submit)
// is rejected as bad params before reaching the backend.
func TestDispatchSendInputEmpty(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()
	h.d.Dispatch(CmdPaneSendInput, params(t, SendInputParams{Pane: 1, Submit: true}), r)
	if !r.okCall || h.b.lastSend != (SendInputParams{Pane: 1, Submit: true}) {
		t.Fatalf("submit-only send should reach the backend: ok=%v last=%+v", r.okCall, h.b.lastSend)
	}

	h = newCmdHarness(t)
	r = h.resp()
	h.d.Dispatch(CmdPaneSendInput, params(t, SendInputParams{Pane: 1}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "bad params") {
		t.Fatalf("empty send: fail=%v msg=%q", r.failCall, r.errMsg)
	}
	if len(*h.log) != 1 { // just the fail — no backend effect
		t.Fatalf("empty send must not reach the backend, log=%v", *h.log)
	}
}

// send_input shares read/capture's gates (unknown pane, daemon down) and
// surfaces a backend send error as a failure.
func TestDispatchSendInputGated(t *testing.T) {
	unknown := newCmdHarness(t)
	unknown.b.paneExists = false
	r := unknown.resp()
	unknown.d.Dispatch(CmdPaneSendInput, params(t, SendInputParams{Pane: 9, Text: "x"}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "unknown pane") {
		t.Fatalf("unknown pane: fail=%v msg=%q", r.failCall, r.errMsg)
	}

	down := newCmdHarness(t)
	down.b.daemonUp = false
	r = down.resp()
	down.d.Dispatch(CmdPaneSendInput, params(t, SendInputParams{Pane: 1, Text: "x"}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "not connected") {
		t.Fatalf("daemon down: fail=%v msg=%q", r.failCall, r.errMsg)
	}

	failing := newCmdHarness(t)
	failing.b.sendErr = errors.New("pane 1 has exited")
	r = failing.resp()
	failing.d.Dispatch(CmdPaneSendInput, params(t, SendInputParams{Pane: 1, Text: "x"}), r)
	if !r.failCall || r.errMsg != "pane 1 has exited" {
		t.Fatalf("backend error: fail=%v msg=%q", r.failCall, r.errMsg)
	}
}

// Matcher compiles a substring or regex predicate, returns the matched line for
// context, and validates the pattern.
func TestWaitForOutputMatcher(t *testing.T) {
	sub, err := WaitForOutputParams{Pattern: "DONE"}.Matcher()
	if err != nil {
		t.Fatalf("substring matcher: %v", err)
	}
	if line, ok := sub("building\n  all DONE here  \nnext"); !ok || line != "all DONE here" {
		t.Fatalf("substring match: line=%q ok=%v", line, ok)
	}
	if _, ok := sub("nothing to see"); ok {
		t.Fatalf("substring should not match")
	}

	re, err := WaitForOutputParams{Pattern: `exit code \d+`, Regex: true}.Matcher()
	if err != nil {
		t.Fatalf("regex matcher: %v", err)
	}
	if line, ok := re("run\nexit code 42\n"); !ok || line != "exit code 42" {
		t.Fatalf("regex match: line=%q ok=%v", line, ok)
	}

	if _, err := (WaitForOutputParams{}).Matcher(); err == nil {
		t.Fatalf("empty pattern should error")
	}
	if _, err := (WaitForOutputParams{Pattern: "(", Regex: true}).Matcher(); err == nil {
		t.Fatalf("bad regex should error")
	}
}

// --- worktree.* / config.* ----------------------------------------------------

// worktree.list is result-only: with no reply channel it short-circuits before
// starting the git round-trip; with one it forwards the caller's responder and
// does not resolve synchronously.
func TestDispatchWorktreeList(t *testing.T) {
	silent := newCmdHarness(t)
	r := &fakeResponder{log: silent.log, wants: false}
	silent.d.Dispatch(CmdWorktreeList, noParams(), r)
	if len(*silent.log) != 0 || silent.b.lastWtList != nil {
		t.Fatalf("id-less worktree.list should do nothing, log=%v", *silent.log)
	}

	h := newCmdHarness(t)
	rr := h.resp()
	h.d.Dispatch(CmdWorktreeList, noParams(), rr)
	if rr.okCall || rr.failCall {
		t.Fatalf("worktree.list must not resolve synchronously")
	}
	if got := *h.log; len(got) != 1 || got[0] != "wtList" || h.b.lastWtList != Responder(rr) {
		t.Fatalf("worktree.list effects = %v", got)
	}
}

// worktree.create forwards its params (all optional — the backend defaults the
// branch and path) and resolves asynchronously.
func TestDispatchWorktreeCreateForwards(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()
	p := WorktreeCreateParams{Branch: "worktree/brave-river-0001", Path: "/w/repo/brave-river"}

	h.d.Dispatch(CmdWorktreeCreate, params(t, p), r)

	if r.okCall || r.failCall {
		t.Fatalf("worktree.create must not resolve synchronously")
	}
	if h.b.lastWtCreate != Responder(r) || h.b.lastWtCreP != p {
		t.Fatalf("worktree.create not forwarded: %+v", h.b.lastWtCreP)
	}
}

// The required-field gates: worktree.open needs a path, worktree.remove a
// workspace id — both fail before reaching the backend.
func TestDispatchWorktreeRequiredFields(t *testing.T) {
	for _, tc := range []struct {
		name, cmd, want string
	}{
		{"open", CmdWorktreeOpen, "path is required"},
		{"remove", CmdWorktreeRemove, "workspace is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCmdHarness(t)
			r := h.resp()
			h.d.Dispatch(tc.cmd, noParams(), r)
			if !r.failCall || !strings.Contains(r.errMsg, tc.want) {
				t.Fatalf("fail=%v msg=%q, want %q", r.failCall, r.errMsg, tc.want)
			}
			if h.b.lastWtOpen != nil || h.b.lastWtRemove != nil {
				t.Fatalf("missing required field must not reach the backend")
			}
		})
	}
}

// worktree.open / worktree.remove forward their params to the backend.
func TestDispatchWorktreeOpenRemoveForward(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()
	h.d.Dispatch(CmdWorktreeOpen, params(t, WorktreeOpenParams{Path: "/w/repo/x"}), r)
	if h.b.lastWtOpen != Responder(r) || h.b.lastWtOpenP.Path != "/w/repo/x" {
		t.Fatalf("worktree.open not forwarded: %+v", h.b.lastWtOpenP)
	}

	h = newCmdHarness(t)
	r = h.resp()
	h.d.Dispatch(CmdWorktreeRemove, params(t, WorktreeRemoveParams{Workspace: "w2", Force: true}), r)
	if h.b.lastWtRemove != Responder(r) || h.b.lastWtRemP != (WorktreeRemoveParams{Workspace: "w2", Force: true}) {
		t.Fatalf("worktree.remove not forwarded: %+v", h.b.lastWtRemP)
	}
}

// config.get is result-only (short-circuits with no reply channel); config.set
// forwards the decoded sections.
func TestDispatchConfig(t *testing.T) {
	silent := newCmdHarness(t)
	r := &fakeResponder{log: silent.log, wants: false}
	silent.d.Dispatch(CmdConfigGet, noParams(), r)
	if len(*silent.log) != 0 {
		t.Fatalf("id-less config.get should do nothing, log=%v", *silent.log)
	}

	h := newCmdHarness(t)
	rr := h.resp()
	h.d.Dispatch(CmdConfigGet, noParams(), rr)
	if !rr.okCall {
		t.Fatalf("config.get should resolve through the backend")
	}
	if res, ok := rr.data.(ConfigGetResult); !ok || res.Path != "/cfg" {
		t.Fatalf("config.get data = %#v", rr.data)
	}

	h = newCmdHarness(t)
	rr = h.resp()
	h.d.Dispatch(CmdConfigSet, params(t, ConfigSetParams{Theme: &ConfigTheme{Font: "monospace"}}), rr)
	if !rr.okCall || h.b.lastCfgSetP.Theme == nil || h.b.lastCfgSetP.Theme.Font != "monospace" {
		t.Fatalf("config.set not forwarded: %+v", h.b.lastCfgSetP)
	}
}

// plugin.list is result-only: with no reply channel it short-circuits before
// scanning the plugins root; with one it forwards the caller's responder and
// does not resolve synchronously (the backend's disk scan resolves it later).
func TestDispatchPluginList(t *testing.T) {
	silent := newCmdHarness(t)
	r := &fakeResponder{log: silent.log, wants: false}
	silent.d.Dispatch(CmdPluginList, noParams(), r)
	if len(*silent.log) != 0 || silent.b.lastPlgList != nil {
		t.Fatalf("reply-less plugin.list should do nothing, log=%v", *silent.log)
	}

	h := newCmdHarness(t)
	rr := h.resp()
	h.d.Dispatch(CmdPluginList, noParams(), rr)
	if rr.okCall || rr.failCall {
		t.Fatalf("plugin.list must not resolve synchronously")
	}
	if got := *h.log; len(got) != 1 || got[0] != "plgList" || h.b.lastPlgList != Responder(rr) {
		t.Fatalf("plugin.list effects = %v", got)
	}
}

// plugin.uninstall requires an id (fails before the backend) and otherwise
// forwards its params, resolving asynchronously.
func TestDispatchPluginUninstall(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()
	h.d.Dispatch(CmdPluginUninstall, params(t, PluginUninstallParams{}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "id is required") {
		t.Fatalf("empty id: fail=%v msg=%q", r.failCall, r.errMsg)
	}
	if h.b.lastPlgUnins != nil {
		t.Fatalf("missing id must not reach the backend")
	}

	h = newCmdHarness(t)
	r = h.resp()
	h.d.Dispatch(CmdPluginUninstall, params(t, PluginUninstallParams{ID: "acme.todo"}), r)
	if r.okCall || r.failCall {
		t.Fatalf("plugin.uninstall must not resolve synchronously")
	}
	if h.b.lastPlgUnins != Responder(r) || h.b.lastPlgUninP.ID != "acme.todo" {
		t.Fatalf("plugin.uninstall not forwarded: %+v", h.b.lastPlgUninP)
	}
}

// path.list is result-only like the other listings: no reply channel means no
// listing to send. Params are optional (a bare path.list is "the anchor
// directory") and forwarded verbatim; the backend's directory read resolves the
// responder later.
func TestDispatchPathList(t *testing.T) {
	silent := newCmdHarness(t)
	r := &fakeResponder{log: silent.log, wants: false}
	silent.d.Dispatch(CmdPathList, noParams(), r)
	if len(*silent.log) != 0 || silent.b.lastPathList != nil {
		t.Fatalf("reply-less path.list should do nothing, log=%v", *silent.log)
	}

	h := newCmdHarness(t)
	rr := h.resp()
	h.d.Dispatch(CmdPathList, noParams(), rr)
	if rr.okCall || rr.failCall {
		t.Fatalf("path.list must not resolve synchronously")
	}
	if got := *h.log; len(got) != 1 || got[0] != "pathList" || h.b.lastPathList != Responder(rr) {
		t.Fatalf("path.list effects = %v", got)
	}

	h = newCmdHarness(t)
	rr = h.resp()
	h.d.Dispatch(CmdPathList, params(t, PathListParams{Dir: "~/projs/", Recents: true}), rr)
	if h.b.lastPathP.Dir != "~/projs/" || !h.b.lastPathP.Recents {
		t.Fatalf("path.list params not forwarded: %+v", h.b.lastPathP)
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

// tab.create returns the new tab's public number and its root pane id (the
// globally focused pane after the switch), so an automation client can drive
// the fresh pane without diffing pane.list.
func TestDispatchTabCreateResult(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdTabCreate, noParams(), r)

	got := okData[TabCreateResult](t, r)
	if lg := *h.log; len(lg) != 2 || lg[0] != "applyModel" || lg[1] != "ok" {
		t.Fatalf("tab.create effects = %v, want [applyModel ok]", lg)
	}
	if got.Num != 2 {
		t.Fatalf("new tab num = %d, want 2", got.Num)
	}
	focused, ok := h.s.FocusedPane()
	if !ok || got.Pane != uint32(focused) {
		t.Fatalf("root pane = %d, want the focused pane %d", got.Pane, focused)
	}
	// The focused pane must be the newly created one, not the original root —
	// the whole point of returning it.
	if len(h.s.AllPaneIDs()) != 2 {
		t.Fatalf("pane count = %d, want 2", len(h.s.AllPaneIDs()))
	}
}

// workspace.create names the new workspace when asked, and stays a no-params
// command otherwise — the shape a key binding and `catctl new-ws` send. Both
// forms return the new workspace's id.
func TestDispatchWorkspaceCreate(t *testing.T) {
	t.Run("named", func(t *testing.T) {
		h := newCmdHarness(t)
		r := h.resp()

		h.d.Dispatch(CmdWorkspaceCreate, params(t, WorkspaceCreateParams{Name: "api rewrite"}), r)

		got := okData[WorkspaceCreateResult](t, r)
		if got.ID == "" {
			t.Fatal("workspace.create returned no id")
		}
		// The rename must land before applyModel, so the workspace never reaches
		// observers under its auto-name first.
		// The issuing window follows the workspace it just created, and it does
		// so before applyModel — the layout that call broadcasts is the one
		// that has to show the new workspace already active in this window.
		if lg := *h.log; len(lg) != 3 || lg[0] != "setViewWorkspace" || lg[1] != "applyModel" || lg[2] != "ok" {
			t.Fatalf("workspace.create effects = %v, want [setViewWorkspace applyModel ok]", lg)
		}
		r = h.resp()
		h.d.Dispatch(CmdWorkspaceList, noParams(), r)
		for _, ws := range okData[WorkspaceListResult](t, r).Workspaces {
			if ws.ID == got.ID && ws.Name != "api rewrite" {
				t.Fatalf("new workspace name = %q, want the requested name", ws.Name)
			}
		}
	})

	t.Run("unnamed keeps auto-naming", func(t *testing.T) {
		h := newCmdHarness(t)
		before := okDataFor[WorkspaceListResult](t, h, CmdWorkspaceList)
		r := h.resp()

		h.d.Dispatch(CmdWorkspaceCreate, noParams(), r)

		got := okData[WorkspaceCreateResult](t, r)
		after := okDataFor[WorkspaceListResult](t, h, CmdWorkspaceList)
		if len(after.Workspaces) != len(before.Workspaces)+1 {
			t.Fatalf("workspace count = %d, want %d", len(after.Workspaces), len(before.Workspaces)+1)
		}
		for _, ws := range after.Workspaces {
			// Auto-naming derives the label from the workspace's cwd; all that
			// matters here is that the create did not leave it blank.
			if ws.ID == got.ID && ws.Name == "" {
				t.Fatal("unnamed workspace has no display name; auto-naming was lost")
			}
		}
	})
}

// workspace.lock closes a workspace to the two paths that put something new in
// motion inside it — a supplied command line, and typed input — while leaving
// everything a user does by hand alone. The flag rides the layout broadcast (it
// is durable, sidebar-visible state) and shows up in workspace.list.
func TestDispatchWorkspaceLock(t *testing.T) {
	// locked reports the active workspace's lock state as workspace.list sees it.
	locked := func(t *testing.T, h cmdHarness) bool {
		t.Helper()
		for _, ws := range okDataFor[WorkspaceListResult](t, h, CmdWorkspaceList).Workspaces {
			if ws.Active {
				return ws.Locked
			}
		}
		t.Fatal("no active workspace in workspace.list")
		return false
	}

	t.Run("toggles and broadcasts", func(t *testing.T) {
		h := newCmdHarness(t)
		id := h.s.ActiveWorkspace().ID
		r := h.resp()

		h.d.Dispatch(CmdWorkspaceLock, params(t, LockWorkspaceParams{ID: id, Locked: true}), r)

		if !r.okCall || r.failCall {
			t.Fatalf("workspace.lock: ok=%v fail=%v (%q)", r.okCall, r.failCall, r.errMsg)
		}
		if lg := *h.log; len(lg) != 2 || lg[0] != "broadcastLayout" || lg[1] != "ok" {
			t.Fatalf("workspace.lock effects = %v, want [broadcastLayout ok]", lg)
		}
		if !locked(t, h) {
			t.Fatal("workspace.list does not report the workspace as locked")
		}

		// Re-locking an already-locked workspace acks without a broadcast: the
		// sidebar has nothing new to draw.
		*h.log = nil
		r = h.resp()
		h.d.Dispatch(CmdWorkspaceLock, params(t, LockWorkspaceParams{ID: id, Locked: true}), r)
		if lg := *h.log; len(lg) != 1 || lg[0] != "ok" {
			t.Fatalf("no-op lock effects = %v, want [ok]", lg)
		}

		// Unlocking reopens it.
		r = h.resp()
		h.d.Dispatch(CmdWorkspaceLock, params(t, LockWorkspaceParams{ID: id, Locked: false}), r)
		if !r.okCall || locked(t, h) {
			t.Fatalf("unlock: ok=%v still locked=%v", r.okCall, locked(t, h))
		}
	})

	t.Run("no id locks the active workspace", func(t *testing.T) {
		h := newCmdHarness(t)
		r := h.resp()

		h.d.Dispatch(CmdWorkspaceLock, params(t, LockWorkspaceParams{Locked: true}), r)

		if !r.okCall || !locked(t, h) {
			t.Fatalf("bare lock: ok=%v locked=%v (%q)", r.okCall, locked(t, h), r.errMsg)
		}
	})

	t.Run("unknown workspace fails", func(t *testing.T) {
		h := newCmdHarness(t)
		r := h.resp()

		h.d.Dispatch(CmdWorkspaceLock, params(t, LockWorkspaceParams{ID: "w404", Locked: true}), r)

		if !r.failCall || !strings.Contains(r.errMsg, "unknown workspace") {
			t.Fatalf("unknown workspace: fail=%v msg=%q", r.failCall, r.errMsg)
		}
	})

	t.Run("refuses a supplied command line", func(t *testing.T) {
		h := newCmdHarness(t)
		lock(t, h)
		r := h.resp()

		// The shape a plugin action arrives in (pluginRunAction / catctl plugin run).
		h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{
			Command: []string{"/opt/plug/bin/tool", "--ui"},
			Env:     map[string]string{"CATS_PLUGIN_ID": "x.tool"},
		}), r)

		if !r.failCall || !strings.Contains(r.errMsg, "is locked") {
			t.Fatalf("plugin launch into a locked workspace: fail=%v msg=%q", r.failCall, r.errMsg)
		}
		if lg := *h.log; len(lg) != 1 || lg[0] != "fail" {
			t.Fatalf("a refused launch must run no effects, log=%v", lg)
		}
		if len(h.s.ActiveWorkspace().Tabs) != 1 {
			t.Fatalf("a refused launch must not create a tab, tabs=%d", len(h.s.ActiveWorkspace().Tabs))
		}
	})

	t.Run("still opens a plain tab", func(t *testing.T) {
		h := newCmdHarness(t)
		lock(t, h)
		r := h.resp()

		// A bare tab.create is the user asking for a shell — the lock keeps
		// automation out, it does not put the workspace behind glass.
		h.d.Dispatch(CmdTabCreate, noParams(), r)

		if !r.okCall {
			t.Fatalf("plain tab.create in a locked workspace: fail=%v msg=%q", r.failCall, r.errMsg)
		}
	})

	t.Run("refuses input into its panes", func(t *testing.T) {
		h := newCmdHarness(t)
		lock(t, h)
		pane, _ := h.s.FocusedPane()
		r := h.resp()

		h.d.Dispatch(CmdPaneSendInput, params(t, SendInputParams{Pane: uint32(pane), Text: "make test", Submit: true}), r)

		if !r.failCall || !strings.Contains(r.errMsg, "is locked") {
			t.Fatalf("send_input into a locked workspace: fail=%v msg=%q", r.failCall, r.errMsg)
		}
		if lg := *h.log; len(lg) != 1 || lg[0] != "fail" {
			t.Fatalf("a refused send must not reach the backend, log=%v", lg)
		}

		// Unlocking lets the same send through — the lock is the only thing that
		// was stopping it.
		r = h.resp()
		h.d.Dispatch(CmdWorkspaceLock, params(t, LockWorkspaceParams{Locked: false}), r)
		r = h.resp()
		h.d.Dispatch(CmdPaneSendInput, params(t, SendInputParams{Pane: uint32(pane), Text: "make test", Submit: true}), r)
		if !r.okCall {
			t.Fatalf("send_input after unlock: fail=%v msg=%q", r.failCall, r.errMsg)
		}
	})

	t.Run("leaves other workspaces open", func(t *testing.T) {
		h := newCmdHarness(t)
		lockedID := h.s.ActiveWorkspace().ID
		lock(t, h)
		// A second workspace, which becomes the active one; the lock is per
		// workspace, so its own tab.create must go through.
		if _, err := h.s.CreateWorkspace(); err != nil {
			t.Fatalf("CreateWorkspace: %v", err)
		}
		if h.s.ActiveWorkspace().ID == lockedID {
			t.Fatal("CreateWorkspace did not switch to the new workspace")
		}
		r := h.resp()

		h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{Command: []string{"/bin/echo", "hi"}}), r)

		if !r.okCall {
			t.Fatalf("launch in an unlocked workspace: fail=%v msg=%q", r.failCall, r.errMsg)
		}
	})
}

// lock closes the harness's active workspace to automation, failing the test if
// the command itself does not ack, and clears the effect log so the caller
// asserts only on what it dispatches next.
func lock(t *testing.T, h cmdHarness) {
	t.Helper()
	r := h.resp()
	h.d.Dispatch(CmdWorkspaceLock, params(t, LockWorkspaceParams{Locked: true}), r)
	if !r.okCall {
		t.Fatalf("workspace.lock: fail=%v msg=%q", r.failCall, r.errMsg)
	}
	*h.log = nil
}

// workspace.create's optional path decides where the new workspace's panes
// spawn: absent inherits the session cwd, present-but-empty means the user's
// home, and a typed path is expanded — or, when it does not exist, refused
// outright rather than silently rooted somewhere else.
func TestDispatchWorkspaceCreatePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "proj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	// created dispatches workspace.create with the given params and returns the
	// new workspace's identity cwd (what every pane in it spawns in).
	created := func(t *testing.T, h cmdHarness, p WorkspaceCreateParams) string {
		t.Helper()
		r := h.resp()
		h.d.Dispatch(CmdWorkspaceCreate, params(t, p), r)
		id := okData[WorkspaceCreateResult](t, r).ID
		for _, ws := range h.s.Workspaces() {
			if ws.ID == id {
				return ws.IdentityCwd
			}
		}
		t.Fatalf("workspace %s missing after create", id)
		return ""
	}
	ptr := func(s string) *string { return &s }

	t.Run("absent inherits the session cwd", func(t *testing.T) {
		h := newCmdHarness(t)
		if got := created(t, h, WorkspaceCreateParams{}); got != h.s.Cwd() {
			t.Fatalf("identity cwd = %q, want the session cwd %q", got, h.s.Cwd())
		}
	})

	t.Run("empty starts at home", func(t *testing.T) {
		h := newCmdHarness(t)
		if got := created(t, h, WorkspaceCreateParams{Path: ptr("")}); got != home {
			t.Fatalf("identity cwd = %q, want the home directory %q", got, home)
		}
	})

	t.Run("expands a typed path", func(t *testing.T) {
		h := newCmdHarness(t)
		if got := created(t, h, WorkspaceCreateParams{Path: ptr("~/proj")}); got != proj {
			t.Fatalf("identity cwd = %q, want %q", got, proj)
		}
	})

	t.Run("a bad path fails the command", func(t *testing.T) {
		h := newCmdHarness(t)
		before := len(h.s.Workspaces())
		r := h.resp()

		h.d.Dispatch(CmdWorkspaceCreate, params(t, WorkspaceCreateParams{Path: ptr("/no/such/dir")}), r)

		if !r.failCall || !strings.Contains(r.errMsg, "no such directory") {
			t.Fatalf("bad path: fail=%v err=%q, want a no-such-directory failure", r.failCall, r.errMsg)
		}
		if len(h.s.Workspaces()) != before {
			t.Fatalf("a refused create must not add a workspace (%d → %d)", before, len(h.s.Workspaces()))
		}
	})

	// The mkdir retry the new-workspace dialog sends after the user confirms:
	// the same path that just failed now comes into existence and the
	// workspace roots there.
	t.Run("mkdir creates a missing path", func(t *testing.T) {
		h := newCmdHarness(t)
		want := filepath.Join(home, "brand", "new")
		if got := created(t, h, WorkspaceCreateParams{Path: ptr("~/brand/new"), Mkdir: true}); got != want {
			t.Fatalf("identity cwd = %q, want %q", got, want)
		}
		if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
			t.Fatalf("%s was not created as a directory: %v", want, err)
		}
	})
}

// okDataFor dispatches a read-only query on a fresh responder and returns its
// data — a one-liner for tests that only need a snapshot.
func okDataFor[T any](t *testing.T, h cmdHarness, method string) T {
	t.Helper()
	r := h.resp()
	h.d.Dispatch(method, noParams(), r)
	return okData[T](t, r)
}

// tab.create's optional params pin the tab title and stage a spawn override for
// the root pane — and the staging must precede applyModel, the call that
// actually creates the pane's PTY.
func TestDispatchTabCreateSpawn(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	p := TabCreateParams{
		Title:   "todo",
		Cwd:     "/tmp/proj",
		Command: []string{"/opt/plug/bin/tool", "--ui"},
		Env:     map[string]string{"CATS_PLUGIN_ID": "x.tool"},
	}
	h.d.Dispatch(CmdTabCreate, params(t, p), r)

	got := okData[TabCreateResult](t, r)
	if lg := *h.log; len(lg) != 3 || lg[0] != "stageSpawn" || lg[1] != "applyModel" || lg[2] != "ok" {
		t.Fatalf("tab.create effects = %v, want [stageSpawn applyModel ok]", lg)
	}
	ov, ok := h.b.staged[got.Pane]
	if !ok {
		t.Fatalf("no spawn override staged for pane %d", got.Pane)
	}
	if ov.Cwd != p.Cwd || len(ov.Command) != 2 || ov.Command[0] != p.Command[0] ||
		ov.Env["CATS_PLUGIN_ID"] != "x.tool" {
		t.Fatalf("staged override = %+v, want %+v", ov, p)
	}

	// The title landed on the new tab (same mutation as tab.rename).
	r = h.resp()
	h.d.Dispatch(CmdTabList, noParams(), r)
	tabs := okData[TabListResult](t, r).Tabs
	var found bool
	for _, tb := range tabs {
		if tb.Num == got.Num {
			found = true
			if tb.Name != "todo" {
				t.Fatalf("new tab name = %q, want %q", tb.Name, "todo")
			}
		}
	}
	if !found {
		t.Fatalf("new tab %d missing from tab.list %+v", got.Num, tabs)
	}

	// A bare tab.create stages nothing when there is no cwd to inherit either —
	// the backend knows no pane metadata here (see TestDispatchTabCreateInheritsCwd
	// for the inheriting case).
	r = h.resp()
	h.d.Dispatch(CmdTabCreate, noParams(), r)
	got2 := okData[TabCreateResult](t, r)
	if _, ok := h.b.staged[got2.Pane]; ok {
		t.Fatalf("bare tab.create staged an override for pane %d", got2.Pane)
	}

	// An empty command slot is rejected before any session mutation.
	r = h.resp()
	h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{Command: []string{""}}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "bad params") {
		t.Fatalf("empty command[0]: fail=%v msg=%q", r.failCall, r.errMsg)
	}
}

// A tab opened with no cwd of its own starts in its left-hand neighbor's
// working directory — the tab it lands beside on the bar, which is the
// workspace's active tab, since tab.create inserts directly to its right.
func TestDispatchTabCreateInheritsCwd(t *testing.T) {
	h := newCmdHarness(t)
	h.b.paneMeta = map[uint32]PaneMeta{}
	tabCwd := func(idx int, cwd string) {
		root := h.s.ActiveWorkspace().Tabs[idx].RootPane
		h.b.paneMeta[uint32(root)] = PaneMeta{Cwd: cwd}
	}
	tabCwd(0, "/tmp/one")

	r := h.resp()
	h.d.Dispatch(CmdTabCreate, noParams(), r)

	got := okData[TabCreateResult](t, r)
	// The staging must still precede applyModel — the call that creates the PTY.
	if lg := *h.log; len(lg) != 3 || lg[0] != "stageSpawn" || lg[1] != "applyModel" {
		t.Fatalf("tab.create effects = %v, want [stageSpawn applyModel ok]", lg)
	}
	if ov := h.b.staged[got.Pane]; ov.Cwd != "/tmp/one" {
		t.Fatalf("inherited cwd = %q, want %q", ov.Cwd, "/tmp/one")
	}

	// The neighbor is the focused tab, not the last one: with the user back on
	// tab 1, a third tab lands beside tab 1 and inherits its cwd — not the
	// bar-end tab 2's.
	tabCwd(1, "/tmp/two")
	if err := h.s.FocusTab(1); err != nil {
		t.Fatalf("focus tab 1: %v", err)
	}
	r = h.resp()
	h.d.Dispatch(CmdTabCreate, noParams(), r)
	got = okData[TabCreateResult](t, r)
	if ov := h.b.staged[got.Pane]; ov.Cwd != "/tmp/one" {
		t.Fatalf("inherited cwd = %q, want the focused tab's %q", ov.Cwd, "/tmp/one")
	}
	// And it lands at index 1 — directly right of the focused tab, with the
	// old tab 2 pushed to the end of the bar.
	if root := h.s.ActiveWorkspace().Tabs[1].RootPane; uint32(root) != got.Pane {
		t.Fatalf("new tab root at index 1 = %d, want pane %d", root, got.Pane)
	}

	// An explicit cwd beats the inherited one, and still carries the rest of the
	// override with it.
	r = h.resp()
	h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{Cwd: "/opt/pinned", Command: []string{"top"}}), r)
	got = okData[TabCreateResult](t, r)
	ov := h.b.staged[got.Pane]
	if ov.Cwd != "/opt/pinned" || len(ov.Command) != 1 || ov.Command[0] != "top" {
		t.Fatalf("staged override = %+v, want cwd /opt/pinned with command [top]", ov)
	}
}

// tab.create can name a workspace other than the one on screen — the fan-out
// the browser's "start in all workspaces" plugin launch sends, one call per
// workspace. Everything the command touches has to follow the target: the tab
// itself, the title (tab numbers are per workspace), the returned root pane,
// the inherited cwd, and the lock.
func TestDispatchTabCreateInWorkspace(t *testing.T) {
	// twoWorkspaces returns a harness whose session holds the original
	// workspace plus a second, active one — the layout a fan-out runs against,
	// with a target that is deliberately not the viewport.
	twoWorkspaces := func(t *testing.T) (cmdHarness, string) {
		t.Helper()
		h := newCmdHarness(t)
		away := h.s.ActiveWorkspace().ID
		if _, err := h.s.CreateWorkspace(); err != nil { // switches to the new one
			t.Fatalf("CreateWorkspace: %v", err)
		}
		if h.s.ActiveWorkspace().ID == away {
			t.Fatal("CreateWorkspace did not switch to the new workspace")
		}
		return h, away
	}

	t.Run("lands in the named workspace, not the viewport", func(t *testing.T) {
		h, away := twoWorkspaces(t)
		active := h.s.ActiveWorkspace()
		r := h.resp()

		h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{Workspace: away, Title: "todo"}), r)

		got := okData[TabCreateResult](t, r)
		target := h.s.WorkspaceByID(away)
		if len(target.Tabs) != 2 {
			t.Fatalf("target workspace tabs = %d, want 2", len(target.Tabs))
		}
		if len(active.Tabs) != 1 {
			t.Fatalf("the viewport's workspace grew a tab: tabs=%d", len(active.Tabs))
		}
		// The viewport does not move: a fan-out that switched workspaces would
		// leave the user wherever the last launch landed.
		if h.s.ActiveWorkspace().ID != active.ID {
			t.Fatalf("active workspace = %s, want %s", h.s.ActiveWorkspace().ID, active.ID)
		}
		// The returned pane is the new tab's root, which is *not* the focused
		// pane — that still belongs to the workspace on screen.
		if root := target.Tabs[1].RootPane; got.Pane != uint32(root) {
			t.Fatalf("result pane = %d, want the target's new root pane %d", got.Pane, root)
		}
		if focused, _ := h.s.FocusedPane(); got.Pane == uint32(focused) {
			t.Fatal("result pane must not be the viewport's focused pane")
		}
		// The title has to be applied against the target: tab numbers restart
		// per workspace, so an unscoped rename would hit the viewport's tab of
		// the same number.
		if name := target.Tabs[1].DisplayName(); name != "todo" {
			t.Fatalf("target tab name = %q, want %q", name, "todo")
		}
		if name := active.Tabs[0].DisplayName(); name == "todo" {
			t.Fatal("the title was applied to the viewport's tab")
		}
	})

	t.Run("inherits the target's cwd, not the viewport's", func(t *testing.T) {
		h, away := twoWorkspaces(t)
		target := h.s.WorkspaceByID(away)
		h.b.paneMeta = map[uint32]PaneMeta{
			uint32(target.Tabs[0].RootPane):                {Cwd: "/tmp/away"},
			uint32(h.s.ActiveWorkspace().Tabs[0].RootPane): {Cwd: "/tmp/onscreen"},
		}
		r := h.resp()

		h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{Workspace: away}), r)

		got := okData[TabCreateResult](t, r)
		if ov := h.b.staged[got.Pane]; ov.Cwd != "/tmp/away" {
			t.Fatalf("inherited cwd = %q, want the target's %q", ov.Cwd, "/tmp/away")
		}
	})

	t.Run("the target's lock is the one that decides", func(t *testing.T) {
		h, away := twoWorkspaces(t)
		if _, err := h.s.SetWorkspaceLock(away, true); err != nil {
			t.Fatalf("lock %s: %v", away, err)
		}
		*h.log = nil
		r := h.resp()

		// The viewport's workspace is unlocked; the target is not, and the
		// target is where the process would run.
		h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{Workspace: away, Command: []string{"/opt/plug/bin/tool"}}), r)

		if !r.failCall || !strings.Contains(r.errMsg, "is locked") {
			t.Fatalf("launch into a locked target: fail=%v msg=%q", r.failCall, r.errMsg)
		}
		if lg := *h.log; len(lg) != 1 || lg[0] != "fail" {
			t.Fatalf("a refused launch must run no effects, log=%v", lg)
		}
		if n := len(h.s.WorkspaceByID(away).Tabs); n != 1 {
			t.Fatalf("a refused launch must not create a tab, tabs=%d", n)
		}
		// A bare tab is still the user asking for a shell, target or not.
		r = h.resp()
		h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{Workspace: away}), r)
		if !r.okCall {
			t.Fatalf("plain tab.create into a locked target: fail=%v msg=%q", r.failCall, r.errMsg)
		}
	})

	t.Run("unknown workspace fails before anything is created", func(t *testing.T) {
		h, _ := twoWorkspaces(t)
		before := len(h.s.AllPaneIDs())
		*h.log = nil
		r := h.resp()

		h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{Workspace: "w404"}), r)

		if !r.failCall || !strings.Contains(r.errMsg, "unknown workspace") {
			t.Fatalf("unknown workspace: fail=%v msg=%q", r.failCall, r.errMsg)
		}
		if lg := *h.log; len(lg) != 1 || lg[0] != "fail" {
			t.Fatalf("a refused create must run no effects, log=%v", lg)
		}
		if after := len(h.s.AllPaneIDs()); after != before {
			t.Fatalf("pane count = %d, want %d — nothing should have been created", after, before)
		}
	})
}

// A split opens where the pane it came from is working — the tab-level rule
// applied to the one pane a split unambiguously descends from.
func TestDispatchPaneSplitInheritsCwd(t *testing.T) {
	h := newCmdHarness(t)
	src, _ := h.s.FocusedPane()
	h.b.paneMeta = map[uint32]PaneMeta{uint32(src): {Cwd: "/srv/app"}}

	r := h.resp()
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH}), r)

	if r.failCall {
		t.Fatalf("pane.split failed: %s", r.errMsg)
	}
	// The staging must precede applyModel — the call that creates the PTY.
	if lg := *h.log; len(lg) != 3 || lg[0] != "stageSpawn" || lg[1] != "applyModel" {
		t.Fatalf("pane.split effects = %v, want [stageSpawn applyModel ok]", lg)
	}
	// The split focuses the new pane, so that is the one that was staged.
	np, _ := h.s.FocusedPane()
	if np == src {
		t.Fatal("split did not focus the new pane")
	}
	if ov := h.b.staged[uint32(np)]; ov.Cwd != "/srv/app" {
		t.Fatalf("new pane cwd = %q, want the split pane's %q", ov.Cwd, "/srv/app")
	}

	// A pane the backend knows nothing about stages nothing at all.
	h.b.paneMeta = nil
	r = h.resp()
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitV}), r)
	after, _ := h.s.FocusedPane()
	if _, ok := h.b.staged[uint32(after)]; ok {
		t.Fatalf("split staged an override for pane %d with no cwd to inherit", after)
	}
}

// pane.split hands back the pane it created. This is the whole point of the
// result: the alternative is diffing pane.list around the call, and the id the
// dispatcher already holds is the only unambiguous answer.
func TestDispatchPaneSplitReturnsItsPane(t *testing.T) {
	h := newCmdHarness(t)
	src, _ := h.s.FocusedPane()
	r := h.resp()

	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH}), r)

	got := okData[SplitResult](t, r)
	if got.Pane == uint32(src) {
		t.Fatalf("split reported the pane it split (%d), not the new one", got.Pane)
	}
	// The split focuses the new pane, which is what makes the id usable straight
	// away — a caller can send_input to it without focusing first.
	focused, _ := h.s.FocusedPane()
	if got.Pane != uint32(focused) {
		t.Fatalf("split reported pane %d but focused %d", got.Pane, focused)
	}
	if !slices.Contains(h.s.VisiblePaneIDs(), layout.PaneID(got.Pane)) {
		t.Fatalf("reported pane %d is not in the session, panes=%v", got.Pane, h.s.VisiblePaneIDs())
	}
}

// pane.split's spawn params stage an override for the new pane, exactly as
// tab.create's do — same fields, same precedence over an inherited cwd, same
// ordering against applyModel (which is what creates the PTY).
func TestDispatchPaneSplitSpawn(t *testing.T) {
	h := newCmdHarness(t)
	src, _ := h.s.FocusedPane()
	// A cwd to inherit, so the explicit one has something to beat.
	h.b.paneMeta = map[uint32]PaneMeta{uint32(src): {Cwd: "/srv/app"}}
	r := h.resp()

	p := SplitParams{
		Direction: SplitV,
		Cwd:       "/tmp/proj",
		Command:   []string{"ced", "--remote", "main.go"},
		Env:       map[string]string{"CED_PANE": "1"},
	}
	h.d.Dispatch(CmdPaneSplit, params(t, p), r)

	got := okData[SplitResult](t, r)
	if lg := *h.log; len(lg) != 3 || lg[0] != "stageSpawn" || lg[1] != "applyModel" || lg[2] != "ok" {
		t.Fatalf("pane.split effects = %v, want [stageSpawn applyModel ok]", lg)
	}
	ov, ok := h.b.staged[got.Pane]
	if !ok {
		t.Fatalf("no spawn override staged for pane %d", got.Pane)
	}
	if ov.Cwd != "/tmp/proj" {
		t.Fatalf("staged cwd = %q, want the explicit %q (not the inherited one)", ov.Cwd, "/tmp/proj")
	}
	if len(ov.Command) != 3 || ov.Command[0] != "ced" || ov.Env["CED_PANE"] != "1" {
		t.Fatalf("staged override = %+v, want %+v", ov, p)
	}

	// An empty argv slot is rejected before the session is touched — the same
	// rule tab.create applies, which is the point of sharing the validator.
	before := len(h.s.VisiblePaneIDs())
	r = h.resp()
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH, Command: []string{""}}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "bad params") {
		t.Fatalf("empty command[0]: fail=%v msg=%q", r.failCall, r.errMsg)
	}
	if now := len(h.s.VisiblePaneIDs()); now != before {
		t.Fatalf("a refused split changed the pane set: %d → %d", before, now)
	}
}

// A locked workspace refuses a split that carries a command, and for the same
// reason it refuses tab.create with one: both are ways to start a process there.
// A bare split is still the user asking for a shell and goes through.
func TestDispatchPaneSplitWorkspaceLock(t *testing.T) {
	h := newCmdHarness(t)
	lock(t, h)

	r := h.resp()
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH, Command: []string{"/opt/plug/bin/tool"}}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "is locked") {
		t.Fatalf("split-with-command into a locked workspace: fail=%v msg=%q", r.failCall, r.errMsg)
	}
	if lg := *h.log; len(lg) != 1 || lg[0] != "fail" {
		t.Fatalf("a refused split must run no effects, log=%v", lg)
	}
	if n := len(h.s.VisiblePaneIDs()); n != 1 {
		t.Fatalf("a refused split must not create a pane, panes=%d", n)
	}

	r = h.resp()
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH}), r)
	if !r.okCall {
		t.Fatalf("plain split in a locked workspace: fail=%v msg=%q", r.failCall, r.errMsg)
	}
}

// pane.list and pane.get merge the backend's runtime metadata (agent, its state
// and model, title, cwd) into each PaneInfo; panes the backend knows nothing
// about stay zero-valued.
func TestDispatchPaneMetaMerge(t *testing.T) {
	h := newCmdHarness(t)
	focused, _ := h.s.FocusedPane()
	meta := PaneMeta{Agent: "claude", AgentState: "working", AgentModel: "claude-opus-5", Title: "vim", Cwd: "/tmp/x"}
	h.b.paneMeta = map[uint32]PaneMeta{uint32(focused): meta}

	r := h.resp()
	h.d.Dispatch(CmdPaneList, noParams(), r)
	list := okData[PaneListResult](t, r)
	if len(list.Panes) != 1 || list.Panes[0].PaneMeta != meta {
		t.Fatalf("pane.list meta = %+v, want %+v", list.Panes, meta)
	}

	r = h.resp()
	h.d.Dispatch(CmdPaneGet, noParams(), r)
	info := okData[PaneInfo](t, r)
	if info.PaneMeta != meta {
		t.Fatalf("pane.get meta = %+v, want %+v", info.PaneMeta, meta)
	}
}

// --- theme.* -----------------------------------------------------------------

// The theme library commands route to the backend, and the two mutating verbs
// reject a missing name before any backend effect.
func TestDispatchThemeCommands(t *testing.T) {
	h := newCmdHarness(t)

	r := h.resp()
	h.d.Dispatch(CmdThemeSave, params(t, ThemeSaveParams{Name: "night", Colors: map[string]string{"bg": "#000000"}, Activate: true}), r)
	if !r.okCall || h.b.lastThemeSaveP.Name != "night" || !h.b.lastThemeSaveP.Activate {
		t.Fatalf("theme.save: ok=%v params=%+v", r.okCall, h.b.lastThemeSaveP)
	}

	r = h.resp()
	h.d.Dispatch(CmdThemeDelete, params(t, ThemeDeleteParams{Name: "night"}), r)
	if !r.okCall || h.b.lastThemeDelP.Name != "night" {
		t.Fatalf("theme.delete: ok=%v params=%+v", r.okCall, h.b.lastThemeDelP)
	}

	for _, cmd := range []string{CmdThemeSave, CmdThemeDelete} {
		r = h.resp()
		h.d.Dispatch(cmd, params(t, map[string]any{}), r)
		if !r.failCall {
			t.Errorf("%s without a name should fail", cmd)
		}
	}

	// theme.list with no reply channel short-circuits (nowhere to send it).
	log := *h.log
	silent := &fakeResponder{log: h.log, wants: false}
	h.d.Dispatch(CmdThemeList, noParams(), silent)
	if len(*h.log) != len(log) {
		t.Fatal("no-reply theme.list should not reach the backend")
	}
}

// host.list is a query like the rest, but its answer comes from the Backend
// (the roster is a set of live connections, not domain state), so this checks
// the one thing the dispatcher owns: it asks the backend and hands the answer
// back untouched.
func TestDispatchHostList(t *testing.T) {
	h := newCmdHarness(t)
	h.b.hosts = []HostInfo{
		{ID: "local", Label: "studio", Connected: true, AddrKind: "unix", Default: true, Panes: 2},
		{ID: "devbox", Label: "devbox", Connected: false, AddrKind: "unix", Panes: 1, Error: "dial: no such file"},
	}

	got := okDataFor[HostListResult](t, h, CmdHostList).Hosts

	if len(got) != 2 || got[0].ID != "local" || got[1].ID != "devbox" {
		t.Fatalf("host.list = %+v; want the backend's roster in order", got)
	}
	if !got[0].Default || got[1].Connected || got[1].Error == "" {
		t.Fatalf("host.list dropped roster detail: %+v", got)
	}
}

// A session with no hosts: block answers with the single synthesized host —
// which is how a client tells "this catway has one machine" from "the remote
// one is down" without a capability flag.
func TestDispatchHostListSingleHost(t *testing.T) {
	h := newCmdHarness(t)
	got := okDataFor[HostListResult](t, h, CmdHostList).Hosts
	if len(got) != 1 || !got[0].Default || !got[0].Connected {
		t.Fatalf("single-host roster = %+v", got)
	}
}

// --- Host roster edits (Phase 5): host.attach / host.detach -------------------
//
// The dispatcher owns only the shape checks here — the roster and the config
// file are the backend's — so these pin down which requests it refuses on its
// own and that everything else arrives intact.

func TestDispatchHostAttach(t *testing.T) {
	h := newCmdHarness(t)
	twoHostBackend(h)

	r := h.resp()
	h.d.Dispatch(CmdHostAttach, params(t, HostAttachParams{
		ID: "buildbox", Label: "the build box", Addr: "tls://buildbox:8422",
		TokenFile: "/etc/cats/token", Fingerprint: "ab12", Default: true,
	}), r)
	if !r.okCall {
		t.Fatalf("host.attach: ok=%v fail=%v (%q)", r.okCall, r.failCall, r.errMsg)
	}
	got := h.b.lastHostAttach
	if got.ID != "buildbox" || got.Addr != "tls://buildbox:8422" || got.Label != "the build box" ||
		got.TokenFile != "/etc/cats/token" || got.Fingerprint != "ab12" || !got.Default {
		t.Fatalf("params reached the backend mangled: %+v", got)
	}
	// The id is deliberately NOT checked against the roster here: "already
	// attached" is the backend's answer, since it is the half that knows.
	if !slices.Contains(*h.log, "hostAttach") {
		t.Fatalf("effects = %v", *h.log)
	}
}

// The two fields without which there is no host at all are refused before the
// backend sees them — an attach that reached the config layer with no address
// would be a validation error about a file the caller never mentioned.
func TestDispatchHostAttachRequiresIDAndAddr(t *testing.T) {
	for _, p := range []HostAttachParams{
		{Addr: "unix:///tmp/x.sock"},
		{ID: "devbox"},
	} {
		h := newCmdHarness(t)
		r := h.resp()
		h.d.Dispatch(CmdHostAttach, params(t, p), r)
		if !r.failCall {
			t.Fatalf("host.attach %+v was accepted", p)
		}
		if slices.Contains(*h.log, "hostAttach") {
			t.Fatalf("host.attach %+v reached the backend: %v", p, *h.log)
		}
	}
}

func TestDispatchHostDetach(t *testing.T) {
	h := newCmdHarness(t)
	twoHostBackend(h)

	r := h.resp()
	h.d.Dispatch(CmdHostDetach, params(t, HostDetachParams{ID: "devbox", Force: true}), r)
	if !r.okCall {
		t.Fatalf("host.detach: ok=%v fail=%v (%q)", r.okCall, r.failCall, r.errMsg)
	}
	if got := h.b.lastHostDetach; got.ID != "devbox" || !got.Force {
		t.Fatalf("params reached the backend mangled: %+v", got)
	}
}

// Detaching something the roster never listed is a caller mistake, and one the
// dispatcher can answer from the roster it already reads for every host param —
// so it does, rather than making the backend restate it.
func TestDispatchHostDetachUnknownHost(t *testing.T) {
	h := newCmdHarness(t)
	twoHostBackend(h)

	r := h.resp()
	h.d.Dispatch(CmdHostDetach, params(t, HostDetachParams{ID: "nowhere"}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "nowhere") {
		t.Fatalf("host.detach of an unknown host: fail=%v err=%q", r.failCall, r.errMsg)
	}
	if slices.Contains(*h.log, "hostDetach") {
		t.Fatalf("it reached the backend anyway: %v", *h.log)
	}
}

// --- Host params (Phase 3): which machine a new pane lands on ----------------
//
// twoHostBackend installs a roster with a local default and one remote host,
// the shape every host-param rule below is about: the difference between "the
// workspace's host" and "the machine whose filesystem a path names".
func twoHostBackend(h cmdHarness) {
	h.b.hosts = []HostInfo{
		{ID: "local", Label: "studio", Connected: true, AddrKind: "unix", Default: true, Local: true},
		{ID: "devbox", Label: "devbox", Connected: true, AddrKind: "tls"},
	}
}

// pane.split's host param puts the new pane on another machine, and the model
// is where that has to land — it is what the runtime reads when it picks a
// connection, and what a restore replays. An unknown host is refused outright:
// creating the pane on the default machine instead would look like success and
// put a shell somewhere nobody asked for.
func TestDispatchSplitHost(t *testing.T) {
	h := newCmdHarness(t)
	twoHostBackend(h)
	src, _ := h.s.FocusedPane()

	r := h.resp()
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH, Host: "devbox"}), r)
	got := okData[SplitResult](t, r)

	if h.s.PaneHost(layout.PaneID(got.Pane)) != "devbox" {
		t.Fatalf("new pane host = %q, want devbox", h.s.PaneHost(layout.PaneID(got.Pane)))
	}
	if h.s.PaneHost(src) == "devbox" {
		t.Fatal("the pane being split must stay where it was")
	}

	// A split naming no host lands beside the pane it split, not on the
	// workspace's default: the previous split focused the devbox pane, so this
	// one is a split OF a guest pane and must stay on devbox. The workspace is
	// still the unpinned default one, which is exactly what used to make this
	// come back "" and put the pane on the wrong machine.
	r = h.resp()
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitV}), r)
	if got := okData[SplitResult](t, r); h.s.PaneHost(layout.PaneID(got.Pane)) != "devbox" {
		t.Fatalf("unqualified split of a guest pane = %q, want devbox", h.s.PaneHost(layout.PaneID(got.Pane)))
	}

	// And splitting a pane that is on the default host still yields one there —
	// the rule is "beside this pane", not "always the last host named".
	r = h.resp()
	srcID := uint32(src)
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitV, Pane: &srcID}), r)
	if got := okData[SplitResult](t, r); h.s.PaneHost(layout.PaneID(got.Pane)) != "" {
		t.Fatalf("unqualified split of a local pane = %q, want the default host", h.s.PaneHost(layout.PaneID(got.Pane)))
	}

	before := len(h.s.VisiblePaneIDs())
	r = h.resp()
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH, Host: "nope"}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "unknown host") {
		t.Fatalf("unknown host: fail=%v err=%q", r.failCall, r.errMsg)
	}
	if len(h.s.VisiblePaneIDs()) != before {
		t.Fatal("a refused split must not create a pane")
	}
}

// The cwd a new pane inherits describes a directory in one machine's
// filesystem. When the split crosses hosts it must not be carried over: the
// path either does not exist there — a dead pane — or names a different
// directory that happens to share its spelling, which is worse.
func TestDispatchSplitCrossHostDropsInheritedCwd(t *testing.T) {
	h := newCmdHarness(t)
	twoHostBackend(h)
	src, _ := h.s.FocusedPane()
	h.b.paneMeta = map[uint32]PaneMeta{uint32(src): {Cwd: "/home/me/proj", Host: "local"}}

	// Same host: inherited, as it always was.
	r := h.resp()
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH}), r)
	same := okData[SplitResult](t, r)
	if ov := h.b.staged[same.Pane]; ov.Cwd != "/home/me/proj" {
		t.Fatalf("same-host split cwd = %q, want the source pane's", ov.Cwd)
	}

	// Across hosts: nothing staged at all, so the pane spawns wherever its own
	// host starts panes.
	r = h.resp()
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH, Host: "devbox"}), r)
	cross := okData[SplitResult](t, r)
	if ov, ok := h.b.staged[cross.Pane]; ok {
		t.Fatalf("cross-host split staged %+v, want no spawn override", ov)
	}

	// An explicit cwd still crosses: the caller named a path knowing where the
	// pane is going, which is the one thing this rule must not second-guess.
	r = h.resp()
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH, Host: "devbox", Cwd: "/srv/app"}), r)
	pinned := okData[SplitResult](t, r)
	if ov := h.b.staged[pinned.Pane]; ov.Cwd != "/srv/app" {
		t.Fatalf("explicit cross-host cwd = %q, want /srv/app", ov.Cwd)
	}
}

// tab.create takes the same param, with the same two rules: the tab's root pane
// lands on the named host, and the neighbor tab's cwd is inherited only when it
// is on that same machine.
func TestDispatchTabCreateHost(t *testing.T) {
	h := newCmdHarness(t)
	twoHostBackend(h)
	root := h.s.ActiveWorkspace().Tabs[0].RootPane
	h.b.paneMeta = map[uint32]PaneMeta{uint32(root): {Cwd: "/home/me/proj", Host: "local"}}

	r := h.resp()
	h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{Host: "devbox"}), r)
	got := okData[TabCreateResult](t, r)

	if h.s.PaneHost(layout.PaneID(got.Pane)) != "devbox" {
		t.Fatalf("new tab's pane host = %q, want devbox", h.s.PaneHost(layout.PaneID(got.Pane)))
	}
	if ov, ok := h.b.staged[got.Pane]; ok {
		t.Fatalf("cross-host tab staged %+v, want no inherited cwd", ov)
	}

	r = h.resp()
	h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{Host: "nope"}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "unknown host") {
		t.Fatalf("unknown host: fail=%v err=%q", r.failCall, r.errMsg)
	}
}

// workspace.create pins a whole workspace to a host — and with it, how its path
// is read. A remote path is passed through exactly as typed: this process
// cannot expand "~" for another machine's user, cannot stat the directory, and
// must not refuse a path that is perfectly real over there.
func TestDispatchWorkspaceCreateHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	h := newCmdHarness(t)
	twoHostBackend(h)
	ptr := func(s string) *string { return &s }

	identity := func(t *testing.T, p WorkspaceCreateParams) (string, string) {
		t.Helper()
		r := h.resp()
		h.d.Dispatch(CmdWorkspaceCreate, params(t, p), r)
		id := okData[WorkspaceCreateResult](t, r).ID
		for _, ws := range h.s.Workspaces() {
			if ws.ID == id {
				return ws.HostID, ws.IdentityCwd
			}
		}
		t.Fatalf("workspace %s missing after create", id)
		return "", ""
	}

	host, cwd := identity(t, WorkspaceCreateParams{Host: "devbox", Path: ptr("~/src/api")})
	if host != "devbox" {
		t.Fatalf("workspace host = %q, want devbox", host)
	}
	if cwd != "~/src/api" {
		t.Fatalf("remote start path = %q, want it verbatim", cwd)
	}

	// A remote workspace naming no path leaves the directory to its cathost
	// rather than inheriting this machine's session cwd.
	if _, cwd := identity(t, WorkspaceCreateParams{Host: "devbox"}); cwd != "" {
		t.Fatalf("remote default start path = %q, want empty", cwd)
	}

	// The local host keeps every bit of the old resolution, including the
	// refusal that drives the dialog's "create folder?" escalation.
	if _, cwd := identity(t, WorkspaceCreateParams{Path: ptr("")}); cwd != home {
		t.Fatalf("local empty path = %q, want home %q", cwd, home)
	}
	r := h.resp()
	h.d.Dispatch(CmdWorkspaceCreate, params(t, WorkspaceCreateParams{Path: ptr("/no/such/dir")}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "no such directory") {
		t.Fatalf("local bad path: fail=%v err=%q", r.failCall, r.errMsg)
	}

	r = h.resp()
	h.d.Dispatch(CmdWorkspaceCreate, params(t, WorkspaceCreateParams{Host: "nope"}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "unknown host") {
		t.Fatalf("unknown host: fail=%v err=%q", r.failCall, r.errMsg)
	}
}

// A pane created in a host-pinned workspace inherits that host without anyone
// naming it again — the point of the workspace-level field. The dispatcher's
// cwd rules have to agree: a pane on the workspace's own host inherits its
// neighbor's directory even though neither side named a host.
func TestDispatchWorkspaceHostFlowsToNewPanes(t *testing.T) {
	h := newCmdHarness(t)
	twoHostBackend(h)

	r := h.resp()
	h.d.Dispatch(CmdWorkspaceCreate, params(t, WorkspaceCreateParams{Host: "devbox"}), r)
	wsID := okData[WorkspaceCreateResult](t, r).ID

	root := h.s.ActiveWorkspace().Tabs[0].RootPane
	if got := h.s.PaneHost(root); got != "devbox" {
		t.Fatalf("root pane host = %q, want devbox", got)
	}
	h.b.paneMeta = map[uint32]PaneMeta{uint32(root): {Cwd: "/srv/app", Host: "devbox"}}

	r = h.resp()
	h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{Workspace: wsID}), r)
	got := okData[TabCreateResult](t, r)
	if h.s.PaneHost(layout.PaneID(got.Pane)) != "devbox" {
		t.Fatalf("tab in a pinned workspace = %q, want devbox", h.s.PaneHost(layout.PaneID(got.Pane)))
	}
	if ov := h.b.staged[got.Pane]; ov.Cwd != "/srv/app" {
		t.Fatalf("same-host inherit = %q, want the neighbor's /srv/app", ov.Cwd)
	}
}

// --- ui.notify / ui.action ----------------------------------------------------

// The dispatcher owns the shape checks — title, kind, labels, unique ids — and
// nothing reaches the backend until they pass. The kind default is applied here
// too, so a caller that omits it does not have to know what "info" is.
func TestDispatchUINotifyValidates(t *testing.T) {
	cases := []struct {
		name string
		p    UINotifyParams
		want string // substring of the refusal; "" means it must reach the backend
	}{
		{"no title", UINotifyParams{Body: "b"}, "title is required"},
		{"blank title", UINotifyParams{Title: "   "}, "title is required"},
		{"unknown kind", UINotifyParams{Title: "t", Kind: "urgent"}, "attention, finished, info"},
		{"unlabelled action", UINotifyParams{Title: "t", Actions: []NotifyAction{{ID: "a"}}}, "label is required"},
		{"duplicate ids", UINotifyParams{Title: "t", Actions: []NotifyAction{
			{ID: "a", Label: "A"}, {ID: "a", Label: "B"}}}, "duplicate id"},
		{"minimal", UINotifyParams{Title: "t"}, ""},
		{"with actions", UINotifyParams{Title: "t", Actions: []NotifyAction{{Label: "Yes"}}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newCmdHarness(t)
			r := h.resp()
			h.d.Dispatch(CmdUINotify, params(t, c.p), r)
			if c.want != "" {
				if !r.failCall || !strings.Contains(r.errMsg, c.want) {
					t.Fatalf("want a refusal containing %q; got ok=%v %q", c.want, r.okCall, r.errMsg)
				}
				for _, e := range *h.log {
					if strings.HasPrefix(e, "uiNotify") {
						t.Fatalf("a refused notification still reached the backend: %v", *h.log)
					}
				}
				return
			}
			if !r.okCall {
				t.Fatalf("unexpected refusal: %q", r.errMsg)
			}
			if h.b.lastNotify.Kind != NotifyKindInfo {
				t.Errorf("kind = %q; want the %q default applied by the dispatcher",
					h.b.lastNotify.Kind, NotifyKindInfo)
			}
		})
	}
}

// An action id left empty is filled in from its position before the backend
// sees it, so a caller that only wants buttons never has to invent names — and
// the ids it gets back are the ones ui.action answers by.
func TestDispatchUINotifyFillsActionIDs(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()
	h.d.Dispatch(CmdUINotify, params(t, UINotifyParams{Title: "t", Actions: []NotifyAction{
		{Label: "Yes"}, {ID: "no", Label: "No"}, {Label: "Later"},
	}}), r)
	if !r.okCall {
		t.Fatalf("refused: %q", r.errMsg)
	}
	got := h.b.lastNotify.Actions
	want := []string{"1", "no", "3"}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("actions[%d].ID = %q; want %q", i, got[i].ID, id)
		}
	}
}

// ui.action needs an id and nothing else the dispatcher can check: whether the
// notification is still answerable is the registry's question, and asking it
// here would be a second answer to it.
func TestDispatchUIAction(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()
	h.d.Dispatch(CmdUIAction, params(t, UIActionParams{Action: "yes"}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "id is required") {
		t.Fatalf("missing id: ok=%v %q", r.okCall, r.errMsg)
	}

	h2 := newCmdHarness(t)
	r2 := h2.resp()
	h2.d.Dispatch(CmdUIAction, params(t, UIActionParams{ID: "n1", Action: "yes"}), r2)
	if !r2.okCall {
		t.Fatalf("refused: %q", r2.errMsg)
	}
	if !slices.Contains(*h2.log, "uiAction:n1:yes") {
		t.Fatalf("action did not reach the backend: %v", *h2.log)
	}
}

// --- pane.open_file -----------------------------------------------------------

// editorHarness is a cmdHarness with an editor policy and a pane meta table
// ready to place editors around the session.
func editorHarness(t *testing.T) cmdHarness {
	t.Helper()
	h := newCmdHarness(t)
	h.b.editor = EditorInfo{Agents: []string{"ced"}, Command: []string{"ced"}, Spawn: true}
	h.b.paneMeta = map[uint32]PaneMeta{}
	return h
}

// The request reaches the editor pane, carrying the path VERBATIM and the line.
// A path is not expanded here: it names a file on the editor's machine.
func TestOpenFileReachesTheEditor(t *testing.T) {
	h := editorHarness(t)
	anchor, _ := h.s.FocusedPane()
	ed, err := h.s.SplitPane(nil, layout.Horizontal)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	h.b.paneMeta[uint32(ed)] = PaneMeta{Agent: "ced"}

	r := h.resp()
	h.d.Dispatch(CmdPaneOpenFile, params(t, OpenFileParams{
		Path: "~/projs/go/cats/main.go", Line: 42, Pane: ptrU32(uint32(anchor)),
	}), r)
	if !r.okCall {
		t.Fatalf("refused: %q", r.errMsg)
	}
	if got := r.data.(OpenFileResult); got.Pane != uint32(ed) || got.Spawned {
		t.Fatalf("result = %+v; want the existing editor pane %d", got, ed)
	}
	if h.b.lastOpen.Path != "~/projs/go/cats/main.go" || h.b.lastOpen.Line != 42 {
		t.Errorf("delivered %+v; the path must travel unexpanded", h.b.lastOpen)
	}
	if h.b.lastOpenPane != uint32(ed) {
		t.Errorf("delivered to pane %d, want %d", h.b.lastOpenPane, ed)
	}
}

// Nearest wins: an editor in the anchor's own tab beats one elsewhere in the
// workspace, which beats one in another workspace. That is the order a person
// means by "the editor" — the one beside what they are looking at.
func TestOpenFilePicksTheNearestEditor(t *testing.T) {
	h := editorHarness(t)
	anchor, _ := h.s.FocusedPane()

	// A far editor first, in a second workspace.
	if _, err := h.s.CreateWorkspaceAt(""); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	far, _ := h.s.FocusedPane()
	h.b.paneMeta[uint32(far)] = PaneMeta{Agent: "ced"}
	if err := h.s.FocusPane(anchor); err != nil {
		t.Fatalf("refocus: %v", err)
	}

	// With only the far one, it is chosen — "anywhere" is still a rung.
	r := h.resp()
	h.d.Dispatch(CmdPaneOpenFile, params(t, OpenFileParams{Path: "a.go", Pane: ptrU32(uint32(anchor))}), r)
	if !r.okCall || r.data.(OpenFileResult).Pane != uint32(far) {
		t.Fatalf("far editor not chosen: ok=%v %+v (%q)", r.okCall, r.data, r.errMsg)
	}

	// A nearer one, in the anchor's own tab, takes over.
	near, err := h.s.SplitPane(&anchor, layout.Horizontal)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	h.b.paneMeta[uint32(near)] = PaneMeta{Agent: "ced"}
	r2 := h.resp()
	h.d.Dispatch(CmdPaneOpenFile, params(t, OpenFileParams{Path: "a.go", Pane: ptrU32(uint32(anchor))}), r2)
	if !r2.okCall || r2.data.(OpenFileResult).Pane != uint32(near) {
		t.Fatalf("near editor not preferred: %+v (%q)", r2.data, r2.errMsg)
	}
}

// With no editor anywhere, one is started as a split beside the anchor, with
// the path in its argv — an editor that has not started cannot be subscribed to
// an event, so the path cannot travel as one.
func TestOpenFileSpawnsAnEditor(t *testing.T) {
	h := editorHarness(t)
	anchor, _ := h.s.FocusedPane()

	r := h.resp()
	h.d.Dispatch(CmdPaneOpenFile, params(t, OpenFileParams{Path: "cmd/x.go", Line: 9, Pane: ptrU32(uint32(anchor))}), r)
	if !r.okCall {
		t.Fatalf("refused: %q", r.errMsg)
	}
	res := r.data.(OpenFileResult)
	if !res.Spawned || res.Pane == uint32(anchor) {
		t.Fatalf("result = %+v; want a freshly spawned pane", res)
	}
	ov, ok := h.b.staged[res.Pane]
	if !ok {
		t.Fatalf("nothing staged for the new pane; staged = %+v", h.b.staged)
	}
	if !slices.Equal(ov.Command, []string{"ced", "cmd/x.go"}) {
		t.Errorf("argv = %v; want the editor command plus the path", ov.Command)
	}
	// No event: the new editor is not listening yet, and one sent into that gap
	// would simply be lost.
	for _, e := range *h.log {
		if strings.HasPrefix(e, "openFileIn") {
			t.Errorf("emitted an open_file event at a pane that has not started: %v", *h.log)
		}
	}
}

// Every refusal, and the one that is not a refusal at all.
func TestOpenFileRefusals(t *testing.T) {
	t.Run("no path", func(t *testing.T) {
		h := editorHarness(t)
		r := h.resp()
		h.d.Dispatch(CmdPaneOpenFile, params(t, OpenFileParams{Line: 3}), r)
		if !r.failCall || !strings.Contains(r.errMsg, "path is required") {
			t.Fatalf("ok=%v %q", r.okCall, r.errMsg)
		}
	})

	t.Run("unknown host", func(t *testing.T) {
		h := editorHarness(t)
		r := h.resp()
		h.d.Dispatch(CmdPaneOpenFile, params(t, OpenFileParams{Path: "a.go", Host: "ghost"}), r)
		if !r.failCall || !strings.Contains(r.errMsg, "unknown host") {
			t.Fatalf("ok=%v %q", r.okCall, r.errMsg)
		}
	})

	t.Run("explicit editor on another machine", func(t *testing.T) {
		// A path is only half an identity: the same string on two machines is
		// two files, so an editor over there must not be handed one from here.
		h := editorHarness(t)
		h.b.hosts = []HostInfo{
			{ID: "local", Connected: true, Default: true, Local: true},
			{ID: "devbox", Connected: true},
		}
		anchor, _ := h.s.FocusedPane()
		ed, _ := h.s.SplitPane(nil, layout.Horizontal)
		h.b.paneMeta[uint32(anchor)] = PaneMeta{Host: "local"}
		h.b.paneMeta[uint32(ed)] = PaneMeta{Agent: "ced", Host: "devbox"}

		r := h.resp()
		h.d.Dispatch(CmdPaneOpenFile, params(t, OpenFileParams{
			Path: "a.go", Pane: ptrU32(uint32(anchor)), Editor: ptrU32(uint32(ed)),
		}), r)
		if !r.failCall || !strings.Contains(r.errMsg, "devbox") {
			t.Fatalf("ok=%v %q", r.okCall, r.errMsg)
		}
	})

	t.Run("an editor on another machine is not found either", func(t *testing.T) {
		h := editorHarness(t)
		h.b.editor.Spawn = false
		h.b.hosts = []HostInfo{
			{ID: "local", Connected: true, Default: true, Local: true},
			{ID: "devbox", Connected: true},
		}
		anchor, _ := h.s.FocusedPane()
		ed, _ := h.s.SplitPane(nil, layout.Horizontal)
		h.b.paneMeta[uint32(anchor)] = PaneMeta{Host: "local"}
		h.b.paneMeta[uint32(ed)] = PaneMeta{Agent: "ced", Host: "devbox"}

		r := h.resp()
		h.d.Dispatch(CmdPaneOpenFile, params(t, OpenFileParams{Path: "a.go", Pane: ptrU32(uint32(anchor))}), r)
		if !r.failCall || !strings.Contains(r.errMsg, "no editor pane") {
			t.Fatalf("ok=%v %q", r.okCall, r.errMsg)
		}
	})

	t.Run("spawn disabled per request", func(t *testing.T) {
		// "Reveal it if the editor is open" — a linter walking twenty findings
		// must not open twenty editors.
		h := editorHarness(t)
		no := false
		r := h.resp()
		h.d.Dispatch(CmdPaneOpenFile, params(t, OpenFileParams{Path: "a.go", Spawn: &no}), r)
		if !r.failCall || !strings.Contains(r.errMsg, "spawning one is disabled") {
			t.Fatalf("ok=%v %q", r.okCall, r.errMsg)
		}
		if len(h.b.staged) != 0 {
			t.Errorf("spawned anyway: %+v", h.b.staged)
		}
	})

	t.Run("locked workspace", func(t *testing.T) {
		// Starting an editor is starting a process, which answers to the same
		// lock as tab.create and a split carrying a command.
		h := editorHarness(t)
		ws := h.s.ActiveWorkspace()
		ws.Locked = true
		r := h.resp()
		h.d.Dispatch(CmdPaneOpenFile, params(t, OpenFileParams{Path: "a.go"}), r)
		if !r.failCall || !strings.Contains(r.errMsg, "locked") {
			t.Fatalf("ok=%v %q", r.okCall, r.errMsg)
		}
	})
}

func ptrU32(v uint32) *uint32 { return &v }

// --- file transfer (file.stat / file.get / file.put) -------------------------

// The dispatcher validates shape and passes the rest through: which machine and
// what a relative path resolves against are the backend's answers, so the params
// must arrive there unaltered.
func TestDispatchFileGetPassesParamsThrough(t *testing.T) {
	h := newCmdHarness(t)
	pane := uint32(7)
	r := h.resp()

	h.d.Dispatch(CmdFileGet, params(t, FileGetParams{
		Path: "~/notes.md", Pane: &pane, Host: "devbox", Offset: 4096, Length: 1024,
	}), r)

	got := h.b.lastFileGetP
	if got.Path != "~/notes.md" || got.Host != "devbox" || got.Offset != 4096 || got.Length != 1024 {
		t.Errorf("params reached the backend as %+v", got)
	}
	if got.Pane == nil || *got.Pane != 7 {
		t.Errorf("pane anchor lost: %v", got.Pane)
	}
	// The path is NOT expanded here: "~" is the answering machine's home.
	if got.Path != "~/notes.md" {
		t.Errorf("the dispatcher resolved a path it cannot resolve: %q", got.Path)
	}
}

// A path is the one field with no sensible default, so all three commands refuse
// an empty one by name rather than passing it to a filesystem to complain about.
func TestDispatchFileCommandsRequireAPath(t *testing.T) {
	for _, name := range []string{CmdFileStat, CmdFileGet, CmdFilePut} {
		t.Run(name, func(t *testing.T) {
			h := newCmdHarness(t)
			r := h.resp()
			h.d.Dispatch(name, params(t, map[string]any{"path": ""}), r)
			if !r.failCall || !strings.Contains(r.errMsg, "path is required") {
				t.Errorf("empty path: ok=%v fail=%v msg=%q", r.okCall, r.failCall, r.errMsg)
			}
		})
	}
}

// Negative arithmetic is a caller bug, and it is named here — on the machine
// that made it — rather than travelling to another box to come back as an
// unhelpful read error.
func TestDispatchFileNegativeRanges(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()
	h.d.Dispatch(CmdFileGet, params(t, FileGetParams{Path: "/x", Offset: -1}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "negative") {
		t.Errorf("negative offset: fail=%v msg=%q", r.failCall, r.errMsg)
	}

	h2 := newCmdHarness(t)
	r2 := h2.resp()
	h2.d.Dispatch(CmdFileGet, params(t, FileGetParams{Path: "/x", Length: -8}), r2)
	if !r2.failCall {
		t.Errorf("negative length was accepted")
	}

	h3 := newCmdHarness(t)
	r3 := h3.resp()
	h3.d.Dispatch(CmdFilePut, params(t, FilePutParams{Path: "/x", Offset: -1}), r3)
	if !r3.failCall || !strings.Contains(r3.errMsg, "negative") {
		t.Errorf("negative put offset: fail=%v msg=%q", r3.failCall, r3.errMsg)
	}
}

// file.put is an effect, so unlike its two siblings it still runs for a caller
// that cannot receive the result — a browser drop that sends no id still wanted
// the file written.
func TestDispatchFilePutRunsWithoutAReplyChannel(t *testing.T) {
	h := newCmdHarness(t)
	r := &fakeResponder{log: h.log, wants: false}

	h.d.Dispatch(CmdFilePut, params(t, FilePutParams{Path: "/tmp/x", Data: []byte("hi")}), r)

	if h.b.lastFilePutP.Path != "/tmp/x" {
		t.Errorf("a reply-less put did not reach the backend: %+v", h.b.lastFilePutP)
	}
	if string(h.b.lastFilePutP.Data) != "hi" {
		t.Errorf("data lost: %q", h.b.lastFilePutP.Data)
	}
}

// The chunk flag's default is the safety property: absent More means "this put
// is the whole file", so the naive one-shot caller gets an atomic write with no
// flag at all.
func TestDispatchFilePutDefaultsToComplete(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()
	h.d.Dispatch(CmdFilePut, params(t, map[string]any{"path": "/tmp/x", "data": []byte("hi")}), r)
	if h.b.lastFilePutP.More {
		t.Error("a put with no More flag was treated as a chunk of a longer transfer")
	}
	if h.b.lastFilePutP.Overwrite {
		t.Error("a put with no Overwrite flag was allowed to clobber")
	}
}

// pane.flag and workspace.flag pin the user's own annotation — a glyph with a
// meaning plus an optional note — to a pane or a workspace. Both are durable
// session state that four different lists draw, so both route through
// BroadcastFlags rather than the layout: the AGENTS rollup is a message of its
// own, and it is the one list that reaches a pane in another workspace.
func TestDispatchPaneFlag(t *testing.T) {
	// paneFlag reads a pane's flag back the way a client would — through
	// pane.list, not off the model — so the projection is covered too.
	paneFlag := func(t *testing.T, h cmdHarness, pane uint32) FlagInfo {
		t.Helper()
		for _, p := range okDataFor[PaneListResult](t, h, CmdPaneList).Panes {
			if p.Pane == pane {
				return p.FlagInfo
			}
		}
		t.Fatalf("pane %d not in pane.list", pane)
		return FlagInfo{}
	}

	t.Run("sets, re-reads and clears", func(t *testing.T) {
		h := newCmdHarness(t)
		pane, _ := h.s.FocusedPane()
		id := uint32(pane)
		r := h.resp()

		h.d.Dispatch(CmdPaneFlag, params(t, FlagPaneParams{
			Pane: id, Kind: "followup", Note: "waiting on the API review"}), r)

		if !r.okCall || r.failCall {
			t.Fatalf("pane.flag: ok=%v fail=%v (%q)", r.okCall, r.failCall, r.errMsg)
		}
		if lg := *h.log; len(lg) != 2 || lg[0] != "broadcastFlags" || lg[1] != "ok" {
			t.Fatalf("pane.flag effects = %v, want [broadcastFlags ok]", lg)
		}
		got := paneFlag(t, h, id)
		if got.Flag != "followup" || got.FlagNote != "waiting on the API review" {
			t.Fatalf("pane.list flag = %+v", got)
		}
		// The timestamp is stamped by the dispatcher, so it must be present and
		// plausible — a flag with no "when" cannot answer "is this still true?".
		if got.FlagAtMs <= 0 || got.FlagAtMs > time.Now().UnixMilli()+1000 {
			t.Fatalf("flag_at_ms = %d, want a recent Unix-ms stamp", got.FlagAtMs)
		}

		// An empty kind clears it, the way an empty name clears a custom title.
		*h.log = nil
		r = h.resp()
		h.d.Dispatch(CmdPaneFlag, params(t, FlagPaneParams{Pane: id}), r)
		if !r.okCall {
			t.Fatalf("clear: fail=%v (%q)", r.failCall, r.errMsg)
		}
		if got := paneFlag(t, h, id); got != (FlagInfo{}) {
			t.Fatalf("after clear, flag = %+v", got)
		}
		// Clearing an already-unflagged pane is a no-op and skips the broadcast.
		*h.log = nil
		r = h.resp()
		h.d.Dispatch(CmdPaneFlag, params(t, FlagPaneParams{Pane: id}), r)
		if lg := *h.log; len(lg) != 1 || lg[0] != "ok" {
			t.Fatalf("no-op clear effects = %v, want [ok]", lg)
		}
	})

	t.Run("a custom glyph is stored verbatim", func(t *testing.T) {
		h := newCmdHarness(t)
		pane, _ := h.s.FocusedPane()
		r := h.resp()

		h.d.Dispatch(CmdPaneFlag, params(t, FlagPaneParams{Pane: uint32(pane), Kind: "🍕"}), r)

		if !r.okCall {
			t.Fatalf("custom glyph: fail=%v (%q)", r.failCall, r.errMsg)
		}
		if got := paneFlag(t, h, uint32(pane)).Flag; got != "🍕" {
			t.Fatalf("custom glyph stored as %q", got)
		}
	})

	t.Run("an unknown kind is refused", func(t *testing.T) {
		h := newCmdHarness(t)
		pane, _ := h.s.FocusedPane()
		r := h.resp()

		h.d.Dispatch(CmdPaneFlag, params(t, FlagPaneParams{Pane: uint32(pane), Kind: "folloup"}), r)

		// Refused before any effect: a typo must not leave the sidebar drawing
		// the word "folloup" where a mark should be.
		if !r.failCall || !strings.Contains(r.errMsg, "unknown flag kind") {
			t.Fatalf("bad kind: fail=%v msg=%q", r.failCall, r.errMsg)
		}
		if lg := *h.log; len(lg) != 1 || lg[0] != "fail" {
			t.Fatalf("bad kind effects = %v, want [fail]", lg)
		}
	})

	t.Run("an unknown pane fails", func(t *testing.T) {
		h := newCmdHarness(t)
		r := h.resp()

		h.d.Dispatch(CmdPaneFlag, params(t, FlagPaneParams{Pane: 9999, Kind: "star"}), r)

		if !r.failCall || !strings.Contains(r.errMsg, "unknown pane") {
			t.Fatalf("unknown pane: fail=%v msg=%q", r.failCall, r.errMsg)
		}
	})
}

func TestDispatchWorkspaceFlag(t *testing.T) {
	// wsFlag reads the active workspace's flag back through workspace.list.
	wsFlag := func(t *testing.T, h cmdHarness) FlagInfo {
		t.Helper()
		for _, ws := range okDataFor[WorkspaceListResult](t, h, CmdWorkspaceList).Workspaces {
			if ws.Active {
				return ws.FlagInfo
			}
		}
		t.Fatal("no active workspace in workspace.list")
		return FlagInfo{}
	}

	t.Run("sets and clears", func(t *testing.T) {
		h := newCmdHarness(t)
		id := h.s.ActiveWorkspace().ID
		r := h.resp()

		h.d.Dispatch(CmdWorkspaceFlag, params(t, FlagWorkspaceParams{
			ID: id, Kind: "warn", Note: "flaky tests here"}), r)

		if !r.okCall || r.failCall {
			t.Fatalf("workspace.flag: ok=%v fail=%v (%q)", r.okCall, r.failCall, r.errMsg)
		}
		if lg := *h.log; len(lg) != 2 || lg[0] != "broadcastFlags" || lg[1] != "ok" {
			t.Fatalf("workspace.flag effects = %v, want [broadcastFlags ok]", lg)
		}
		if got := wsFlag(t, h); got.Flag != "warn" || got.FlagNote != "flaky tests here" {
			t.Fatalf("workspace.list flag = %+v", got)
		}

		r = h.resp()
		h.d.Dispatch(CmdWorkspaceFlag, params(t, FlagWorkspaceParams{ID: id}), r)
		if !r.okCall {
			t.Fatalf("clear: fail=%v (%q)", r.failCall, r.errMsg)
		}
		if got := wsFlag(t, h); got != (FlagInfo{}) {
			t.Fatalf("after clear, flag = %+v", got)
		}
	})

	t.Run("no id flags the active workspace", func(t *testing.T) {
		h := newCmdHarness(t)
		r := h.resp()

		h.d.Dispatch(CmdWorkspaceFlag, params(t, FlagWorkspaceParams{Kind: "star"}), r)

		if !r.okCall || wsFlag(t, h).Flag != "star" {
			t.Fatalf("bare flag: ok=%v flag=%+v (%q)", r.okCall, wsFlag(t, h), r.errMsg)
		}
	})

	t.Run("unknown workspace fails", func(t *testing.T) {
		h := newCmdHarness(t)
		r := h.resp()

		h.d.Dispatch(CmdWorkspaceFlag, params(t, FlagWorkspaceParams{ID: "w404", Kind: "star"}), r)

		if !r.failCall || !strings.Contains(r.errMsg, "unknown workspace") {
			t.Fatalf("unknown workspace: fail=%v msg=%q", r.failCall, r.errMsg)
		}
	})

	t.Run("a note is normalized, and a note with no kind is dropped", func(t *testing.T) {
		h := newCmdHarness(t)
		r := h.resp()

		h.d.Dispatch(CmdWorkspaceFlag, params(t, FlagWorkspaceParams{
			Kind: "note", Note: "  two\r\nlines  "}), r)
		if got := wsFlag(t, h).FlagNote; got != "two lines" {
			t.Fatalf("note = %q, want %q", got, "two lines")
		}

		// A note with no kind has nothing to hang from: every surface draws the
		// glyph, so an unmarked note would be a write nobody can see.
		r = h.resp()
		h.d.Dispatch(CmdWorkspaceFlag, params(t, FlagWorkspaceParams{Note: "orphan"}), r)
		if !r.okCall {
			t.Fatalf("clear-with-note: fail=%v (%q)", r.failCall, r.errMsg)
		}
		if got := wsFlag(t, h); got != (FlagInfo{}) {
			t.Fatalf("note with no kind stored: %+v", got)
		}
	})
}
