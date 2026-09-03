//go:build ghostty

package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// The tidy-exit auto-close (reap.go's second half): a pane whose child exits
// with status 0 counts down in the header and then closes itself. These cover
// the four judgements that make that safe — only a clean exit, only when a
// close would actually be allowed, never a pane that came back, and always
// cancellable — plus the countdown's trip to the client, which is the part the
// user reads.

// dispatchExit feeds the orchestrator the pane_exited event a cathost sends,
// which is the only path that arms a countdown, and waits for the loop to
// finish with it.
func dispatchExit(t *testing.T, o *orch, pid uint32, code int) {
	t.Helper()
	payload, err := json.Marshal(orchestration.NewPaneExited(pid, code))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	o.hosts[o.defaultHost].dispatch(orchestration.MsgPaneExited, payload)
	waitFor(t, o, func() bool { return o.panes[pid] == nil || o.panes[pid].exited != nil })
	waitFor(t, o, func() bool { return len(o.mailbox) == 0 })
}

// lastExitMsg returns the most recent pane_exited for pid on a connection's
// queue, consuming everything on the way. The LAST one matters rather than the
// first because a cancel is delivered as a second pane_exited with no
// countdown on it.
func lastExitMsg(t *testing.T, c *client, pid uint32) *browserproto.PaneExited {
	t.Helper()
	var found *browserproto.PaneExited
	for {
		select {
		case b := <-c.out:
			msg, err := browserproto.DecodeDown(b)
			if err != nil {
				t.Fatalf("decode down: %v", err)
			}
			if e, ok := msg.(*browserproto.PaneExited); ok && e.Pane == pid {
				found = e
			}
		default:
			return found
		}
	}
}

// The feature itself: exit 0, wait, and the pane is gone — with the countdown
// announced to the window watching so the header can show it.
func TestAutocloseClosesCleanlyExitedPane(t *testing.T) {
	o, live, dead := mcOrch(t)
	c := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients()
	drain(c)
	o.autocloseAfter = 20 * time.Millisecond

	dispatchExit(t, o, dead, 0)

	o.flushClients()
	msg := lastExitMsg(t, c, dead)
	if msg == nil {
		t.Fatal("no pane_exited reached the window watching the pane")
	}
	if msg.AutocloseMS <= 0 {
		t.Fatalf("pane_exited carried autoclose_ms=%d, want the countdown", msg.AutocloseMS)
	}
	if o.panes[dead].autoclose == nil {
		t.Fatal("a clean exit armed no countdown")
	}

	waitFor(t, o, func() bool { return o.panes[dead] == nil })
	if got := livePanes(o); !slices.Equal(got, []uint32{live}) {
		t.Fatalf("panes after the countdown = %v, want just the live one (%d)", got, live)
	}
}

// A pane that died non-zero is showing the failure you are about to read. It
// keeps the old behaviour exactly: a red header, no countdown, and the reaper's
// four hours as its only clock.
func TestAutocloseIgnoresFailedExit(t *testing.T) {
	o, _, dead := mcOrch(t)
	c := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients()
	drain(c)
	o.autocloseAfter = 20 * time.Millisecond

	dispatchExit(t, o, dead, 1)

	if o.panes[dead].autoclose != nil {
		t.Fatal("a failed exit armed a countdown")
	}
	o.flushClients()
	if msg := lastExitMsg(t, c, dead); msg == nil || msg.AutocloseMS != 0 {
		t.Fatalf("pane_exited for a failed exit = %+v, want no countdown", msg)
	}
	time.Sleep(60 * time.Millisecond)
	waitFor(t, o, func() bool { return len(o.mailbox) == 0 })
	if o.panes[dead] == nil {
		t.Fatal("the failed pane was auto-closed")
	}
}

// "off" is off: panes.autoclose_exited disabled leaves every corpse to the
// reaper, which is what cats did before this existed.
func TestAutocloseHonoursTheOffSwitch(t *testing.T) {
	o, _, dead := mcOrch(t)
	o.autocloseAfter = 0

	dispatchExit(t, o, dead, 0)

	if o.panes[dead].autoclose != nil {
		t.Fatal("a countdown was armed with autoclose_exited off")
	}
	if o.panes[dead].exited == nil {
		t.Fatal("the exit itself was not recorded")
	}
}

// A countdown that cannot end in a close must not start: the session's last
// pane is unclosable, so it never counts down at all.
func TestAutocloseNeverArmsOnTheLastPane(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	o.syncDaemon()
	only := uint32(o.session.AllPaneIDs()[0])
	o.autocloseAfter = 20 * time.Millisecond

	dispatchExit(t, o, only, 0)

	if o.panes[only].autoclose != nil {
		t.Fatal("the session's last pane armed a countdown it could not honour")
	}
	time.Sleep(60 * time.Millisecond)
	waitFor(t, o, func() bool { return len(o.mailbox) == 0 })
	if got := livePanes(o); !slices.Equal(got, []uint32{only}) {
		t.Fatalf("panes = %v, want the last pane (%d) intact", got, only)
	}
}

