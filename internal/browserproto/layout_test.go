package browserproto

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/rohanthewiz/herdr-web/internal/layout"
	"github.com/rohanthewiz/herdr-web/internal/workspace"
)

type fakeSpawner struct{ n int }

func (f *fakeSpawner) Spawn(spec workspace.SpawnSpec) (workspace.TerminalID, error) {
	f.n++
	return workspace.TerminalID(fmt.Sprintf("t%d", f.n)), nil
}

func (f *fakeSpawner) Despawn(workspace.TerminalID) {}

func mkWorkspace(t *testing.T, cwd string) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.New(&fakeSpawner{}, cwd, workspace.SpawnSpec{})
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	return ws
}

func TestBorderIDRoundTrip(t *testing.T) {
	tests := []struct {
		path []bool
		id   string
	}{
		{nil, "r"},
		{[]bool{false}, "r0"},
		{[]bool{false, true}, "r01"},
		{[]bool{true, true, false}, "r110"},
	}
	for _, tc := range tests {
		if got := BorderID(tc.path); got != tc.id {
			t.Errorf("BorderID(%v) = %q, want %q", tc.path, got, tc.id)
		}
		got, ok := BorderPath(tc.id)
		if !ok {
			t.Errorf("BorderPath(%q) rejected", tc.id)
			continue
		}
		if len(got) != len(tc.path) {
			t.Errorf("BorderPath(%q) = %v, want %v", tc.id, got, tc.path)
			continue
		}
		for i := range got {
			if got[i] != tc.path[i] {
				t.Errorf("BorderPath(%q) = %v, want %v", tc.id, got, tc.path)
			}
		}
	}
	for _, bad := range []string{"", "x01", "r02", "0r"} {
		if _, ok := BorderPath(bad); ok {
			t.Errorf("BorderPath(%q) should be rejected", bad)
		}
	}
}

// TestBorderIDDrivesResize pins that the id in the layout message decodes to
// a path SetRatioAt accepts — the pane.resize_border round trip.
func TestBorderIDDrivesResize(t *testing.T) {
	ws := mkWorkspace(t, "/tmp/proj")
	if _, err := ws.SplitFocused(layout.Horizontal, workspace.SpawnSpec{}); err != nil {
		t.Fatal(err)
	}
	area := layout.Rect{Width: 80, Height: 24}
	lay := ws.ActiveTab().Layout

	borders := lay.Splits(area)
	if len(borders) != 1 {
		t.Fatalf("want 1 border, got %d", len(borders))
	}
	id := BorderID(borders[0].Path)
	path, ok := BorderPath(id)
	if !ok {
		t.Fatalf("BorderPath(%q) rejected its own encoding", id)
	}
	lay.SetRatioAt(path, 0.75)
	if got := lay.Splits(area)[0].Ratio; got != 0.75 {
		t.Fatalf("ratio after resize via border id = %v, want 0.75", got)
	}
}

