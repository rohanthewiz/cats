//go:build ghostty

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/ledger"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// newLedgerOrch builds an orch with a real ledger on a throwaway path.
func newLedgerOrch(t *testing.T) *orch {
	t.Helper()
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"), 0)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	o.ledger = l
	return o
}

// A start/end pair becomes one record carrying everything only the SESSION
// knows (host, pane, public handle, who was driving) alongside everything only
// the DAEMON knows (the command, the cwd at that instant, the duration measured
// where it ran).
func TestLedgerRecordsACommand(t *testing.T) {
	o := newLedgerOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	o.noteCommandStart("local", orchestration.CommandStart{PaneID: pane, Cmd: "go test ./...", Cwd: "/p/cats"})
	exit := 2
	o.noteCommandEnd("local", orchestration.CommandEnd{PaneID: pane, Exit: &exit, DurationMs: 1500})

	got := o.ledger.List(ledger.Query{})
	if len(got) != 1 {
		t.Fatalf("stored %d records, want 1", len(got))
	}
	e := got[0]
	if e.Cmd != "go test ./..." || e.Cwd != "/p/cats" || e.Host != "local" || e.Pane != pane {
		t.Errorf("entry = %+v", e)
	}
	if e.Exit == nil || *e.Exit != 2 || e.DurationMs != 1500 {
		t.Errorf("exit/duration = %v/%d", e.Exit, e.DurationMs)
	}
	if e.Handle == "" {
		t.Error("no public handle recorded; a closed pane must still be nameable")
	}
	if e.Origin != ledgerOriginHuman {
		t.Errorf("origin = %q, want %q", e.Origin, ledgerOriginHuman)
	}
}

// Origin is the field no shell history can have: an agent drives a PTY itself,
// so its commands are absent from every ~/.zsh_history on the machine.
func TestLedgerRecordsTheAgentAsOrigin(t *testing.T) {
	o := newLedgerOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pane, Agent: "claude", State: "working"})

	o.noteCommandStart("local", orchestration.CommandStart{PaneID: pane, Cmd: "rg TODO"})
	exit := 0
	o.noteCommandEnd("local", orchestration.CommandEnd{PaneID: pane, Exit: &exit, DurationMs: 40})

	got := o.ledger.List(ledger.Query{})
	if len(got) != 1 || got[0].Origin != "claude" {
		t.Fatalf("origin = %+v", got)
	}
}

// Origin and handle are captured at the START, because both can change while
// the command runs — an agent can take a pane over mid-build. What the history
// should say is what was true when the command was issued.
func TestLedgerCapturesOriginAtTheStart(t *testing.T) {
	o := newLedgerOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	o.noteCommandStart("local", orchestration.CommandStart{PaneID: pane, Cmd: "make"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pane, Agent: "claude", State: "working"})
	exit := 0
	o.noteCommandEnd("local", orchestration.CommandEnd{PaneID: pane, Exit: &exit, DurationMs: 10})

	got := o.ledger.List(ledger.Query{})
	if len(got) != 1 || got[0].Origin != ledgerOriginHuman {
		t.Fatalf("origin = %+v; an agent arriving mid-command must not claim it", got)
	}
}

// An end with no start of ours is ignored — a mark stream that began
// mid-command, which is what a subscription turned on while something is
// running produces.
func TestLedgerIgnoresAnUnpairedEnd(t *testing.T) {
	o := newLedgerOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	exit := 0
	o.noteCommandEnd("local", orchestration.CommandEnd{PaneID: pane, Exit: &exit, DurationMs: 10})
	if got := o.ledger.List(ledger.Query{}); len(got) != 0 {
		t.Fatalf("recorded %+v from an end with no start", got)
	}
}

