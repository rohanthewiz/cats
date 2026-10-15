package app

import (
	"errors"
	"slices"
	"testing"

	"github.com/rohanthewiz/cats/internal/layout"
)

// RetirePane (sleep.go) is the close the runtime's housekeeping uses — the
// clean-exit auto-close and the exited-pane reaper. These cover its three rows:
// a pane with siblings closes as before, a workspace's last pane sleeps the
// workspace instead of dropping it, and the only awake workspace's last pane is
// refused. CanRetirePane must agree with each.

// twoWorkspaces returns a session with w1 (the original, one pane) and a second
// workspace (one pane, active), plus each workspace's only pane.
func twoWorkspaces(t *testing.T) (s *Session, w1, w2 string, p1, p2 layout.PaneID) {
	t.Helper()
	s = newTestSession(t)
	w1 = s.Workspaces()[0].ID
	p1 = s.Workspaces()[0].Tabs[0].RootPane
	w2, err := s.CreateWorkspace()
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	p2 = s.WorkspaceByID(w2).Tabs[0].RootPane
	return s, w1, w2, p1, p2
}

// The bug this exists for: a workspace whose only shell died was dropped by
// the auto-close/reaper, vanishing from both Open and Asleep. It must sleep.
func TestRetireLastPaneSleepsTheWorkspace(t *testing.T) {
	s, w1, w2, p1, _ := twoWorkspaces(t)
	if err := s.RenameWorkspace(w1, "chronos"); err != nil {
		t.Fatalf("RenameWorkspace: %v", err)
	}
	if !s.CanRetirePane(p1) {
		t.Fatal("CanRetirePane = false for a workspace's last pane with another workspace awake")
	}

	wsID, slept, err := s.RetirePane(p1)
	if err != nil || !slept || wsID != w1 {
		t.Fatalf("RetirePane = (%q, %v, %v), want (%q, true, nil)", wsID, slept, err, w1)
	}
	ws := s.WorkspaceByID(w1)
	if ws == nil {
		t.Fatal("the workspace was dropped — it should still be in the list, asleep")
	}
	if !ws.Asleep || ws.CustomName != "chronos" {
		t.Fatalf("after retire: asleep=%v name=%q, want asleep and the name kept", ws.Asleep, ws.CustomName)
	}
	if len(s.Workspaces()) != 2 {
		t.Fatalf("workspaces = %d, want 2", len(s.Workspaces()))
	}
	// The dead pane itself is gone; only the sleep placeholder stands in.
	if slices.Contains(s.AllPaneIDs(), p1) {
		t.Error("the retired pane is still in the model")
	}
	if s.ActiveWorkspaceID() != w2 {
		t.Errorf("active = %s, want the awake %s", s.ActiveWorkspaceID(), w2)
	}
}

// Sleeping the ACTIVE workspace moves the active index off it, as a manual
// sleep does, so the session never shows a pane with no terminal.
func TestRetireLastPaneOfActiveWorkspaceMovesActive(t *testing.T) {
	s, w1, w2, _, p2 := twoWorkspaces(t) // w2 is active
	_, slept, err := s.RetirePane(p2)
	if err != nil || !slept {
		t.Fatalf("RetirePane = (%v, %v), want slept", slept, err)
	}
	if s.ActiveWorkspaceID() != w1 {
		t.Fatalf("active = %s, want %s once %s sleeps", s.ActiveWorkspaceID(), w1, w2)
	}
}

// A pane with a sibling closes exactly as ClosePaneIn always has — nothing
// sleeps, and the workspace keeps its other pane.
func TestRetirePaneWithSiblingJustCloses(t *testing.T) {
	s, w1, _, p1, _ := twoWorkspaces(t)
	if err := s.FocusWorkspace(w1); err != nil {
		t.Fatalf("FocusWorkspace: %v", err)
	}
	sib, err := s.SplitPane(&p1, layout.Horizontal)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	wsID, slept, err := s.RetirePane(sib)
	if err != nil || slept || wsID != w1 {
		t.Fatalf("RetirePane = (%q, %v, %v), want (%q, false, nil)", wsID, slept, err, w1)
	}
	ws := s.WorkspaceByID(w1)
	if ws.Asleep || slices.Contains(s.AllPaneIDs(), sib) || !slices.Contains(s.AllPaneIDs(), p1) {
		t.Fatalf("after retire: asleep=%v panes=%v, want awake with only %d", ws.Asleep, s.AllPaneIDs(), p1)
	}
}

// The only awake workspace has nowhere to sleep to; dropping it instead would
// be the loss this prevents. Refused, nothing changes, and CanRetirePane says
// so up front.
func TestRetireRefusesLastPaneOfOnlyAwakeWorkspace(t *testing.T) {
	s, w1, w2, _, p2 := twoWorkspaces(t)
	if err := s.SleepWorkspace(w1); err != nil {
		t.Fatalf("SleepWorkspace: %v", err)
	}
	if s.CanRetirePane(p2) {
		t.Fatal("CanRetirePane = true for the only awake workspace's last pane")
	}
	_, slept, err := s.RetirePane(p2)
	if !errors.Is(err, ErrLastAwakeWorkspacePane) || slept {
		t.Fatalf("RetirePane = (%v, %v), want ErrLastAwakeWorkspacePane", slept, err)
	}
	if ws := s.WorkspaceByID(w2); ws == nil || ws.Asleep || !slices.Contains(s.AllPaneIDs(), p2) {
		t.Fatal("a refused retire changed the workspace")
	}
}

// A single-workspace session: its one pane is the session's last, refused by
// both rules, and CanRetirePane agrees.
func TestRetireRefusesTheSessionsLastPane(t *testing.T) {
	s := newTestSession(t)
	only := s.AllPaneIDs()[0]
	if s.CanRetirePane(only) {
		t.Fatal("CanRetirePane = true for the session's last pane")
	}
	if _, _, err := s.RetirePane(only); err == nil {
		t.Fatal("RetirePane closed the session's last pane")
	}
	if len(s.Workspaces()) != 1 || len(s.AllPaneIDs()) != 1 {
		t.Fatal("a refused retire changed the session")
	}
}

func TestRetireUnknownPane(t *testing.T) {
	s := newTestSession(t)
	if s.CanRetirePane(9999) {
		t.Error("CanRetirePane = true for an unknown pane")
	}
	if _, _, err := s.RetirePane(9999); err == nil {
		t.Error("RetirePane accepted an unknown pane")
	}
}
