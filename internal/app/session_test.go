package app

import (
	"fmt"
	"testing"

	"github.com/rohanthewiz/herdr-web/internal/layout"
	"github.com/rohanthewiz/herdr-web/internal/workspace"
)

// fakeSpawner satisfies workspace.PaneSpawner with monotonic terminal ids; the
// app layer never touches real PTYs (that's the orchestrator runtime's job).
type fakeSpawner struct{ n int }

func (f *fakeSpawner) Spawn(workspace.SpawnSpec) (workspace.TerminalID, error) {
	f.n++
	return workspace.TerminalID(fmt.Sprintf("t%d", f.n)), nil
}
func (f *fakeSpawner) Despawn(workspace.TerminalID) {}

func newTestSession(t *testing.T) *Session {
	t.Helper()
	s, err := NewSession(&fakeSpawner{}, "/tmp/work")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return s
}

func TestNewSessionShape(t *testing.T) {
	s := newTestSession(t)
	if len(s.Workspaces()) != 1 {
		t.Fatalf("workspaces = %d, want 1", len(s.Workspaces()))
	}
	if len(s.AllPaneIDs()) != 1 || len(s.VisiblePaneIDs()) != 1 {
		t.Fatalf("panes: all=%d visible=%d, want 1/1", len(s.AllPaneIDs()), len(s.VisiblePaneIDs()))
	}
	if _, ok := s.FocusedPane(); !ok {
		t.Error("no focused pane")
	}
}

func TestSplitAndClosePane(t *testing.T) {
	s := newTestSession(t)
	root, _ := s.FocusedPane()

	np, err := s.SplitPane(nil, layout.Horizontal)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if len(s.VisiblePaneIDs()) != 2 {
		t.Fatalf("after split visible=%d, want 2", len(s.VisiblePaneIDs()))
	}
	if f, _ := s.FocusedPane(); f != np {
		t.Errorf("focused=%d, want new pane %d", f, np)
	}

	// Close the new pane by explicit target → back to just root.
	closed, err := s.ClosePane(&np)
	if err != nil {
		t.Fatalf("ClosePane: %v", err)
	}
	if closed != np {
		t.Errorf("closed=%d, want %d", closed, np)
	}
	if len(s.VisiblePaneIDs()) != 1 {
		t.Fatalf("after close visible=%d, want 1", len(s.VisiblePaneIDs()))
	}

	// Cannot close the session's last pane.
	if _, err := s.ClosePane(&root); err == nil {
		t.Error("closing the last pane should error")
	}
}

func TestSplitUnknownPane(t *testing.T) {
	s := newTestSession(t)
	bogus := layout.PaneID(9999)
	if _, err := s.SplitPane(&bogus, layout.Vertical); err == nil {
		t.Error("splitting an unknown pane should error")
	}
}

func TestTabsViewportIsolation(t *testing.T) {
	s := newTestSession(t)
	tab1Pane := s.VisiblePaneIDs()[0]

	num, err := s.CreateTab()
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	// New tab is active: viewport = the new tab's single pane; all panes = 2.
	if len(s.VisiblePaneIDs()) != 1 {
		t.Fatalf("new tab visible=%d, want 1", len(s.VisiblePaneIDs()))
	}
	if len(s.AllPaneIDs()) != 2 {
		t.Fatalf("all panes=%d, want 2", len(s.AllPaneIDs()))
	}
	tab2Pane := s.VisiblePaneIDs()[0]
	if tab2Pane == tab1Pane {
		t.Fatal("new tab reused the old pane id")
	}

	// Focusing tab 1 flips the viewport back.
	if err := s.FocusTab(1); err != nil {
		t.Fatalf("FocusTab(1): %v", err)
	}
	if got := s.VisiblePaneIDs(); len(got) != 1 || got[0] != tab1Pane {
		t.Fatalf("after FocusTab(1) visible=%v, want [%d]", got, tab1Pane)
	}

	// Close the (non-active) new tab by number → back to one tab.
	if err := s.CloseTab(&num); err != nil {
		t.Fatalf("CloseTab(%d): %v", num, err)
	}
	if len(s.AllPaneIDs()) != 1 {
		t.Fatalf("after CloseTab all=%d, want 1", len(s.AllPaneIDs()))
	}
}