// A host that goes away takes its in-flight commands with it. Completing one
// later against a duration measured across an outage would be a fiction, and
// the ledger's whole value is that its fields are true.
func TestLedgerDropsInFlightCommandsWithTheirHost(t *testing.T) {
	o := newLedgerOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	o.noteCommandStart("devbox", orchestration.CommandStart{PaneID: pane, Cmd: "long build"})
	o.noteCommandStart("local", orchestration.CommandStart{PaneID: pane + 1, Cmd: "here"})
	o.dropOpenCommands("devbox")

	exit := 0
	o.noteCommandEnd("devbox", orchestration.CommandEnd{PaneID: pane, Exit: &exit, DurationMs: 10})
	o.noteCommandEnd("local", orchestration.CommandEnd{PaneID: pane + 1, Exit: &exit, DurationMs: 10})

	got := o.ledger.List(ledger.Query{})
	if len(got) != 1 || got[0].Cmd != "here" {
		t.Fatalf("entries = %+v; only the surviving host's command should land", got)
	}
}

// The subscription is only ever sent to a host that advertised it, and only
// while a ledger is open — a cathost that cannot answer would take the unknown
// message type as an error and toast the user about a request they never made.
func TestCommandMarksSubscriptionFollowsTheLedger(t *testing.T) {
	o := newLedgerOrch(t)
	pd := newPipeDaemon(t, o)
	d := o.hosts[o.defaultHost]

	d.setFeatures(nil)
	o.syncCommandMarks()
	select {
	case m := <-pd.msgs:
		t.Fatalf("subscribed a host that never advertised the feature: %v", m.mt)
	case <-time.After(50 * time.Millisecond):
	}

	d.setFeatures([]string{orchestration.FeatureCommandLedger})
	d.setCommandMarks(false) // clear the state syncCommandMarks just set
	o.syncCommandMarks()
	if payload := pd.expect(t, orchestration.MsgRequestCommandMarks); len(payload) == 0 {
		t.Fatal("empty subscription payload")
	}

	// Closing the ledger turns it off again.
	o.ledger = nil
	o.syncCommandMarks()
	pd.expect(t, orchestration.MsgRequestCommandMarks)
}

// ledger.list refuses a host that is not in the roster. "Nothing ran there" is
// true for a typo and useless.
func TestLedgerListRefusesAnUnknownHost(t *testing.T) {
	o := newLedgerOrch(t)
	var r notifyResponder
	o.LedgerList(&r, app.LedgerListParams{Host: "ghost"})
	if !r.fail {
		t.Fatalf("unknown host accepted: %+v", r.data)
	}
}

// With no ledger the command says so by name, rather than answering "no history"
// — which would be indistinguishable from a session that has run nothing.
func TestLedgerListSaysWhenItIsDisabled(t *testing.T) {
	o := newLedgerOrch(t)
	o.ledger = nil
	var r notifyResponder
	o.LedgerList(&r, app.LedgerListParams{})
	if !r.fail || r.err == "" {
		t.Fatalf("disabled ledger answered: fail=%v %q", r.fail, r.err)
	}
}

// --- blocks -------------------------------------------------------------------

// ledger.output is a round trip to the pane's own cathost: the marks that bound
// a block are the terminal's, and only it can say where they are now.
func TestLedgerOutputRoundTrip(t *testing.T) {
	o := newLedgerOrch(t)
	pd := newPipeDaemon(t, o)
	o.hosts[o.defaultHost].setFeatures([]string{orchestration.FeatureCommandLedger})
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.LedgerOutput(&r, app.LedgerBlockParams{Pane: pane, Block: 5})
	if r.ok || r.fail {
		t.Fatal("answered before the daemon did")
	}

	payload := pd.expect(t, orchestration.MsgRequestBlock)
	var req orchestration.RequestBlock
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req.PaneID != pane || req.Block != 5 || !req.Text {
		t.Fatalf("request = %+v; ledger.output must ask for the text", req)
	}

	o.resolvePending(blockKey(o.defaultHost, req.ID),
		orchestration.NewBlockResult(req.ID, true, 10, 14, 100, "hello"))
	if !r.ok {
		t.Fatalf("not answered: fail=%v %q", r.fail, r.err)
	}
	got := r.data.(app.LedgerOutputResult)
	if !got.Found || got.Text != "hello" || got.StartRow != 10 || got.EndRow != 14 {
		t.Errorf("result = %+v", got)
	}
}

