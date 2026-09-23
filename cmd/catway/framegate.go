//go:build ghostty

package main

import (
	"slices"

	"github.com/rohanthewiz/cats/internal/orchestration"
)

// The frame gate, client side (see orchestration.FeatureFrameGate): each host is
// told which of its panes some window is showing, and stops producing frames
// for the rest.
//
// The list is the viewport union (o.visible), split by host. It is recomputed
// wherever the union is — refreshViewport — and sent only when a host's share
// of it changed, so a focus move inside one tab costs nothing here.
//
// Why it is safe to stop the frames: a pane only ever re-enters a window through
// refreshViewport, whose caller then asks the pane's host for a full frame
// (resyncViews, or the per-pane resync a new connection gets). That request
// goes out on the same connection AFTER the new list, so by the time the full
// frame is taken the host is already streaming the pane again. The frames that
// were never taken would only have been dropped on arrival anyway.

// syncFrameGates recomputes each host's share of the viewport union and sends
// the ones that changed. Also invalidates the grid of every pane that just left
// the union: from here on its frames stop (or, from an older host, are dropped
// unapplied), so whatever the grid holds is not the base the host's next diff
// will be taken against. The full frame the pane gets when it returns is what
// makes the grid whole again (browserproto.Grid).
//
// prev is the union before this refresh.
func (o *orch) syncFrameGates(prev map[uint32]bool) {
	for pid := range prev {
		if !o.visible[pid] {
			if rt := o.panes[pid]; rt != nil {
				rt.grid.Invalidate()
			}
		}
	}
	byHost := make(map[string][]uint32, len(o.hosts))
	for pid := range o.visible {
		id := o.paneHostID(pid)
		byHost[id] = append(byHost[id], pid)
	}
	for id, d := range o.hosts {
		d.setFramePanes(byHost[id])
	}
}

// setFramePanes records this host's share of the viewport and sends it if it
// changed. Remembered while the host is down, like the stats interval and the
// ledger switch: the reconnect is what applies it (sendFramePanes).
func (d *daemon) setFramePanes(panes []uint32) {
	panes = slices.Clone(panes)
	slices.Sort(panes)
	d.mu.Lock()
	changed := !d.framePanesKnown || !slices.Equal(d.framePanes, panes)
	d.framePanes, d.framePanesKnown = panes, true
	d.mu.Unlock()
	if changed {
		d.sendFramePanes()
	}
}

// sendFramePanes tells the connected host which panes to take frames for. A
// no-op on a host that cannot gate — it would take the unknown message as an
// error — and before the orchestrator has worked out a viewport at all, since
// an empty list is a real answer ("show nothing") that would starve the first
// window of frames.
func (d *daemon) sendFramePanes() {
	if !d.supports(orchestration.FeatureFrameGate) {
		return
	}
	d.mu.Lock()
	panes, known := slices.Clone(d.framePanes), d.framePanesKnown
	d.mu.Unlock()
	if !known {
		return
	}
	d.send(orchestration.NewSetFramePanes(panes))
}
