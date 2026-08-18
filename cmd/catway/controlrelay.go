//go:build ghostty

package main

import (
	"bytes"
	"io"
	"log"
	"sync"

	"github.com/rohanthewiz/cats/internal/orchestration"
)

// The control relay, orchestrator side: a connection that arrived on some
// cathost's socket, served by this catway's own control server.
//
// The whole design is the synthetic connection below. relayConn is an
// io.ReadWriteCloser whose reads are the bytes the remote client sent and whose
// writes go back across the seam, so ctlproto.Server.ServeConn drives it exactly
// as it drives a local socket. Nothing about the command table, the streaming
// method, the per-request backstops or the cancellation is reimplemented here —
// which matters because a second implementation would agree with the first until
// the day it didn't, and the thing it would be disagreeing about is who may run
// commands against every pane in the session.
//
// # Permission
//
// A daemon advertising a control socket is not permission to use one. The gate
// is config.Host.ControlRelay, checked HERE, on every open — not at the point
// where a pane's environment is filled in.
//
// That placement is the whole of the security story. The environment variable is
// a convenience: it tells in-pane tooling where to dial. Turning the flag off
// cannot unset it in panes that are already running, and the socket on the other
// machine goes on existing either way. So the environment cannot be the boundary
// and this must be: a host without the flag has its opens refused, whatever its
// panes were told earlier and whatever its cathost chooses to send.

// relayConn is one relayed control connection as an io.ReadWriteCloser.
//
// Reads block until the daemon forwards more bytes, which is what a control
// server expects of a socket: after a unary request there is nothing more to
// read and the server does not ask, while a subscription's server-side watcher
// blocks on a read that only ever returns when the client goes away. Both work
// out of the same buffer.
type relayConn struct {
	o    *orch
	d    *daemon
	id   uint64
	mu   sync.Mutex
	cond *sync.Cond
	buf  bytes.Buffer
	// closed is set by either end: the daemon reporting its client gone, or the
	// control server finishing. It makes pending and future reads return EOF and
	// writes fail.
	closed bool
	// sentClose guards the goodbye across the seam, so a close from the daemon
	// is not echoed back to it as one of ours.
	sentClose bool
}

func newRelayConn(o *orch, d *daemon, id uint64) *relayConn {
	rc := &relayConn{o: o, d: d, id: id}
	rc.cond = sync.NewCond(&rc.mu)
	return rc
}

// feed adds bytes from the relayed client and wakes a blocked read.
func (rc *relayConn) feed(b []byte) {
	rc.mu.Lock()
	if !rc.closed {
		rc.buf.Write(b)
	}
	rc.mu.Unlock()
	rc.cond.Broadcast()
}

func (rc *relayConn) Read(p []byte) (int, error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for rc.buf.Len() == 0 && !rc.closed {
		rc.cond.Wait()
	}
	if rc.buf.Len() > 0 {
		return rc.buf.Read(p)
	}
	return 0, io.EOF
}

// Write sends the control server's bytes back to the relayed client. Whole
// frames as the server produces them — it writes one newline-terminated message
// per call — but nothing here depends on that: the bytes are forwarded as they
// come and the client's own reader does the framing.
func (rc *relayConn) Write(p []byte) (int, error) {
	rc.mu.Lock()
	closed := rc.closed
	rc.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}
	// Copied because the caller owns p and the send is asynchronous; a shared
	// backing array would be the classic quiet corruption.
	rc.d.send(orchestration.NewControlReply(rc.id, bytes.Clone(p)))
	return len(p), nil
}

// Close ends the connection. Called by the control server when it is done with
// a request, and by the orchestrator when the daemon says its client left; only
// the first tells the other side, and only when we are the side finishing.
func (rc *relayConn) Close() error {
	rc.mu.Lock()
	if rc.closed {
		rc.mu.Unlock()
		return nil
	}
	rc.closed = true
	tell := !rc.sentClose
	rc.sentClose = true
	rc.mu.Unlock()
	rc.cond.Broadcast()
	if tell {
		rc.d.send(orchestration.NewControlClose(rc.id))
	}
	rc.o.post(func() { delete(rc.o.ctlRelays, relayKey{host: rc.d.id, id: rc.id}) })
	return nil
}

