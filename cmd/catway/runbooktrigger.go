//go:build ghostty

package main

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/runbook"
)

// This file is the autorun half of runbooks: an `on:` clause turning a session
// event into a run.
//
// Phase 6a deliberately shipped the format and the manual run first, because
// this half is the one with the failure modes. A runbook triggered by
// pane_exited can spawn panes that exit; two runbooks can trigger each other; a
// flapping host link can fire a trigger every second for an hour. None of that
// is hypothetical enough to leave to good behaviour, so there are four
// protections and they are all in this file:
//
//  1. ONE RUN PER RUNBOOK. A trigger that fires while that runbook is already
//     running is dropped, never queued. A queue would be a backlog of stale side
//     effects: the event that queued a run described a session that has since
//     been changed by the run in front of it.
//  2. A GLOBAL CAP on runs in flight, manual ones included. It bounds a single
//     event that matches many runbooks.
//  3. A RATE LIMIT per runbook, and a suspension when it trips. This is what
//     actually terminates a mutual-trigger loop, since neither of the first two
//     can: A and B taking turns are never running at the same time.
//  4. RESERVE-THEN-START. Every decision above is made at the moment the event
//     fires, and the run itself starts on a later turn of the orchestrator loop
//     (see startReservedRunbooks). Without that a runbook's steps would execute
//     nested inside the emitEvent that started them, and a step that emits an
//     event would recurse.
//
// What there is NOT is a way to disable a runaway permanently, or a persisted
// record of one. A suspension expires; a restart clears everything. The state
// this file holds is all in memory and all rebuildable, because the durable
// artefact is the YAML file the user can simply edit.

const (
	// maxRunbookRunsInFlight bounds concurrent runs across the whole session,
	// counting manual ones. Small on purpose: a runbook is a sequence of visible
	// side effects on somebody's desktop, and four of them interleaving is
	// already more than anyone can follow.
	maxRunbookRunsInFlight = 4

	// triggerRateWindow / maxTriggerStarts are the rate limit: a runbook may be
	// STARTED BY A TRIGGER this many times in this window. Manual runs are not
	// counted — a human typing the command is not a runaway, and the one thing a
	// suspension must never do is stop somebody debugging the runbook that got
	// suspended.
	triggerRateWindow = time.Minute
	maxTriggerStarts  = 10

	// triggerSuspension is how long a runbook's triggers stay off after the rate
	// limit trips. It expires rather than latching: the usual cause is a burst
	// (twelve panes exiting when a workspace closes), and a runbook that stays
	// dead until someone notices a log line is worse than one that resumes.
	triggerSuspension = 5 * time.Minute

	// triggerIndexTTL is how stale the trigger index may be. Unlike runbook.list
	// and runbook.run — which re-scan every call, so "edit, run" cannot execute
	// the previous version — the index is consulted on every emitted event, and
	// pane_title alone fires several times a second while a build runs.
	//
	// One second is the trade: a re-scan of a few small YAML files, at most once
	// a second, against a trigger edit taking up to a second to take effect. A
	// trigger is not typed and immediately awaited the way a manual run is, so
	// the staleness that would be a bug there is unnoticeable here.
	triggerIndexTTL = time.Second
)

// runbookTriggers is the orchestrator's autorun state. Loop-goroutine only,
// like everything else the orchestrator holds.
type runbookTriggers struct {
	// --- the index, rebuilt from disk at most once per triggerIndexTTL ---
	scannedAt time.Time
	// print is a fingerprint of the runbook directory (names, sizes, mtimes). A
	// scan whose fingerprint is unchanged skips the parse entirely, so the
	// steady-state cost of the whole subsystem is one readdir per second.
	print string
	// books are only the runbooks that declare triggers. events is the union of
	// the names they trigger on — the map that makes an event no runbook cares
	// about cost one lookup.
	books  []*runbook.Runbook
	events map[string]bool

	// --- per-runbook accounting, keyed by runbook name ---
	// running holds the runbooks with a run in flight, whatever started it, and
	// what is known about that run. A struct rather than a bool because this map
	// is the ONLY record of what the session is running: the run objects
	// themselves are private to the executor's chain, and every client that
	// wants to draw "deploy is running right now" is asking this map. Keeping
	// the origin beside the fact avoids a second map that could disagree with
	// it about which runs exist.
	running map[string]runbookRunInfo
	// lastFired is when each CLAUSE last fired, keyed by name and clause index,
	// for min_interval. Per clause rather than per runbook because two clauses
	// on one runbook are two different questions, and a `pane_cwd` throttle must
	// not also throttle the `pane_exited` that shares the file.
	lastFired map[string]time.Time
	// starts are the trigger-started runs inside the rate window, oldest first.
	starts map[string][]time.Time
	// suspended is when each suspended runbook's triggers come back.
	suspended map[string]time.Time

	// dirty is "some run's step cursor moved during this loop turn". See
	// flushRunbookRuns for why progress is coalesced and the edges are not.
	dirty bool

	// inFlight counts runs against maxRunbookRunsInFlight.
	inFlight int
	// reserved are runs a fired trigger has already accounted for and that the
	// loop has not started yet. Their slots are held from the moment the trigger
	// fired, so the accounting cannot be raced by the drain.
	reserved []reservedRun
}

