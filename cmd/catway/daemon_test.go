//go:build ghostty

package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// The multi-host seam's whole promise is containment: one cathost going away,
// or reporting a pane set that disagrees with the model, must affect exactly
// that host's panes. These tests hold two hosts at once — the only shape in
// which the promise can actually be broken — and check the two places catway
// makes host-wide decisions: reconcile (which panes to adopt, respawn, close)
// and the disconnect flush (which requests and waiters can no longer resolve).

const testRemoteHost = "remote"

// twoHostOrch builds an orch with the synthesized local host plus a second
// host, one pane on each: the local session's original pane, and the root pane
// of a workspace created on the remote host. Both hosts get a pipe connection,
// so sends are observable and connected() is true for both.
func twoHostOrch(t *testing.T) (o *orch, localPane, remotePane uint32, pdLocal, pdRemote *pipeDaemon) {
	t.Helper()
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	// A second host needs no dialer: the test drives its connection directly,
	// which is exactly what run() would hand it. It joins hostOrder too, since
	// that (not the map) is what the roster is read from.
	o.hosts[testRemoteHost] = &daemon{o: o, id: testRemoteHost, label: testRemoteHost, kind: "unix"}
	o.hostOrder = append(o.hostOrder, testRemoteHost)

	localPane = uint32(o.session.AllPaneIDs()[0])
	if _, err := o.session.CreateWorkspaceAtOn(t.TempDir(), testRemoteHost); err != nil {
		t.Fatalf("CreateWorkspaceAtOn: %v", err)
	}
	remotePane = uint32(o.session.ActiveWorkspace().Tabs[0].RootPane)

	pdLocal = newPipeDaemonFor(t, o, o.defaultHost)
	pdRemote = newPipeDaemonFor(t, o, testRemoteHost)

	o.syncDaemon() // builds both runtimes, resolves their hosts, creates the PTYs
	o.refreshViewport()

	if got := o.panes[localPane].host; got != o.defaultHost {
		t.Fatalf("local pane host = %q, want %q", got, o.defaultHost)
	}
	if got := o.panes[remotePane].host; got != testRemoteHost {
		t.Fatalf("remote pane host = %q, want %q", got, testRemoteHost)
	}
	return o, localPane, remotePane, pdLocal, pdRemote
}

// collect drains a pipe for a short settling window and returns the message
// types seen. Used for the negative assertions ("host B was never told to close
// or create anything"), where waiting for a specific message is the wrong shape.
func (pd *pipeDaemon) collect(d time.Duration) []orchestration.MessageType {
	var got []orchestration.MessageType
	deadline := time.After(d)
	for {
		select {
		case m, ok := <-pd.msgs:
			if !ok {
				return got
			}
			got = append(got, m.mt)
		case <-deadline:
			return got
		}
	}
}

func hasType(msgs []orchestration.MessageType, want orchestration.MessageType) bool {
	for _, mt := range msgs {
		if mt == want {
			return true
		}
	}
	return false
}

// sync runs fn on the loop and waits for it — reconcile posts its work, so the
// assertions need a barrier behind it.
func syncPost(o *orch, fn func()) {
	done := make(chan struct{})
	o.post(func() { fn(); close(done) })
	<-done
}

// A host reconnecting with an empty survivor set must respawn its own panes and
// leave the other host's alone. Before the seam, reconcile compared *every*
// model pane against one host's alive list, so a remote host reconnecting would
// have declared the local panes dead and respawned them — duplicating live
// shells on a machine that never lost anything.
func TestReconcileIsHostScoped(t *testing.T) {
	o, localPane, remotePane, pdLocal, pdRemote := twoHostOrch(t)
	go o.run()

	// Both panes' PTYs are live as far as the model is concerned.
	syncPost(o, func() {
		o.panes[localPane].created = true
		o.panes[remotePane].created = true
	})
	pdLocal.collect(50 * time.Millisecond) // discard the initial creates
	pdRemote.collect(50 * time.Millisecond)

	// The remote host comes back holding nothing: its pane must be respawned.
	o.hosts[testRemoteHost].reconcile(nil)
	syncPost(o, func() {}) // the reconcile closure is ahead of this one

	if cp := pdRemote.expect(t, orchestration.MsgCreatePane); cp == nil {
		t.Fatal("remote host should have been asked to respawn its pane")
	}
	local := pdLocal.collect(100 * time.Millisecond)
	if hasType(local, orchestration.MsgCreatePane) || hasType(local, orchestration.MsgClosePane) {
		t.Fatalf("local host must be untouched by the remote reconcile, got %v", local)
	}
	syncPost(o, func() {
		if !o.panes[localPane].created {
			t.Error("local pane lost its created flag to another host's reconcile")
		}
	})
}

