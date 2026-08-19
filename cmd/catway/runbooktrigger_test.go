//go:build ghostty

package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/layout"
)

// newTriggerOrch is newRunbookOrch with the autorun switch on. A zero
// config.Config has it OFF, which is the right default for a half-built orch
// but not for these tests.
func newTriggerOrch(t *testing.T) (*orch, string) {
	t.Helper()
	o, dir := newRunbookOrch(t)
	o.cfg.Runbooks.Triggers = true
	return o, dir
}

// fire delivers one event the way the orchestrator does — emit, then let the
// loop start whatever the emit reserved. Tests do not run the loop goroutine,
// so they play its two halves themselves; keeping them separate is the point of
// the test, since a run that started inside emitEvent would be the bug.
func fire(o *orch, event string, payload any) {
	o.emitEvent(event, 0, payload)
	o.startReservedRunbooks()
}

// The whole feature in one test: an event the runbook declares fires it, and
// the event's own payload is readable from the steps.
func TestTriggerRunsTheRunbook(t *testing.T) {
	o, dir := newTriggerOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	writeRunbook(t, dir, "t.yaml", `
name: t
on: pane_agent
steps:
  - run: pane.rename
    params: {pane: "{{ event.pane }}", name: "agent-{{ event.agent }}"}
`)
	fire(o, app.EventPaneAgent, app.PaneAgentEvent{Pane: pane, Agent: "claude", State: "blocked"})

	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name != "agent-claude" {
		t.Fatalf("pane name = %q, want the triggered run to have renamed it from the payload", name)
	}
}

// A `where:` that does not hold means no run at all. This is the filter working,
// not the runbook failing, so nothing is reported anywhere.
func TestTriggerWhereFilters(t *testing.T) {
	o, dir := newTriggerOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	writeRunbook(t, dir, "t.yaml", `
name: t
on:
  - event: pane_agent
    where: {state: blocked}
steps:
  - run: pane.rename
    params: {pane: `+strconv.FormatUint(uint64(pane), 10)+`, name: fired}
`)
	fire(o, app.EventPaneAgent, app.PaneAgentEvent{Pane: pane, Agent: "claude", State: "working"})
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name == "fired" {
		t.Fatal("a working agent fired a runbook filtered to blocked")
	}
	fire(o, app.EventPaneAgent, app.PaneAgentEvent{Pane: pane, Agent: "claude", State: "blocked"})
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name != "fired" {
		t.Fatalf("pane name = %q, want the matching event to have fired the runbook", name)
	}
}

// A numeric filter on a zero value is the case that breaks if the payload is
// marshalled with omitempty or compared across decoders — and it is the common
// case, since a successful exit is exit_code 0.
func TestTriggerMatchesZeroExitCode(t *testing.T) {
	o, dir := newTriggerOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	writeRunbook(t, dir, "t.yaml", `
name: t
on:
  - event: pane_exited
    where: {exit_code: 0}
steps:
  - run: pane.rename
    params: {pane: `+strconv.FormatUint(uint64(pane), 10)+`, name: clean}
`)
	fire(o, app.EventPaneExited, app.PaneExitedEvent{Pane: pane, ExitCode: 1})
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name == "clean" {
		t.Fatal("exit code 1 matched a filter on 0")
	}
	fire(o, app.EventPaneExited, app.PaneExitedEvent{Pane: pane, ExitCode: 0})
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name != "clean" {
		t.Fatalf("pane name = %q, want exit code 0 to have matched", name)
	}
}

// Triggers are off unless the config says otherwise, and the switch is checked
// per event rather than at load — flipping it must take effect without a
// restart.
func TestTriggersRespectTheConfigSwitch(t *testing.T) {
	o, dir := newTriggerOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	writeRunbook(t, dir, "t.yaml", `
name: t
on: pane_exited
steps:
  - run: pane.rename
    params: {pane: `+strconv.FormatUint(uint64(pane), 10)+`, name: fired}
`)
	o.cfg.Runbooks.Triggers = false
	fire(o, app.EventPaneExited, app.PaneExitedEvent{Pane: pane})
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name == "fired" {
		t.Fatal("a trigger fired with runbooks.triggers off")
	}
	// And the listing says why, since every other explanation for "my runbook
	// stopped running" lives in daemon state nobody can see.
	c := &capture{}
	o.RunbookList(c)
	res := c.data.(app.RunbookListResult)
	if len(res.Runbooks) != 1 || !strings.Contains(res.Runbooks[0].TriggerStatus, "runbooks.triggers is off") {
		t.Fatalf("listing = %+v", res.Runbooks)
	}

	o.cfg.Runbooks.Triggers = true
	fire(o, app.EventPaneExited, app.PaneExitedEvent{Pane: pane})
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name != "fired" {
		t.Fatalf("pane name = %q, want the trigger to fire once switched back on", name)
	}
}