// runbookRunInfo is what the session knows about one run in flight from the
// outside: who started it and when. Not the run itself — that is runbookRun in
// runbook.go, which the executor owns and nothing else may see.
type runbookRunInfo struct {
	source  string // runbookSourceControl | runbookSourceTrigger
	trigger string // the event name, "" for a manual run
	started time.Time
	// step is the 1-based index of the step being executed, 0 before the run
	// reaches its first one; total is how many the document has. Written by
	// noteRunbookStep as the executor advances, and read only by the message
	// builder — the executor keeps its own cursor (runbookRun.i) and this is a
	// copy of it for the outside world, not a second source of truth. Nothing
	// about the run's behaviour reads these.
	step  int
	total int
}

// reservedRun is one trigger firing that has passed every check and is waiting
// for the loop to start it.
type reservedRun struct {
	book    *runbook.Runbook
	event   string
	payload map[string]any
}

// fireRunbookTriggers is emitEvent's tap: it decides, for one event, which
// runbooks should run because of it, and reserves them.
//
// Nothing is started here. See the file comment's point 4.
func (o *orch) fireRunbookTriggers(event string, data any) {
	if !o.cfg.Runbooks.Triggers {
		return
	}
	// The index is only refreshed when there is a chance it matters. Refreshing
	// unconditionally would be correct too, but every event in the session would
	// then pay for a directory scan, and the overwhelming majority of them are
	// pane_title and pane_cwd in a session with no triggered runbooks at all.
	idx := o.runbookIndex()
	if len(idx.books) == 0 || !idx.events[event] {
		return
	}

	// The payload is converted once for all candidates. runbook.EventMap fills
	// in the fields `omitempty` would have dropped, so a filter on `exit_code:
	// 0` — the ordinary, successful case — matches instead of being the one
	// value that never does.
	payload := runbook.EventMap(data)
	now := time.Now()

	for _, rb := range idx.books {
		i, t := rb.MatchTriggers(event, payload)
		if t == nil {
			continue
		}
		if reason := o.triggerBlocked(rb, i, t, now); reason != "" {
			// Logged rather than silent: "my runbook did not run" has half a
			// dozen causes and only this line distinguishes them.
			log.Printf("catway: runbook %s not triggered by %s: %s", rb.Name, event, reason)
			continue
		}
		o.reserveRunbook(rb, i, event, payload, now)
	}
}

// triggerBlocked reports why this firing must not start a run, or "" to let it
// through. Taking the slot is reserveRunbook's job, so the decision and the
// accounting can be read separately; the one thing this does write is clearing a
// suspension that has expired, which is lazy rather than timer-driven because
// nothing needs to happen at the moment it lapses.
func (o *orch) triggerBlocked(rb *runbook.Runbook, clause int, t *runbook.Trigger, now time.Time) string {
	rt := &o.runbooks
	if until, ok := rt.suspended[rb.Name]; ok {
		if now.Before(until) {
			return "its triggers are suspended for another " + until.Sub(now).Round(time.Second).String()
		}
		delete(rt.suspended, rb.Name)
		delete(rt.starts, rb.Name) // a served suspension starts the window over
	}
	if _, ok := rt.running[rb.Name]; ok {
		return "a run of it is already in flight"
	}
	if t.MinInterval > 0 {
		if last, ok := rt.lastFired[clauseKey(rb.Name, clause)]; ok && now.Sub(last) < t.MinInterval {
			return "min_interval " + t.MinInterval.String() + " has not elapsed"
		}
	}
	if rt.inFlight >= maxRunbookRunsInFlight {
		return "the session already has " + strconv.Itoa(rt.inFlight) + " runbook runs in flight"
	}
	// Reached only if a suspension was cleared without its window being cleared
	// too — reserveRunbook suspends on the start that reaches the limit, so the
	// ordinary path out of the rate limit is the suspension check above.
	if len(trimStarts(rt.starts[rb.Name], now)) >= maxTriggerStarts {
		return "it has already started " + strconv.Itoa(maxTriggerStarts) + " times in the last " +
			triggerRateWindow.String()
	}
	return ""
}

