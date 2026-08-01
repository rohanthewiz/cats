//go:build ghostty

package main

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/rohanthewiz/rweb"
)

// The keepalive is the only thing standing between catway and a permanent
// zombie in o.conns when a peer vanishes without closing its socket. These
// tests drive a real rweb.WSConn over a net.Pipe — no HTTP, no orchestrator
// loop, no daemon — and read the raw frames off the far end, because what
// matters here is bytes on the wire, not anything the application protocol
// can observe.

// wsPair returns a server-side WSConn and the peer's end of the pipe. net.Pipe
// is unbuffered and synchronous, so a write from one side blocks until the
// other reads — which is exactly the property a stalled-socket test wants.
func wsPair(t *testing.T) (*rweb.WSConn, net.Conn) {
	t.Helper()
	srv, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close(); _ = srv.Close() })
	return rweb.NewWSConn(srv, true), peer
}

// newTestClient builds a client wired to a bare orch whose mailbox is buffered,
// so post() from the writer goroutine never blocks on a loop that isn't running.
func newTestClient(t *testing.T, ws *rweb.WSConn) (*orch, *client) {
	t.Helper()
	o := &orch{mailbox: make(chan func(), 8), conns: map[*client]struct{}{}}
	c := &client{o: o, ws: ws, out: make(chan []byte, 8), pong: make(chan []byte, 4)}
	o.conns[c] = struct{}{}
	return o, c
}

// srvFrame reads one unmasked server frame off the peer end, returning its
// opcode and payload. Deliberately minimal — the server never fragments and
// never masks, so anything else is a bug worth failing on.
func srvFrame(t *testing.T, r io.Reader) (opcode byte, payload []byte) {
	t.Helper()
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		t.Fatalf("read frame header: %v", err)
	}
	if hdr[0]&0x80 == 0 {
		t.Fatalf("server sent a fragmented frame: %#x", hdr[0])
	}
	if hdr[1]&0x80 != 0 {
		t.Fatal("server frames must not be masked")
	}
	n := int(hdr[1] & 0x7f)
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			t.Fatalf("read ext len: %v", err)
		}
		n = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			t.Fatalf("read ext len: %v", err)
		}
		n = int(binary.BigEndian.Uint64(ext[:]))
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	return hdr[0] & 0x0f, payload
}

