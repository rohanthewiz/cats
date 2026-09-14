//go:build ghostty

package main

import (
	"os"
	"time"

	"github.com/rohanthewiz/cats/internal/dlog"
)

// shutdownDeadline bounds a signalled shutdown. The graceful path — save, the
// final scrollback capture (finalCaptureTimeout, 1s), then the 250ms flush in
// o.stop — normally exits in about 1.3s; the headroom is for a slow disk. It
// stays below catapp's own wait for catway (cmd/catapp/supervise.go), so a
// quit from the app sees catway leave on its own rather than be SIGKILLed.
const shutdownDeadline = 3 * time.Second

// handleSignals drives shutdown on SIGINT/SIGTERM: the first signal asks the
// orchestrator loop for a graceful Shutdown, a second exits at once.
//
// Neither step may depend on the loop, because the loop is exactly what is
// stuck when an operator reaches for a signal. The handler used to be
//
//	<-sigc; o.post(Shutdown); <-sigc; os.Exit(1)
//
// and o.post blocks while the loop's mailbox is full, so on a jammed loop the
// second receive was never reached: SIGTERM did nothing at all, however many
// times it was sent, and only SIGKILL worked (2026-09-13). So the post runs on
// its own goroutine, leaving this one free to see the second signal, and a
// timer that does not go through the loop exits the process if the graceful
// path has not finished by the deadline. What that skips is the final save and
// capture; the session is still saved on every mutation, so the loss is at
// most the save debounce.
//
// post, shutdown and exit are parameters so the jammed case can be tested
// without a loop or a real exit.
func handleSignals(sigc <-chan os.Signal, post func(func()), shutdown func(), exit func(int), deadline time.Duration) {
	<-sigc
	time.AfterFunc(deadline, func() {
		dlog.Errorf("catway: shutdown did not finish within %s (orchestrator loop not responding) — exiting without the final save", deadline)
		exit(1)
	})
	go post(shutdown)
	<-sigc
	dlog.Warnf("catway: second signal — exiting now")
	exit(1)
}
