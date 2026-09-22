package orchestration

import (
	"errors"
	"sync"
)

// paneInput is one pane's queue of bytes bound for its PTY master, drained by
// that pane's own writer goroutine (Host.inputPump). It is the input-side twin
// of Outbox, and it exists for the same reason: to keep a blocking write off
// every goroutine that must not stop.
//
// Before it, input was written with a blocking ptmx.Write straight from the
// session's dispatch goroutine — ONE goroutine shared by every pane — and the
// emulator's query-reply callback wrote from inside the parse, holding emuMu.
// A PTY write blocks as soon as the kernel's buffer fills, which is exactly
// what happens when the child stops reading stdin for a moment (busy painting,
// suspended, wedged). A free-spinning mouse wheel then fills that buffer in a
// fraction of a second, and from there:
//
//	dispatch ──ptmx.Write── pane A's pty buffer FULL (A is not reading)
//	    │
//	    └─▶ every later message for EVERY pane waits: keys, resizes, closes
//
//	readPump(A) ── emu.Write ── query reply ──ptmx.Write── blocked, holding emuMu
//	    │
//	    └─▶ the flusher's takeFrame(A) waits on emuMu ─▶ no frames for anyone
//
// One slow pane froze the whole workspace (2026-09-22, an MX Master
// free-spinning into ced at the end of a 4000-line file). With producers only
// ever appending here, a pane that stops reading stalls its own writer and
// nothing else.
//
// Backpressure policy — why some input may be dropped and the rest may not:
//
//   - Wheel reports and buttonless-motion reports are DROPPABLE (isDroppable).
//     They describe a continuous gesture, the child only cares about the most
//     recent ones, and a backlog of them is precisely the pathology: thousands
//     of queued notches become seconds of scrolling after the user has let go.
//     Once more than droppableBacklog bytes are waiting — which on a healthy
//     pane never happens, since the writer empties the queue as fast as the
//     child reads — a droppable chunk is discarded whole. Whole chunks only:
//     a chunk is one or more complete reports, so the byte stream is never cut
//     mid-sequence.
//   - Everything else (keys, pastes, button presses and releases, drags, and
//     the emulator's own query replies) is LOSSLESS. Dropping a keystroke or a
//     button release would put the child in a state the user never asked for.
//     It is bounded only by budget; a backlog that large means the child has
//     stopped reading for good, and the caller reports an error instead of
//     growing without limit.
//
// The writer coalesces too: each wake takes every queued chunk and hands the
// PTY one write, so a burst costs one syscall rather than one per report.
//
//	dispatch / emu callback ─push─▶ [chunk][chunk][chunk] ─take─▶ inputPump ─▶ ptmx
//	                                 droppable past droppableBacklog; lossless to budget
type paneInput struct {
	mu     sync.Mutex
	chunks [][]byte
	// queued counts bytes accepted and not yet written — the waiting chunks
	// plus the batch the writer is blocked on — so backpressure is measured
	// against the real backlog, not just the part still in the slice. A write
	// the child is not draining is exactly the case that must count.
	queued int
	budget int
	closed bool
	// dropped counts droppable chunks discarded under backpressure, for tests
	// and diagnostics.
	dropped int
	// wake holds at most one pending "chunks arrived" signal; depth 1 because
	// the writer takes everything per wake (Outbox's reasoning).
	wake chan struct{}
	done chan struct{}
}

// droppableBacklog is how much unwritten input a pane may hold before new
// wheel/motion reports are discarded. One SGR wheel report is ~10 bytes, so
// this keeps a few hundred notches in flight — far more than any child needs
// to catch up with a gesture, and far less than the seconds of replay a
// free-spinning wheel otherwise queues.
const droppableBacklog = 4 << 10

// defaultPaneInputBudget bounds lossless input. Two maximum-size input frames,
// so any single legal message (a large paste) always fits.
const defaultPaneInputBudget = 2 * MaxFrameSize

var (
	// errPaneInputClosed means the pane is gone; the input has nowhere to go.
	errPaneInputClosed = errors.New("pane input closed")
	// errPaneInputFull means the child has stopped reading and the lossless
	// backlog is past its budget.
	errPaneInputFull = errors.New("pane is not reading its input (backlog full)")
)

