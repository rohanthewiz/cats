package app

import (
	"testing"

	"github.com/rohanthewiz/cats/internal/layout"
)

// loc is shorthand for a NavLocation in workspace w1, tab 1 unless said
// otherwise — the coalescing comparison only reads Workspace and Tab, so most
// cases vary just the pane.
func loc(pane layout.PaneID) NavLocation {
	return NavLocation{Workspace: "w1", Tab: 1, Pane: pane}
}

func alwaysValid(NavLocation) bool { return true }

// Note dedups against the current entry: re-offering where we already are —
// which is what every non-focus command does via the post-dispatch hook — must
// not grow the stack.
func TestNavNoteDedup(t *testing.T) {
	h := NewNavHistory()
	h.Note(loc(1), false)
	h.Note(loc(1), false)
	h.Note(loc(1), true)
	if len(h.entries) != 1 || h.cur != 0 {
		t.Fatalf("entries=%d cur=%d, want 1/0", len(h.entries), h.cur)
	}
}

// A coalesced move within the same workspace+tab replaces the current entry;
// the same move across a tab boundary pushes.
func TestNavNoteCoalesce(t *testing.T) {
	h := NewNavHistory()
	h.Note(loc(1), false)
	h.Note(loc(2), true) // hjkl hop within the tab: replace
	if len(h.entries) != 1 || h.entries[0].Pane != 2 {
		t.Fatalf("coalesced entries=%v, want single entry at pane 2", h.entries)
	}
	other := NavLocation{Workspace: "w1", Tab: 2, Pane: 3}
	h.Note(other, true) // same flag, different tab: a jump, so push
	if len(h.entries) != 2 || h.entries[1] != other {
		t.Fatalf("cross-tab coalesce entries=%v, want push", h.entries)
	}
}

// A new location while mid-stack truncates the forward tail (editor-style).
func TestNavNoteTruncatesForward(t *testing.T) {
	h := NewNavHistory()
	h.Note(loc(1), false)
	h.Note(loc(2), false)
	h.Note(loc(3), false)
	if _, ok := h.Step(true, alwaysValid); !ok { // back to 2
		t.Fatal("back from 3 should land on 2")
	}
	h.Note(loc(4), false) // new jump: 3 is abandoned
	if _, ok := h.Step(false, alwaysValid); ok {
		t.Fatal("forward after a new jump should have nowhere to go")
	}
	got, ok := h.Step(true, alwaysValid)
	if !ok || got.Pane != 2 {
		t.Fatalf("back after truncation = %v/%v, want pane 2", got, ok)
	}
}

// The cap drops the oldest entry, keeping the cursor on the same location.
func TestNavNoteCap(t *testing.T) {
	h := NewNavHistory()
	for i := range navHistoryCap + 10 {
		h.Note(loc(layout.PaneID(i+1)), false)
	}
	if len(h.entries) != navHistoryCap {
		t.Fatalf("entries=%d, want cap %d", len(h.entries), navHistoryCap)
	}
	if h.entries[h.cur].Pane != layout.PaneID(navHistoryCap+10) {
		t.Fatalf("cursor pane=%d, want the last noted", h.entries[h.cur].Pane)
	}
}

// Back-then-forward is a round trip.
func TestNavStepRoundTrip(t *testing.T) {
	h := NewNavHistory()
	h.Note(loc(1), false)
	h.Note(loc(2), false)
	back, ok := h.Step(true, alwaysValid)
	if !ok || back.Pane != 1 {
		t.Fatalf("back = %v/%v, want pane 1", back, ok)
	}
	fwd, ok := h.Step(false, alwaysValid)
	if !ok || fwd.Pane != 2 {
		t.Fatalf("forward = %v/%v, want pane 2", fwd, ok)
	}
	if _, ok := h.Step(false, alwaysValid); ok {
		t.Fatal("forward at the newest entry should report nowhere to go")
	}
}

// Stale entries are removed in passing and the walk continues through them —
// in both directions.
func TestNavStepDropsStale(t *testing.T) {
	h := NewNavHistory()
	h.Note(loc(1), false)
	h.Note(loc(2), false) // will be "closed"
	h.Note(loc(3), false) // will be "closed"
	h.Note(loc(4), false)
	valid := func(l NavLocation) bool { return l.Pane != 2 && l.Pane != 3 }
	got, ok := h.Step(true, valid)
	if !ok || got.Pane != 1 {
		t.Fatalf("back over stale = %v/%v, want pane 1", got, ok)
	}
	if len(h.entries) != 2 {
		t.Fatalf("stale entries kept: %v", h.entries)
	}
	got, ok = h.Step(false, valid)
	if !ok || got.Pane != 4 {
		t.Fatalf("forward after drop = %v/%v, want pane 4", got, ok)
	}
}

