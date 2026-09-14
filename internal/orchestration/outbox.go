package orchestration

import (
	"errors"
	"sync"
)

// Outbox is one β connection's queue of encoded frames, drained by that
// connection's single writer goroutine. Both ends of the seam use it: catway's
// daemon.writePump for what the orchestrator sends cathost, and Host.Attach's
// writer for what cathost sends back.
//
// It exists to keep socket I/O off every goroutine that must not stop. On
// catway's side that is the orchestrator loop, which used to write to cathost
// itself; on cathost's side it is the session reader and every pane's pty pump,
// which used to block on a fixed-size channel in front of the writer. A write
// blocks for as long as the peer declines to read, and those two blocked sends
// were the two halves of the cycle behind the 2026-09-13 freeze:
//
//	catway loop blocked writing  →  mailbox fills  →  catway pump stops reading cathost
//	            ↑                                                  ↓
//	cathost stops reading  ←  cathost reader parked in emit  ←  cathost writer blocked
//
// With producers only ever appending here, neither side's reader can be parked
// behind its own writer, and the cycle has no link on either side. The writer
// may still stall on a peer that stops reading, but it stalls alone: it holds
// no lock and nobody waits on it, and the stall bound (NewStallWriter) or a
// watchdog closes the connection behind it.
//
// Why unbounded-with-a-budget rather than a fixed-size channel: a channel that
// fills blocks its sender, which would put the producer right back on the
// socket's schedule. Push never blocks; a backlog past the budget is instead
// treated as the dead connection it almost certainly is.
//
//	send ─encode on caller─▶ Push ─▶ [frame][frame][frame] ─Take─▶ writer ─▶ conn
//	                                   budget: queued bytes
//
// Lifecycle: open → (CloseWhenDrained → draining →) closed. Close drops what is
// queued (the connection is already gone); CloseWhenDrained lets the writer
// finish first (the connection is being given up on purpose, and the last
// frames — a detach's close_pane, a refused hello's welcome — still matter).
type Outbox struct {
	mu     sync.Mutex
	frames [][]byte
	// queued is the bytes accepted and not yet written — the frames waiting here
	// plus the batch the writer is working through — so the budget measures the
	// real backlog, not just the part still in the slice.
	queued int
	budget int
	// draining refuses new frames while the writer empties the queue; Take
	// closes the outbox once it finds nothing left.
	draining bool
	closed   bool
	// wake holds at most one pending "frames arrived" signal. Buffered so Push
	// never blocks, and depth 1 because the writer takes every queued frame per
	// wake, so a second signal would add nothing.
	wake chan struct{}
	// done is closed when the outbox closes, so a writer waiting for work exits.
	done chan struct{}
}

// DefaultOutboxBudget is how far behind a connection may fall before its owner
// gives up on it: eight maximum-size frames, so any single legal message always
// fits. Real backlogs are far smaller — a whole session's history seeds come to
// hundreds of kilobytes — and a local socket drains faster than any producer
// can fill it, so reaching this means the peer has effectively stopped.
const DefaultOutboxBudget = 8 * MaxFrameSize

var (
	// ErrOutboxClosed means the frame was dropped because the connection is gone
	// or going — the same outcome as any send while disconnected.
	ErrOutboxClosed = errors.New("outbox closed")
	// ErrOutboxFull means the backlog is past the budget; the caller should drop
	// the connection.
	ErrOutboxFull = errors.New("outbox over budget")
)

// NewOutbox returns an open outbox. A budget ≤ 0 selects DefaultOutboxBudget.
func NewOutbox(budget int) *Outbox {
	if budget <= 0 {
		budget = DefaultOutboxBudget
	}
	return &Outbox{
		budget: budget,
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

// Push queues one frame without blocking. See ErrOutboxClosed and ErrOutboxFull
// for the two refusals.
func (b *Outbox) Push(frame []byte) error {
	b.mu.Lock()
	if b.closed || b.draining {
		b.mu.Unlock()
		return ErrOutboxClosed
	}
	if b.queued+len(frame) > b.budget {
		b.mu.Unlock()
		return ErrOutboxFull
	}
	b.frames = append(b.frames, frame)
	b.queued += len(frame)
	b.mu.Unlock()
	b.signal()
	return nil
}

// signal leaves a wake pending for the writer, without blocking.
func (b *Outbox) signal() {
	select {
	case b.wake <- struct{}{}:
	default: // a wake is already pending; the writer will see this state too
	}
}

// Take waits until frames are queued and returns all of them, in order. It
// returns nil once the outbox is closed, or once a draining outbox has handed
// over its last frame. Writer goroutine only.
func (b *Outbox) Take() [][]byte {
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

// Sent releases n written bytes from the budget. Writer goroutine only.
func (b *Outbox) Sent(n int) {
	b.mu.Lock()
	b.queued -= n
	b.mu.Unlock()
}

// Queued is the current backlog in bytes: accepted and not yet reported
// written. A producer whose output can be coalesced (cathost's frame flusher)
// reads it to hold back instead of queueing work the peer is not keeping up
// with.
func (b *Outbox) Queued() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.queued
}

// Budget is the backlog, in bytes, past which Push refuses.
func (b *Outbox) Budget() int { return b.budget }

// Close ends the outbox now: queued frames are dropped, later pushes are
// refused, and a waiting writer exits. Idempotent, since the writer (on a failed
// write), a replaced connection, the budget check and a shutdown can all get
// there first.
func (b *Outbox) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeLocked()
}

func (b *Outbox) closeLocked() {
	if b.closed {
		return
	}
	b.closed = true
	b.frames = nil
	b.queued = 0
	close(b.done)
}

// CloseWhenDrained refuses further frames but lets the writer put the ones
// already queued on the wire before the outbox closes. It does not wait: its
// callers (catway's loop, cathost's session reader) must never wait on a
// socket. The flush is still bounded, because the writer's own stall bound
// fails a write to a peer that has stopped reading.
func (b *Outbox) CloseWhenDrained() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.draining = true
	b.mu.Unlock()
	b.signal() // an idle writer must wake to notice there is nothing left
}

// IsClosed reports whether the outbox has closed.
func (b *Outbox) IsClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}