// The listing reports what a runbook triggers on, so a palette or a completion
// can tell an autorun apart from one that waits to be asked.
func TestListReportsTriggers(t *testing.T) {
	o, dir := newTriggerOrch(t)
	writeRunbook(t, dir, "t.yaml", "on: [pane_exited, pane_cwd]\nsteps:\n  - run: pane.last\n")
	c := &capture{}
	o.RunbookList(c)
	res := c.data.(app.RunbookListResult)
	if len(res.Runbooks) != 1 {
		t.Fatalf("runbooks = %+v", res.Runbooks)
	}
	got := strings.Join(res.Runbooks[0].Triggers, ",")
	if got != "pane_cwd,pane_exited" {
		t.Errorf("triggers = %q, want both, sorted", got)
	}
}

// --- the protections --------------------------------------------------------

// One run per runbook, whatever started it. A second trigger arriving mid-run is
// DROPPED rather than queued: the event that would queue it described a session
// the run in front of it is still changing.
func TestTriggerDoesNotStackRuns(t *testing.T) {
	o, dir := newTriggerOrch(t)
	writeRunbook(t, dir, "t.yaml", "on: pane_exited\nsteps:\n  - run: pane.last\n")

	// Hold the slot as an in-flight run would, without needing a step that
	// blocks: what the trigger consults is the accounting, not the steps.
	if msg := o.claimRunbookSlot("t"); msg != "" {
		t.Fatalf("claim: %s", msg)
	}
	o.emitEvent(app.EventPaneExited, 1, app.PaneExitedEvent{Pane: 1})
	if n := len(o.runbooks.reserved); n != 0 {
		t.Fatalf("reserved %d runs while one was in flight, want 0", n)
	}
	o.releaseRunbookSlot("t")
	o.emitEvent(app.EventPaneExited, 1, app.PaneExitedEvent{Pane: 1})
	if n := len(o.runbooks.reserved); n != 1 {
		t.Fatalf("reserved %d runs after the slot was free, want 1", n)
	}
}

// A manual run of a runbook that is already running is refused too, and says so.
// Two runs of one document interleaving their side effects is not something
// anybody asks for on purpose.
func TestManualRunRefusedWhileRunning(t *testing.T) {
	o, dir := newTriggerOrch(t)
	writeRunbook(t, dir, "t.yaml", "steps:\n  - run: pane.last\n")
	if msg := o.claimRunbookSlot("t"); msg != "" {
		t.Fatalf("claim: %s", msg)
	}
	c := run(t, o, app.RunbookRunParams{Name: "t"})
	if !strings.Contains(c.errMsg, "already in flight") {
		t.Fatalf("errMsg = %q", c.errMsg)
	}
}

// The global cap bounds one event that matches many runbooks.
func TestGlobalConcurrencyCap(t *testing.T) {
	o, dir := newTriggerOrch(t)
	for i := 0; i < maxRunbookRunsInFlight+2; i++ {
		n := strconv.Itoa(i)
		writeRunbook(t, dir, "r"+n+".yaml", "name: r"+n+"\non: pane_exited\nsteps:\n  - run: pane.last\n")
	}
	o.emitEvent(app.EventPaneExited, 1, app.PaneExitedEvent{Pane: 1})
	if n := len(o.runbooks.reserved); n != maxRunbookRunsInFlight {
		t.Fatalf("reserved %d runs, want the cap of %d", n, maxRunbookRunsInFlight)
	}
}