func TestBuildLayout(t *testing.T) {
	ws1 := mkWorkspace(t, "/tmp/proj-a")
	ws2 := mkWorkspace(t, "/tmp/proj-b")
	newPane, err := ws1.SplitFocused(layout.Horizontal, workspace.SpawnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	rootPane := ws1.ActiveTab().RootPane
	area := layout.Rect{Width: 80, Height: 24}

	msg := BuildLayout([]*workspace.Workspace{ws1, ws2}, 0, area)

	if msg.T != MsgLayout {
		t.Fatalf("t = %q", msg.T)
	}
	wantWs := []WorkspaceInfo{
		{ID: ws1.ID, Name: "proj-a", Active: true},
		{ID: ws2.ID, Name: "proj-b", Active: false},
	}
	if !reflect.DeepEqual(msg.Workspaces, wantWs) {
		t.Errorf("workspaces = %+v, want %+v", msg.Workspaces, wantWs)
	}
	if len(msg.Tabs) != 1 || msg.Tabs[0].Num != 1 || !msg.Tabs[0].Active || msg.Tabs[0].Zoomed {
		t.Errorf("tabs = %+v", msg.Tabs)
	}

	if len(msg.Panes) != 2 {
		t.Fatalf("panes = %+v, want 2", msg.Panes)
	}
	// Layout order is in-order tree traversal: root first, then the new pane.
	root, split := msg.Panes[0], msg.Panes[1]
	if root.Pane != uint32(rootPane) || split.Pane != uint32(newPane.PaneID) {
		t.Errorf("pane ids = %d,%d want %d,%d", root.Pane, split.Pane, rootPane, newPane.PaneID)
	}
	if root.Pub != ws1.ID+":p1" || split.Pub != ws1.ID+":p2" {
		t.Errorf("pub handles = %q,%q", root.Pub, split.Pub)
	}
	if root.Focused || !split.Focused {
		t.Errorf("focus flags = %v,%v — split focuses the new pane", root.Focused, split.Focused)
	}
	if root.Rect != (Rect{0, 0, 40, 24}) || split.Rect != (Rect{40, 0, 40, 24}) {
		t.Errorf("rects = %v, %v", root.Rect, split.Rect)
	}

	if len(msg.Borders) != 1 {
		t.Fatalf("borders = %+v, want 1", msg.Borders)
	}
	b := msg.Borders[0]
	if b.ID != "r" || b.Pos != 40 || b.Dir != uint8(layout.Horizontal) || b.Ratio != 0.5 ||
		b.Area != (Rect{0, 0, 80, 24}) {
		t.Errorf("border = %+v", b)
	}
}

func TestBuildLayoutSecondTabNotActive(t *testing.T) {
	ws := mkWorkspace(t, "/tmp/proj")
	if _, err := ws.CreateTab("/tmp/proj", workspace.SpawnSpec{}); err != nil {
		t.Fatal(err)
	}
	msg := BuildLayout([]*workspace.Workspace{ws}, 0, layout.Rect{Width: 80, Height: 24})
	if len(msg.Tabs) != 2 || !msg.Tabs[0].Active || msg.Tabs[1].Active {
		t.Fatalf("tabs = %+v, want first active", msg.Tabs)
	}
	if msg.Tabs[0].Num != 1 || msg.Tabs[1].Num != 2 {
		t.Fatalf("tab numbers = %+v", msg.Tabs)
	}
	// Panes come from the active tab only.
	if len(msg.Panes) != 1 || msg.Panes[0].Pane != uint32(ws.Tabs[0].RootPane) {
		t.Fatalf("panes = %+v, want active tab's root only", msg.Panes)
	}
}

func TestBuildLayoutZoomed(t *testing.T) {
	ws := mkWorkspace(t, "/tmp/proj")
	newPane, err := ws.SplitFocused(layout.Vertical, workspace.SpawnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	ws.ActiveTab().Zoomed = true
	area := layout.Rect{Width: 80, Height: 24}

	msg := BuildLayout([]*workspace.Workspace{ws}, 0, area)

	if !msg.Tabs[0].Zoomed {
		t.Error("tab should report zoomed")
	}
	if len(msg.Panes) != 1 {
		t.Fatalf("zoomed tab should expose only the focused pane, got %+v", msg.Panes)
	}
	p := msg.Panes[0]
	if p.Pane != uint32(newPane.PaneID) || !p.Focused {
		t.Errorf("zoomed pane = %+v, want focused pane %d", p, newPane.PaneID)
	}
	if p.Rect != (Rect{0, 0, 80, 24}) || p.Inner != (Rect{0, 0, 80, 24}) {
		t.Errorf("zoomed pane should fill the area, got %+v", p)
	}
	if p.Pub != ws.ID+":p2" {
		t.Errorf("zoomed pub = %q", p.Pub)
	}
	if len(msg.Borders) != 0 {
		t.Errorf("zoomed tab should have no draggable borders, got %+v", msg.Borders)
	}
}

func TestBuildLayoutEdgeCases(t *testing.T) {
	area := layout.Rect{Width: 80, Height: 24}
	msg := BuildLayout(nil, 0, area)
	if len(msg.Workspaces) != 0 || len(msg.Tabs) != 0 || len(msg.Panes) != 0 || len(msg.Borders) != 0 {
		t.Errorf("empty input should build an empty layout, got %+v", msg)
	}
	ws := mkWorkspace(t, "/tmp/proj")
	msg = BuildLayout([]*workspace.Workspace{ws}, 5, area)
	if len(msg.Workspaces) != 1 || msg.Workspaces[0].Active {
		t.Errorf("out-of-range active: workspaces = %+v", msg.Workspaces)
	}
	if len(msg.Tabs) != 0 || len(msg.Panes) != 0 {
		t.Errorf("out-of-range active should carry no viewport, got %+v", msg)
	}
}

func TestPaneRectFromScrollbar(t *testing.T) {
	sb := layout.Rect{X: 39, Y: 0, Width: 1, Height: 24}
	info := layout.PaneInfo{
		ID:            9,
		Rect:          layout.Rect{Width: 40, Height: 24},
		InnerRect:     layout.Rect{X: 1, Y: 1, Width: 38, Height: 22},
		ScrollbarRect: &sb,
		IsFocused:     true,
	}
	p := PaneRectFrom(info, "w1:p4")
	if p.Scrollbar == nil || *p.Scrollbar != (Rect{39, 0, 1, 24}) {
		t.Errorf("scrollbar = %v", p.Scrollbar)
	}
	if p.Inner != (Rect{1, 1, 38, 22}) || p.Pub != "w1:p4" || !p.Focused {
		t.Errorf("pane rect = %+v", p)
	}
}
