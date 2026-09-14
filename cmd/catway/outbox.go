//go:build ghostty

package main

import (
	"errors"
	"sync"

	"github.com/rohanthewiz/cats/internal/orchestration"
)

// outbox is one cathost connection's queue of encoded frames, drained by that
// connection's single writer goroutine (daemon.writePump).
//
// It is what keeps socket I/O off the orchestrator loop. The loop used to write
// to cathost itself, and a write blocks for as long as the peer declines to
// read. That was one link of the cycle behind the 2026-09-13 freeze:
//
//	loop blocked writing  →  mailbox fills  →  pump stops reading cathost
//	        ↑                                              ↓
//	cathost stops reading  ←  cathost reader parked in emit  ←  cathost writer blocked
//
// With the loop only ever appending here, the loop always drains the mailbox,
// the pump always reads, and the cycle has no link on this side. The writer
// may still stall on a peer that stops reading, but it stalls alone: it holds
// no lock and nobody waits on it, and the stall bound (NewStallWriter) or the
// ping watchdog closes the connection behind it.
//
// Why unbounded-with-a-budget rather than a fixed-size channel: a channel that
// fills blocks its sender, which would put the loop right back on the socket's
// schedule. push never blocks; a backlog past the budget is instead treated as
// the dead connection it almost certainly is.
//
//	send ─encode on caller─▶ push ─▶ [frame][frame][frame] ─take─▶ writePump ─▶ conn
//	                                   budget: queued bytes
//
// Lifecycle: open → (closeWhenDrained → draining →) closed. close drops what is
// queued (the connection is already gone); closeWhenDrained lets the writer
// finish first (the connection is being given up on purpose, and the last
// frames — a detach's close_pane — still matter).
type outbox struct {
	mu     sync.Mutex
	frames [][]byte
	// queued is the bytes accepted and not yet written — the frames waiting here
	// plus the batch the writer is working through — so the budget measures the
	// real backlog, not just the part still in the slice.
	queued int
	budget int
	// draining refuses new frames while the writer empties the queue; take
	// closes the outbox once it finds nothing left.
	draining bool
	closed   bool
	// wake holds at most one pending "frames arrived" signal. Buffered so push
	// never blocks, and depth 1 because the writer takes every queued frame per
	// wake, so a second signal would add nothing.
	wake chan struct{}
	// done is closed when the outbox closes, so a writer waiting for work exits.
	done chan struct{}
}

// defaultOutboxBudget is how far behind a connection may fall before catway
// gives up on it: eight maximum-size frames, so any single legal message always
// fits. Real backlogs are far smaller — a whole session's history seeds come to
// hundreds of kilobytes — and a local socket drains faster than the loop can
// fill it, so reaching this means the peer has effectively stopped.
const defaultOutboxBudget = 8 * orchestration.MaxFrameSize

var (
	errOutboxClosed = errors.New("outbox closed")
	errOutboxFull   = errors.New("outbox over budget")
)

func newOutbox(budget int) *outbox {
	if budget <= 0 {
		budget = defaultOutboxBudget
	}
	return &outbox{
		budget: budget,
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

// push queues one frame without blocking. errOutboxClosed means the connection
// is gone or going and the frame is dropped, the same as any send while
// disconnected; errOutboxFull means the backlog is past the budget, and the
// caller should drop the connection.
func (b *outbox) push(frame []byte) error {
	b.mu.Lock()
	if b.closed || b.draining {
		b.mu.Unlock()
		return errOutboxClosed
	}
	if b.queued+len(frame) > b.budget {
		b.mu.Unlock()
		return errOutboxFull
	}
	b.frames = append(b.frames, frame)
	b.queued += len(frame)
	b.mu.Unlock()
	b.signal()
	return nil
}

// signal leaves a wake pending for the writer, without blocking.
func (b *outbox) signal() {
	select {
	case b.wake <- struct{}{}:
	default: // a wake is already pending; the writer will see this state too
	}
}

// take waits until frames are queued and returns all of them, in order. It
// returns nil once the outbox is closed, or once a draining outbox has handed
// over its last frame. Writer goroutine only.
func (b *outbox) take() [][]byte {
	for {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return nil
		}
		if len(b.frames) > 0 {
			batch := b.frames
			b.frames = nil
			b.mu.Unlock()
			return batch
		}
		if b.draining {
			b.closeLocked()
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()
		select {
		case <-b.wake:
		case <-b.done:
		}
	}
}

// sent releases n written bytes from the budget. Writer goroutine only.
func (b *outbox) sent(n int) {
	b.mu.Lock()
	b.queued -= n
	b.mu.Unlock()
}

// close ends the outbox now: queued frames are dropped, later pushes are
// refused, and a waiting writer exits. Idempotent, since the writer (on a failed
// write), setConn (on a replaced connection), the budget check and stop can all
// get there first.
func (b *outbox) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeLocked()
}

func (b *outbox) closeLocked() {
	if b.closed {
		return
	}
	b.closed = true
	b.frames = nil
	b.queued = 0
	close(b.done)
}

// closeWhenDrained refuses further frames but lets the writer put the ones
// already queued on the wire before the outbox closes. It does not wait: it is
// called from the orchestrator loop, which must never wait on a socket. The
// flush is still bounded, because the writer's own stall bound fails a write to
// a peer that has stopped reading.
func (b *outbox) closeWhenDrained() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.draining = true
	b.mu.Unlock()
	b.signal() // an idle writer must wake to notice there is nothing left
}

// isClosed reports whether the outbox has closed.
func (b *outbox) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}