// A block whose rows have been discarded answers Found false rather than the
// text that now occupies those rows — the whole reason blocks are marks.
func TestLedgerOutputReportsAGoneBlock(t *testing.T) {
	o := newLedgerOrch(t)
	pd := newPipeDaemon(t, o)
	o.hosts[o.defaultHost].setFeatures([]string{orchestration.FeatureCommandLedger})
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.LedgerOutput(&r, app.LedgerBlockParams{Pane: pane, Block: 5})
	payload := pd.expect(t, orchestration.MsgRequestBlock)
	var req orchestration.RequestBlock
	_ = json.Unmarshal(payload, &req)
	o.resolvePending(blockKey(o.defaultHost, req.ID),
		orchestration.NewBlockResult(req.ID, false, 0, 0, 0, ""))

	if !r.ok {
		t.Fatalf("a gone block should answer, not fail: %q", r.err)
	}
	if got := r.data.(app.LedgerOutputResult); got.Found || got.Text != "" {
		t.Errorf("result = %+v", got)
	}
}

// ledger.jump asks for no text (it wants rows) and scrolls the pane up by the
// distance from the buffer's bottom to the block's first row.
func TestLedgerJumpScrollsToTheBlock(t *testing.T) {
	o := newLedgerOrch(t)
	pd := newPipeDaemon(t, o)
	o.hosts[o.defaultHost].setFeatures([]string{orchestration.FeatureCommandLedger})
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.LedgerJump(&r, app.LedgerBlockParams{Pane: pane, Block: 9})
	payload := pd.expect(t, orchestration.MsgRequestBlock)
	var req orchestration.RequestBlock
	_ = json.Unmarshal(payload, &req)
	if req.Text {
		t.Error("jump asked for the text it will not use")
	}

	// The block starts 60 rows above the viewport's current top, so the jump
	// scrolls up by 60 — putting the block's FIRST row at the top, not its last.
	o.resolvePending(blockKey(o.defaultHost, req.ID),
		orchestration.NewBlockResult(req.ID, true, 40, 55, 100, ""))
	if !r.ok {
		t.Fatalf("jump failed: %q", r.err)
	}
	scroll := pd.expect(t, orchestration.MsgScrollViewport)
	var sv orchestration.ScrollViewport
	if err := json.Unmarshal(scroll, &sv); err != nil {
		t.Fatalf("decode scroll: %v", err)
	}
	if sv.PaneID != pane || sv.Delta != -60 {
		t.Errorf("scroll = pane %d delta %d; want pane %d delta -60 (start 40 - top 100)",
			sv.PaneID, sv.Delta, pane)
	}
}

// A block BELOW the viewport scrolls back down. The delta is signed for exactly
// this: a history row for a command run since you scrolled up has to be
// reachable too.
func TestLedgerJumpScrollsDownToALaterBlock(t *testing.T) {
	o := newLedgerOrch(t)
	pd := newPipeDaemon(t, o)
	o.hosts[o.defaultHost].setFeatures([]string{orchestration.FeatureCommandLedger})
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.LedgerJump(&r, app.LedgerBlockParams{Pane: pane, Block: 9})
	payload := pd.expect(t, orchestration.MsgRequestBlock)
	var req orchestration.RequestBlock
	_ = json.Unmarshal(payload, &req)
	o.resolvePending(blockKey(o.defaultHost, req.ID),
		orchestration.NewBlockResult(req.ID, true, 120, 130, 100, ""))
	if !r.ok {
		t.Fatalf("jump failed: %q", r.err)
	}
	scroll := pd.expect(t, orchestration.MsgScrollViewport)
	var sv orchestration.ScrollViewport
	_ = json.Unmarshal(scroll, &sv)
	if sv.Delta != 20 {
		t.Errorf("delta = %d, want +20 (start 120 - top 100)", sv.Delta)
	}
}