// newPaneInput returns an open queue. A budget ≤ 0 selects the default.
func newPaneInput(budget int) *paneInput {
	if budget <= 0 {
		budget = defaultPaneInputBudget
	}
	return &paneInput{
		budget: budget,
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

// push queues b without blocking. It copies b, because one of its callers (the
// emulator's write callback) hands over a buffer it will reuse. It reports
// dropped=true when b was droppable and discarded under backpressure — not an
// error: discarding it is the policy working.
func (q *paneInput) push(b []byte) (dropped bool, err error) {
	if len(b) == 0 {
		return false, nil
	}
	droppable := isDroppable(b)
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false, errPaneInputClosed
	}
	if droppable && q.queued >= droppableBacklog {
		q.dropped++
		q.mu.Unlock()
		return true, nil
	}
	if q.queued+len(b) > q.budget {
		q.mu.Unlock()
		return false, errPaneInputFull
	}
	q.chunks = append(q.chunks, append([]byte(nil), b...))
	q.queued += len(b)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default: // a wake is already pending; the writer will see this chunk too
	}
	return false, nil
}

// take waits for input and returns every queued chunk joined into one buffer,
// in order. It returns nil once the queue is closed. Writer goroutine only.
func (q *paneInput) take() []byte {
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return nil
		}
		if len(q.chunks) > 0 {
			var out []byte
			if len(q.chunks) == 1 {
				out = q.chunks[0]
			} else {
				n := 0
				for _, c := range q.chunks {
					n += len(c)
				}
				out = make([]byte, 0, n)
				for _, c := range q.chunks {
					out = append(out, c...)
				}
			}
			q.chunks = nil
			q.mu.Unlock()
			return out
		}
		q.mu.Unlock()
		select {
		case <-q.wake:
		case <-q.done:
		}
	}
}

// sent releases n written bytes from the backlog. Writer goroutine only, and
// only AFTER the write returns — the in-flight batch is backlog too.
func (q *paneInput) sent(n int) {
	q.mu.Lock()
	q.queued -= n
	if q.queued < 0 {
		q.queued = 0 // close() zeroes it under a writer still finishing a batch
	}
	q.mu.Unlock()
}

// close drops whatever is queued, refuses later pushes and releases a waiting
// writer. Idempotent: closePane can be reached from readPump, close_pane and
// shutdown.
func (q *paneInput) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	q.chunks = nil
	q.queued = 0
	close(q.done)
}

// droppedCount reports how many droppable chunks were discarded.
func (q *paneInput) droppedCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

// isDroppable reports whether b consists ONLY of SGR mouse reports that are
// wheel events or buttonless motion — the continuous-gesture reports it is
// safe to discard under backpressure.
//
// An SGR report is ESC [ < Cb ; Cx ; Cy (M|m). In Cb, bits 0–1 are the button,
// 4/8/16 are shift/alt/ctrl, 32 marks motion and 64 marks the wheel group
// (64–67: up, down, left, right). So:
//
//   - wheel:            Cb & 64 != 0 and Cb & 128 == 0 (128 is the extended
//     buttons 8–11, which are real buttons, not a wheel)
//   - buttonless motion: Cb & 32 != 0 and the button bits are 3 ("none")
//
// A release ('m') is never droppable, and neither is anything that is not SGR
// (the legacy X10 form, keys, alternate-scroll arrows, a paste): those are
// indistinguishable from input that must arrive. Conservative by design — a
// wrong "no" costs nothing, a wrong "yes" loses input.
func isDroppable(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for len(b) > 0 {
		cb, rest, ok := parseSGRMouse(b)
		if !ok {
			return false
		}
		wheel := cb&64 != 0 && cb&128 == 0
		motionNoButton := cb&32 != 0 && cb&64 == 0 && cb&128 == 0 && cb&3 == 3
		if !wheel && !motionNoButton {
			return false
		}
		b = rest
	}
	return true
}

// parseSGRMouse parses one SGR mouse PRESS/MOTION report ('M' final) off the
// front of b, returning its button code and the remaining bytes.
func parseSGRMouse(b []byte) (cb int, rest []byte, ok bool) {
	if len(b) < 3 || b[0] != 0x1b || b[1] != '[' || b[2] != '<' {
		return 0, nil, false
	}
	i := 3
	field := 0
	val, digits := 0, 0
	for ; i < len(b); i++ {
		c := b[i]
		switch {
		case c >= '0' && c <= '9':
			val = val*10 + int(c-'0')
			digits++
			if digits > 6 { // no legal field is this long; not a report
				return 0, nil, false
			}
		case c == ';':
			if digits == 0 || field >= 2 {
				return 0, nil, false
			}
			if field == 0 {
				cb = val
			}
			field++
			val, digits = 0, 0
		case c == 'M':
			if digits == 0 || field != 2 {
				return 0, nil, false
			}
			return cb, b[i+1:], true
		default: // 'm' (release) or anything else
			return 0, nil, false
		}
	}
	return 0, nil, false
}
