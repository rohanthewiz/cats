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

func TestFocusPaneDirection(t *testing.T) {
	s := newTestSession(t)
	area := layout.Rect{Width: 120, Height: 32}
	left, _ := s.FocusedPane()

	// Horizontal split → left|right; the new (right) pane is focused.
	right, err := s.SplitPane(nil, layout.Horizontal)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if f, _ := s.FocusedPane(); f != right {
		t.Fatalf("after split focused=%d, want right %d", f, right)
	}

	// Move focus left → lands on the left pane.
	moved, err := s.FocusPaneDirection(layout.Left, area)
	if err != nil {
		t.Fatalf("FocusPaneDirection(Left): %v", err)
	}
	if !moved {
		t.Fatal("FocusPaneDirection(Left) reported no move")
	}
	if f, _ := s.FocusedPane(); f != left {
		t.Fatalf("after focus-left focused=%d, want left %d", f, left)
	}

	// No neighbour further left → no-op, no error, focus unchanged.
	moved, err = s.FocusPaneDirection(layout.Left, area)
	if err != nil {
		t.Fatalf("FocusPaneDirection(Left) at edge: %v", err)
	}
	if moved {
		t.Error("FocusPaneDirection(Left) at the left edge should not move")
	}
	if f, _ := s.FocusedPane(); f != left {
		t.Errorf("focus drifted to %d after edge no-op, want %d", f, left)
	}

	// Move focus right → back to the right pane. Direction stays within the tab.
	moved, err = s.FocusPaneDirection(layout.Right, area)
	if err != nil {
		t.Fatalf("FocusPaneDirection(Right): %v", err)
	}
	if !moved {
		t.Fatal("FocusPaneDirection(Right) reported no move")
	}
	if f, _ := s.FocusedPane(); f != right {
		t.Fatalf("after focus-right focused=%d, want right %d", f, right)
	}
}

func TestCyclePane(t *testing.T) {
	s := newTestSession(t)
	// Single pane → nothing to cycle to.
	if s.CyclePane(true) {
		t.Fatal("CyclePane on a single pane should report no move")
	}
	a, _ := s.FocusedPane()
	b, err := s.SplitPane(nil, layout.Horizontal) // focus now on b
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	// Two panes [a, b], focus on b. next wraps b→a; next again a→b.
	if !s.CyclePane(true) {
		t.Fatal("CyclePane(next) reported no move")
	}
	if f, _ := s.FocusedPane(); f != a {
		t.Fatalf("after cycle-next focused=%d, want %d", f, a)
	}
	s.CyclePane(true) // a → b (wrap)
	if f, _ := s.FocusedPane(); f != b {
		t.Fatalf("after 2nd cycle-next focused=%d, want %d", f, b)
	}
	// prev from b → a.
	s.CyclePane(false)
	if f, _ := s.FocusedPane(); f != a {
		t.Fatalf("after cycle-prev focused=%d, want %d", f, a)
	}
}

func TestSwapPaneDirection(t *testing.T) {
	s := newTestSession(t)
	area := layout.Rect{Width: 120, Height: 32}
	left, _ := s.FocusedPane()
	right, err := s.SplitPane(nil, layout.Horizontal) // left | right, focus=right
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	xOf := func(id layout.PaneID) uint16 {
		for _, info := range s.ActiveWorkspace().ActiveTab().Layout.Panes(area) {
			if info.ID == id {
				return info.Rect.X
			}
		}
		t.Fatalf("pane %d not found", id)
		return 0
	}
	if xOf(left) != 0 {
		t.Fatalf("pre-swap left pane x=%d, want 0", xOf(left))
	}
	// Swap the focused (right) pane leftward: it travels to x=0, keeps focus.
	swapped, err := s.SwapPaneDirection(layout.Left, area)
	if err != nil {
		t.Fatalf("SwapPaneDirection: %v", err)
	}
	if !swapped {
		t.Fatal("SwapPaneDirection(Left) reported no swap")
	}
	if xOf(right) != 0 {
		t.Errorf("post-swap right pane x=%d, want 0 (it moved left)", xOf(right))
	}
	if f, _ := s.FocusedPane(); f != right {
		t.Errorf("focus=%d after swap, want the travelling pane %d", f, right)
	}
	// No neighbour further left of the (now leftmost) focused pane → no-op.
	if swapped, _ := s.SwapPaneDirection(layout.Left, area); swapped {
		t.Error("SwapPaneDirection(Left) at the edge should not swap")
	}
}

