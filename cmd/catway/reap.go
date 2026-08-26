//go:build ghostty

package main

import (
	"log"
	"slices"
	"time"

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
