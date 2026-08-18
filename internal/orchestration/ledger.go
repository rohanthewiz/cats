//go:build ghostty

package orchestration

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/rohanthewiz/cats/internal/terminal"
)

// The command ledger's daemon half: watch each pane's output for shell
// integration marks (osc133.go) and report the commands they describe.
//
// This is a subscription, and off by default, for a reason worth stating
// plainly: scanning is a fifth state machine over every byte of every pane, and
// for a shell with no integration installed it produces nothing at all. A
// session that keeps no ledger should not pay for it, and neither should the
// panes of a client that never asked.
//
// The subscription belongs to the CONNECTION, like the host-stats one — a
// persistent cathost that kept scanning for an orchestrator that has gone would
// be doing bookkeeping for nobody.

// maxBlocksPerPane bounds the marks a pane keeps. Every block holds two
// terminal-owned pins, so this is a real resource rather than a map entry —
// and the questions blocks answer ("jump to that build", "copy that output")
// are about recent work, so the oldest are the ones to let go.
const maxBlocksPerPane = 64

// block is one recorded command's extent, held as two marks the terminal moves
// as its scrollback shifts. See terminal.Mark for why this is not two row
// numbers.
type block struct {
	id    uint64
	start terminal.Mark
	end   terminal.Mark
}

// close releases both pins. Idempotent, because a pane teardown and a ring
// eviction can both reach the same block.
func (b *block) close() {
	if b.start != nil {
		b.start.Close()
		b.start = nil
	}
	if b.end != nil {
		b.end.Close()
		b.end = nil
	}
}

// blockFields is a pane's block ring. Marks are created on the readPump
// goroutine and resolved on the dispatch goroutine, so the ring has a mutex of
// its own — emuMu guards the emulator, not this.
type blockFields struct {
	blockMu  sync.Mutex
	blocks   []*block
	blockSeq uint64
}

// ledgerFields is the Host's command-ledger state, embedded into Host.
type ledgerFields struct {
	// cmdMarks is the live switch. Read from every pane's readPump goroutine and
	// written from the dispatch goroutine, so it is atomic rather than mutexed:
	// it is checked once per read, and a flip that lands one chunk late is
	// indistinguishable from one that landed one chunk early.
	cmdMarks atomic.Bool
}

// requestCommandMarks applies a MsgRequestCommandMarks.
//
// Turning it OFF does not reset the per-pane trackers, deliberately. A tracker
// holds at most one open command, and a client that switches the ledger off
// mid-command has no use for its end anyway; the next prompt clears it. Resetting
// them would mean reaching into every pane's readPump-owned state from this
// goroutine, which is the one thing the scanner ownership rule forbids.
func (h *Host) requestCommandMarks(c RequestCommandMarks) { h.cmdMarks.Store(c.On) }

// commandMarksOn reports whether this connection asked for shell marks.
func (h *Host) commandMarksOn() bool { return h.cmdMarks.Load() }

// stopCommandMarks drops the subscription when its connection ends.
func (h *Host) stopCommandMarks() { h.cmdMarks.Store(false) }

// scanCommandMarks feeds one chunk of a pane's output to its shell-integration
// scanner and emits whatever commands complete. Called from that pane's readPump
// goroutine, which owns both the scanner and the tracker.
//
// The cwd is captured HERE, at the moment the command starts, rather than left
// for the client to join against its own cwd stream: by the time a ledger row is
// written the pane may have cd'd twice, and "where did I run this" is one of the
// two questions a ledger is asked.
func (h *Host) scanCommandMarks(p *pane, chunk []byte) {
	marks := p.osc133.scan(chunk)
	if len(marks) == 0 {
		h.feed(p, chunk)
		return
	}
	// Feed up to each mark before pinning the cursor there. This interleaving is
	// the whole reason the scanner reports offsets: fed as one chunk, every mark
	// in it would pin the cursor's FINAL position, and a command's output range
	// would come out empty — which is exactly what the first version did.
	fed := 0
	for _, m := range marks {
		if m.Offset > fed {
			h.feed(p, chunk[fed:m.Offset])
			fed = m.Offset
		}
		rec, ok := p.cmds.mark(m, time.Now())
		if !ok {
			continue
		}
		if rec.Start {
			h.emit(NewCommandStart(p.id, rec.Cmd, p.cwdMeta(), h.openBlock(p)))
			continue
		}
		h.emit(NewCommandEnd(p.id, rec.Exit, rec.Duration, h.closeBlock(p)))
	}
	if fed < len(chunk) {
		h.feed(p, chunk[fed:])
	}
}

