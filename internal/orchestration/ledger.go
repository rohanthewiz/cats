//go:build ghostty

package orchestration

import (
	"sync/atomic"
	"time"
)

// The command ledger's daemon half: watch each pane's output for shell
// integration marks (osc133.go) and report the commands they describe.
//
// This is a subscription, and off by default, for a reason worth stating
// plainly: scanning is a fifth state machine over every byte of every pane, and
// for a shell with no integration installed it produces nothing at all. A
// session that keeps no ledger should not pay for it, and neither should the
// panes of a client that never asked.
//
// The subscription belongs to the CONNECTION, like the host-stats one — a
// persistent cathost that kept scanning for an orchestrator that has gone would
// be doing bookkeeping for nobody.

// ledgerFields is the Host's command-ledger state, embedded into Host.
type ledgerFields struct {
	// cmdMarks is the live switch. Read from every pane's readPump goroutine and
	// written from the dispatch goroutine, so it is atomic rather than mutexed:
	// it is checked once per read, and a flip that lands one chunk late is
	// indistinguishable from one that landed one chunk early.
	cmdMarks atomic.Bool
}

// requestCommandMarks applies a MsgRequestCommandMarks.
//
// Turning it OFF does not reset the per-pane trackers, deliberately. A tracker
// holds at most one open command, and a client that switches the ledger off
// mid-command has no use for its end anyway; the next prompt clears it. Resetting
// them would mean reaching into every pane's readPump-owned state from this
// goroutine, which is the one thing the scanner ownership rule forbids.
func (h *Host) requestCommandMarks(c RequestCommandMarks) { h.cmdMarks.Store(c.On) }

// commandMarksOn reports whether this connection asked for shell marks.
func (h *Host) commandMarksOn() bool { return h.cmdMarks.Load() }

// stopCommandMarks drops the subscription when its connection ends.
func (h *Host) stopCommandMarks() { h.cmdMarks.Store(false) }

// scanCommandMarks feeds one chunk of a pane's output to its shell-integration
// scanner and emits whatever commands complete. Called from that pane's readPump
// goroutine, which owns both the scanner and the tracker.
//
// The cwd is captured HERE, at the moment the command starts, rather than left
// for the client to join against its own cwd stream: by the time a ledger row is
// written the pane may have cd'd twice, and "where did I run this" is one of the
// two questions a ledger is asked.
func (h *Host) scanCommandMarks(p *pane, chunk []byte) {
	for _, m := range p.osc133.scan(chunk) {
		rec, ok := p.cmds.mark(m, time.Now())
		if !ok {
			continue
		}
		if rec.Start {
			h.emit(NewCommandStart(p.id, rec.Cmd, p.cwdMeta()))
			continue
		}
		h.emit(NewCommandEnd(p.id, rec.Exit, rec.Duration))
	}
}
