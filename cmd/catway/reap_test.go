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

// The exited-pane reaper (reap.go). A dead pane is kept so its last screen can
// still be read, but not forever; these cover the three judgements that keep it
// safe — old enough, still dead, and never the last one standing — plus the
// knob that sets "old enough" and the switch that turns the whole thing off.

// livePanes reports the model's pane set, which is what the reaper actually
// changes (o.panes follows it through applyModel → syncDaemon).
func livePanes(o *orch) []uint32 {
	ids := o.session.AllPaneIDs()
	out := make([]uint32, 0, len(ids))
	for _, id := range ids {
		out = append(out, uint32(id))
	}
	slices.Sort(out)
	return out
}

// markExited puts a pane in the state a pane_exited event leaves it in, with
// the exit stamped `ago` in the past.
func markExited(o *orch, pid uint32, ago time.Duration) {
	rt := o.panes[pid]
	code := 0
	rt.exited = &code
	rt.exitedAt = time.Now().Add(-ago)
}

// The point of the feature: a pane whose child died before lunch is gone by
// mid-afternoon, and its scrollback seed goes with it.
func TestReapClosesPanesDeadLongerThanTTL(t *testing.T) {
	o, live, dead := mcOrch(t)
	markExited(o, dead, o.reapAfter+time.Minute)
	o.capturedHist[dead] = "some scrollback"

	if n := o.reapExitedPanes(time.Now()); n != 1 {
		t.Fatalf("reaped %d panes, want 1", n)
	}
	if got := livePanes(o); !slices.Equal(got, []uint32{live}) {
		t.Fatalf("panes after the sweep = %v, want just the live one (%d)", got, live)
	}
	if o.panes[dead] != nil {
		t.Error("the reaped pane's runtime survived the sweep")
	}
	if _, ok := o.capturedHist[dead]; ok {
		t.Error("the reaped pane's captured scrollback survived the sweep")
	}
}

// The other half of the same judgement: a pane that died minutes ago is still
// being read, and a pane that never died is not the reaper's business at all.
func TestReapKeepsRecentAndLivePanes(t *testing.T) {
	o, live, dead := mcOrch(t)
	markExited(o, dead, o.reapAfter-time.Minute)

	if n := o.reapExitedPanes(time.Now()); n != 0 {
		t.Fatalf("reaped %d panes, want 0", n)
	}
	if got := livePanes(o); !slices.Equal(got, []uint32{min(live, dead), max(live, dead)}) {
		t.Fatalf("panes after the sweep = %v, want both", got)
	}
	// And the live one carries no clock at all, so no amount of elapsed time
	// makes it a candidate.
	if !o.panes[live].exitedAt.IsZero() {
		t.Error("a live pane has a reap clock running")
	}
	if n := o.reapExitedPanes(time.Now().Add(72 * time.Hour)); n != 1 {
		t.Fatal("three days on, the dead pane should have been reaped and the live one kept")
	}
	if got := livePanes(o); !slices.Equal(got, []uint32{live}) {
		t.Fatalf("panes after the late sweep = %v, want just the live one (%d)", got, live)
	}
}

// An idle session must not reap itself out of existence: the last pane stays,
// however long it has been dead, and the sweep keeps trying rather than
// clearing the stamp — so it goes the moment a second pane exists.
func TestReapNeverClosesTheLastPane(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	o.syncDaemon()
	only := uint32(o.session.AllPaneIDs()[0])
	markExited(o, only, 30*24*time.Hour)

	if n := o.reapExitedPanes(time.Now()); n != 0 {
		t.Fatalf("reaped %d panes, want 0 — the session's last pane is not reapable", n)
	}
	if got := livePanes(o); !slices.Equal(got, []uint32{only}) {
		t.Fatalf("panes after the sweep = %v, want the last pane (%d) intact", got, only)
	}

	// A new pane makes the corpse reapable on the very next sweep.
	if _, err := o.session.SplitPane(nil, layout.Horizontal); err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	o.syncDaemon()
	if n := o.reapExitedPanes(time.Now()); n != 1 {
		t.Fatalf("reaped %d panes after the split, want 1", n)
	}
}

// respawn is what reconcile does to a pane whose host no longer holds it: drop
// the created flag and let syncDaemon spawn a fresh PTY.
func respawn(o *orch, pid uint32) {
	o.panes[pid].created = false
	o.syncDaemon()
}

// A respawn (cold restore, host reconnect, or a move to another host) gives the
// pane a live PTY again. The reaper must not close it out from under the new
// shell.
func TestReapSkipsRespawnedPane(t *testing.T) {
	o, _, dead := mcOrch(t)
	markExited(o, dead, o.reapAfter+time.Hour)
	respawn(o, dead)

	if !o.panes[dead].exitedAt.IsZero() {
		t.Fatal("the respawn left the reap clock running")
	}
	if n := o.reapExitedPanes(time.Now()); n != 0 {
		t.Fatalf("reaped %d panes, want 0 — the pane was respawned", n)
	}
}

