//go:build ghostty

package main

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/orchestration"
)

// The latency probe has three jobs and they fail in different ways: report a
// round trip, ignore an answer that does not belong to the outstanding probe,
// and notice a link that has stopped answering at all. The last one is the
// reason the probe exists at all — a TCP connection to a machine that slept
// stays perfectly writable, so nothing else in catway can tell.

// A capability the peer did not advertise is never used. This is the rule that
// keeps a catway from talking to an older cathost in a dialect it does not
// speak — which does not fail silently there, it answers with an "unknown
// message type" error event and toasts somebody's browser.
func TestSupportsRequiresBothConnectionAndAdvertisement(t *testing.T) {
	d := &daemon{id: "h", label: "h"}
	if d.supports(orchestration.FeaturePing) {
		t.Fatal("a daemon with no connection supports nothing")
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	d.setConn(client)
	if d.supports(orchestration.FeaturePing) {
		t.Fatal("a connected daemon that advertised nothing supports nothing")
	}
	d.setFeatures([]string{orchestration.FeaturePing})
	if !d.supports(orchestration.FeaturePing) {
		t.Fatal("an advertised feature on a live connection should be supported")
	}
	// A drop retires the advertisement with the connection that carried it: the
	// next cathost on this address may be an older build.
	d.setConn(nil)
	if d.supports(orchestration.FeaturePing) {
		t.Fatal("a disconnected daemon must forget what the last one could do")
	}
}

// A pong is only a measurement if it answers the probe that is outstanding.
func TestNotePongMatchesTheOutstandingProbe(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	d := &daemon{id: "h", label: "h", quit: make(chan struct{})}
	d.setConn(client)
	d.pingID, d.pingAt = 4, time.Now().Add(-8*time.Millisecond)

	if d.notePong(3) {
		t.Fatal("a pong for a different id must not be taken as a reading")
	}
	if d.latency != 0 {
		t.Fatalf("latency = %v after a mismatched pong, want unset", d.latency)
	}
	if !d.notePong(4) {
		t.Fatal("the first reading of a connection is always worth pushing")
	}
	if d.latency < 8*time.Millisecond {
		t.Fatalf("latency = %v, want at least the 8ms the probe was outstanding", d.latency)
	}
	// The probe is now answered; a duplicate (a retransmit, a confused daemon)
	// is not a second reading.
	if d.notePong(4) {
		t.Fatal("a repeat pong for an answered probe must be ignored")
	}
}

// The roster's figure is fractional and connection-scoped: a local unix socket
// measures well under a millisecond, and rounding that to zero would report
// every healthy session as unmeasured.
func TestLatencyMsIsFractionalAndDiesWithTheConnection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	d := &daemon{id: "h", label: "h"}
	if got := d.latencyMs(); got != 0 {
		t.Fatalf("unmeasured latency = %v, want 0 (unknown)", got)
	}
	d.setConn(client)
	d.latency = 123 * time.Microsecond
	if got := d.latencyMs(); got != 0.12 {
		t.Fatalf("latencyMs = %v, want 0.12", got)
	}
	d.latency = 41500 * time.Microsecond
	if got := d.latencyMs(); got != 41.5 {
		t.Fatalf("latencyMs = %v, want 41.5", got)
	}
	d.setConn(nil)
	if got := d.latencyMs(); got != 0 {
		t.Fatalf("latency after a drop = %v, want 0 — the reading belonged to that link", got)
	}
}

// Pushing the roster on every sample would redraw every browser's sidebar three
// times a minute per host to move a number by a tenth of a millisecond; pushing
// on none of them would leave a link that went bad looking fine.
func TestLatencyWorthPushing(t *testing.T) {
	ms := func(f float64) time.Duration { return time.Duration(f * float64(time.Millisecond)) }
	for _, tc := range []struct {
		name       string
		prev, next time.Duration
		want       bool
	}{
		{"first reading", 0, ms(0.05), true},
		{"first reading of nothing", 0, 0, false},
		{"local jitter", ms(0.05), ms(0.09), false},
		{"remote jitter", ms(40), ms(43), false},
		{"remote degraded", ms(40), ms(400), true},
		{"remote recovered", ms(400), ms(40), true},
		{"small absolute, large ratio", ms(0.2), ms(1.2), false},
		{"crossing into the visible", ms(1), ms(12), true},
	} {
		if got := latencyWorthPushing(tc.prev, tc.next); got != tc.want {
			t.Errorf("%s: latencyWorthPushing(%v, %v) = %v, want %v", tc.name, tc.prev, tc.next, got, tc.want)
		}
	}
}

// The full loop over a real connection: the probe writes a ping, the answer
// comes back through the pump's dispatch, and the roster carries the number.
func TestPingProbeMeasuresThroughDispatch(t *testing.T) {
	o, _, _, _, pdRemote := twoHostOrch(t)
	d := o.hosts[testRemoteHost]
	d.quit = make(chan struct{})
	d.setFeatures([]string{orchestration.FeaturePing})

	go d.pingProbe(d.conn)
	defer close(d.quit)

	payload := pdRemote.expect(t, orchestration.MsgPing)
	var p orchestration.Ping
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("decode ping: %v", err)
	}
	d.dispatch(orchestration.MsgPong, mustJSON(t, orchestration.NewPong(p.ID)))

	if got := d.latencyMs(); got <= 0 {
		t.Fatalf("latency after a pong = %v, want a positive reading", got)
	}
	// And it reaches the roster the Backend seam serves, which is what host.list
	// and the sidebar both read.
	for _, h := range o.Hosts() {
		if h.ID != testRemoteHost {
			continue
		}
		if h.LatencyMs <= 0 {
			t.Fatalf("roster latency for %s = %v, want a positive reading", h.ID, h.LatencyMs)
		}
		return
	}
	t.Fatalf("host %s missing from the roster", testRemoteHost)
}

// A link that stops answering is closed, which is the whole point: everything
// downstream — the pending flush, the toast, the redial — already handles a
// dropped connection, so the probe's job is only to turn a silent one into a
// dropped one.
func TestUnansweredPingClosesTheConnection(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	drain := make(chan struct{})
	go func() { // a peer that reads and never answers
		defer close(drain)
		for {
			if _, _, err := orchestration.ReadMessage(server); err != nil {
				return
			}
		}
	}()

	d := &daemon{id: "h", label: "h", quit: make(chan struct{})}
	d.setConn(client)
	if !d.sendPing(client) {
		t.Fatal("the first probe should go out")
	}
	// Backdate the outstanding probe past the tolerance rather than waiting a
	// minute for it: the clock is the input, not the subject.
	d.mu.Lock()
	d.pingAt = time.Now().Add(-hostPingTimeout - time.Second)
	d.mu.Unlock()

	if d.sendPing(client) {
		t.Fatal("an unanswered probe past the timeout must end the probe loop")
	}
	if _, err := client.Write([]byte("x")); err == nil {
		t.Fatal("the connection should have been closed")
	}
	<-drain
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