// A pane the reconciling host holds but the model does not is closed; one that
// belongs to *another* host is not — the alive list is per host, and pane ids
// are globally unique, so a stray id is only ever the reconciling host's own.
func TestReconcileClosesOnlyItsOwnStrays(t *testing.T) {
	o, _, remotePane, _, pdRemote := twoHostOrch(t)
	go o.run()
	pdRemote.collect(50 * time.Millisecond)

	const stray = 9999
	o.hosts[testRemoteHost].reconcile([]uint32{remotePane, stray})
	syncPost(o, func() {})

	payload := pdRemote.expect(t, orchestration.MsgClosePane)
	var cp orchestration.ClosePane
	if err := json.Unmarshal(payload, &cp); err != nil {
		t.Fatalf("decode close_pane: %v", err)
	}
	if cp.PaneID != stray {
		t.Fatalf("closed pane %d, want the stray %d", cp.PaneID, stray)
	}
}

// Losing one host fails only the work that host could have answered. The
// session-wide flush is still available (shutdown uses it), but a disconnect
// must not fail a capture or a wait that a healthy machine is about to resolve.
func TestFlushForHostIsScoped(t *testing.T) {
	o, localPane, remotePane, _, _ := twoHostOrch(t)

	c := &client{o: o, out: make(chan []byte, 8)}
	o.conns[c] = struct{}{}
	o.pendingReqs[paneKey(localPane, reqText)] = []*pending{pend(o, c, "local-req")}
	o.pendingReqs[paneKey(remotePane, reqText)] = []*pending{pend(o, c, "remote-req")}

	localWaiter, remoteWaiter := &recWaiter{}, &recWaiter{}
	o.waiters[localPane] = []*waiter{{resp: localWaiter, match: matcher(t, "x")}}
	o.waiters[remotePane] = []*waiter{{resp: remoteWaiter, match: matcher(t, "x")}}

	o.flushPendingFor(testRemoteHost, "cathost connection lost")
	o.flushWaitersFor(testRemoteHost, "cathost connection lost")

	if _, ok := o.pendingReqs[paneKey(localPane, reqText)]; !ok {
		t.Fatal("the local host's request was flushed by the remote host's disconnect")
	}
	if _, ok := o.pendingReqs[paneKey(remotePane, reqText)]; ok {
		t.Fatal("the remote host's request survived its own disconnect")
	}
	if r := recvResult(t, c); r.ID != "remote-req" || r.Ok {
		t.Fatalf("expected only the remote request to fail, got id=%q ok=%v", r.ID, r.Ok)
	}
	if len(c.out) != 0 {
		t.Fatalf("no other request should have been answered, queued %d", len(c.out))
	}
	if !remoteWaiter.fail {
		t.Fatal("the remote host's waiter should have failed")
	}
	if localWaiter.fail || localWaiter.ok {
		t.Fatal("the local host's waiter should still be waiting")
	}
	if _, ok := o.waiters[localPane]; !ok {
		t.Fatal("the local host's waiter entry was cleared")
	}
}

// PaneHostConnected is the gate the pane-addressed commands take. It answers
// per pane, so a pane on a live host stays usable while another host is down —
// and an unknown pane reports disconnected rather than borrowing the default
// host's health.
func TestPaneHostConnectedIsPerPane(t *testing.T) {
	o, localPane, remotePane, _, _ := twoHostOrch(t)

	if !o.PaneHostConnected(localPane) || !o.PaneHostConnected(remotePane) {
		t.Fatal("both panes should report connected while both hosts have a conn")
	}
	o.hosts[testRemoteHost].setConn(nil)
	if !o.PaneHostConnected(localPane) {
		t.Fatal("the local pane must stay usable when the remote host drops")
	}
	if o.PaneHostConnected(remotePane) {
		t.Fatal("the remote pane should report disconnected")
	}
	if !o.DaemonConnected() {
		t.Fatal("DaemonConnected tracks the default host, which is still up")
	}
	if o.PaneHostConnected(4242) {
		t.Fatal("an unknown pane must not report connected")
	}
}

// A pane whose recorded host no longer exists falls back to the default rather
// than becoming unreachable: a pane on the wrong machine can be closed and
// recreated, a permanently black one cannot.
func TestUnknownHostFallsBackToDefault(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	if _, err := o.session.CreateWorkspaceAtOn(t.TempDir(), "vanished"); err != nil {
		t.Fatalf("CreateWorkspaceAtOn: %v", err)
	}
	pid := uint32(o.session.ActiveWorkspace().Tabs[0].RootPane)
	if o.session.PaneHost(layout.PaneID(pid)) != "vanished" {
		t.Fatal("the model should have recorded the configured host")
	}

	o.syncDaemon()

	if got := o.panes[pid].host; got != o.defaultHost {
		t.Fatalf("pane host = %q, want the default %q", got, o.defaultHost)
	}
	if o.hostForPane(pid) != o.hosts[o.defaultHost] {
		t.Fatal("the pane should route to the default host's daemon")
	}
}
