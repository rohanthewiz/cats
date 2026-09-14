//go:build darwin

package main

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rohanthewiz/cats/internal/dlog"
)

// Keeping catway up.
//
// catway is the half of local mode that holds nothing a restart loses: the panes
// live in cathost (-persistent), the session layout is saved to disk, and the
// page reconnects by itself every 1.5s (web/js/37-session.js). So an exit nobody
// asked for is answered with the same launch again — same port, same sockets —
// rather than a blank window. A catway-only restart was checked live on
// 2026-09-14 to bring the same shells back, pids unchanged.
//
//	catway exits ──▶ quitting? ──yes──▶ done
//	                    │ no
//	                    ▼
//	             restart budget ──spent──▶ overlay in every window
//	                    │ left                     │ "Restart catway"
//	                    ▼                          ▼
//	              sleep (backoff) ──────────▶ launch, same args
//	                                               │
//	                              ready? ──no──▶ counts as another exit
//	                                │ yes
//	                                ▼
//	                   clear the overlay, wait for the next exit
//
// Why a budget rather than retrying forever: a catway that dies on startup (a
// config it rejects, a state file it cannot read) would spin, and every crash
// writes a goroutine dump into daemons.log — at 1 MiB, a spin rotates the first
// and most useful dump out within minutes. Five exits a minute is far past what
// a healthy session produces and stops well before that. After it, a person
// decides; the overlay's button is a fresh budget, not a single attempt, since
// whatever they changed may need a moment to settle.
//
// cathost is deliberately not restarted. A cathost exit takes the panes with it,
// so relaunching it would quietly swap live shells for fresh ones — the kind of
// loss that should be seen, not smoothed over. The overlay says so instead.

// The restart policy.
const (
	restartWindow      = time.Minute
	restartMax         = 5
	restartBackoffBase = 250 * time.Millisecond
	restartBackoffCap  = 4 * time.Second
	// catwayReadyTimeout bounds a (re)started catway's wait to accept TCP. It is
	// the startup step's old fixed value; an exit ends the wait sooner.
	catwayReadyTimeout = 10 * time.Second
)

// restartBudget decides whether an exit gets another launch, and after how long.
//
// It counts launches in a sliding window rather than in a row, so a catway that
// crashes once an hour always comes straight back, while one that crashes on
// every start is refused after max. The delay doubles with each launch still in
// the window — 250ms, 500ms, 1s, 2s, 4s by default — which gives a transient
// cause (a port a moment from free) room without making the ordinary one-off
// crash wait.
//
// Not safe for concurrent use; only the supervisor goroutine touches it.
type restartBudget struct {
	window    time.Duration
	max       int
	base, cap time.Duration
	recent    []time.Time // launches granted within the window, oldest first
}

func newRestartBudget() restartBudget {
	return restartBudget{
		window: restartWindow,
		max:    restartMax,
		base:   restartBackoffBase,
		cap:    restartBackoffCap,
	}
}

// next spends one launch and returns its delay, or false when the window's
// budget is gone. A refusal spends nothing.
func (r *restartBudget) next(now time.Time) (time.Duration, bool) {
	keep := r.recent[:0]
	for _, t := range r.recent {
		if now.Sub(t) < r.window {
			keep = append(keep, t)
		}
	}
	r.recent = keep
	if len(r.recent) >= r.max {
		return 0, false
	}
	d := r.base << len(r.recent)
	if d > r.cap || d <= 0 { // <= 0: the shift overflowed
		d = r.cap
	}
	r.recent = append(r.recent, now)
	return d, true
}

// used is how many launches are in the window, for the log line.
func (r *restartBudget) used() int { return len(r.recent) }

func (r *restartBudget) reset() { r.recent = nil }

// backendUI is what the supervisor tells the windows. windowBackendUI
// (backenddown.go) draws it over the pages; a test records it.
type backendUI interface {
	// catwayDown: automatic restarts are spent. canRetry is false when a restart
	// could not help (cathost is gone too).
	catwayDown(detail string, canRetry bool)
	// catwayRestarting: a restart the user asked for is under way.
	catwayRestarting()
	// catwayBack: a restarted catway is accepting connections.
	catwayBack()
}

var errBackendStopped = errors.New("the backend is stopping")