// A block already at the viewport's top needs no scroll at all.
func TestLedgerJumpDoesNotScrollWhenAlreadyVisible(t *testing.T) {
	o := newLedgerOrch(t)
	pd := newPipeDaemon(t, o)
	o.hosts[o.defaultHost].setFeatures([]string{orchestration.FeatureCommandLedger})
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.LedgerJump(&r, app.LedgerBlockParams{Pane: pane, Block: 9})
	payload := pd.expect(t, orchestration.MsgRequestBlock)
	var req orchestration.RequestBlock
	_ = json.Unmarshal(payload, &req)
	o.resolvePending(blockKey(o.defaultHost, req.ID),
		orchestration.NewBlockResult(req.ID, true, 100, 110, 100, ""))
	if !r.ok {
		t.Fatalf("jump failed: %q", r.err)
	}
	select {
	case m := <-pd.msgs:
		if m.mt == orchestration.MsgScrollViewport {
			t.Fatal("scrolled for a block already at the viewport's top")
		}
	case <-time.After(50 * time.Millisecond):
	}
}

// A jump to a block that is gone refuses by name — unlike output, which answers
// with found:false. The difference is what the caller can do: a listing wants to
// keep walking, a jump has nowhere to go.
func TestLedgerJumpRefusesAGoneBlock(t *testing.T) {
	o := newLedgerOrch(t)
	pd := newPipeDaemon(t, o)
	o.hosts[o.defaultHost].setFeatures([]string{orchestration.FeatureCommandLedger})
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.LedgerJump(&r, app.LedgerBlockParams{Pane: pane, Block: 9})
	payload := pd.expect(t, orchestration.MsgRequestBlock)
	var req orchestration.RequestBlock
	_ = json.Unmarshal(payload, &req)
	o.resolvePending(blockKey(o.defaultHost, req.ID),
		orchestration.NewBlockResult(req.ID, false, 0, 0, 0, ""))
	if !r.fail || !strings.Contains(r.err, "scrolled out") {
		t.Fatalf("refusal = %v %q", r.fail, r.err)
	}
}

// A host too old to hold blocks is refused by name rather than answered from
// nothing — the same rule path.list and the worktree commands follow.
func TestLedgerBlockRefusesAnIncapableHost(t *testing.T) {
	o := newLedgerOrch(t)
	newPipeDaemon(t, o)
	o.hosts[o.defaultHost].setFeatures(nil)
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.LedgerOutput(&r, app.LedgerBlockParams{Pane: pane, Block: 1})
	if !r.fail {
		t.Fatalf("an incapable host answered: %+v", r.data)
	}
}

// The block id recorded is the one the END carried: a start that could be
// pinned and an end that could not is not an addressable block, and offering it
// would promise a lookup that always answers "gone".
func TestLedgerRecordsTheBlockFromTheEnd(t *testing.T) {
	o := newLedgerOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	exit := 0

	o.noteCommandStart("local", orchestration.CommandStart{PaneID: pane, Cmd: "pinned", Block: 7})
	o.noteCommandEnd("local", orchestration.CommandEnd{PaneID: pane, Exit: &exit, Block: 7})
	o.noteCommandStart("local", orchestration.CommandStart{PaneID: pane, Cmd: "half", Block: 8})
	o.noteCommandEnd("local", orchestration.CommandEnd{PaneID: pane, Exit: &exit, Block: 0})

	got := o.ledger.List(ledger.Query{})
	if len(got) != 2 {
		t.Fatalf("stored %d records", len(got))
	}
	if got[0].Block != 0 || got[0].Cmd != "half" {
		t.Errorf("half-pinned command kept block %d", got[0].Block)
	}
	if got[1].Block != 7 {
		t.Errorf("pinned command block = %d, want 7", got[1].Block)
	}
}
