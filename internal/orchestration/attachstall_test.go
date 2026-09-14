//go:build ghostty

package orchestration

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// A client that stops reading must cost cathost the connection, never the
// session's reader. This is cathost's half of the 2026-09-13 freeze: the writer
// was parked on a catway that had stopped reading, out filled, and the reader
// parked in emit behind it. sessDone could not rescue anyone, because it only
// closes after the reader leaves its loop — so Attach never returned, and the
// serial accept loop could never let a restarted catway back in.
//
// The setup puts every party exactly where it was in the live process:
//
//	writer:  blocked writing to a client that never reads
//	out:     full
//	pumps:   300 emitters blocked on out
//	reader:  blocked in emit (dispatching a message that answers with an error)
func TestAttachDropsAClientThatStopsReading(t *testing.T) {
	h := NewHost()
	// Long enough that the reader reliably parks in emit before the writer gives
	// up (that parking is the subject), short enough to keep the test quick.
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

	// Before the sink exists, emit drops, and nothing would ever fill.
	var out chan any
	pollUntilTrue(t, func() bool {
		h.connMu.Lock()
		defer h.connMu.Unlock()
		out = h.out
		return out != nil
	})

	var emitters sync.WaitGroup
	for range 300 {
		emitters.Add(1)
		go func() {
			defer emitters.Done()
			h.emit(NewError(0, "filler"))
		}()
	}
	pollUntilTrue(t, func() bool { return len(out) == cap(out) })

	// Park the reader: input for a pane that does not exist is answered with an
	// error event, and that emit has nowhere to go. The write returns once the
	// reader has taken the whole frame off the pipe, i.e. once it is dispatching.
	if err := WriteMessage(clientEnd, map[string]any{"type": MsgInput, "pane_id": 99}); err != nil {
		t.Fatalf("send input: %v", err)
	}

	select {
	case <-attached:
	case <-time.After(10 * time.Second):
		t.Fatal("Attach never returned: the reader is still parked in emit behind a writer that gave up")
	}

	// Every pump that was blocked must be free again, or panes stop draining
	// their ptys and the agents inside them eventually stall on output.
	released := make(chan struct{})
	go func() {
		emitters.Wait()
		close(released)
	}()
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("emitters stayed blocked after the session ended")
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
