//go:build ghostty

package main

import (
	"log"
	"slices"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/layout"
)

// Reaping exited panes (catway side).
//
// A pane whose child exits is deliberately KEPT: the chrome turns red, the last
// screen stays put, and the exit code is on the header — that is the whole point
// of holding a dead pane open, because the build output or the stack trace that
// preceded the exit is usually why anyone is looking. Nothing, however, ever took
// it away again: only a hand-issued pane.close did. A session left running for
// days therefore silted up with dead panes, each still holding a slot in its
// tab's BSP tree, a paneRuntime, and a scrollback seed in history.json.
//
// So the pane is kept for as long as it is plausibly still being read, and no
// longer. Four hours is "since before lunch" — past that, the pane is scenery.
// `panes.reap_exited` in the config file moves that line, and setting it to
// "off" restores the old keep-forever behaviour outright.
//
// Two invariants make this safe to run unattended:
//
//   - The session's last pane is never reaped. Session.ClosePaneIn refuses it
//     ("cannot close the last pane"), and the sweep just skips what it refuses,
//     so an idle session cannot reap itself down to nothing.
//   - A pane whose PTY was respawned under it (a cold restore, a host reconnect,
//     or a move to another host — all three route through createPane) has its
//     whole exit state cleared there, reap clock included, so the reaper only
//     ever closes a pane that is still dead.
//
// Everything here runs on the orchestrator loop goroutine except the ticker
// goroutine (runExitedReaper), which only posts closures onto it.
const (
	// defaultExitedPaneTTL is how long a pane's corpse is kept after its child
	// exits, before `panes.reap_exited` has a say. It is duplicated from
	// config.Default() rather than read from it because an orch built without a
	// config file (tests, an embedded caller) must still reap: a zero
	// o.reapAfter means "never", and a constructor that forgot to fill the
	// field would otherwise switch the feature off in silence.
	defaultExitedPaneTTL = 4 * time.Hour
	// reapInterval paces the sweep. It is deliberately coarse relative to any
	// sane TTL: nothing depends on reaping promptly at the four-hour mark, and
	// a five-minute tick over a handful of panes costs nothing.
	reapInterval = 5 * time.Minute
)

// reapAfterFromConfig resolves the configured TTL. Config.Validate has already
// refused an unparseable value at load, so the fallback is unreachable in
// practice — but a sweep parameter is not worth failing to start over, and the
// log line beats silently reaping on a schedule nobody asked for.
func reapAfterFromConfig(p config.Panes) time.Duration {
	d, err := p.ReapExitedAfter()
	if err != nil {
		log.Printf("catway: panes.%v — using %s", err, defaultExitedPaneTTL)
		return defaultExitedPaneTTL
	}
	return d
}

// runExitedReaper is the periodic sweep pacer (own goroutine, started by main):
// it only posts onto the loop.
func (o *orch) runExitedReaper() {
	t := time.NewTicker(reapInterval)
	defer t.Stop()
	for range t.C {
		o.post(func() { o.reapExitedPanes(time.Now()) })
	}
}

// reapExitedPanes closes every pane whose child exited more than the configured
// TTL ago, and returns how many it closed. `now` is a parameter rather than read
// here so the sweep is testable without waiting four hours. Loop goroutine.
func (o *orch) reapExitedPanes(now time.Time) int {
	ttl := o.reapAfter
	if ttl <= 0 {
		return 0 // panes.reap_exited is off: keep corpses forever, as before
	}
	// Collect first, close second: closing mutates o.panes (applyModel →
	// syncDaemon drops the runtime), and that must not happen under the range.
	// Sorted so a multi-pane sweep is deterministic — the order decides which
	// pane survives as "the last pane", and a test that cannot predict it is
	// a test that flakes.
	var due []uint32
	for pid, rt := range o.panes {
		if rt.exited == nil || rt.exitedAt.IsZero() {
			continue
		}
		if now.Sub(rt.exitedAt) < ttl {
			continue
		}
		due = append(due, pid)
	}
	slices.Sort(due)

	n := 0
	for _, pid := range due {
		id := layout.PaneID(pid)
		if _, err := o.session.ClosePaneIn("", &id); err != nil {
			// The only expected failure is the last-pane refusal. Leave the
			// stamp alone: if the session grows a second pane later, the next
			// sweep reaps this one then.
			continue
		}
		// The corpse's scrollback seed goes with it. histSaveNow already prunes
		// to panes still in the model, but only when a capture arms a write —
		// and a reaped pane produces no more captures, so the seed would sit in
		// history.json until some other pane happened to trigger one.
		delete(o.capturedHist, pid)
		log.Printf("catway: reaped pane %d — exited more than %s ago", pid, ttl)
		n++
	}
	if n > 0 {
		// One applyModel for the whole sweep, not one per pane: the layout
		// broadcast, viewport recompute and pane_removed events are all
		// diff-based, so a batch costs the same as a single close.
		o.applyModel()
		o.histSaveSoon()
	}
	return n
}

