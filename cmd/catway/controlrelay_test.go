//go:build ghostty

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// The control relay lets a machine that is not this one run every command the
// session has, on every host it holds. So the tests that matter most are the
// ones about who may: the gate is checked when a connection ARRIVES, not when a
// pane's environment is filled in, because the environment cannot be taken back
// from panes that are already running.

// controlHarness is twoHostOrch with a real control server behind it, so a
// relayed request runs the same dispatch a local one would.
func controlHarness(t *testing.T) (*orch, *pipeDaemon) {
	t.Helper()
	o, _, _, _, pdRemote := twoHostOrch(t)
	o.control = ctlproto.NewServer(o.controlDispatch, controlTimeout, "catway")
	go o.run()
	return o, pdRemote
}

// trust turns the relay on for the remote host, the way a config entry would.
func trust(o *orch, on bool) {
	d := o.hosts[testRemoteHost]
	d.spec = config.Host{ID: testRemoteHost, ControlRelay: on}
}

// A trusted host's connection is served by the real control server: this drives
// `ping` end to end, which proves the relayed API is the API rather than a
// second implementation of it.
func TestControlRelayServesATrustedHost(t *testing.T) {
	o, pd := controlHarness(t)
	trust(o, true)
	d := o.hosts[testRemoteHost]

	syncPost(o, func() { o.openControlRelay(d, 1) })
	syncPost(o, func() { o.feedControlRelay(d, 1, []byte(`{"id":"7","method":"ping"}`+"\n")) })

	var resp ctlproto.Response
	if err := json.Unmarshal(relayPayload(t, pd), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.ID != "7" {
		t.Fatalf("response = %+v, want an ok answer to id 7", resp)
	}
}

// An untrusted host is refused at the open. Nothing it sends afterwards is
// served, which is the point: the environment variable is a convenience and this
// is the boundary.
func TestControlRelayRefusesAnUntrustedHost(t *testing.T) {
	o, pd := controlHarness(t)
	trust(o, false)
	d := o.hosts[testRemoteHost]

	syncPost(o, func() { o.openControlRelay(d, 1) })
	// The refusal is a close, so the caller on the other machine gets a clean
	// EOF rather than waiting out a timeout.
	if mt := firstOf(pd, 200*time.Millisecond, orchestration.MsgControlClose, orchestration.MsgControlReply); mt != orchestration.MsgControlClose {
		t.Fatalf("refusal sent %q, want a control_close", mt)
	}
	// And a request on the refused id is served by nobody.
	syncPost(o, func() { o.feedControlRelay(d, 1, []byte(`{"id":"7","method":"ping"}`+"\n")) })
	if hasType(pd.collect(200*time.Millisecond), orchestration.MsgControlReply) {
		t.Fatal("an untrusted host's request was answered")
	}
}

// Turning the flag off takes effect on the next connection, without a redial —
// applyHostRoster refreshes a kept host's spec precisely so a policy change does
// not have to drop the terminals to express itself.
func TestControlRelayGateFollowsTheLiveConfig(t *testing.T) {
	o, pd := controlHarness(t)
	trust(o, true)
	d := o.hosts[testRemoteHost]

	syncPost(o, func() { o.openControlRelay(d, 1) })
	syncPost(o, func() { o.feedControlRelay(d, 1, []byte(`{"id":"a","method":"ping"}`+"\n")) })
	relayPayload(t, pd) // served

	trust(o, false)
	syncPost(o, func() { o.openControlRelay(d, 2) })
	syncPost(o, func() { o.feedControlRelay(d, 2, []byte(`{"id":"b","method":"ping"}`+"\n")) })
	if hasType(pd.collect(200*time.Millisecond), orchestration.MsgControlReply) {
		t.Fatal("a connection opened after the flag was cleared was still served")
	}
}

// The pane environment follows the same flag, so a pane on an untrusted host is
// told there is nothing to dial rather than being handed a path that would be
// refused.
func TestControlRelaySocketForPaneEnv(t *testing.T) {
	o, _, remotePane, _, pdRemote := twoHostOrch(t)
	d := o.hosts[testRemoteHost]
	d.setControlSocket("/tmp/cats-ctlrelay-1-1.sock")

	trust(o, false)
	if got := o.controlRelaySocket(d); got != "" {
		t.Fatalf("untrusted host offered %q, want nothing", got)
	}
	trust(o, true)
	if got := o.controlRelaySocket(d); got != "/tmp/cats-ctlrelay-1-1.sock" {
		t.Fatalf("trusted host offered %q, want the relay path", got)
	}

	// And it reaches the pane's environment.
	pdRemote.collect(50 * time.Millisecond) // discard the setup create_pane
	o.controlSocket = "/tmp/local-control.sock"
	o.createPane(o.panes[remotePane])
	var cp orchestration.CreatePane
	if err := json.Unmarshal(pdRemote.expect(t, orchestration.MsgCreatePane), &cp); err != nil {
		t.Fatalf("decode create_pane: %v", err)
	}
	if cp.Env[ctlproto.SocketEnvVar] != "/tmp/cats-ctlrelay-1-1.sock" {
		t.Fatalf("remote pane control socket = %q, want that host's relay", cp.Env[ctlproto.SocketEnvVar])
	}
	// Never this catway's own path: it names a file in a filesystem the pane
	// cannot see, and on a box that runs cats it names a different session's.
	if cp.Env[ctlproto.SocketEnvVar] == o.controlSocket {
		t.Fatal("a remote pane was handed this machine's control socket")
	}
}

// The local host is never relayed: its panes get this process's socket
// directly, so there is nothing in between to grant and nothing to refuse.
func TestControlRelayNeverAppliesToTheLocalHost(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)
	local := o.hosts[o.defaultHost]
	local.spec = config.Host{ID: localHostID, ControlRelay: true}
	if o.controlRelayAllowed(local) {
		t.Fatal("the local host must never be treated as a relay, flag or not")
	}
}

// A host whose connection drops strands every relayed caller on it; each one is
// waiting for an answer that can no longer arrive.
func TestControlRelayDropsWithTheHost(t *testing.T) {
	o, pd := controlHarness(t)
	trust(o, true)
	d := o.hosts[testRemoteHost]

	syncPost(o, func() { o.openControlRelay(d, 1) })
	syncPost(o, func() { o.dropHostRelays(testRemoteHost) })
	var live int
	syncPost(o, func() { live = len(o.ctlRelays) })
	if live != 0 {
		t.Fatalf("%d relayed connections survived the host going away", live)
	}
	_ = pd
}

// relayPayload waits for one control_reply and returns its payload.
func relayPayload(t *testing.T, pd *pipeDaemon) []byte {
	t.Helper()
	var ev orchestration.ControlReply
	if err := json.Unmarshal(pd.expect(t, orchestration.MsgControlReply), &ev); err != nil {
		t.Fatalf("decode control_reply: %v", err)
	}
	return []byte(strings.TrimSpace(string(ev.Payload)))
}

// firstOf returns whichever of want arrives first, or "" if none does.
func firstOf(pd *pipeDaemon, d time.Duration, want ...orchestration.MessageType) orchestration.MessageType {
	deadline := time.After(d)
	for {
		select {
		case m, ok := <-pd.msgs:
			if !ok {
				return ""
			}
			for _, w := range want {
				if m.mt == w {
					return w
				}
			}
		case <-deadline:
			return ""
		}
	}
}