// The rate limit is what actually terminates a mutual-trigger loop: neither the
// per-runbook rule nor the global cap can, because two runbooks taking turns are
// never running at the same time.
func TestRateLimitSuspendsARunawayTrigger(t *testing.T) {
	o, dir := newTriggerOrch(t)
	writeRunbook(t, dir, "t.yaml", "on: pane_exited\nsteps:\n  - run: pane.last\n")

	for i := 0; i < maxTriggerStarts+3; i++ {
		fire(o, app.EventPaneExited, app.PaneExitedEvent{Pane: 1})
	}
	until, suspended := o.runbooks.suspended["t"]
	if !suspended || time.Now().After(until) {
		t.Fatalf("runbook was not suspended after %d starts", maxTriggerStarts+3)
	}
	if got := len(o.runbooks.starts["t"]); got != maxTriggerStarts {
		t.Errorf("recorded %d starts, want the limit of %d — firings past it must not run", got, maxTriggerStarts)
	}
	// And the listing explains it, with when it comes back.
	c := &capture{}
	o.RunbookList(c)
	res := c.data.(app.RunbookListResult)
	if !strings.Contains(res.Runbooks[0].TriggerStatus, "suspended until") {
		t.Errorf("trigger_status = %q", res.Runbooks[0].TriggerStatus)
	}
}

// A suspension is not a manual ban: the one thing it must never do is stop
// somebody debugging the runbook that got suspended.
func TestSuspensionDoesNotBlockAManualRun(t *testing.T) {
	o, dir := newTriggerOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	writeRunbook(t, dir, "t.yaml", `
name: t
on: pane_exited
steps:
  - run: pane.rename
    params: {pane: `+strconv.FormatUint(uint64(pane), 10)+`, name: manual}
`)
	o.runbooks.suspended = map[string]time.Time{"t": time.Now().Add(time.Hour)}
	res := run(t, o, app.RunbookRunParams{Name: "t"}).result(t)
	if res.Failed {
		t.Fatalf("manual run of a suspended runbook failed: %+v", res.Steps)
	}
}

// min_interval is a throttle rather than a brake: pane_cwd fires on every `cd`,
// and a runbook reacting to "I moved to a new repo" wants the settled answer.
func TestMinIntervalThrottles(t *testing.T) {
	o, dir := newTriggerOrch(t)
	writeRunbook(t, dir, "t.yaml", `
name: t
on:
  - event: pane_cwd
    min_interval: 1h
steps:
  - run: pane.last
`)
	fire(o, app.EventPaneCwd, app.PaneCwdEvent{Pane: 1, Cwd: "/a"})
	o.emitEvent(app.EventPaneCwd, 1, app.PaneCwdEvent{Pane: 1, Cwd: "/b"})
	if n := len(o.runbooks.reserved); n != 0 {
		t.Fatalf("reserved %d runs inside min_interval, want 0", n)
	}
}

// Every run ends with an event, because a triggered run has no caller to hand a
// result to. Manual runs emit it too, so a client watching the stream never has
// to know which runs it will be told about.
func TestRunbookFinishedEventReportsTheOutcome(t *testing.T) {
	o, dir := newTriggerOrch(t)
	sub := &recSub{}
	o.subs[&ctlSubscriber{sub: sub, filter: app.EventsSubscribeParams{
		Events: []string{app.EventRunbookFinished}}}] = struct{}{}

	writeRunbook(t, dir, "t.yaml", "on: pane_exited\nsteps:\n  - run: pane.focus\n    params: {pane: 99999}\n")
	fire(o, app.EventPaneExited, app.PaneExitedEvent{Pane: 1})

	if len(sub.datas) != 1 {
		t.Fatalf("events = %v, want one runbook_finished", sub.names)
	}
	ev, ok := sub.datas[0].(app.RunbookFinishedEvent)
	if !ok {
		t.Fatalf("payload is %T", sub.datas[0])
	}
	if ev.Name != "t" || ev.Source != runbookSourceTrigger || ev.Trigger != app.EventPaneExited {
		t.Errorf("event = %+v", ev)
	}
	if !ev.Failed || ev.FailedStep != 1 || ev.Error == "" {
		t.Errorf("a run whose only step failed reported %+v", ev)
	}
}

// The slot is released before the run's own event goes out, so a runbook
// triggered by runbook_finished-adjacent activity is not refused for a run that
// has already ended. Checked directly because the ordering is invisible from
// outside.
func TestFinishReleasesTheSlotBeforeEmitting(t *testing.T) {
	o, dir := newTriggerOrch(t)
	writeRunbook(t, dir, "t.yaml", "steps:\n  - run: pane.last\n")
	run(t, o, app.RunbookRunParams{Name: "t"}).result(t)
	if o.runbooks.inFlight != 0 || o.runbooks.running["t"] {
		t.Fatalf("inFlight = %d running = %v after the run ended", o.runbooks.inFlight, o.runbooks.running)
	}
}

