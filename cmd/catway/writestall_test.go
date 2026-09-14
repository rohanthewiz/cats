//go:build ghostty

package main

import (
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/orchestration"
)

// A cathost that stops reading costs catway the connection, and costs the
// caller of send nothing at all. Before, the orchestrator loop wrote to the
// socket itself, so a peer that never read parked the loop for good, and with it
// every browser.
func TestDaemonSendDropsAPeerThatStopsReading(t *testing.T) {
	client, server := net.Pipe() // nothing ever reads server
	defer server.Close()

	d := &daemon{id: "h", label: "h", quit: make(chan struct{}), writeStall: 50 * time.Millisecond}
	d.setConn(client)
	box := d.outbox

	start := time.Now()
	d.send(orchestration.NewPing(1))
	if took := time.Since(start); took > time.Second {
		t.Fatalf("send took %v; it must never wait on the socket", took)
	}
	// The writer gives up on the stall bound and closes the connection, because a
	// stalled write can leave half a frame on the wire and the pump's failed read
	// is what starts the redial.
	pollUntil(t, func() bool {
		_, err := client.Write([]byte("x"))
		return err != nil && box.IsClosed()
	})
}

// A stuck writer must not blind the things whose job is to notice it. Before,
// a send parked on a full socket held the lock that connected(), status() and
// the ping watchdog all need — so the watchdog that closes silent links sat
// queued behind the silence.
//
// The stall bound is an hour here on purpose: this test is about who waits on
// the writer, so it must stay stuck until the watchdog, and only the watchdog,
// frees it.
func TestStuckWriterLeavesTheLoopAndWatchdogFree(t *testing.T) {
	client, server := net.Pipe() // nothing ever reads server
	defer server.Close()

	d := &daemon{id: "h", label: "h", quit: make(chan struct{}), writeStall: time.Hour}
	d.setConn(client)
	box := d.outbox

	// The first frame parks the writer; everything after it just queues.
	sent := make(chan struct{})
	go func() {
		for i := range 1000 {
			d.send(orchestration.NewPing(uint64(i)))
		}
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("sends waited on a writer that is stuck; the loop would freeze with it")
	}

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
		t.Fatal("state readers queued behind a stuck write; the roster would freeze with it")
	}

	probed := make(chan bool, 1)
	go func() { probed <- d.sendPing(client) }()
	select {
	case ok := <-probed:
		if !ok {
			t.Fatal("a probe queued behind a busy writer should not end the probe loop")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the probe waited on the stuck writer")
	}
	d.mu.Lock()
	if d.pingSince.IsZero() {
		d.mu.Unlock()
		t.Fatal("the probe did not start the silence clock; a jammed link would never age out")
	}
	d.pingSince = time.Now().Add(-hostPingTimeout - time.Second)
	d.mu.Unlock()

	// Past the tolerance the watchdog closes the link, which frees the writer.
	if d.sendPing(client) {
		t.Fatal("a probe past the timeout must end the probe loop")
	}
	pollUntil(t, box.IsClosed)
	if !d.takeStalled() {
		t.Fatal("the stall was not recorded; the roster would blame our own close")
	}
}

// A writer that is making progress, just not enough, is caught by the budget
// rather than left to grow catway's memory without bound.
func TestBackloggedConnectionIsDropped(t *testing.T) {
	client, server := net.Pipe() // nothing ever reads server
	defer server.Close()

	d := &daemon{id: "h", label: "h", quit: make(chan struct{}), writeStall: time.Hour, writeBudget: 4096}
	d.setConn(client)
	box := d.outbox

	for i := range 1000 {
		d.send(orchestration.NewPing(uint64(i)))
	}
	pollUntil(t, box.IsClosed)
	if _, err := client.Write([]byte("x")); err == nil {
		t.Fatal("a connection past the budget should have been closed")
	}
}

// Moving the writes onto a goroutine must not reorder them: cathost applies
// create, resize and input in the order catway decided them.
func TestSendsArriveInOrder(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	d := &daemon{id: "h", label: "h", quit: make(chan struct{})}
	d.setConn(client)
	const n = 500
	for i := 1; i <= n; i++ {
		d.send(orchestration.NewPing(uint64(i)))
	}
	for want := uint64(1); want <= n; want++ {
		_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
		mt, payload, err := orchestration.ReadMessage(server)
		if err != nil {
			t.Fatalf("read message %d: %v", want, err)
		}
		var p orchestration.Ping
		if mt != orchestration.MsgPing || json.Unmarshal(payload, &p) != nil || p.ID != want {
			t.Fatalf("message %d = %s %s, want ping %d", want, mt, payload, want)
		}
	}
}

// A detach queues close_pane for the departing host's panes and calls stop on
// the next line. stop must let those reach the host before the connection
// closes, or the detached machine keeps shells running that nobody can see.
func TestStopFlushesWhatWasQueued(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	d := &daemon{id: "h", label: "h", quit: make(chan struct{})}
	d.setConn(client)
	for i := range 50 {
		d.send(orchestration.NewClosePane(uint32(i)))
	}
	d.stop()

	got := 0
	for {
		_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
		mt, _, err := orchestration.ReadMessage(server)
		if err != nil {
			break // the writer closes the connection once the queue is empty
		}
		if mt == orchestration.MsgClosePane {
			got++
		}
	}
	if got != 50 {
		t.Fatalf("host received %d of 50 close_pane before the connection closed", got)
	}
}

// End to end, the freeze itself: a cathost that stops reading must not take the
// orchestrator loop with it. The loop is flooded with sends to that host and
// must still run the next closure posted to it.
func TestLoopOutlivesACathostThatStopsReading(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	client, server := net.Pipe() // this cathost never reads
	defer server.Close()
	d := o.hosts[o.defaultHost]
	d.quit = make(chan struct{})
	d.setConn(client)
	go o.run()

	big := map[string]any{"type": orchestration.MsgInput, "pane_id": 1, "data": strings.Repeat("x", 64<<10)}
	for range 200 {
		o.post(func() { d.send(big) })
	}
	alive := make(chan struct{})
	o.post(func() { close(alive) })
	select {
	case <-alive:
	case <-time.After(5 * time.Second):
		t.Fatal("the orchestrator loop stopped running behind a cathost that stopped reading")
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