// activeBackend is the backend the overlay's button reaches. The button arrives
// through a cgo export with no receiver, on the main thread, while the backend is
// set from the startup goroutine — hence atomic.
var activeBackend atomic.Pointer[backend]

// requestBackendRestart is the overlay's "Restart catway". Non-blocking and
// idempotent: retry holds one request, and a second click while one waits is
// the same request.
func requestBackendRestart() {
	b := activeBackend.Load()
	if b == nil {
		return
	}
	select {
	case b.retry <- struct{}{}:
	default:
	}
}

// superviseCatway keeps catway running until the backend stops. It runs for the
// life of the app on its own goroutine, started once the first catway is ready.
func (b *backend) superviseCatway(ui backendUI) {
	for {
		p := b.currentCatway()
		select {
		case <-p.done:
		case <-b.quit:
			return
		}
		// stop closes quit before it signals catway, so an exit that stop caused
		// always finds this set.
		if b.isStopped() {
			return
		}
		last := p.exitStatus()
		for {
			if !b.waitForRestart(ui, last) {
				return
			}
			err := b.relaunch(ui)
			if err == nil {
				break
			}
			if errors.Is(err, errBackendStopped) {
				return
			}
			last = err.Error()
		}
	}
}

// waitForRestart spends one launch from the budget and sleeps its backoff, or,
// when the budget is spent, puts the overlay up and waits for the user. false
// means the app is quitting.
func (b *backend) waitForRestart(ui backendUI, last string) bool {
	if b.isStopped() {
		return false
	}
	delay, ok := b.restarts.next(time.Now())
	if ok {
		b.log.note(dlog.LevelWarn, "restarting catway in %s (%d of %d within %s)",
			delay, b.restarts.used(), b.restarts.max, b.restarts.window)
		select {
		case <-time.After(delay):
			return true
		case <-b.quit:
			return false
		}
	}

	cathostUp := !b.cathostExited()
	b.log.note(dlog.LevelError, "catway exited %d times within %s (last: %s) — no more automatic restarts",
		b.restarts.max, b.restarts.window, last)
	// A request left over from before this outage (a click that landed as the
	// last one recovered) must not skip the wait for this one.
	select {
	case <-b.retry:
	default:
	}
	ui.catwayDown(catwayDownDetail(last, cathostUp), cathostUp)
	select {
	case <-b.retry:
	case <-b.quit:
		return false
	}
	b.log.note(dlog.LevelWarn, "catway restart requested from the window")
	ui.catwayRestarting()
	b.restarts.reset()
	b.restarts.next(time.Now()) // this launch is the first of the fresh budget
	return !b.isStopped()
}

// relaunch starts catway again with its original arguments and waits for it to
// accept connections. A catway that does not come up is stopped and reported as
// an error, which the caller counts as another exit.
func (b *backend) relaunch(ui backendUI) error {
	gw, err := b.launch(&b.catway, b.catwayPath, b.catwayArgs...)
	if err != nil {
		if !errors.Is(err, errBackendStopped) {
			// No process at all: the binary went away (an app update replaced the
			// bundle under us) or the exec failed outright.
			b.log.note(dlog.LevelError, "could not restart catway: %v", err)
		}
		return err
	}
	if err := b.ready(gw); err != nil {
		gw.stop(catwayStopGrace) // returns at once when it has already exited
		if b.isStopped() {
			return errBackendStopped
		}
		b.log.note(dlog.LevelError, "restarted catway (pid %d) did not come up: %v", gw.pid(), err)
		return err
	}
	b.log.note(dlog.LevelWarn, "catway restarted (pid %d) at http://%s", gw.pid(), b.addr)
	ui.catwayBack()
	return nil
}

// catwayDownDetail is the overlay's body: what happened, what it means for the
// panes, and where the evidence is.
func catwayDownDetail(last string, cathostUp bool) string {
	var s strings.Builder
	fmt.Fprintf(&s, "catway kept exiting, so it has been left stopped.\nLast exit: %s\n\n", last)
	if cathostUp {
		s.WriteString("Your panes are still running in cathost. Restarting catway reconnects every window to them.")
	} else {
		s.WriteString("cathost has exited too, so the panes are gone. Quit and reopen Cats to start a new session.")
	}
	if p := daemonLogPath(); p != "" {
		fmt.Fprintf(&s, "\n\nWarnings and crash reports: %s", p)
	}
	return s.String()
}
