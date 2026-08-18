//go:build ghostty

package main

import (
	"log"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
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
			Block:  ev.Block,
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
	// A block is only addressable once BOTH its marks exist, so the id the end
	// carries is the authoritative one: a start that could be pinned and an end
	// that could not is not a block, and offering it would promise a lookup that
	// always answers "gone".
	e.Block = ev.Block
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
		return
	}
	o.broadcastHistory()
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
		out = append(out, wireEntry(e))
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

// --- blocks: a recorded command's output, still in its pane ------------------
//
// A ledger row says what ran. A BLOCK is where its output still is — held by
// that pane's cathost as two marks the terminal moves as its scrollback shifts
// (internal/terminal.Mark). Everything here is therefore a round trip: the rows
// are only meaningful at the instant the daemon reads them, which is exactly
// why they are not stored alongside the command.

// LedgerOutput implements app.Backend: read a recorded command's output.
func (o *orch) LedgerOutput(r app.Responder, p app.LedgerBlockParams) {
	o.requestBlock(r, p, true, func(r app.Responder, res orchestration.BlockResult) {
		r.OK(app.LedgerOutputResult{
			Found: res.Found, Text: res.Text,
			StartRow: res.StartRow, EndRow: res.EndRow,
		})
	})
}

// LedgerJump implements app.Backend: put a recorded command's output on screen.
//
// The pane is revealed first and scrolled second, and the order matters: a
// scroll applies to a pane, and revealing one that lives in another workspace
// or tab is what makes "jump to that build" work from a history list rather
// than only from the pane you are already looking at.
func (o *orch) LedgerJump(r app.Responder, p app.LedgerBlockParams) {
	o.requestBlock(r, p, false, func(r app.Responder, res orchestration.BlockResult) {
		if !res.Found {
			r.Fail("that command's output has scrolled out of the pane's buffer")
			return
		}
		if err := o.session.RevealPane(layout.PaneID(p.Pane)); err != nil {
			r.Fail(err.Error())
			return
		}
		o.applyModel()
		// One subtraction, and it is signed on purpose: a block above the
		// viewport scrolls up, one below it scrolls down, and a block already at
		// the top needs nothing. Putting the block's FIRST row at the top is the
		// point — landing it at the bottom would scroll to a command whose
		// output is all off-screen, which is what the first live run did.
		if delta := int32(res.StartRow) - int32(res.TopRow); delta != 0 {
			o.hostForPane(p.Pane).send(orchestration.NewScrollViewport(p.Pane, delta))
		}
		r.OK(nil)
	})
}

// requestBlock is the one round trip both verbs make. done runs on the loop
// when the daemon answers; a timeout or a disconnect fails the caller through
// the same pending machinery read and capture use.
func (o *orch) requestBlock(r app.Responder, p app.LedgerBlockParams, wantText bool,
	done func(app.Responder, orchestration.BlockResult)) {
	hostID := o.paneHostID(p.Pane)
	d := o.hostByID(hostID)
	if !d.supports(orchestration.FeatureCommandLedger) {
		r.Fail(o.hostCapabilityErr(hostID, "address a command's output", orchestration.FeatureCommandLedger))
		return
	}
	o.nextBlockReq++
	o.registerPending(blockResponder{r: r, done: done}, blockKey(hostID, o.nextBlockReq))
	d.send(orchestration.NewRequestBlock(o.nextBlockReq, p.Pane, p.Block, wantText))
}

// blockResponder turns the daemon's BlockResult into the command's answer on
// the way back, the way worktreeResponder does. The pending queue carries one
// Responder and knows nothing about block results, so the shaping lives here.
type blockResponder struct {
	r    app.Responder
	done func(app.Responder, orchestration.BlockResult)
}

func (b blockResponder) WantsReply() bool { return b.r.WantsReply() }
func (b blockResponder) Fail(msg string)  { b.r.Fail(msg) }

func (b blockResponder) OK(data any) {
	res, ok := data.(orchestration.BlockResult)
	if !ok {
		b.r.Fail("malformed block result")
		return
	}
	b.done(b.r, res)
}

// historyMsg renders the browser's HISTORY section: the most recent commands,
// newest first. ok is false when there is nothing to show — a session whose
// shells have no OSC 133 integration installed — so the client never receives a
// message that would draw an empty section.
func (o *orch) historyMsg() (browserproto.History, bool) {
	if o.ledger == nil {
		return browserproto.History{}, false
	}
	entries := o.ledger.List(ledger.Query{Limit: historyPushLimit})
	if len(entries) == 0 {
		return browserproto.History{}, false
	}
	out := make([]app.LedgerEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, wireEntry(e))
	}
	return browserproto.NewHistory(out), true
}

// historyPushLimit is how many entries the sidebar carries. A screenful, and
// short enough that pushing the whole list on every recorded command is cheaper
// than a delta protocol the client could fall out of step with.
const historyPushLimit = 40

// broadcastHistory re-pushes the section. Called when a command is recorded,
// which is human-paced — one record per prompt line, not per line of output.
func (o *orch) broadcastHistory() {
	if m, ok := o.historyMsg(); ok {
		o.broadcast(m)
	}
}

// wireEntry is the one place a stored entry becomes a wire entry, shared by the
// browser push and the ledger.list reply so the two can never disagree about a
// field.
func wireEntry(e ledger.Entry) app.LedgerEntry {
	return app.LedgerEntry{
		At:         e.At.UTC().Format(time.RFC3339Nano),
		Host:       e.Host,
		Pane:       e.Pane,
		Handle:     e.Handle,
		Cmd:        e.Cmd,
		Cwd:        e.Cwd,
		Exit:       e.Exit,
		DurationMs: e.DurationMs,
		Origin:     e.Origin,
		Block:      e.Block,
	}
}
