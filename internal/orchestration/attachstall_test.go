//go:build ghostty

package orchestration

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// A client that stops reading must cost cathost the connection, never the
// session's reader or a pane's pty pump. This is cathost's half of the
// 2026-09-13 freeze: the writer was parked on a catway that had stopped
// reading, the 256-slot channel in front of it filled, and the reader and the
// pty pumps parked in emit behind it — so Attach never returned, the serial
// accept loop could never let a restarted catway back in, and an agent whose
// pane pump was parked blocked on a full pty.
//
// The setup puts every party where it was in the live process:
//
//	writer:  blocked writing to a client that never reads
//	pumps:   300 emitters, each emitting far more than the old channel held
//	reader:  dispatching a message that answers with an error
//
// Now none of them may block at all, and Attach must still end once the
// writer's stall bound gives up.
func TestAttachDropsAClientThatStopsReading(t *testing.T) {
	h := NewHost()
	h.WriteStallTimeout = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	h.Start(ctx)
	defer func() {
		cancel()
		h.Stop()
	}()

	serverEnd, clientEnd := net.Pipe() // the test never reads clientEnd
	defer clientEnd.Close()
	attached := make(chan error, 1)
	go func() { attached <- h.Attach(ctx, serverEnd) }()

	// Before the sink exists, emit drops, and nothing would ever queue.
	pollUntilTrue(t, h.attached)

	var emitters sync.WaitGroup
	emitted := make(chan struct{})
	for range 300 {
		emitters.Add(1)
		go func() {
			defer emitters.Done()
			for range 10 {
				h.emit(NewError(0, "filler"))
			}
		}()
	}
	go func() {
		emitters.Wait()
		close(emitted)
	}()
	select {
	case <-emitted:
	case <-time.After(5 * time.Second):
		t.Fatal("emitters blocked behind a client that stopped reading — pty pumps would stop draining")
	}

	// The reader answers input for a pane that does not exist with an error
	// event. It used to park in that emit. The write returns once the reader has
	// taken the whole frame off the pipe, i.e. once it is dispatching.
	if err := WriteMessage(clientEnd, map[string]any{"type": MsgInput, "pane_id": 99}); err != nil {
		t.Fatalf("send input: %v", err)
	}

	select {
	case <-attached:
	case <-time.After(10 * time.Second):
		t.Fatal("Attach never returned after the writer's stall bound")
	}
	if h.attached() {
		t.Fatal("the dropped client is still attached")
	}
}

// A client that falls past the budget is dropped at once — without waiting out
// the stall bound — and the drop ends the session, so a reconnect can take its
// place.
func TestAttachDropsAClientPastTheBudget(t *testing.T) {
	h := NewHost()
	h.WriteStallTimeout = time.Hour // prove the budget, not the stall, ends it
	h.OutboxBudget = 64 << 10
	ctx, cancel := context.WithCancel(context.Background())
	h.Start(ctx)
	defer func() {
		cancel()
		h.Stop()
	}()

	serverEnd, clientEnd := net.Pipe()
	defer clientEnd.Close()
	attached := make(chan error, 1)
	go func() { attached <- h.Attach(ctx, serverEnd) }()
	pollUntilTrue(t, h.attached)

	// ~100 bytes each: 2000 of them is three times the budget.
	for range 2000 {
		h.emit(NewError(0, "filler filler filler filler filler filler filler filler"))
	}
	select {
	case <-attached:
	case <-time.After(5 * time.Second):
		t.Fatal("a client past the budget was not dropped")
	}
}

// Frames are held back while the client is behind, and only while it is: the
// flusher reads the backlog and skips a tick instead of queueing stale screens.
func TestClientBackloggedTracksTheQueue(t *testing.T) {
	h := NewHost()
	h.WriteStallTimeout = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	h.Start(ctx)
	defer func() {
		cancel()
		h.Stop()
	}()
	if h.clientBacklogged() {
		t.Fatal("backlogged with no client attached")
	}

	serverEnd, clientEnd := net.Pipe()
	defer clientEnd.Close()
	go func() { _ = h.Attach(ctx, serverEnd) }()
	pollUntilTrue(t, h.attached)

	big := make([]byte, 256<<10)
	for i := range big {
		big[i] = 'x'
	}
	for range 6 { // 1.5 MiB, past flushBackpressure
		h.emit(NewPaneOutput(1, big))
	}
	if !h.clientBacklogged() {
		t.Fatal("not backlogged with 1.5 MiB queued")
	}

	// Drain the pipe: the backlog clears, and frames flow again.
	go drainFrames(clientEnd)
	pollUntilTrue(t, func() bool { return !h.clientBacklogged() })
}

// drainFrames reads and discards framed messages until the pipe closes.
func drainFrames(r io.Reader) {
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		if _, err := io.CopyN(io.Discard, r, int64(binary.LittleEndian.Uint32(hdr[:]))); err != nil {
			return
		}
	}
}

// pollUntilTrue waits for cond, failing the test after a generous bound.
func pollUntilTrue(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never became true")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