// reserveRunbook takes the slot and queues the run. Everything the decision in
// triggerBlocked depends on is updated here, at the moment of the decision,
// rather than when the run actually starts — otherwise two triggers firing in
// one loop turn would both see zero runs in flight.
func (o *orch) reserveRunbook(rb *runbook.Runbook, clause int, event string, payload map[string]any, now time.Time) {
	rt := &o.runbooks
	o.initRunbookAccounting()
	// A triggered run is marked from the moment its slot is taken, which is a
	// loop turn before its first step. That is deliberate: the session has
	// already committed to running it and refused every other start of it, so a
	// window told anything else would be told something that is not true.
	rt.running[rb.Name] = runbookRunInfo{source: runbookSourceTrigger, trigger: event, started: now,
		total: len(rb.Steps)}
	rt.inFlight++
	rt.lastFired[clauseKey(rb.Name, clause)] = now
	rt.starts[rb.Name] = append(trimStarts(rt.starts[rb.Name], now), now)
	if len(rt.starts[rb.Name]) >= maxTriggerStarts {
		// Suspend on REACHING the limit rather than on the firing after it, so
		// the log line names the run that tripped it and the next event does not
		// have to arrive for the brake to be on.
		rt.suspended[rb.Name] = now.Add(triggerSuspension)
		log.Printf("catway: runbook %s hit %d trigger starts in %s; its triggers are suspended until %s",
			rb.Name, maxTriggerStarts, triggerRateWindow, now.Add(triggerSuspension).Format(time.TimeOnly))
	}
	rt.reserved = append(rt.reserved, reservedRun{book: rb, event: event, payload: payload})
	o.broadcastRunbookRuns()
}

// startReservedRunbooks starts everything a trigger reserved during the loop
// turn that just ended. Called from the loop, never from a step.
//
// It drains in rounds because a run's first steps execute synchronously here,
// and a step that emits an event can reserve another run. The round bound keeps
// one burst from monopolising a loop turn — a mutual-trigger loop really does
// take a run per round until the rate limit stops it, and that was measured at
// twenty rounds — rather than bounding the work, which the rate limit does.
//
// Leftovers are therefore never dropped: their slots are already reserved, so
// nothing else can take them, and a dropped one would leave its runbook wedged
// at "a run is already in flight" for good. They are carried to the next turn,
// and a turn is GUARANTEED because the tail posts one — otherwise a burst that
// hit the cap in a session that then went completely silent would leave the last
// runs reserved and unstarted with nothing to wake them.
func (o *orch) startReservedRunbooks() {
	const maxRounds = 8
	for round := 0; round < maxRounds && len(o.runbooks.reserved) > 0; round++ {
		batch := o.runbooks.reserved
		o.runbooks.reserved = nil
		for _, res := range batch {
			log.Printf("catway: runbook %s triggered by %s", res.book.Name, res.event)
			o.beginRunbook(res.book, nil, res.payload, runbookSourceTrigger, res.event, nil)
			o.flushClients()
		}
	}
	if len(o.runbooks.reserved) == 0 {
		return
	}
	// From a goroutine, because this runs ON the loop: post blocks when the
	// mailbox is full, and the loop is the only thing that drains it. The
	// closure is empty — the loop calls this function itself after every one —
	// so what is being posted is the turn, not the work.
	go o.post(func() {})
}

// clauseKey addresses one trigger clause of one runbook.
func clauseKey(name string, clause int) string { return name + "\x00" + strconv.Itoa(clause) }

// trimStarts drops the start times that have fallen out of the rate window.
func trimStarts(ts []time.Time, now time.Time) []time.Time {
	cut := now.Add(-triggerRateWindow)
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	if i == 0 {
		return ts
	}
	return ts[i:]
}

// --- the index -------------------------------------------------------------------

