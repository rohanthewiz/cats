//go:build ghostty

package main

import (
	"path/filepath"
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