// openBlock pins the cursor as a new block's start and returns its id, or 0
// when the pin could not be taken. Zero is a real answer: the command is
// recorded without an addressable block rather than with a wrong one.
func (h *Host) openBlock(p *pane) uint64 {
	mark := h.markCursor(p)
	if mark == nil {
		return 0
	}
	p.blockMu.Lock()
	defer p.blockMu.Unlock()
	p.blockSeq++
	b := &block{id: p.blockSeq, start: mark}
	p.blocks = append(p.blocks, b)
	for len(p.blocks) > maxBlocksPerPane {
		p.blocks[0].close()
		p.blocks = p.blocks[1:]
	}
	return b.id
}

// closeBlock pins the cursor as the newest block's end and returns its id.
//
// It closes the NEWEST open block rather than looking one up, because a pane
// runs one foreground command at a time — the same fact that makes cmdTracker a
// single value rather than a queue.
func (h *Host) closeBlock(p *pane) uint64 {
	mark := h.markCursor(p)
	p.blockMu.Lock()
	defer p.blockMu.Unlock()
	if len(p.blocks) == 0 {
		if mark != nil {
			mark.Close()
		}
		return 0
	}
	b := p.blocks[len(p.blocks)-1]
	if b.end != nil {
		// Already closed — an end with no start of its own. Drop the new pin
		// rather than overwriting, which would stretch a finished block over
		// somebody else's output.
		if mark != nil {
			mark.Close()
		}
		return 0
	}
	b.end = mark
	return b.id
}

// markCursor pins the pane's cursor, under emuMu like every other emulator
// touch. Returns nil on any failure — a block that cannot be pinned is simply
// not offered.
func (h *Host) markCursor(p *pane) terminal.Mark {
	p.emuMu.Lock()
	defer p.emuMu.Unlock()
	if p.closed {
		return nil
	}
	mark, err := p.emu.MarkCursor()
	if err != nil {
		return nil
	}
	return mark
}

// resolveBlock answers a RequestBlock: where the block is now, and its text.
//
// Both marks must still have a value. A block with only its start left is one
// whose output is half out of the buffer, and half of a command's output
// presented as all of it is exactly the quiet wrongness marks exist to prevent.
func (h *Host) resolveBlock(c RequestBlock) {
	p := h.getPane(c.PaneID)
	if p == nil {
		h.emit(NewBlockResult(c.ID, false, 0, 0, 0, ""))
		return
	}
	p.blockMu.Lock()
	var b *block
	for _, cand := range p.blocks {
		if cand.id == c.Block {
			b = cand
			break
		}
	}
	p.blockMu.Unlock()
	if b == nil || b.start == nil || b.end == nil {
		h.emit(NewBlockResult(c.ID, false, 0, 0, 0, ""))
		return
	}

	p.emuMu.Lock()
	defer p.emuMu.Unlock()
	if p.closed {
		h.emit(NewBlockResult(c.ID, false, 0, 0, 0, ""))
		return
	}
	startRow, startCol, okStart := b.start.Point()
	endRow, endCol, okEnd := b.end.Point()
	if !okStart || !okEnd {
		h.emit(NewBlockResult(c.ID, false, 0, 0, 0, ""))
		return
	}
	// The viewport's current top row. MaxOffsetFromBottom is where the top sits
	// when the pane is scrolled all the way down; OffsetFromBottom is how far up
	// from there it currently is.
	var topRow uint32
	if m, err := p.emu.ScrollMetrics(); err == nil {
		topRow = uint32(max(0, m.MaxOffsetFromBottom-m.OffsetFromBottom))
	}
	// The end mark is taken at OSC 133;D, which a shell emits from its prompt
	// hook — by which time the cursor has already moved to the line AFTER the
	// command's output. Left as-is the block would end at column 0 of that line
	// and pick up the first character of the next prompt, which is exactly what
	// the first live run showed: every block's text ended in a stray "b" from
	// "bash-5.2$". So a mark at column 0 ends the block at the end of the row
	// above it, which is where the output actually ended.
	if endCol == 0 && endRow > startRow {
		endRow--
		if cols := p.cols.Load(); cols > 0 {
			endCol = uint16(cols - 1)
		}
	}
	var text string
	if c.Text {
		text, _ = p.emu.FormatSelection(
			terminal.SelectionEndpoint{Row: startRow, Col: startCol},
			terminal.SelectionEndpoint{Row: endRow, Col: endCol}, false)
	}
	h.emit(NewBlockResult(c.ID, true, startRow, endRow, topRow, text))
}

// closeBlocks releases a pane's pins. Called from the pane teardown, since the
// terminal's own Close does not free them.
func (p *pane) closeBlocks() {
	p.blockMu.Lock()
	defer p.blockMu.Unlock()
	for _, b := range p.blocks {
		b.close()
	}
	p.blocks = nil
}