// runbookIndex returns the current set of triggered runbooks, re-reading the
// directory at most once per triggerIndexTTL and re-parsing only when the
// directory's fingerprint changed.
func (o *orch) runbookIndex() *runbookTriggers {
	rt := &o.runbooks
	now := time.Now()
	if !rt.scannedAt.IsZero() && now.Sub(rt.scannedAt) < triggerIndexTTL {
		return rt
	}
	rt.scannedAt = now

	dir := runbook.UserDir()
	print := dirPrint(dir)
	if print == rt.print && rt.events != nil {
		return rt // nothing on disk moved; the parsed index still stands
	}
	rt.print = print

	rt.books = nil
	rt.events = map[string]bool{}
	if dir == "" {
		return rt
	}
	for _, rb := range runbook.Load(dir).Books {
		evs := rb.TriggerEvents()
		if len(evs) == 0 {
			continue
		}
		rt.books = append(rt.books, rb)
		for _, e := range evs {
			rt.events[e] = true
		}
	}
	// A file that would not parse is NOT reported here. runbook.list is where a
	// broken runbook is surfaced, with its error; repeating it once a second in
	// the log for as long as the file exists would bury everything else.
	return rt
}

// dirPrint fingerprints the runbook directory cheaply: one readdir, and each
// entry's name, size and modification time. It changes when a file is added,
// removed, renamed, or written, which is every way a runbook's triggers can
// change.
//
// Deliberately not a content hash. The point is to skip the READ and the parse;
// hashing the contents would have to do the read anyway.
func dirPrint(dir string) string {
	if dir == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "" // a missing directory is the same fingerprint as an empty one
	}
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch filepath.Ext(e.Name()) {
		case ".yaml", ".yml":
		default:
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		parts = append(parts, e.Name()+":"+strconv.FormatInt(info.Size(), 10)+":"+
			strconv.FormatInt(info.ModTime().UnixNano(), 10))
	}
	sort.Strings(parts) // ReadDir is sorted, but the fingerprint must not depend on that
	out := ""
	for _, p := range parts {
		out += p + "\n"
	}
	return out
}

// --- slot accounting -------------------------------------------------------------

// initRunbookAccounting allocates the per-runbook maps on first use. Lazily,
// because a session that never runs a runbook should carry none of them, and in
// one place because the two callers that take a slot — the manual path and the
// trigger path — are the two halves of one accounting and a map allocated in
// only one of them would nil-panic in the other.
func (o *orch) initRunbookAccounting() {
	rt := &o.runbooks
	if rt.running != nil {
		return
	}
	rt.running = map[string]runbookRunInfo{}
	rt.lastFired = map[string]time.Time{}
	rt.starts = map[string][]time.Time{}
	rt.suspended = map[string]time.Time{}
}

// claimRunbookSlot reserves the concurrency slot for a MANUAL run, reporting why
// it could not. The trigger path reserves its own slot in reserveRunbook, so
// this is the other half of the same accounting rather than a second policy.
func (o *orch) claimRunbookSlot(name string, total int) string {
	rt := &o.runbooks
	o.initRunbookAccounting()
	if _, ok := rt.running[name]; ok {
		// Refused rather than queued, and refused for a manual caller too: two
		// runs of one runbook interleaving their side effects is not something
		// anybody asks for on purpose, and the caller waiting on a queued run
		// would be waiting on a run whose steps were decided before the one in
		// front of it changed the session.
		return "a run of runbook " + name + " is already in flight"
	}
	if rt.inFlight >= maxRunbookRunsInFlight {
		return "the session already has " + strconv.Itoa(rt.inFlight) +
			" runbook runs in flight, which is the limit"
	}
	rt.running[name] = runbookRunInfo{source: runbookSourceControl, started: time.Now(), total: total}
	rt.inFlight++
	o.broadcastRunbookRuns()
	return ""
}

// releaseRunbookSlot gives back what claimRunbookSlot or reserveRunbook took.
// Called exactly once per started run, from finishRunbook.
func (o *orch) releaseRunbookSlot(name string) {
	rt := &o.runbooks
	if _, held := rt.running[name]; !held {
		// Unreachable: every start claims and every finish releases once. Guarded
		// because a double release would drift inFlight negative and silently
		// raise the concurrency cap.
		log.Printf("catway: runbook %s released a slot it did not hold", name)
		return
	}
	delete(rt.running, name)
	if rt.inFlight > 0 {
		rt.inFlight--
	}
	o.broadcastRunbookRuns()
}