func TestCannotCloseLastTab(t *testing.T) {
	s := newTestSession(t)
	if err := s.CloseTab(nil); err == nil {
		t.Error("closing the only tab of the only workspace should error")
	}
}

func TestRenameTab(t *testing.T) {
	s := newTestSession(t)
	if err := s.RenameTab(1, "build"); err != nil {
		t.Fatalf("RenameTab: %v", err)
	}
	if got := s.ActiveWorkspace().Tabs[0].DisplayName(); got != "build" {
		t.Errorf("tab name = %q, want build", got)
	}
	if err := s.RenameTab(42, "x"); err == nil {
		t.Error("renaming an unknown tab should error")
	}
}

func TestWorkspacesLifecycle(t *testing.T) {
	s := newTestSession(t)
	ws1 := s.ActiveWorkspace().ID

	ws2, err := s.CreateWorkspace()
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if len(s.Workspaces()) != 2 || s.ActiveWorkspace().ID != ws2 {
		t.Fatalf("after create: n=%d active=%s, want 2/%s", len(s.Workspaces()), s.ActiveWorkspace().ID, ws2)
	}
	if ws2 == ws1 {
		t.Fatal("new workspace reused the id")
	}
	// Each workspace contributes a pane to AllPaneIDs; viewport shows only ws2's.
	if len(s.AllPaneIDs()) != 2 || len(s.VisiblePaneIDs()) != 1 {
		t.Fatalf("panes all=%d visible=%d, want 2/1", len(s.AllPaneIDs()), len(s.VisiblePaneIDs()))
	}

	if err := s.FocusWorkspace(ws1); err != nil {
		t.Fatalf("FocusWorkspace: %v", err)
	}
	if s.ActiveWorkspace().ID != ws1 {
		t.Errorf("active=%s, want %s", s.ActiveWorkspace().ID, ws1)
	}

	if err := s.CloseWorkspace(&ws2); err != nil {
		t.Fatalf("CloseWorkspace: %v", err)
	}
	if len(s.Workspaces()) != 1 || s.ActiveWorkspace().ID != ws1 {
		t.Fatalf("after close n=%d active=%s, want 1/%s", len(s.Workspaces()), s.ActiveWorkspace().ID, ws1)
	}
	if err := s.CloseWorkspace(nil); err == nil {
		t.Error("closing the last workspace should error")
	}
}

func TestClosingWorkspaceLastPaneDropsWorkspace(t *testing.T) {
	s := newTestSession(t)
	ws1 := s.ActiveWorkspace().ID
	if _, err := s.CreateWorkspace(); err != nil { // active = ws2, 1 pane
		t.Fatalf("CreateWorkspace: %v", err)
	}
	pane, _ := s.FocusedPane()

	// Closing ws2's only pane must drop ws2 and fall back to ws1.
	if _, err := s.ClosePane(&pane); err != nil {
		t.Fatalf("ClosePane: %v", err)
	}
	if len(s.Workspaces()) != 1 || s.ActiveWorkspace().ID != ws1 {
		t.Fatalf("after close: n=%d active=%s, want 1/%s", len(s.Workspaces()), s.ActiveWorkspace().ID, ws1)
	}
}

func TestPublicPaneIDAcrossWorkspaces(t *testing.T) {
	s := newTestSession(t)
	p1, _ := s.FocusedPane()
	if _, err := s.CreateWorkspace(); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	p2, _ := s.FocusedPane()

	pub1, ok1 := s.PublicPaneID(p1)
	pub2, ok2 := s.PublicPaneID(p2)
	if !ok1 || !ok2 || pub1 == "" || pub2 == "" || pub1 == pub2 {
		t.Errorf("public ids: %q/%v %q/%v (want two distinct non-empty)", pub1, ok1, pub2, ok2)
	}
}