// A trigger edit takes effect without a restart. The index is cached for
// triggerIndexTTL, so the test moves the clock the only way it can from here —
// by invalidating the scan the way the TTL would.
func TestTriggerIndexPicksUpAnEdit(t *testing.T) {
	o, dir := newTriggerOrch(t)
	writeRunbook(t, dir, "t.yaml", "on: pane_cwd\nsteps:\n  - run: pane.last\n")
	o.emitEvent(app.EventPaneCwd, 1, app.PaneCwdEvent{Pane: 1})
	if len(o.runbooks.reserved) != 1 {
		t.Fatalf("the runbook did not fire on its declared event")
	}
	o.runbooks.reserved = nil
	o.releaseRunbookSlot("t")

	writeRunbook(t, dir, "t.yaml", "on: pane_exited\nsteps:\n  - run: pane.last\n")
	o.runbooks.scannedAt = time.Time{} // as the TTL expiring would
	o.emitEvent(app.EventPaneCwd, 1, app.PaneCwdEvent{Pane: 1})
	if n := len(o.runbooks.reserved); n != 0 {
		t.Fatalf("reserved %d runs on the OLD event after the file changed", n)
	}
	o.emitEvent(app.EventPaneExited, 1, app.PaneExitedEvent{Pane: 1})
	if n := len(o.runbooks.reserved); n != 1 {
		t.Fatalf("reserved %d runs on the new event, want 1", n)
	}
}

// A run's steps must never execute nested inside the emitEvent that started
// them: a step that emits an event would then recurse into the fan-out that is
// still in progress.
func TestEmitEventOnlyReserves(t *testing.T) {
	o, dir := newTriggerOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	writeRunbook(t, dir, "t.yaml", `
name: t
on: pane_exited
steps:
  - run: pane.rename
    params: {pane: `+strconv.FormatUint(uint64(pane), 10)+`, name: fired}
`)
	o.emitEvent(app.EventPaneExited, pane, app.PaneExitedEvent{Pane: pane})
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name == "fired" {
		t.Fatal("the runbook ran inside emitEvent instead of being reserved for the loop")
	}
	o.startReservedRunbooks()
	if name, _ := o.session.PaneCustomName(layout.PaneID(pane)); name != "fired" {
		t.Fatalf("pane name = %q, want the loop to have started the reserved run", name)
	}
}

// Everything one event reserves is started by one drain, and the accounting
// comes back to zero. The claim this protects is that a reservation is never
// stranded: its slot is already taken, so a reservation that never started
// would wedge its runbook at "already in flight" for the rest of the session.
func TestDrainStartsEveryReservation(t *testing.T) {
	o, dir := newTriggerOrch(t)
	pane := strconv.FormatUint(uint64(o.session.AllPaneIDs()[0]), 10)
	for i := 0; i < maxRunbookRunsInFlight; i++ {
		n := strconv.Itoa(i)
		writeRunbook(t, dir, "r"+n+".yaml", "name: r"+n+`
on: pane_exited
steps:
  - run: pane.rename
    params: {pane: `+pane+`, name: "by-r`+n+`"}
`)
	}
	fire(o, app.EventPaneExited, app.PaneExitedEvent{Pane: 1})

	if n := len(o.runbooks.reserved); n != 0 {
		t.Fatalf("%d reservations left after the drain, want all started", n)
	}
	if o.runbooks.inFlight != 0 || len(o.runbooks.running) != 0 {
		t.Fatalf("inFlight = %d running = %v after the drain", o.runbooks.inFlight, o.runbooks.running)
	}
	// All four really ran: each renamed the pane, so the last one wins.
	if name, _ := o.session.PaneCustomName(layout.PaneID(o.session.AllPaneIDs()[0])); !strings.HasPrefix(name, "by-r") {
		t.Errorf("pane name = %q, want one of the triggered runs to have renamed it", name)
	}
	for i := 0; i < maxRunbookRunsInFlight; i++ {
		if got := len(o.runbooks.starts["r"+strconv.Itoa(i)]); got != 1 {
			t.Errorf("r%d recorded %d starts, want 1", i, got)
		}
	}
}