// closeFromPeer ends the connection because the daemon said its client went
// away. No goodbye travels back — the connection it would describe is already
// gone at the other end, and a subscription that ends this way ends because the
// caller hung up, which is not news to them.
func (rc *relayConn) closeFromPeer() {
	rc.mu.Lock()
	rc.sentClose = true
	rc.mu.Unlock()
	_ = rc.Close()
}

// relayKey identifies one relayed connection. The id is the daemon's, so it is
// only unique within a host — two cathosts will both open their first
// connection as 1.
type relayKey struct {
	host string
	id   uint64
}

// controlRelayAllowed reports whether this host may reach the control API.
//
// Read from the daemon's live config entry rather than a copy taken at connect
// time, so a `control_relay: false` written into the config and reloaded takes
// effect on the next open — applyHostRoster refreshes the spec of a host it
// keeps, precisely so a policy change does not need a redial.
//
// The local host is never relayed: its panes get this process's own socket
// directly, and there is nothing in between to grant.
func (o *orch) controlRelayAllowed(d *daemon) bool {
	return d != nil && d.id != localHostID && d.spec.ControlRelay
}

// openControlRelay starts serving one relayed connection. Loop goroutine.
func (o *orch) openControlRelay(d *daemon, id uint64) {
	if o.control == nil {
		// The control API is switched off on this catway, so there is nothing to
		// relay. One switch, not two: an operator who disabled the socket did
		// not mean "except from other machines".
		log.Printf("catway: refused a control-relay connection from host %s (the control API is disabled here)", d.id)
		d.send(orchestration.NewControlClose(id))
		return
	}
	if !o.controlRelayAllowed(d) {
		// Logged rather than silent, and logged as a refusal rather than a
		// failure: somebody on that machine tried to drive this session, and
		// whether that is an operator who forgot the flag or something worse,
		// the one thing that helps is a line saying it happened.
		log.Printf("catway: refused a control-relay connection from host %s (control_relay is off for it)", d.id)
		d.send(orchestration.NewControlClose(id))
		return
	}
	if o.ctlRelays == nil {
		o.ctlRelays = make(map[relayKey]*relayConn)
	}
	key := relayKey{host: d.id, id: id}
	if _, dup := o.ctlRelays[key]; dup {
		return // a repeated open for a live id; the daemon allocates them, so this is a bug there
	}
	rc := newRelayConn(o, d, id)
	o.ctlRelays[key] = rc
	// Off the loop: ServeConn blocks for the whole conversation, which for a
	// subscription is as long as the caller cares to listen.
	go o.control.ServeConn(rc)
}

// feedControlRelay hands the daemon's bytes to a live relayed connection.
func (o *orch) feedControlRelay(d *daemon, id uint64, payload []byte) {
	if rc := o.ctlRelays[relayKey{host: d.id, id: id}]; rc != nil {
		rc.feed(payload)
	}
}

// closeControlRelay ends a relayed connection at the daemon's request.
func (o *orch) closeControlRelay(d *daemon, id uint64) {
	if rc := o.ctlRelays[relayKey{host: d.id, id: id}]; rc != nil {
		rc.closeFromPeer()
	}
}

// dropHostRelays ends every relayed connection belonging to a host — its
// connection dropped, or it left the roster. Each one is a caller on the other
// machine waiting for an answer that can no longer come.
func (o *orch) dropHostRelays(hostID string) {
	for key, rc := range o.ctlRelays {
		if key.host == hostID {
			rc.closeFromPeer()
		}
	}
}

// controlRelaySocket is the path to hand a pane created on this host, "" when
// the host has no relay or is not trusted with one.
//
// The flag is checked here as well as at every open, but for a different
// reason: this one keeps a pane from being told about a socket that would only
// refuse it. The open-time check is the boundary; this is the courtesy.
func (o *orch) controlRelaySocket(d *daemon) string {
	if !o.controlRelayAllowed(d) {
		return ""
	}
	return d.controlSocket()
}
