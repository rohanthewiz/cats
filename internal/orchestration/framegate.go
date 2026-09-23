//go:build ghostty

package orchestration

import (
	"sync"
	"time"
)

// The frame gate (FeatureFrameGate): frames only for the panes a client is
// showing.
//
// A pane's frames are the most expensive thing cathost produces — a snapshot of
// the whole grid through cgo, a diff against the last one, an encode, a write —
// and the flusher produced them for every dirty pane every tick, whether or not
// anybody would ever draw them. catway dropped the ones for off-screen panes on
// arrival. With the gate, catway names the panes some window is showing, and a
// pane outside that set is never snapshotted by the flusher at all.
//
//	          ┌─ in the gate ──▶ snapshot ─▶ diff ─▶ pane_frame      (every tick)
//	dirty ────┤
//	          └─ outside ──────▶ modes check ──────▶ pane_activity   (≤ 1 / 2 s)
//
// What keeps that correct is the client's side of the bargain, which it already
// kept before the gate existed: a pane entering a viewport is sent a
// request_resync, which re-baselines the pane's FrameBuilder and replays a full
// frame. The frames skipped while it was hidden are never needed, because
// nothing is ever diffed against them.
//
// Input modes are still read for a gated pane that produced output: they
// decide how input is encoded (bracketed paste, the kitty protocol), and input
// does reach panes nobody is looking at — catctl send, a plugin, a runbook.

// gatedActivityInterval floors the gap between two pane_activity reports for
// one pane. The client's consumer is a history sweep measured in minutes, so
// this only has to be far below that, and far above the flush tick.
const gatedActivityInterval = 2 * time.Second

// frameGateFields is the Host's frame-gate state, embedded into Host.
type frameGateFields struct {
	gateMu sync.Mutex
	// gate is the set of panes the client wants frames for, or nil when the
	// client never set one (stream everything). Replaced whole by
	// setFramePanes, never mutated in place, so the flusher can use the map
	// it read under the lock after letting go of it.
	gate map[uint32]bool
}

// setFramePanes installs the client's list, replacing any previous one.
func (h *Host) setFramePanes(c SetFramePanes) {
	gate := make(map[uint32]bool, len(c.Panes))
	for _, id := range c.Panes {
		gate[id] = true
	}
	h.gateMu.Lock()
	h.gate = gate
	h.gateMu.Unlock()
}

// clearFrameGate drops the gate when the session that set it ends. The next
// client may be an older build that never sends one and expects every frame.
func (h *Host) clearFrameGate() {
	h.gateMu.Lock()
	h.gate = nil
	h.gateMu.Unlock()
}

// frameGate returns the current gate (nil: no gate). The map is read-only.
func (h *Host) frameGate() map[uint32]bool {
	h.gateMu.Lock()
	defer h.gateMu.Unlock()
	return h.gate
}

// flushGated is the flusher's work for a pane outside the gate: consume its
// dirty flag, keep its input modes current, and report that it has output —
// at most once per gatedActivityInterval, but never lost: output that lands
// inside the interval is reported when the interval ends.
//
// Called only from the flusher goroutine, which owns activityDue/activityAt.
func (h *Host) flushGated(p *pane, now time.Time) {
	if p.dirty.Swap(false) {
		p.activityDue = true
		h.emitModeChanges(p)
	}
	h.reportActivity(p, now)
}

// reportActivity emits the pane_activity owed for output that produced no
// frame — a gated pane's, or a pane whose output left the screen unchanged —
// at most once per gatedActivityInterval, and late rather than never. Flusher
// goroutine only, like the fields it reads.
func (h *Host) reportActivity(p *pane, now time.Time) {
	if p.activityDue && now.Sub(p.activityAt) >= gatedActivityInterval {
		p.activityDue = false
		p.activityAt = now
		h.emit(NewPaneActivity(p.id))
	}
}