// ---- Auto-closing a cleanly exited pane -------------------------------------
//
// The reaper above is the long timescale: it stops a session silting up over
// hours. This is the short one, and it exists because most dead panes are not
// interesting for even a minute — you ran a command, it finished, and the pane
// is now a rectangle with a prompt-less shell in it.
//
// So a pane whose child exits with status 0 counts itself down and closes:
//
//	pane 3 · build · ~/src · exited (0) — close in 7s ✕
//
// Three properties are what make that safe to do automatically:
//
//   - Only status 0 arms it. A non-zero exit is a stack trace or a failed
//     build — precisely what keeping dead panes is FOR — so those keep the old
//     behaviour untouched and wait for the reaper.
//   - The clock is here, not in the browser. One timer per pane means the pane
//     is closed once however many windows are watching, every window draws the
//     same remaining time (PaneExited.AutocloseMS), and a window joining
//     mid-countdown picks it up where it is instead of starting a fresh
//     countdown of its own.
//   - It is cancellable, and the cancel is shared: the header's ✕ sends
//     pane.keep, which stops the timer and re-broadcasts the exit with no
//     countdown, so one person's "keep this" reaches everyone.
//
// Everything here runs on the loop goroutine; the timer callback only posts.
const (
	// defaultAutocloseTTL mirrors config.Default()'s panes.autoclose_exited,
	// duplicated for the same reason defaultExitedPaneTTL is: an orch built
	// with no config file (tests, an embedded caller) must still behave, and a
	// zero field means "off" rather than "default".
	defaultAutocloseTTL = 20 * time.Second
)

// autocloseAfterFromConfig resolves the configured countdown, falling back to
// the built-in default if the value is unparseable — Config.Validate has
// already refused that at load, so this is belt-and-braces plus a log line.
func autocloseAfterFromConfig(p config.Panes) time.Duration {
	d, err := p.AutocloseExitedAfter()
	if err != nil {
		log.Printf("catway: panes.%v — using %s", err, defaultAutocloseTTL)
		return defaultAutocloseTTL
	}
	return d
}

// armAutoclose starts the countdown on a pane that has just exited cleanly, and
// reports the countdown to send to clients (0 = none, which is what every
// caller-visible "no countdown" case resolves to).
//
// Called from the pane_exited path only, which is also why re-arming is not a
// concern: that path arms only on the FIRST exit, so a duplicate pane_exited
// replayed by a reconnecting host cannot push a running countdown back to ten.
// Loop goroutine.
func (o *orch) armAutoclose(rt *paneRuntime, code int) time.Duration {
	if code != 0 || o.autocloseAfter <= 0 {
		return 0
	}
	// The last pane is never auto-closed — ClosePaneIn would refuse it anyway
	// (see reapExitedPanes), and a countdown that visibly does nothing when it
	// reaches zero is worse than no countdown. Checked when arming rather than
	// when firing so the header never shows a promise that cannot be kept.
	//
	// It is deliberately not re-checked later: a session that grows a second
	// pane during the countdown leaves this corpse to the reaper, which is
	// the same answer the sweep gives.
	if o.session.PaneCount() <= 1 {
		return 0
	}
	d := o.autocloseAfter
	pid := rt.id
	rt.autocloseAt = time.Now().Add(d)
	rt.autoclose = time.AfterFunc(d, func() { o.post(func() { o.fireAutoclose(pid) }) })
	return d
}

// cancelAutoclose stops a pane's countdown if one is running and reports
// whether it actually stopped one. Idempotent, and safe on a live pane.
//
// The Stop() return is ignored on purpose: a timer that has already fired has
// posted its closure onto the loop, and fireAutoclose re-reads rt.autoclose —
// which this has just cleared — so the fire is dropped. Clearing the field is
// what cancels, not stopping the timer. Loop goroutine.
func (o *orch) cancelAutoclose(rt *paneRuntime) bool {
	if rt == nil || rt.autoclose == nil {
		return false
	}
	rt.autoclose.Stop()
	rt.autoclose = nil
	rt.autocloseAt = time.Time{}
	return true
}

// autocloseLeft is how much of a pane's countdown remains, 0 when none is
// running — what a late joiner's chrome carries so its header continues the
// countdown instead of restarting it. Loop goroutine.
func (o *orch) autocloseLeft(rt *paneRuntime) time.Duration {
	if rt == nil || rt.autoclose == nil {
		return 0
	}
	if d := time.Until(rt.autocloseAt); d > 0 {
		return d
	}
	// Due, but the fire has not been processed yet. Report a tick rather than
	// 0, which a client reads as "no countdown" and would draw as a header that
	// silently stops counting a moment before the pane vanishes.
	return time.Millisecond
}

// fireAutoclose is the countdown reaching zero: close the pane. Posted by the
// timer, so it re-validates everything — the pane may have been closed, kept,
// or respawned in the meantime, and each of those clears rt.autoclose, which is
// the one flag this trusts. Loop goroutine.
func (o *orch) fireAutoclose(pid uint32) {
	rt := o.panes[pid]
	if rt == nil || rt.autoclose == nil || rt.exited == nil {
		return // gone, kept, or alive again
	}
	rt.autoclose = nil
	rt.autocloseAt = time.Time{}
	id := layout.PaneID(pid)
	if _, err := o.session.ClosePaneIn("", &id); err != nil {
		// The last-pane refusal, almost certainly (the session shrank under the
		// countdown). Leave the corpse and its exit stamp alone: the reaper
		// still owns it, and the clients' countdown simply ends without the
		// pane going away.
		log.Printf("catway: pane %d auto-close refused: %v", pid, err)
		return
	}
	delete(o.capturedHist, pid) // the corpse's scrollback seed goes with it
	o.applyModel()
	o.histSaveSoon()
}

// keepPane cancels a pane's auto-close (the pane.keep command) and tells every
// window watching, by re-sending the exit with no countdown on it. Returns
// false for an unknown pane so the command can fail rather than silently
// succeed; cancelling a pane with no countdown running is a no-op success, since
// "keep this pane" is already true of it. Loop goroutine.
func (o *orch) keepPane(pid uint32) bool {
	rt := o.panes[pid]
	if rt == nil {
		return false
	}
	if o.cancelAutoclose(rt) && rt.exited != nil {
		o.sendVisible(pid, browserproto.NewPaneExited(pid, *rt.exited))
	}
	return true
}
