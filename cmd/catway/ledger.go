//go:build ghostty

package main

import (
	"log"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/ledger"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// The command ledger's catway half: subscribe to each host's shell-integration
// marks, and turn the pairs they report into durable records.
//
// The division of labour is the multi-host rule again. The daemon owns
// everything only that machine can answer — where the marks are in the byte
// stream, what the working directory was at that instant, how long the command
// actually took on that machine's clock. catway owns everything only the
// SESSION can answer: which host a pane is on, what its public handle is, and
// whether a human or an agent was driving it.
//
// That last field is the one a shell history cannot have. `~/.zsh_history`
// records what was typed into a shell that writes one; an agent runs commands
// through a PTY it drives itself, so its work is absent from every shell history
// on the machine. Here it is one field, resolved at the moment the command
// starts from the pane's live agent state.

// openCmd is a command that has started and not yet ended. Held in memory,
// keyed by pane: a pane runs one foreground command at a time, which is what
// makes the pairing a map rather than a queue.
//
// An open command whose end never arrives — catway restarts, the host is
// detached, the pane closes mid-run — is simply dropped. Writing a half record
// would put a row in the history whose duration and status are inventions, and
// the ledger's whole value is that its fields are true.
type openCmd struct {
	entry ledger.Entry
}

// syncCommandMarks tells every capable host whether to report shell marks. Sent
// on every roster change and whenever the ledger is opened or closed, so a host
// that reconnects is re-told rather than assumed (the daemon re-sends on its own
// welcome for the same reason).
func (o *orch) syncCommandMarks() {
	on := o.ledger != nil
	for _, id := range o.hostOrder {
		if d := o.hosts[id]; d != nil {
			d.setCommandMarks(on)
		}
	}
}

// noteCommandStart records the beginning of a command. Loop goroutine.
//
// Origin and handle are captured HERE rather than at the end, because both can
// change while the command runs: an agent can take a pane over mid-build, and a
// pane can be renumbered by a workspace closing. What the history should say is
// what was true when the command was issued.
func (o *orch) noteCommandStart(hostID string, ev orchestration.CommandStart) {
	if o.ledger == nil {
		return
	}
	if o.openCmds == nil {
		o.openCmds = map[uint32]*openCmd{}
	}
	now := time.Now()
	handle, _ := o.session.PublicPaneID(layout.PaneID(ev.PaneID))
	o.openCmds[ev.PaneID] = &openCmd{
		entry: ledger.Entry{
			At:     now,
			Host:   hostID,
			Pane:   ev.PaneID,
			Handle: handle,
			Cmd:    ev.Cmd,
			Cwd:    ev.Cwd,
			Origin: o.commandOrigin(ev.PaneID),
		},
	}
}

// noteCommandEnd completes the pane's open command and writes it. Loop goroutine.
//
// The daemon's duration wins over the difference between catway's two clock
// readings: it was measured on the machine that ran the command, so it does not
// include a network hop whose size varies with the wifi. catway's own timing is
// the fallback for a daemon too old to report one.
func (o *orch) noteCommandEnd(hostID string, ev orchestration.CommandEnd) {
	open := o.openCmds[ev.PaneID]
	if open == nil {
		return // an end with no start of ours — a mark stream that began mid-command
	}
	delete(o.openCmds, ev.PaneID)
	if o.ledger == nil {
		return
	}
	e := open.entry
	e.Exit = ev.Exit
	// The daemon's figure, always — it was measured on the machine that ran the
	// command, so it excludes a network hop whose size varies with the wifi. A
	// zero is a real answer (a sub-millisecond command) rather than a missing
	// one, so there is nothing here to fall back to; substituting catway's own
	// clock would replace a true 0 with a made-up 1.
	e.DurationMs = ev.DurationMs
	if err := o.ledger.Add(e); err != nil {
		// One line, not per command: a ledger that cannot write stays unable to,
		// and a busy session would otherwise fill the log with the same line.
		o.logLedgerOnce(err.Error())
	}
}

// dropOpenCommands forgets a host's in-flight commands — on disconnect, and on
// detach. Their ends are never coming, and a record completed later against a
// duration measured across an outage would be a fiction.
func (o *orch) dropOpenCommands(hostID string) {
	for pane, open := range o.openCmds {
		if open.entry.Host == hostID {
			delete(o.openCmds, pane)
		}
	}
}

// commandOrigin answers "who ran this": the agent holding the pane, or a human.
//
// It reads the ARBITRATED agent state — the same one the sidebar draws — so a
// hook-reported agent and a detected one give the same answer, and a pane whose
// agent has just exited is a human again immediately.
func (o *orch) commandOrigin(pane uint32) string {
	rt := o.panes[pane]
	if rt == nil {
		return ledgerOriginHuman
	}
	if agent, _ := rt.effectiveAgent(); agent != "" {
		return agent
	}
	return ledgerOriginHuman
}

// ledgerOriginHuman is the origin recorded for a pane with no agent. Spelled out
// rather than left empty so a listing never has to explain a blank column, and
// so "origin: human" is a filter a caller can actually write.
const ledgerOriginHuman = "human"

// logLedgerOnce reports a ledger write failure exactly once per reason,
// following the push bridge's discipline.
func (o *orch) logLedgerOnce(reason string) {
	if o.ledgerLogged == nil {
		o.ledgerLogged = map[string]bool{}
	}
	if o.ledgerLogged[reason] {
		return
	}
	o.ledgerLogged[reason] = true
	log.Printf("catway: ledger write failed (%s)", reason)
}

// LedgerList implements app.Backend: answer a history query.
func (o *orch) LedgerList(r app.Responder, p app.LedgerListParams) {
	if o.ledger == nil {
		r.Fail("the command ledger is disabled (see ledger.enabled in the config)")
		return
	}
	// A history filtered on a host that does not exist would answer "nothing ran
	// there", which is true and useless — and indistinguishable from a typo.
	if p.Host != "" && o.hosts[p.Host] == nil {
		r.Fail(unknownHostErrForLedger(p.Host))
		return
	}
	entries := o.ledger.List(ledger.Query{
		Host:     p.Host,
		Pane:     p.Pane,
		Cwd:      p.Cwd,
		Contains: p.Contains,
		Failed:   p.Failed,
		Limit:    p.Limit,
	})
	out := make([]app.LedgerEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, app.LedgerEntry{
			At:         e.At.UTC().Format(time.RFC3339Nano),
			Host:       e.Host,
			Pane:       e.Pane,
			Handle:     e.Handle,
			Cmd:        e.Cmd,
			Cwd:        e.Cwd,
			Exit:       e.Exit,
			DurationMs: e.DurationMs,
			Origin:     e.Origin,
		})
	}
	r.OK(app.LedgerListResult{Entries: out})
}

// unknownHostErrForLedger phrases the refusal the way every other host-scoped
// command does. A history filtered on a host that does not exist would answer
// "nothing ran there", which is true and useless.
func unknownHostErrForLedger(id string) string {
	return "unknown host " + `"` + id + `"` + " (see host.list)"
}

// setCommandMarks turns this host's shell-mark subscription on or off. Like the
// stats interval it is remembered whether or not it can be sent right now,
// because a host that is down still has a subscription state and the reconnect
// is what applies it.
func (d *daemon) setCommandMarks(on bool) {
	d.mu.Lock()
	changed := d.cmdMarks != on
	d.cmdMarks = on
	d.mu.Unlock()
	if changed {
		d.sendCommandMarksRequest()
	}
}

// sendCommandMarksRequest tells the connected host whether to scan. A no-op on a
// host that cannot answer — an older cathost would take the unknown message type
// as an error and toast the user about a request they never made.
func (d *daemon) sendCommandMarksRequest() {
	if !d.supports(orchestration.FeatureCommandLedger) {
		return
	}
	d.mu.Lock()
	on := d.cmdMarks
	d.mu.Unlock()
	d.send(orchestration.NewRequestCommandMarks(on))
}