// The rest of the corpse goes with the clock. A respawned pane that stayed
// marked exited refused input to the shell that had just started, skipped every
// scrollback capture, and held a red header until the page was reloaded.
func TestRespawnClearsTheExitState(t *testing.T) {
	o, _, dead := mcOrch(t)
	markExited(o, dead, time.Minute)

	if err := o.SendInput(dead, "echo hi", true); err == nil {
		t.Fatal("a dead pane should refuse input")
	}
	respawn(o, dead)

	if o.panes[dead].exited != nil {
		t.Fatalf("the respawned pane is still marked exited (%d)", *o.panes[dead].exited)
	}
	if err := o.SendInput(dead, "echo hi", true); err != nil {
		t.Fatalf("the respawned pane still refuses input: %v", err)
	}
}

// And a window already showing the pane is told, because the client REMEMBERS
// the exit: the chrome a late joiner gets omits pane_exited for a live pane,
// which retracts nothing for a window that already drew the red header.
func TestRespawnTellsTheWatchingWindows(t *testing.T) {
	o, _, dead := mcOrch(t)
	c := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients() // what run() does between mailbox closures
	markExited(o, dead, time.Minute)
	drain(c)

	respawn(o, dead)

	if !sawRespawn(t, c, dead) {
		t.Fatalf("no pane_respawned for pane %d reached the window watching it", dead)
	}
	// A pane that never died is not announced as coming back to life.
	drain(c)
	respawn(o, dead)
	if sawRespawn(t, c, dead) {
		t.Error("re-spawning a live pane announced a respawn")
	}
}

// sawRespawn reports whether a pane_respawned for pid is on the connection's
// queue, consuming everything else on the way.
func sawRespawn(t *testing.T, c *client, pid uint32) bool {
	t.Helper()
	for {
		select {
		case b := <-c.out:
			msg, err := browserproto.DecodeDown(b)
			if err != nil {
				t.Fatalf("decode down: %v", err)
			}
			if r, ok := msg.(*browserproto.PaneRespawned); ok && r.Pane == pid {
				return true
			}
		default:
			return false
		}
	}
}

// The clock is started by the exit event itself, and a duplicate exit — a
// replayed event on reconnect, say — must not push it back to zero.
func TestPaneExitStartsTheReapClockOnce(t *testing.T) {
	o, _, pid := mcOrch(t)
	d := o.hosts[o.defaultHost]

	exit := func(code int) {
		payload, err := json.Marshal(orchestration.NewPaneExited(pid, code))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		d.dispatch(orchestration.MsgPaneExited, payload)
	}

	exit(1)
	waitFor(t, o, func() bool { return o.panes[pid].exited != nil })
	first := o.panes[pid].exitedAt
	if first.IsZero() {
		t.Fatal("pane_exited did not start the reap clock")
	}

	exit(1)
	waitFor(t, o, func() bool { return len(o.mailbox) == 0 })
	if got := o.panes[pid].exitedAt; !got.Equal(first) {
		t.Fatalf("a duplicate pane_exited moved the clock from %v to %v", first, got)
	}
}

// The TTL is panes.reap_exited, not a constant: a shorter one reaps sooner, and
// "off" (0) restores the keep-forever behaviour cats had before the reaper.
func TestReapHonoursTheConfiguredTTL(t *testing.T) {
	o, live, dead := mcOrch(t)
	markExited(o, dead, 90*time.Minute)

	o.reapAfter = 0 // panes.reap_exited: "off"
	if n := o.reapExitedPanes(time.Now()); n != 0 {
		t.Fatalf("reaped %d panes with the reaper off, want 0", n)
	}
	o.reapAfter = 4 * time.Hour // the default: 90 minutes is not enough
	if n := o.reapExitedPanes(time.Now()); n != 0 {
		t.Fatalf("reaped %d panes at a 4h TTL, want 0", n)
	}
	o.reapAfter = time.Hour // a shorter TTL the operator asked for
	if n := o.reapExitedPanes(time.Now()); n != 1 {
		t.Fatalf("reaped %d panes at a 1h TTL, want 1", n)
	}
	if got := livePanes(o); !slices.Equal(got, []uint32{live}) {
		t.Fatalf("panes after the sweep = %v, want just the live one (%d)", got, live)
	}
}

// An orch built without a config file still reaps: a zero reapAfter means
// "never", so the constructor — not the config layer — owns the default.
func TestOrchDefaultsToTheBuiltInTTL(t *testing.T) {
	o, _, _ := mcOrch(t)
	if o.reapAfter != defaultExitedPaneTTL {
		t.Fatalf("a fresh orch reaps after %v, want the built-in %v", o.reapAfter, defaultExitedPaneTTL)
	}
}