// runbookTriggerStatus describes a runbook's autorun state for runbook.list:
// "" when it is ready to fire, otherwise why it would not.
func (o *orch) runbookTriggerStatus(rb *runbook.Runbook) string {
	if len(rb.Triggers) == 0 {
		return ""
	}
	if !o.cfg.Runbooks.Triggers {
		return "runbooks.triggers is off in the config, so no trigger fires"
	}
	rt := &o.runbooks
	if until, ok := rt.suspended[rb.Name]; ok && time.Now().Before(until) {
		return "suspended until " + until.Format(time.TimeOnly) +
			" after starting " + strconv.Itoa(maxTriggerStarts) + " times in " + triggerRateWindow.String()
	}
	if _, ok := rt.running[rb.Name]; ok {
		return "a run is in flight"
	}
	return ""
}

// --- what the browser is told --------------------------------------------------

// runbookRunsMsg is the set of runs in flight as the browser sees it. An empty
// set is an ordinary message rather than no message at all, for the reason
// recordMsg's idle case is one: the client has marks to turn OFF, and both the
// connect burst and the last release have to be able to say so.
func (o *orch) runbookRunsMsg() browserproto.RunbookRuns {
	rt := &o.runbooks
	runs := make([]browserproto.RunbookRun, 0, len(rt.running))
	for name, info := range rt.running {
		runs = append(runs, browserproto.RunbookRun{
			Name:      name,
			Source:    info.source,
			Trigger:   info.trigger,
			StartedAt: info.started.UTC().Format(time.RFC3339),
			Step:      info.step,
			Steps:     info.total,
		})
	}
	// Sorted because map iteration is not. Two identical sets that serialise
	// differently would be two different messages on the wire, which turns a
	// listing a client could compare against its last one into one it cannot.
	sort.Slice(runs, func(i, j int) bool { return runs[i].Name < runs[j].Name })
	return browserproto.NewRunbookRuns(runs)
}

// broadcastRunbookRuns pushes that set to every connected window.
//
// Called from the three places the accounting changes — claimRunbookSlot,
// reserveRunbook and releaseRunbookSlot — rather than from the executor,
// because those three are the complete set of transitions BY CONSTRUCTION: a
// run that did not take a slot does not exist, and a slot that is never
// released wedges the runbook at "already in flight" whether or not anybody is
// watching. Announcing where the truth changes is the same rule
// macroRecorder.changed follows, and for the same reason: an announcement
// hung off the caller instead would be one `catctl` path away from being
// forgotten.
//
// See browserproto.RunbookRuns for why this is a browser broadcast and
// deliberately not a control-API event.
func (o *orch) broadcastRunbookRuns() {
	// Any broadcast, for any reason, is the whole set — so it answers whatever
	// the dirty flag was going to ask for. Clearing it here is what stops a run
	// that finished inside one loop turn from being followed by a redundant
	// flush of the position it no longer has.
	o.runbooks.dirty = false
	o.broadcast(o.runbookRunsMsg())
}

// noteRunbookStep records how far a run has got. It does not broadcast.
//
// The executor calls this once per step, and a run of inline commands executes
// every one of its steps inside a single turn of the orchestrator loop — so
// broadcasting here would send a burst of messages describing positions that
// existed for microseconds and were never drawn. The flag defers that to
// flushRunbookRuns, which the loop calls once per turn.
//
// A name with no slot is ignored rather than logged: the only way to reach that
// is a step resolving after finishRunbook already released the run, which the
// double-answer guard in advanceRunbook drops on its own.
func (o *orch) noteRunbookStep(name string, step int) {
	rt := &o.runbooks
	info, ok := rt.running[name]
	if !ok || info.step == step {
		return
	}
	info.step = step
	rt.running[name] = info
	rt.dirty = true
}

// flushRunbookRuns sends the coalesced progress, if any moved. Called from the
// loop between closures, the same place and for the same reason flushClients is
// called there: it is the one point where no broadcast is in progress, and a
// closure that advanced a run through forty steps sends one message rather than
// forty.
//
// Progress is coalesced and the EDGES are not, which is the whole shape of this:
// a run starting and a run ending are transitions, there are exactly two of them
// per run, and a window that learns about them a loop turn late would flash a
// mark on and off for a run that had already ended. Progress is a number that is
// only ever read as "the latest one", so the only position worth sending is the
// one still true when the turn ends.
func (o *orch) flushRunbookRuns() {
	if o.runbooks.dirty {
		o.broadcastRunbookRuns()
	}
}