// pane.keep: the header's ✕. It stops the timer and says so to every window,
// by re-sending the exit with no countdown on it.
func TestKeepPaneCancelsTheCountdown(t *testing.T) {
	o, _, dead := mcOrch(t)
	c := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients()
	drain(c)
	o.autocloseAfter = 30 * time.Millisecond

	dispatchExit(t, o, dead, 0)
	drain(c)

	if !o.KeepPane(dead) {
		t.Fatal("KeepPane refused a pane that exists")
	}
	if o.panes[dead].autoclose != nil {
		t.Fatal("pane.keep left the countdown running")
	}
	o.flushClients()
	if msg := lastExitMsg(t, c, dead); msg == nil || msg.AutocloseMS != 0 {
		t.Fatalf("the cancel broadcast = %+v, want a pane_exited with no countdown", msg)
	}

	time.Sleep(80 * time.Millisecond)
	waitFor(t, o, func() bool { return len(o.mailbox) == 0 })
	if o.panes[dead] == nil {
		t.Fatal("a kept pane was closed anyway")
	}
	// And keeping it again is a success, not an error: two windows racing the
	// same ✕ must both be told yes.
	if !o.KeepPane(dead) {
		t.Fatal("a second pane.keep on the same pane failed")
	}
	if o.KeepPane(9999) {
		t.Fatal("pane.keep succeeded on a pane that does not exist")
	}
}

// A respawn inside the countdown — a cathost reconnect is exactly this — must
// not close the shell that has just started.
func TestAutocloseCancelledByRespawn(t *testing.T) {
	o, _, dead := mcOrch(t)
	o.autocloseAfter = 30 * time.Millisecond

	dispatchExit(t, o, dead, 0)
	respawn(o, dead)

	if o.panes[dead].autoclose != nil {
		t.Fatal("the respawn left the countdown running")
	}
	time.Sleep(80 * time.Millisecond)
	waitFor(t, o, func() bool { return len(o.mailbox) == 0 })
	if o.panes[dead] == nil {
		t.Fatal("a respawned pane was auto-closed")
	}
}

// A window that joins mid-countdown continues it rather than restarting it:
// the chrome it gets carries what is LEFT, which is strictly less than the
// configured countdown.
func TestChromeCarriesTheRemainingCountdown(t *testing.T) {
	o, _, dead := mcOrch(t)
	o.autocloseAfter = 500 * time.Millisecond

	dispatchExit(t, o, dead, 0)
	time.Sleep(50 * time.Millisecond)

	late := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients()
	msg := lastExitMsg(t, late, dead)
	if msg == nil {
		t.Fatal("the late joiner's chrome carried no pane_exited")
	}
	if msg.AutocloseMS <= 0 || msg.AutocloseMS >= o.autocloseAfter.Milliseconds() {
		t.Fatalf("late joiner got autoclose_ms=%d, want a remainder under %d",
			msg.AutocloseMS, o.autocloseAfter.Milliseconds())
	}
	o.KeepPane(dead) // don't leave a timer running into the next test
}

// A duplicate pane_exited — a host replaying the event on reconnect — must not
// re-arm a countdown the user has already cancelled.
func TestDuplicateExitDoesNotReArmAKeptPane(t *testing.T) {
	o, _, dead := mcOrch(t)
	o.autocloseAfter = 40 * time.Millisecond

	dispatchExit(t, o, dead, 0)
	o.KeepPane(dead)
	dispatchExit(t, o, dead, 0)

	if o.panes[dead].autoclose != nil {
		t.Fatal("a replayed pane_exited re-armed a cancelled countdown")
	}
	time.Sleep(90 * time.Millisecond)
	waitFor(t, o, func() bool { return len(o.mailbox) == 0 })
	if o.panes[dead] == nil {
		t.Fatal("a kept pane was closed by a replayed exit")
	}
}

// Closing a pane while its countdown runs must not leave the timer holding a
// pane id that a later pane could be given.
func TestClosingAPaneCancelsItsCountdown(t *testing.T) {
	o, _, dead := mcOrch(t)
	o.autocloseAfter = time.Hour

	dispatchExit(t, o, dead, 0)
	rt := o.panes[dead]
	id := layout.PaneID(dead)
	if _, err := o.session.ClosePaneIn("", &id); err != nil {
		t.Fatalf("ClosePaneIn: %v", err)
	}
	o.syncDaemon()

	if o.panes[dead] != nil {
		t.Fatal("the closed pane's runtime survived")
	}
	if rt.autoclose != nil {
		t.Fatal("the closed pane's countdown was left armed")
	}
}
