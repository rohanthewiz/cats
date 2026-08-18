//go:build ghostty

package orchestration

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// The control relay: a socket on the DAEMON's machine carrying the
// orchestrator's control API, so in-pane tooling there — catctl, cats-todo, a
// plugin binary — can drive the session it is part of.
//
// It is the hook relay's problem one level up. A pane is spawned with
// CATS_CONTROL_SOCKET so anything inside it can find the session; for a pane on
// another machine that path named a file in a filesystem it could not see, and
// on a box that runs cats itself, a DIFFERENT session's socket. The hook relay
// answered that for agent state; this answers it for the API proper.
//
// Two things make it more than a second copy of the hook relay:
//
//   - it relays a CONNECTION, not a message pair. The control protocol has a
//     streaming half (events.subscribe) where one request is followed by an ack
//     and then events for as long as the caller stays connected, so "one request,
//     one reply, close" is the wrong shape.
//   - the orchestrator decides whether to listen. This daemon opens the socket
//     and says so; whether anything arriving on it is honoured is the
//     orchestrator's call, per host, and its default is no. Opening a socket is
//     not permission — see config.Host.ControlRelay for what granting it means.
//
// The daemon parses none of the traffic. It moves bytes in both directions and
// closes when either end is done.

const (
	// controlRelayMaxLine bounds one line from a relayed client. Generous next
	// to the hook relay's, because a control request legitimately carries a
	// payload — a pane.send_input of a pasted buffer — where a hook report
	// carries a status. The seam's own frame cap is the real ceiling above this.
	controlRelayMaxLine = 4 << 20
	// controlRelayIdleRead is how long a connection may sit with nothing to say
	// before its read is checked again. It is not a timeout on the conversation:
	// a subscription is silent by design and must survive indefinitely, so the
	// deadline is re-armed each time rather than being a deadline on the whole
	// connection.
	controlRelayIdleRead = 30 * time.Second
)

var controlRelaySeq atomic.Uint64

// controlFields is embedded in Host.
type controlFields struct {
	// ControlSocketPath overrides where the control relay listens. Empty picks a
	// per-daemon path; "-" disables the relay, which is how a machine that must
	// never be able to drive the session says so from its own side.
	ControlSocketPath string

	ctlMu    sync.Mutex
	ctlSock  string
	ctlLn    net.Listener
	ctlConns map[uint64]*relayedControl
	ctlSeq   uint64
}

// relayedControl is one live connection on the relay socket.
type relayedControl struct {
	conn     net.Conn
	closeOne sync.Once
}

// controlSocketPath is the live relay socket's path, "" when there is none.
func (h *Host) controlSocketPath() string {
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	return h.ctlSock
}

// defaultControlRelayPath builds this daemon's control relay socket path, on the
// same reasoning as defaultHookRelayPath (/tmp for the length limit, pid plus a
// counter for uniqueness within a process that serves a Host per connection).
func defaultControlRelayPath() string {
	return filepath.Join("/tmp", fmt.Sprintf("cats-ctlrelay-%d-%d.sock", os.Getpid(), controlRelaySeq.Add(1)))
}

// startControlRelay opens the relay socket. Failure is logged and otherwise
// ignored: a daemon that cannot open it still serves terminals, and the
// orchestrator simply finds no path in the welcome.
func (h *Host) startControlRelay() {
	if h.ControlSocketPath == "-" {
		return
	}
	path := h.ControlSocketPath
	if path == "" {
		path = defaultControlRelayPath()
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		log.Printf("cathost: control relay unavailable (%v) — in-pane catctl here will have nothing to dial", err)
		return
	}
	// Owner-only. This is the same boundary the orchestrator's own control
	// socket keeps, and it has to be: anything that can open this can run every
	// command the session has, on every host it holds.
	if err := os.Chmod(path, 0o600); err != nil {
		log.Printf("cathost: control relay chmod: %v", err)
	}
	h.ctlMu.Lock()
	h.ctlSock, h.ctlLn = path, ln
	h.ctlConns = make(map[uint64]*relayedControl)
	h.ctlMu.Unlock()
	go h.acceptControl(ln)
	log.Printf("cathost: control relay listening on %s", path)
}