// An empty history has nowhere to go in either direction.
func TestNavStepEmpty(t *testing.T) {
	h := NewNavHistory()
	if _, ok := h.Step(true, alwaysValid); ok {
		t.Fatal("back on empty history should report nowhere to go")
	}
	if _, ok := h.Step(false, alwaysValid); ok {
		t.Fatal("forward on empty history should report nowhere to go")
	}
}

// --- dispatch ------------------------------------------------------------------

// navBackend is fakeBackend plus the optional history seam, standing in for
// catway's viewBackend the way fakeBackend stands in for orch.
type navBackend struct {
	*fakeBackend
	nav *NavHistory
}

func (b *navBackend) NavHistory() *NavHistory { return b.nav }

// A backend without the seam (every plain fake) makes nav.back a silent
// no-op, matching pane.last's convention — not an error.
func TestDispatchNavNoBackend(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()
	h.d.Dispatch(CmdNavBack, noParams(), r)
	if !r.okCall || r.failCall {
		t.Fatalf("nav.back without a history seam: ok=%v fail=%v (%q)", r.okCall, r.failCall, r.errMsg)
	}
	if got := *h.log; len(got) != 1 || got[0] != "ok" {
		t.Fatalf("effects = %v, want [ok] only", got)
	}
}

// nav.back reveals the previous location in the issuing view — the agent.focus
// effect order (setViewWorkspace, then applyModel) — and nav.forward returns.
func TestDispatchNavBackForward(t *testing.T) {
	log := &[]string{}
	s := newTestSession(t)
	fb := &fakeBackend{log: log, area: layout.Rect{Width: 120, Height: 32}, paneExists: true, daemonUp: true}
	nb := &navBackend{fakeBackend: fb, nav: NewNavHistory()}
	d := NewDispatcher(s, nb)

	first, _ := s.FocusedPane()
	d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH}), &fakeResponder{log: log, wants: true})
	second, _ := s.FocusedPane()
	if first == second {
		t.Fatal("split should focus the new pane")
	}
	wsID := s.ActiveWorkspaceID()
	// The runtime hook would have noted both locations; stand in for it here.
	nb.nav.Note(NavLocation{Workspace: wsID, Tab: 1, Pane: first}, false)
	nb.nav.Note(NavLocation{Workspace: wsID, Tab: 1, Pane: second}, false)

	*log = nil
	r := &fakeResponder{log: log, wants: true}
	d.Dispatch(CmdNavBack, noParams(), r)
	if !r.okCall {
		t.Fatalf("nav.back failed: %q", r.errMsg)
	}
	if got, _ := s.FocusedPane(); got != first {
		t.Fatalf("nav.back focused %d, want %d", got, first)
	}
	if got := *log; len(got) != 3 || got[0] != "setViewWorkspace" || got[1] != "applyModel" || got[2] != "ok" {
		t.Fatalf("nav.back effects = %v, want [setViewWorkspace applyModel ok]", got)
	}

	d.Dispatch(CmdNavForward, noParams(), &fakeResponder{log: log, wants: true})
	if got, _ := s.FocusedPane(); got != second {
		t.Fatalf("nav.forward focused %d, want %d", got, second)
	}
}

// An entry whose pane has since closed is skipped: the walk lands on the next
// live location behind it.
func TestDispatchNavSkipsClosedPane(t *testing.T) {
	log := &[]string{}
	s := newTestSession(t)
	fb := &fakeBackend{log: log, area: layout.Rect{Width: 120, Height: 32}, paneExists: true, daemonUp: true}
	nb := &navBackend{fakeBackend: fb, nav: NewNavHistory()}
	d := NewDispatcher(s, nb)

	first, _ := s.FocusedPane()
	d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH}), &fakeResponder{log: log, wants: true})
	second, _ := s.FocusedPane()
	wsID := s.ActiveWorkspaceID()
	nb.nav.Note(NavLocation{Workspace: wsID, Tab: 1, Pane: first}, false)
	nb.nav.Note(NavLocation{Workspace: wsID, Tab: 1, Pane: 999}, false) // a pane that no longer exists
	nb.nav.Note(NavLocation{Workspace: wsID, Tab: 1, Pane: second}, false)

	r := &fakeResponder{log: log, wants: true}
	d.Dispatch(CmdNavBack, noParams(), r)
	if !r.okCall {
		t.Fatalf("nav.back failed: %q", r.errMsg)
	}
	if got, _ := s.FocusedPane(); got != first {
		t.Fatalf("nav.back over a closed pane focused %d, want %d", got, first)
	}
}
