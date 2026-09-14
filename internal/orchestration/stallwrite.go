package orchestration

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// DefaultWriteStallTimeout is how long either end of the β connection waits for
// its peer to accept bytes before treating the connection as dead.
//
// Why a write needs a bound at all: each end runs a reader that hands every
// message to a bounded queue, and a writer drained from a bounded queue. When
// both directions fill at once, each side's writer waits on the other side's
// reader, which is waiting on its own queue — a cycle with no timeout in it:
//
//	catway loop ──write──▶ cathost socket buffer FULL
//	     ▲                         │  cathost reader parked in emit (out FULL)
//	catway mailbox FULL            ▼
//	catway pump ◀──write── cathost writer (catway socket buffer FULL)
//
// Nothing crashes and nothing logs; the UI simply stops answering. A write that
// makes no progress for this long is therefore turned into a dropped
// connection, which both ends already recover from: cathost detaches with its
// panes preserved, catway redials and reconciles.
//
// The bound measures silence, not duration (see stallWriter), so it only has to
// exceed the longest a healthy peer can go without reading anything — which on
// a link that is actually up is far below this. It sits well under the ping
// watchdog's tolerance (catway's hostPingTimeout, 60s) so a jam is broken by
// the side doing the writing, without waiting on the probe.
const DefaultWriteStallTimeout = 20 * time.Second

// stallChunk caps how much one deadline has to cover. A Go write deadline is
// absolute for the whole Write call, so one multi-megabyte frame (a history
// seed on create_pane, a full capture) crossing a slow but healthy link could
// blow a whole-call deadline while bytes flow steadily. Re-arming per chunk
// turns "finish within T" into "make progress within T".
const stallChunk = 64 << 10

// writeDeadliner is the part of net.Conn the stall writer needs. net.Pipe,
// *net.UnixConn, *net.TCPConn and *tls.Conn all provide it.
type writeDeadliner interface {
	SetWriteDeadline(t time.Time) error
}

// NewStallWriter wraps w so that each write must be accepted by the peer, one
// chunk at a time, within timeout. A w that cannot carry a deadline (a
// bytes.Buffer in a test), or a non-positive timeout, returns w unchanged —
// the bound is a safety net, never a requirement for writing.
//
// A write that fails may already have put part of a frame on the wire, and a
// length-prefixed stream cannot be resynchronised after that. Callers must
// close the connection on any error from it, not retry.
func NewStallWriter(w io.Writer, timeout time.Duration) io.Writer {
	d, ok := w.(writeDeadliner)
	if !ok || timeout <= 0 {
		return w
	}
	return &stallWriter{w: w, d: d, timeout: timeout}
}

type stallWriter struct {
	w       io.Writer
	d       writeDeadliner
	timeout time.Duration
}

func (s *stallWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > stallChunk {
			chunk = chunk[:stallChunk]
		}
		if err := s.d.SetWriteDeadline(time.Now().Add(s.timeout)); err != nil {
			return written, err
		}
		n, err := s.w.Write(chunk)
		written += n
		if err != nil {
			// Named for what it means rather than left as "i/o timeout", which
			// in a log line reads as a network fault. %w keeps errors.Is working
			// for callers that branch on os.ErrDeadlineExceeded.
			if errors.Is(err, os.ErrDeadlineExceeded) {
				err = fmt.Errorf("peer stopped reading: no progress for %s: %w", s.timeout, err)
			}
			return written, err
		}
		p = p[n:]
	}
	// Cleared so a deadline armed here cannot fire on a later write that does not
	// come through this wrapper, and so an idle connection holds no armed timer.
	_ = s.d.SetWriteDeadline(time.Time{})
	return written, nil
}