// clientFrame writes one masked client control frame (clients must mask;
// RFC 6455 §5.3).
func clientFrame(t *testing.T, w io.Writer, opcode byte, payload []byte) {
	t.Helper()
	if len(payload) > 125 {
		t.Fatalf("control frames cap at 125 bytes, got %d", len(payload))
	}
	mask := []byte{0x0a, 0x0b, 0x0c, 0x0d}
	frame := []byte{0x80 | opcode, byte(0x80 | len(payload))}
	frame = append(frame, mask...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	if _, err := w.Write(frame); err != nil {
		t.Fatalf("write client frame: %v", err)
	}
}

// The writer must ping on its own schedule; without it nothing ever asks a
// silent peer to prove it is still there.
func TestWriterSendsKeepalivePings(t *testing.T) {
	ws, peer := wsPair(t)
	_, c := newTestClient(t, ws)
	go c.writeLoop(10 * time.Millisecond)

	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	for i := 0; i < 2; i++ {
		op, payload := srvFrame(t, peer)
		if op != 0x9 {
			t.Fatalf("frame %d: opcode = %#x, want ping (0x9)", i, op)
		}
		if len(payload) != 0 {
			t.Fatalf("frame %d: ping payload = %q, want empty", i, payload)
		}
	}
}

// A peer's ping must come back as a pong carrying the same payload, and it must
// be written by the writer goroutine rather than inline on the reader — that
// single-writer invariant is what makes the per-write deadline race-free.
func TestPeerPingIsAnsweredByWriter(t *testing.T) {
	ws, peer := wsPair(t)
	_, c := newTestClient(t, ws)
	c.installKeepalive()

	// The reader goroutine: ReadMessage dispatches control frames internally and
	// returns only for data frames, so it parks here for the test's duration.
	go func() { _, _ = ws.ReadMessage() }()

	_ = peer.SetWriteDeadline(time.Now().Add(2 * time.Second))
	clientFrame(t, peer, 0x9, []byte("are you there"))

	// Handed off, not written: with rweb's default handler the reply would go
	// out inline from the reader and c.pong would stay empty. Asserting the
	// queue before starting the writer is what separates the two designs — and
	// with nothing draining this pipe, an inline reply would simply deadlock.
	select {
	case queued := <-c.pong:
		if string(queued) != "are you there" {
			t.Fatalf("queued pong payload = %q, want the ping's payload", queued)
		}
		c.pong <- queued // put it back for the writer to send
	case <-time.After(2 * time.Second):
		t.Fatal("peer ping never reached the writer's pong queue")
	}

	// An hour-long ping interval keeps the expectation unambiguous: every frame
	// the peer sees is a consequence of the ping it sent.
	go c.writeLoop(time.Hour)

	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	op, payload := srvFrame(t, peer)
	if op != 0xa {
		t.Fatalf("opcode = %#x, want pong (0xa)", op)
	}
	if string(payload) != "are you there" {
		t.Fatalf("pong payload = %q, want the ping's payload echoed", payload)
	}
}

// A pong is the only traffic an idle peer produces, and rweb never surfaces it
// from ReadMessage — so if the pong handler failed to push the read deadline
// out, an idle-but-healthy browser would be reaped on schedule.
func TestPongRefreshesReadDeadline(t *testing.T) {
	ws, peer := wsPair(t)
	_, c := newTestClient(t, ws)
	c.installKeepalive()

	// Short enough that the read would fail almost immediately if the pong did
	// not move it; the handler replaces it with wsReadTimeout.
	_ = ws.SetReadDeadline(time.Now().Add(150 * time.Millisecond))

	read := make(chan error, 1)
	go func() {
		_, err := ws.ReadMessage()
		read <- err
	}()

	_ = peer.SetWriteDeadline(time.Now().Add(2 * time.Second))
	clientFrame(t, peer, 0xa, nil) // pong

	// Well past the original deadline: the read must still be in flight.
	select {
	case err := <-read:
		t.Fatalf("read ended after the pong refreshed the deadline: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
}

// The reaper's whole mechanism is that rweb's SetReadDeadline reaches the
// socket. Nothing in catway can observe that directly, so pin it here: if a
// dependency bump ever made the setter advisory, silent peers would accumulate
// again with no other test noticing.
func TestReadDeadlineReapsASilentPeer(t *testing.T) {
	ws, _ := wsPair(t)
	_ = ws.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

	read := make(chan error, 1)
	go func() {
		_, err := ws.ReadMessage()
		read <- err
	}()

	select {
	case err := <-read:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("read error = %v, want a deadline timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read never timed out; a dead peer would hold its slot forever")
	}
}

// A write that cannot complete must take the connection down rather than park
// the writer goroutine and strand the client's slot in o.conns. In production
// the trigger is wsWriteTimeout expiring against a half-open socket; here it is
// a closed peer, which produces the identical failure on the identical path
// without spending ten seconds of test time proving the deadline arithmetic.
func TestFailedWriteDropsConnection(t *testing.T) {
	ws, peer := wsPair(t)
	o, c := newTestClient(t, ws)
	_ = peer.Close()

	done := make(chan bool, 1)
	go func() { done <- c.write(rweb.TextMessage, []byte("nobody is reading this")) }()

	select {
	case ok := <-done:
		if ok {
			t.Fatal("write reported success against a closed peer")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write never gave up; the writer goroutine would leak")
	}

	// The failure path posts a dropConn onto the loop; run it as the loop would.
	select {
	case fn := <-o.mailbox:
		fn()
	default:
		t.Fatal("failed write posted nothing to the loop")
	}
	if _, still := o.conns[c]; still {
		t.Fatal("connection survived a failed write")
	}
}
