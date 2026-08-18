//go:build ghostty

package orchestration

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// The hook relay: a socket on the DAEMON's machine that carries agent hook
// reports back to the orchestrator.
//
// The problem it solves is a wrong answer rather than a missing one. Every pane
// is spawned with CATS_SOCKET_PATH so the hooks `catctl integration install`
// plants can report an agent's state. For a pane on this machine that path is
// the orchestrator's own socket and everything works. For a pane on ANOTHER
// machine the orchestrator used to inject its own path anyway — a path in a
// filesystem the pane cannot see. At best the hooks failed silently; at worst
// the remote box was itself running cats on the conventional /tmp path, and a
// remote agent's reports landed on a different server's panes.
//
// So the daemon opens its own socket, and the orchestrator injects THAT into
// the panes it creates here. What arrives is forwarded verbatim and answered by
// the orchestrator: the daemon parses nothing and decides nothing, because the
// pane the report is about belongs to the orchestrator's model and the hook API
// is the orchestrator's to define. Relaying bytes also keeps the daemon out of
// the way of the next field added to that API.
//
// The socket's path is stable for the daemon's LIFETIME, not the connection's.
// Panes outlive a reconnect in persistent mode and their environment cannot be
// rewritten afterwards, so a per-connection path would leave every surviving
// pane's hooks pointing at a socket that no longer exists.

const (
	// hookRelayReadTimeout bounds reading the one request line off a hook
	// connection, and hookRelayMaxRequest its size. Both match the
	// orchestrator's own limits on the same protocol — this is the same socket
	// wearing a different address, so a request it would refuse must not get
	// further by arriving here.
	hookRelayReadTimeout  = 5 * time.Second
	hookRelayMaxRequest   = 1 << 20
	hookRelayReplyTimeout = 10 * time.Second
)

// hookRelaySeq disambiguates relay sockets within one process. A non-persistent
// cathost serves a Host per connection, so a path built from the pid alone
// would have them fighting over one file.
var hookRelaySeq atomic.Uint64

// relayFields is embedded in Host.
type relayFields struct {
	// HookSocketPath overrides where the relay listens. Left empty, Start picks
	// a per-daemon path; set to "-" the relay is disabled entirely, which is
	// what a daemon whose panes must not report anywhere wants.
	HookSocketPath string

	relayMu   sync.Mutex
	relaySock string                 // the live path, "" when there is no relay
	relayLn   net.Listener           //
	relayWait map[uint64]chan []byte // report id → where its reply is awaited
	relaySeq  uint64                 // report ids, relay-scoped
}

// hookSocketPath is the live relay socket's path, "" when there is none. It is
// what the welcome advertises.
func (h *Host) hookSocketPath() string {
	h.relayMu.Lock()
	defer h.relayMu.Unlock()
	return h.relaySock
}

// defaultHookRelayPath builds this daemon's relay socket path.
//
// /tmp rather than os.TempDir(): it is where every other cats socket lives by
// convention, and on macOS TMPDIR is a long per-user path that eats into the
// ~104-byte limit a unix socket address has — a limit this project has already
// been bitten by.
func defaultHookRelayPath() string {
	return filepath.Join("/tmp", fmt.Sprintf("cats-hookrelay-%d-%d.sock", os.Getpid(), hookRelaySeq.Add(1)))
}

// startHookRelay opens the relay socket. Failure is logged and otherwise
// ignored: a daemon that cannot open it still serves terminals perfectly well,
// and the orchestrator simply finds no path in the welcome and injects none.
func (h *Host) startHookRelay() {
	if h.HookSocketPath == "-" {
		return
	}
	path := h.HookSocketPath
	if path == "" {
		path = defaultHookRelayPath()
	}
	_ = os.Remove(path) // a path this process just derived; nothing else owns it
	ln, err := net.Listen("unix", path)
	if err != nil {
		log.Printf("cathost: hook relay unavailable (%v) — remote panes will report no agent state", err)
		return
	}
	// Owner-only, like the orchestrator's own hook socket: the hooks run as this
	// user and the path IS the capability. Anything that can open it can move an
	// agent's state on the orchestrator's model.
	if err := os.Chmod(path, 0o600); err != nil {
		log.Printf("cathost: hook relay chmod: %v", err)
	}
	h.relayMu.Lock()
	h.relaySock, h.relayLn = path, ln
	h.relayWait = make(map[uint64]chan []byte)
	h.relayMu.Unlock()
	go h.acceptHooks(ln)
	log.Printf("cathost: hook relay listening on %s", path)
}