func TestToggleZoom(t *testing.T) {
	s := newTestSession(t)
	if _, err := s.SplitPane(nil, layout.Horizontal); err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if len(s.VisiblePaneIDs()) != 2 {
		t.Fatalf("pre-zoom visible=%d, want 2", len(s.VisiblePaneIDs()))
	}
	focus, _ := s.FocusedPane()

	on, err := s.ToggleZoom(nil)
	if err != nil {
		t.Fatalf("ToggleZoom: %v", err)
	}
	if !on {
		t.Fatal("ToggleZoom did not turn zoom on")
	}
	// Zoomed → the viewport is just the focused pane; all panes still live.
	if got := s.VisiblePaneIDs(); len(got) != 1 || got[0] != focus {
		t.Fatalf("zoomed visible=%v, want [%d]", got, focus)
	}
	if len(s.AllPaneIDs()) != 2 {
		t.Fatalf("zoomed all=%d, want 2 (siblings stay live)", len(s.AllPaneIDs()))
	}

	off, err := s.ToggleZoom(nil)
	if err != nil {
		t.Fatalf("ToggleZoom (off): %v", err)
	}
	if off {
		t.Fatal("second ToggleZoom did not turn zoom off")
	}
	if len(s.VisiblePaneIDs()) != 2 {
		t.Fatalf("post-unzoom visible=%d, want 2", len(s.VisiblePaneIDs()))
	}

	// Zooming a specific pane focuses it first.
	other := layout.PaneID(0)
	for _, id := range s.VisiblePaneIDs() {
		if id != focus {
			other = id
		}
	}
	if _, err := s.ToggleZoom(&other); err != nil {
		t.Fatalf("ToggleZoom(target): %v", err)
	}
	if got := s.VisiblePaneIDs(); len(got) != 1 || got[0] != other {
		t.Fatalf("targeted zoom visible=%v, want [%d]", got, other)
	}
	bogus := layout.PaneID(9999)
	s.ToggleZoom(nil) // unzoom before testing the error path
	if _, err := s.ToggleZoom(&bogus); err == nil {
		t.Error("zooming an unknown pane should error")
	}
}

func TestResizeBorder(t *testing.T) {
	s := newTestSession(t)
	area := layout.Rect{Width: 120, Height: 32}
	left, _ := s.FocusedPane()
	if _, err := s.SplitPane(nil, layout.Horizontal); err != nil { // 50/50
		t.Fatalf("SplitPane: %v", err)
	}
	wOf := func(id layout.PaneID) uint16 {
		for _, info := range s.ActiveWorkspace().ActiveTab().Layout.Panes(area) {
			if info.ID == id {
				return info.Rect.Width
			}
		}
		t.Fatalf("pane %d not found", id)
		return 0
	}
	before := wOf(left)
	// Root split, first child = left. Shrink it to 0.3 → narrower left pane.
	if err := s.ResizeBorder([]bool{}, 0.3); err != nil {
		t.Fatalf("ResizeBorder: %v", err)
	}
	after := wOf(left)
	if after >= before {
		t.Fatalf("left width %d→%d, want it to shrink", before, after)
	}
	// Roughly 30% of 120 (allowing for chrome/gaps): sanity bound.
	if after == 0 || after > 60 {
		t.Errorf("left width=%d after 0.3 resize, want ~30%% of 120", after)
	}
}

func TestFocusLastPane(t *testing.T) {
	s := newTestSession(t)
	// Single pane → no previous pane to toggle to.
	if s.FocusLastPane() {
		t.Fatal("FocusLastPane with one pane should report no move")
	}
	a, _ := s.FocusedPane()
	b, err := s.SplitPane(nil, layout.Horizontal) // focus a→b (prev=a)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	// last toggles back to a, then again forward to b (ping-pong).
	if !s.FocusLastPane() {
		t.Fatal("FocusLastPane reported no move")
	}
	if f, _ := s.FocusedPane(); f != a {
		t.Fatalf("after last focused=%d, want %d", f, a)
	}
	if !s.FocusLastPane() {
		t.Fatal("second FocusLastPane reported no move")
	}
	if f, _ := s.FocusedPane(); f != b {
		t.Fatalf("after 2nd last focused=%d, want %d", f, b)
	}

	// Closing the previous pane invalidates the toggle target.
	s.FocusPane(a)       // focus a (prev=b)
	if _, err := s.ClosePane(&b); err != nil {
		t.Fatalf("ClosePane: %v", err)
	}
	if s.FocusLastPane() {
		t.Error("FocusLastPane should not jump to a closed pane")
	}
}

func TestRenamePane(t *testing.T) {
	s := newTestSession(t)
	id, _ := s.FocusedPane()

	if name, ok := s.PaneCustomName(id); !ok || name != "" {
		t.Fatalf("initial custom name = %q/%v, want \"\"/true", name, ok)
	}
	if err := s.RenamePane(id, "builder"); err != nil {
		t.Fatalf("RenamePane: %v", err)
	}
	if name, _ := s.PaneCustomName(id); name != "builder" {
		t.Errorf("custom name = %q, want builder", name)
	}
	// Clearing reverts to terminal-derived titles.
	if err := s.RenamePane(id, ""); err != nil {
		t.Fatalf("RenamePane clear: %v", err)
	}
	if name, _ := s.PaneCustomName(id); name != "" {
		t.Errorf("custom name after clear = %q, want empty", name)
	}
	// Unknown pane errors; its name query reports not-found.
	if err := s.RenamePane(layout.PaneID(9999), "x"); err == nil {
		t.Error("renaming an unknown pane should error")
	}
	if _, ok := s.PaneCustomName(layout.PaneID(9999)); ok {
		t.Error("PaneCustomName for an unknown pane should report not-found")
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
