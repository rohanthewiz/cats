//go:build ghostty

package main

import (
	"net"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/orchestration"
)

// A cathost that stops reading costs catway the connection, not the loop. The
// orchestrator loop calls send; before the stall bound, a send to a peer that
// never read parked the loop for good, and with it every browser.
func TestDaemonSendDropsAPeerThatStopsReading(t *testing.T) {
	client, server := net.Pipe() // nothing ever reads server
	defer server.Close()

	d := &daemon{id: "h", label: "h", quit: make(chan struct{}), writeStall: 50 * time.Millisecond}
	d.setConn(client)

	sent := make(chan struct{})
	go func() {
		d.send(orchestration.NewPing(1))
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("send to a peer that never reads did not give up")
	}
	// Closed, because a stalled write can leave half a frame on the wire and the
	// pump's failed read is what starts the redial.
	if _, err := client.Write([]byte("x")); err == nil {
		t.Fatal("the connection should have been closed")
	}
}

// A stuck write must not blind the things whose job is to notice it. With one
// mutex for both state and writes, a send parked on a full socket held the
// lock that connected(), status() and the ping watchdog all need — so the
// watchdog that closes silent links sat queued behind the silence.
//
// The stall bound is set to an hour here on purpose: this test is about the
// lock, so the write must stay stuck until the watchdog, and only the
// watchdog, frees it.
func TestStuckWriteDoesNotBlindTheWatchdog(t *testing.T) {
	client, server := net.Pipe() // nothing ever reads server
	defer server.Close()

	d := &daemon{id: "h", label: "h", quit: make(chan struct{}), writeStall: time.Hour}
	d.setConn(client)

	sent := make(chan struct{})
	go func() {
		d.send(orchestration.NewPing(1))
		close(sent)
	}()
	// Wait until that send owns the writer.
	pollUntil(t, func() bool {
		if d.wmu.TryLock() {
			d.wmu.Unlock()
			return false
		}
		return true
	})

	// Readers of the daemon's state still get answers.
	answered := make(chan struct{})
	go func() {
		_ = d.connected()
		_, _ = d.status()
		_ = d.latencyMs()
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		t.Fatal("state readers queued behind a stuck write; the roster and the loop would freeze with it")
	}

	// A probe that finds the writer busy is skipped, not queued — and still
	// starts the silence clock, since not reaching the wire is itself silence.
	probed := make(chan bool, 1)
	go func() { probed <- d.sendPing(client) }()
	select {
	case ok := <-probed:
		if !ok {
			t.Fatal("a probe that could not get the writer should not end the probe loop")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the probe queued behind the stuck write")
	}
	d.mu.Lock()
	if d.pingSince.IsZero() {
		d.mu.Unlock()
		t.Fatal("a skipped probe did not start the silence clock; a jammed link would never age out")
	}
	d.pingSince = time.Now().Add(-hostPingTimeout - time.Second)
	d.mu.Unlock()

	// Past the tolerance the watchdog closes the link, which releases the write.
	if d.sendPing(client) {
		t.Fatal("a probe past the timeout must end the probe loop")
	}
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the connection did not release the stuck write")
	}
	if !d.takeStalled() {
		t.Fatal("the stall was not recorded; the roster would blame our own close")
	}
}

// pollUntil waits for cond, failing the test after a generous bound.
func pollUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never became true")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
