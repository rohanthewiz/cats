//go:build ghostty

package main

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rohanthewiz/rweb"
)

// writeCounter counts writes reaching the socket.
type writeCounter struct {
	net.Conn
	writes atomic.Int32
}

func (w *writeCounter) Write(p []byte) (int, error) {
	w.writes.Add(1)
	return w.Conn.Write(p)
}

// Messages already queued when the writer wakes go out together: each its
// own WebSocket message, in order, in a single write to the socket — and the
// backlog accounting sees every byte of them leave.
func TestWriterBatchesWhatIsQueued(t *testing.T) {
	srv, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close(); _ = srv.Close() })
	counter := &writeCounter{Conn: srv}
	_, c := newTestClient(t, rweb.NewWSConn(counter, true))

	msgs := []string{`{"t":"a"}`, `{"t":"b"}`, `{"t":"c"}`}
	for _, m := range msgs {
		c.out <- []byte(m)
		c.queued.Add(int64(len(m)))
	}
	go c.writeLoop(time.Hour)

	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	for i, want := range msgs {
		op, payload := srvFrame(t, peer)
		if op != 0x1 || string(payload) != want {
			t.Fatalf("message %d: opcode %#x payload %q, want text %q", i, op, payload, want)
		}
	}
	if n := counter.writes.Load(); n != 1 {
		t.Fatalf("%d socket writes for %d queued messages, want 1", n, len(msgs))
	}
	// Counted down just after the write returns, so give the writer a moment.
	deadline := time.Now().Add(2 * time.Second)
	for c.queued.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d bytes still counted as queued after the batch was written", c.queued.Load())
		}
		time.Sleep(time.Millisecond)
	}
}
