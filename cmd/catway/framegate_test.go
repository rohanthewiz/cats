//go:build ghostty

package main

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// These tests own the orchestrator loop (mcOrch starts no run goroutine): they
// call loop-side code directly, and waitFor pumps whatever the daemon dispatch
// posts.

// nextGate pops β messages until a set_frame_panes arrives and returns its list.
func nextGate(t *testing.T, pd *pipeDaemon) []uint32 {
	t.Helper()
	var g orchestration.SetFramePanes
	if err := json.Unmarshal(pd.expect(t, orchestration.MsgSetFramePanes), &g); err != nil {
		t.Fatalf("decode set_frame_panes: %v", err)
	}
	return g.Panes
}

func sorted(ids ...uint32) []uint32 {
	slices.Sort(ids)
	return ids
}

// The host is told exactly the panes on screen, whenever that changes; and a
// pane coming back into view is streamed again BEFORE its full frame is asked
// for — the order is what guarantees the resync frame is taken at all.
func TestFrameGateFollowsTheViewport(t *testing.T) {
	o, focused, other := mcOrch(t)
	c := newConn(o, false, browserproto.Init{Cols: 120, Rows: 40})
	o.flushClients()
	drain(c)
	pd := newPipeDaemon(t, o)
	d := o.hosts[o.defaultHost]
	d.setFeatures([]string{orchestration.FeatureFrameGate})

	// What the reconnect sends: the list as it stands.
	d.sendFramePanes()
	if got := nextGate(t, pd); !slices.Equal(got, sorted(focused, other)) {
		t.Fatalf("gate on connect = %v, want both panes %v", got, sorted(focused, other))
	}

	// Give the pane that is about to be hidden a grid, so its invalidation shows.
	dispatchFrame(t, o, other, fullFrame("a", "b"))

	// Zoom hides the other pane.
	if _, err := o.session.ToggleZoom(nil); err != nil {
		t.Fatalf("zoom: %v", err)
	}
	o.applyModel()
	if got := nextGate(t, pd); !slices.Equal(got, []uint32{focused}) {
		t.Fatalf("gate while zoomed = %v, want just %d", got, focused)
	}
	if _, applied := o.panes[other].grid.Apply(sparseDiff(2, 0, "z")); applied {
		t.Fatal("the hidden pane's grid still takes sparse diffs — its host stopped sending the frames it would need")
	}

	// Unzoom: the other pane is back, and the host must hear so before the
	// resync that asks it for the pane's full frame.
	if _, err := o.session.ToggleZoom(nil); err != nil {
		t.Fatalf("unzoom: %v", err)
	}
	o.applyModel()
	sawGate := false
	deadline := time.After(2 * time.Second)
	for {
		var m pipeMsg
		select {
		case m = <-pd.msgs:
		case <-deadline:
			t.Fatal("no request_resync for the returning pane")
		}
		if m.mt == orchestration.MsgSetFramePanes {
			var g orchestration.SetFramePanes
			_ = json.Unmarshal(m.payload, &g)
			sawGate = slices.Contains(g.Panes, other)
			continue
		}
		if m.mt == orchestration.MsgRequestResync {
			var r orchestration.RequestResync
			_ = json.Unmarshal(m.payload, &r)
			if r.PaneID != other {
				continue
			}
			if !sawGate {
				t.Fatal("the full frame was asked for before the host was told to stream the pane again")
			}
			break
		}
	}

	// Nothing moved: nothing is sent.
	o.refreshViewport()
	select {
	case m := <-pd.msgs:
		if m.mt == orchestration.MsgSetFramePanes {
			t.Fatalf("an unchanged viewport re-sent the gate: %s", m.payload)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

// A host that never advertised the gate is never sent one — it would answer
// the unknown message with an error, and it streams every pane regardless.
func TestFrameGateNeedsTheFeature(t *testing.T) {
	o, _, _ := mcOrch(t)
	pd := newPipeDaemon(t, o)
	o.hosts[o.defaultHost].setFeatures(nil)
	o.hosts[o.defaultHost].sendFramePanes()
	if _, err := o.session.ToggleZoom(nil); err != nil {
		t.Fatalf("zoom: %v", err)
	}
	o.applyModel()
	for _, mt := range pd.collect(100 * time.Millisecond) {
		if mt == orchestration.MsgSetFramePanes {
			t.Fatal("set_frame_panes sent to a host without the feature")
		}
	}
}

// pane_activity is what an off-screen pane's frames used to be good for: the
// history sweep re-captures only panes that changed.
func TestPaneActivityMarksHistoryDirty(t *testing.T) {
	o, _, other := mcOrch(t)
	o.panes[other].histDirty = false
	payload, err := json.Marshal(orchestration.NewPaneActivity(other))
	if err != nil {
		t.Fatal(err)
	}
	o.hosts[o.defaultHost].dispatch(orchestration.MsgPaneActivity, payload)
	// The test goroutine owns the loop here, so waitFor pumps the posted work.
	waitFor(t, o, func() bool { return o.panes[other].histDirty })
}
