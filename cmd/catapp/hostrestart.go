//go:build darwin

package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/dlog"
)

// Keeping cathost up.
//
// A cathost exit is not like a catway exit: cathost owns the PTYs, so the
// programs running in every pane end with it, and no restart brings them back.
// N-025 left it stopped for that reason, so the loss would be seen rather than
// smoothed over. In practice it was not seen at all (N-068): the overlay only
// goes up when catway's budget is spent, so on 2026-10-22 a cathost panic left
// every window on "cathost daemon not connected — retrying" for good, with
// one line in daemons.log as the only record.
//
// So cathost is now restarted too, and the loss is announced instead of left to
// be inferred:
//
//	cathost exits ──▶ quitting? ──yes──▶ done
//	                    │ no
//	                    ▼
//	           restart budget ──spent──▶ "cathost stopped" card, no button
//	                    │ left               (quit and reopen; supervisor ends)
//	                    ▼
//	          sleep (backoff), launch on the same socket
//	                    │
//	   alive after settle? ──no──▶ counts as another exit
//	                    │ yes
//	                    ▼
//	  "pane processes were lost" card (OK dismisses), wait for the next exit
//
// Nothing else has to be told. catway redials the socket forever on its own
// backoff (1s doubling to 5s, cmd/catway/daemon.go), and when it reaches the new
// cathost its reconcile finds none of the session's panes there and respawns
// every one as a fresh shell (the same path as a cold start). The layout
// survives, but what ran in the panes does not, and the card says so.
//
// The budget is the same policy as catway's (restartWindow, restartMax) and
// is there for the same reason: a cathost that dies on every start would spin
// crash dumps through daemons.log. Unlike catway's, a spent budget offers no
// button. A fresh cathost has nothing to reconnect to, so quitting and reopening
// costs the user nothing extra and starts a clean session.

// cathostSettle is how long a restarted cathost must stay running to count as
// up. Readiness here is deliberately "did not exit" rather than "accepts on the
// socket": a persistent cathost attaches one client at a time (cmd/cathost
// runPersistent's serial Attach), so a probe connection would either be
// attached itself, logging a spurious "session ended" when it hung up, or sit in
// the backlog behind catway. Either way it would get in the way of the one
// client that matters. A cathost that fails to start fails fast (a socket it
// cannot bind, a bad flag), well inside a second.
const cathostSettle = time.Second

// superviseCathost keeps cathost running until the backend stops. It runs for
// the life of the app on its own goroutine, beside superviseCatway.
func (b *backend) superviseCathost(ui backendUI) {
	for {
		p := b.currentCathost()
		if p == nil {
			return // no cathost was ever started (a partially built backend)
		}
		select {
		case <-p.done:
		case <-b.quit:
			return
		}
		// stop closes quit before it signals cathost, so an exit that stop caused
		// always finds this set. The exit itself is already logged by the
		// daemonProc watcher, crash dump first.
		if b.isStopped() {
			return
		}
		last := p.exitStatus()
		for {
			delay, ok := b.cathostRestarts.next(time.Now())
			if !ok {
				b.log.note(dlog.LevelError, "cathost exited %d times within %s (last: %s) — no more automatic restarts",
					b.cathostRestarts.max, b.cathostRestarts.window, last)
				ui.cathostDown(cathostDownDetail(last))
				return
			}
			b.log.note(dlog.LevelWarn, "restarting cathost in %s (%d of %d within %s)",
				delay, b.cathostRestarts.used(), b.cathostRestarts.max, b.cathostRestarts.window)
			select {
			case <-time.After(delay):
			case <-b.quit:
				return
			}
			err := b.relaunchCathost()
			if err == nil {
				ui.cathostLost(cathostLostDetail(last))
				break
			}
			if errors.Is(err, errBackendStopped) {
				return
			}
			last = err.Error()
		}
	}
}

// relaunchCathost starts cathost again with its original arguments, on the same
// socket (it removes the dead one's stale socket file itself before listening,
// cmd/cathost/listen.go), and waits out cathostReady. A cathost that does not
// stay up is reported as an error, which the caller counts as another exit.
func (b *backend) relaunchCathost() error {
	th, err := b.launch(&b.cathost, b.cathostPath, b.cathostArgs...)
	if err != nil {
		if !errors.Is(err, errBackendStopped) {
			// No process at all: the binary went away (an app update replaced the
			// bundle under us) or the exec failed outright.
			b.log.note(dlog.LevelError, "could not restart cathost: %v", err)
		}
		return err
	}
	if err := b.cathostReady(th); err != nil {
		th.stop(cathostStopGrace) // returns at once when it has already exited
		if b.isStopped() {
			return errBackendStopped
		}
		b.log.note(dlog.LevelError, "restarted cathost (pid %d) did not come up: %v", th.pid(), err)
		return err
	}
	b.log.note(dlog.LevelWarn, "cathost restarted (pid %d) on %s — the panes' processes were lost", th.pid(), b.socket)
	return nil
}

// waitAlive returns nil once p has run for d without exiting, or an error with
// its exit status if it exits first.
func waitAlive(p *daemonProc, d time.Duration) error {
	select {
	case <-p.done:
		return fmt.Errorf("%s exited within %s of starting: %s", p.name, d, p.exitStatus())
	case <-time.After(d):
		return nil
	}
}

// cathostLostDetail is the restarted card's body. It leads with the loss, which
// is the part the user has to act on: the layout is back, so nothing on screen
// shows that a build or an agent that was running is no longer.
func cathostLostDetail(last string) string {
	var s strings.Builder
	fmt.Fprintf(&s, "cathost exited unexpectedly (%s), and every program running in your panes ended with it.\n\n", last)
	s.WriteString("It has been restarted and the panes reopened with fresh shells. " +
		"Anything that was running in them needs to be started again.")
	if p := daemonLogPath(); p != "" {
		fmt.Fprintf(&s, "\n\nWarnings and crash reports: %s", p)
	}
	return s.String()
}

// cathostDownDetail is the card's body once automatic restarts are spent.
func cathostDownDetail(last string) string {
	var s strings.Builder
	fmt.Fprintf(&s, "cathost kept exiting, so it has been left stopped.\nLast exit: %s\n\n", last)
	s.WriteString("The panes are gone and cannot reconnect. Quit and reopen Cats to start a new session.")
	if p := daemonLogPath(); p != "" {
		fmt.Fprintf(&s, "\n\nWarnings and crash reports: %s", p)
	}
	return s.String()
}