// stopControlRelay closes the socket and every connection on it.
func (h *Host) stopControlRelay() {
	h.ctlMu.Lock()
	ln, path := h.ctlLn, h.ctlSock
	conns := h.ctlConns
	h.ctlLn, h.ctlSock, h.ctlConns = nil, "", nil
	h.ctlMu.Unlock()
	if ln != nil {
		_ = ln.Close()
		_ = os.Remove(path)
	}
	for _, rc := range conns {
		rc.close()
	}
}

func (h *Host) acceptControl(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // closed on shutdown
		}
		go h.relayControlConn(conn)
	}
}

// relayControlConn carries one control connection across the seam: announce it,
// forward everything the client says, and close when either end is finished.
//
// The replies travel the other way (deliverControlReply), written by whichever
// goroutine the orchestrator's answer arrives on. That asymmetry is fine — one
// writer per direction — and it is what lets a subscription's events reach the
// client as they happen rather than being polled for.
func (h *Host) relayControlConn(conn net.Conn) {
	if !h.attached() {
		// Nobody to relay to. Closing without a word is the honest answer: a
		// caller gets a clean EOF instead of waiting out a timeout for a session
		// that is not there.
		_ = conn.Close()
		return
	}
	id, rc, ok := h.registerControl(conn)
	if !ok {
		_ = conn.Close()
		return
	}
	defer func() {
		h.dropControl(id)
		rc.close()
		h.emit(NewControlClose(id))
	}()

	h.emit(NewControlOpen(id))

	br := bufio.NewReaderSize(conn, 4096)
	for {
		// Re-armed per line rather than set once: a subscription sends its one
		// request and then stays silent for as long as the caller cares to
		// listen, so a deadline on the conversation would end exactly the case
		// this relay exists to carry.
		_ = conn.SetReadDeadline(time.Now().Add(controlRelayIdleRead))
		line, err := readLimitedLine(br, controlRelayMaxLine)
		if len(line) > 0 {
			h.emit(NewControlData(id, line))
		}
		if err != nil {
			if isTimeout(err) && h.controlLive(id) {
				continue // idle, not gone
			}
			return
		}
	}
}

// registerControl allocates an id for a new relayed connection.
func (h *Host) registerControl(conn net.Conn) (uint64, *relayedControl, bool) {
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	if h.ctlConns == nil {
		return 0, nil, false
	}
	h.ctlSeq++
	id := h.ctlSeq
	rc := &relayedControl{conn: conn}
	h.ctlConns[id] = rc
	return id, rc, true
}

func (h *Host) dropControl(id uint64) {
	h.ctlMu.Lock()
	if h.ctlConns != nil {
		delete(h.ctlConns, id)
	}
	h.ctlMu.Unlock()
}

// controlLive reports whether a relayed connection is still registered.
func (h *Host) controlLive(id uint64) bool {
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	_, ok := h.ctlConns[id]
	return ok
}

// deliverControlReply writes the orchestrator's bytes to the relayed client.
// Bytes for a connection that has gone are dropped: the caller left, and there
// is nothing to write to.
func (h *Host) deliverControlReply(c ControlReply) {
	h.ctlMu.Lock()
	rc := h.ctlConns[c.ID]
	h.ctlMu.Unlock()
	if rc == nil {
		return
	}
	_ = rc.conn.SetWriteDeadline(time.Now().Add(controlRelayIdleRead))
	if _, err := rc.conn.Write(c.Payload); err != nil {
		rc.close() // unblocks the reader, which tears the relay down
	}
}

// closeControlConn ends a relayed connection because the orchestrator's side
// finished — a unary response has been written, or a subscription was dropped.
func (h *Host) closeControlConn(c ControlClose) {
	h.ctlMu.Lock()
	rc := h.ctlConns[c.ID]
	h.ctlMu.Unlock()
	if rc != nil {
		rc.close()
	}
}

func (rc *relayedControl) close() {
	rc.closeOne.Do(func() { _ = rc.conn.Close() })
}

// isTimeout reports whether err is a deadline expiry rather than a real
// failure. The relay re-arms its read deadline instead of treating one as the
// end of the connection, so this is the difference between an idle subscription
// and a caller that has gone.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