// stopHookRelay closes the socket and fails everything waiting on it.
func (h *Host) stopHookRelay() {
	h.relayMu.Lock()
	ln, path := h.relayLn, h.relaySock
	waiters := h.relayWait
	h.relayLn, h.relaySock, h.relayWait = nil, "", nil
	h.relayMu.Unlock()
	if ln != nil {
		_ = ln.Close()
		_ = os.Remove(path)
	}
	for _, ch := range waiters {
		close(ch) // an unblocked waiter writes nothing and closes its connection
	}
}

func (h *Host) acceptHooks(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // closed on shutdown
		}
		go h.relayHookConn(conn)
	}
}

// relayHookConn carries one hook request across the seam and the reply back.
//
// The read limits are enforced here rather than left to the orchestrator
// because this end owns the socket: a request that would be refused there must
// not get as far as occupying a frame on the seam.
func (h *Host) relayHookConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(hookRelayReadTimeout))
	line, err := readLimitedLine(bufio.NewReaderSize(conn, 4096), hookRelayMaxRequest)
	if len(line) == 0 && err != nil {
		return
	}

	// No client attached means nobody can answer. Closing without a reply is the
	// honest response — the hooks ignore replies, and the CLI equivalents get a
	// clean EOF rather than a fabricated "ok" for a report that went nowhere.
	if !h.attached() {
		return
	}

	id, ch, ok := h.awaitHookReply()
	if !ok {
		return // relay stopped between accept and now
	}
	defer h.dropHookWaiter(id)
	h.emit(NewHookReport(id, line))

	select {
	case reply, open := <-ch:
		if !open || len(reply) == 0 {
			return
		}
		_ = conn.SetDeadline(time.Now().Add(hookRelayReadTimeout))
		_, _ = conn.Write(reply)
	case <-time.After(hookRelayReplyTimeout):
		// The orchestrator is wedged or the connection died mid-flight. The hook
		// has already done its work by reporting; blocking its process for
		// longer than this to hear "ok" would be the worse failure.
	case <-h.closed:
	}
}

// awaitHookReply registers a waiter and returns its report id.
func (h *Host) awaitHookReply() (uint64, chan []byte, bool) {
	h.relayMu.Lock()
	defer h.relayMu.Unlock()
	if h.relayWait == nil {
		return 0, nil, false
	}
	h.relaySeq++
	id := h.relaySeq
	ch := make(chan []byte, 1)
	h.relayWait[id] = ch
	return id, ch, true
}

func (h *Host) dropHookWaiter(id uint64) {
	h.relayMu.Lock()
	if h.relayWait != nil {
		delete(h.relayWait, id)
	}
	h.relayMu.Unlock()
}

// deliverHookReply hands a reply to the connection waiting for it. A reply for
// an id nobody is waiting on is dropped: the hook client has already given up
// and gone, and there is nothing left to write to.
func (h *Host) deliverHookReply(c HookReply) {
	h.relayMu.Lock()
	ch := h.relayWait[c.ID]
	h.relayMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- c.Payload:
	default: // already answered; the buffer of one is the whole protocol
	}
}

// attached reports whether a client is currently connected. Used only to fail a
// hook fast rather than have it wait out the reply timeout for an answer that
// cannot come.
func (h *Host) attached() bool {
	h.connMu.Lock()
	defer h.connMu.Unlock()
	return h.out != nil
}

// readLimitedLine reads one newline-terminated request, bounded by max. A
// request without a trailing newline before EOF still decodes, which is what
// the orchestrator's own reader allows.
func readLimitedLine(br *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > max {
			return nil, fmt.Errorf("request too large")
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return buf, err
	}
}
