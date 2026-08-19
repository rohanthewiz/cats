package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// Views (view.go) split the session's one implicit viewport into a per-window
// one. Two properties have to hold for that to be safe:
//
//  1. Every "…In" form behaves EXACTLY like the default it was extracted from
//     when handed "" — otherwise a view-less caller (catctl, a hook, a runbook
//     step) silently changes behaviour.
//  2. Every "…In" form scoped to another workspace acts THERE and nowhere else
//     — otherwise a second window drives the first one's tabs.
//
// A third test reads the dispatcher's own source, so the next command added has
// to answer the question rather than quietly defaulting to s.active.

// twoWSSession builds a session with two workspaces: w1 (active, two panes in
// its one tab) and w2 (two tabs, one pane each). Enough shape for every command
// below to have somewhere wrong to land.
func twoWSSession(t *testing.T) (s *Session, ws1, ws2 string) {
	t.Helper()
	s = newTestSession(t)
	ws1 = s.Workspaces()[0].ID
	if _, err := s.SplitPane(nil, layout.Horizontal); err != nil {
		t.Fatalf("split in ws1: %v", err)
	}
	var err error
	ws2, err = s.CreateWorkspace() // becomes active
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if _, _, err := s.CreateTabIn(ws2); err != nil {
		t.Fatalf("CreateTabIn: %v", err)
	}
	if err := s.FocusWorkspace(ws1); err != nil { // back to w1 as the default view
		t.Fatalf("FocusWorkspace: %v", err)
	}
	return s, ws1, ws2
}

// shape renders a session's structure with pane ids replaced by the ordinal of
// their first appearance. The global id counters (layout.ReservePaneIDs,
// workspace.ReserveWorkspaceIDs) keep moving between sessions, so two sessions
// built by the same steps are never id-identical — but they are shape-identical,
// and shape is what "the default form delegates" actually claims.
func shape(s *Session) string {
	ord := map[layout.PaneID]int{}
	num := func(id layout.PaneID) int {
		if n, ok := ord[id]; ok {
			return n
		}
		ord[id] = len(ord) + 1
		return ord[id]
	}
	var b strings.Builder
	for i, ws := range s.Workspaces() {
		fmt.Fprintf(&b, "ws%d active=%t tab=%d\n", i, i == s.ActiveIndex(), ws.ActiveTabIndex())
		for j, tab := range ws.Tabs {
			fmt.Fprintf(&b, "  tab%d num=%d zoom=%t panes=", j, tab.Number, tab.Zoomed)
			for _, id := range tab.Layout.PaneIDs() {
				fmt.Fprintf(&b, "%d,", num(id))
			}
			fmt.Fprintf(&b, " focus=%d\n", num(tab.Layout.Focused()))
		}
	}
	return b.String()
}

// --- 1. the default forms delegate ----------------------------------------------

// Each pair runs the historical form on one session and the "…In with an empty
// workspace" form on an identically-built one. Same shape afterwards means the
// view-less path is byte-for-byte what it was.
func TestDefaultFormsMatchTheirViewScopedTwin(t *testing.T) {
	num2 := 2
	cases := []struct {
		name string
		def  func(*Session)
		in   func(*Session)
	}{
		{"SplitPane", func(s *Session) { _, _ = s.SplitPane(nil, layout.Vertical) },
			func(s *Session) { _, _ = s.SplitPaneWithIn("", nil, layout.Vertical, workspace.SpawnSpec{}) }},
		{"ClosePane", func(s *Session) { _, _ = s.ClosePane(nil) },
			func(s *Session) { _, _ = s.ClosePaneIn("", nil) }},
		{"CyclePane", func(s *Session) { s.CyclePane(true) },
			func(s *Session) { s.CyclePaneIn("", true) }},
		{"FocusLastPane", func(s *Session) { s.FocusLastPane() },
			func(s *Session) { s.FocusLastPaneIn("") }},
		{"ToggleZoom", func(s *Session) { _, _ = s.ToggleZoom(nil) },
			func(s *Session) { _, _ = s.ToggleZoomIn("", nil) }},
		{"FocusPaneDirection", func(s *Session) { _, _ = s.FocusPaneDirection(layout.Left, testArea) },
			func(s *Session) { _, _ = s.FocusPaneDirectionIn("", layout.Left, testArea) }},
		{"SwapPaneDirection", func(s *Session) { _, _ = s.SwapPaneDirection(layout.Left, testArea) },
			func(s *Session) { _, _ = s.SwapPaneDirectionIn("", layout.Left, testArea) }},
		{"ResizeBorder", func(s *Session) { _ = s.ResizeBorder([]bool{}, 0.3) },
			func(s *Session) { _ = s.ResizeBorderIn("", []bool{}, 0.3) }},
		{"FocusTab", func(s *Session) { _ = s.FocusTab(1) },
			func(s *Session) { _ = s.FocusTabIn("", 1) }},
		{"CloseTab", func(s *Session) { _ = s.CloseTab(nil) },
			func(s *Session) { _ = s.CloseTabIn("", nil) }},
		{"MoveTab", func(s *Session) { _, _ = s.MoveTab(1, 1) },
			func(s *Session) { _, _ = s.MoveTabIn("", 1, 1) }},
		{"CloseTabByNum", func(s *Session) { _ = s.CloseTab(&num2) },
			func(s *Session) { _ = s.CloseTabIn("", &num2) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := twoWSSession(t)
			b, _, _ := twoWSSession(t)
			tc.def(a)
			tc.in(b)
			if got, want := shape(b), shape(a); got != want {
				t.Fatalf("%sIn(\"\") diverged from %s\n--- default ---\n%s--- In(\"\") ---\n%s",
					tc.name, tc.name, want, got)
			}
		})
	}
}

// testArea is the grid the directional commands resolve neighbours against.
var testArea = layout.Rect{Width: 120, Height: 40}

// The queries delegate too — including "visible", which a window asks about its
// own viewport and a view-less caller asks about the session's.
func TestDefaultQueriesMatchTheirViewScopedTwin(t *testing.T) {
	s, _, _ := twoWSSession(t)

	id1, ok1 := s.FocusedPane()
	id2, ok2 := s.FocusedPaneIn("")
	if id1 != id2 || ok1 != ok2 {
		t.Fatalf("FocusedPaneIn(\"\") = %d/%t, want %d/%t", id2, ok2, id1, ok1)
	}
	if got, want := len(s.VisiblePaneIDsIn("")), len(s.VisiblePaneIDs()); got != want {
		t.Fatalf("VisiblePaneIDsIn(\"\") = %d panes, want %d", got, want)
	}
	if got, want := s.InfoIn("").ActiveWorkspace, s.Info().ActiveWorkspace; got != want {
		t.Fatalf("InfoIn(\"\") active = %q, want %q", got, want)
	}
	if got, want := len(s.ListPanesIn("")), len(s.ListPanes()); got != want {
		t.Fatalf("ListPanesIn(\"\") = %d panes, want %d", got, want)
	}
}

// --- 2. a scoped form acts where it is aimed -------------------------------------

// The property a second window depends on: a command scoped to w2 changes w2
// and leaves the active workspace's tabs, focus and panes exactly as they were.
func TestScopedFormsLeaveTheOtherWorkspaceAlone(t *testing.T) {
	cases := []struct {
		name string
		run  func(*Session, string)
	}{
		{"FocusTabIn", func(s *Session, ws string) { _ = s.FocusTabIn(ws, 1) }},
		{"CyclePaneIn", func(s *Session, ws string) { s.CyclePaneIn(ws, true) }},
		{"ToggleZoomIn", func(s *Session, ws string) { _, _ = s.ToggleZoomIn(ws, nil) }},
		{"SplitPaneIn", func(s *Session, ws string) { _, _ = s.SplitPaneOnIn(ws, nil, layout.Vertical, "") }},
		{"MoveTabIn", func(s *Session, ws string) { _, _ = s.MoveTabIn(ws, 1, 2) }},
		{"CloseTabIn", func(s *Session, ws string) { _ = s.CloseTabIn(ws, nil) }},
		{"ResizeBorderIn", func(s *Session, ws string) { _ = s.ResizeBorderIn(ws, []bool{}, 0.25) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ws1, ws2 := twoWSSession(t)
			before := workspaceShape(s, ws1)

			tc.run(s, ws2)

			if got := workspaceShape(s, ws1); got != before {
				t.Fatalf("%s aimed at %s changed %s\n--- before ---\n%s--- after ---\n%s",
					tc.name, ws2, ws1, before, got)
			}
			if s.ActiveWorkspaceID() != ws1 {
				t.Fatalf("%s aimed at %s moved the session default to %s",
					tc.name, ws2, s.ActiveWorkspaceID())
			}
		})
	}
}

// workspaceShape is shape() for one workspace — what "the other window was left
// alone" means concretely.
func workspaceShape(s *Session, wsID string) string {
	ws := s.WorkspaceByID(wsID)
	if ws == nil {
		return "<gone>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "tab=%d\n", ws.ActiveTabIndex())
	for j, tab := range ws.Tabs {
		fmt.Fprintf(&b, "  tab%d num=%d zoom=%t panes=%v focus=%d\n",
			j, tab.Number, tab.Zoomed, tab.Layout.PaneIDs(), tab.Layout.Focused())
	}
	return b.String()
}

// An unaddressed pane command resolves against the VIEW's focused pane, not the
// session's — the split that used to land in the wrong project.
func TestUnaddressedTargetResolvesThroughTheView(t *testing.T) {
	s, ws1, ws2 := twoWSSession(t)
	p1, _ := s.FocusedPaneIn(ws1)
	p2, _ := s.FocusedPaneIn(ws2)
	if p1 == p2 {
		t.Fatal("test setup: the two workspaces share a focused pane")
	}

	got1, err := s.ResolvePaneTargetIn(ws1, nil)
	if err != nil || got1 != p1 {
		t.Fatalf("ResolvePaneTargetIn(%s) = %d (%v), want %d", ws1, got1, err, p1)
	}
	got2, err := s.ResolvePaneTargetIn(ws2, nil)
	if err != nil || got2 != p2 {
		t.Fatalf("ResolvePaneTargetIn(%s) = %d (%v), want %d", ws2, got2, err, p2)
	}
}

// A view pointing at a workspace that has been closed falls back to the active
// one rather than erroring: a window must not start refusing every command
// because another window closed what it was showing.
func TestStaleViewFallsBackToTheActiveWorkspace(t *testing.T) {
	s, ws1, ws2 := twoWSSession(t)
	if err := s.CloseWorkspace(&ws2); err != nil {
		t.Fatalf("CloseWorkspace: %v", err)
	}

	if got := s.ResolveViewWorkspace(ws2); got != ws1 {
		t.Fatalf("a stale view resolves to %q, want the fallback %q", got, ws1)
	}
	if _, ok := s.FocusedPaneIn(ws2); !ok {
		t.Fatal("a stale view has no focused pane; it should fall back to the active workspace's")
	}
	if len(s.VisiblePaneIDsIn(ws2)) == 0 {
		t.Fatal("a stale view streams nothing")
	}
}

// --- 3. the dispatcher routes through the view -----------------------------------

// A dispatcher built for a window acts in that window's workspace even though
// the session's active one is elsewhere. This is the end-to-end shape of the
// whole feature, in the layer that has no windows in it.
func TestDispatcherActsInItsView(t *testing.T) {
	s, ws1, ws2 := twoWSSession(t)
	log := &[]string{}
	b := &fakeBackend{log: log, area: layout.Rect{Width: 120, Height: 32}, paneExists: true, daemonUp: true,
		view: View{WorkspaceID: ws2}}
	d := NewDispatcherFor(s, b, View{WorkspaceID: ws2})

	before := workspaceShape(s, ws1)
	r := &fakeResponder{log: log, wants: true}
	d.Dispatch(CmdTabFocus, params(t, TabParams{Num: 1}), r)
	if r.failCall {
		t.Fatalf("tab.focus failed: %s", r.errMsg)
	}

	if got := workspaceShape(s, ws1); got != before {
		t.Fatalf("a command from the %s window changed %s", ws2, ws1)
	}
	if got := s.WorkspaceByID(ws2).ActiveTabIndex(); got != 0 {
		t.Fatalf("%s active tab = %d, want the tab the command named", ws2, got)
	}
	if s.ActiveWorkspaceID() != ws1 {
		t.Fatalf("the session default moved to %s", s.ActiveWorkspaceID())
	}
}

// A view-less dispatcher is unchanged: it acts on the session default.
func TestViewLessDispatcherActsOnTheSessionDefault(t *testing.T) {
	s, ws1, ws2 := twoWSSession(t)
	log := &[]string{}
	b := &fakeBackend{log: log, area: layout.Rect{Width: 120, Height: 32}, paneExists: true, daemonUp: true}
	d := NewDispatcher(s, b)

	before := workspaceShape(s, ws2)
	r := &fakeResponder{log: log, wants: true}
	d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitV}), r)
	if r.failCall {
		t.Fatalf("pane.split failed: %s", r.errMsg)
	}

	if got := workspaceShape(s, ws2); got != before {
		t.Fatalf("a view-less split landed in %s", ws2)
	}
	if len(s.WorkspaceByID(ws1).Tabs[0].Layout.PaneIDs()) != 3 {
		t.Fatalf("the split did not land in the session default %s", ws1)
	}
}

// workspace.focus is a view move, not a session mutation: the dispatcher asks
// the backend to move the issuing window and leaves s.active to the runtime's
// primary-view bookkeeping.
func TestWorkspaceFocusMovesTheViewNotTheSession(t *testing.T) {
	s, ws1, ws2 := twoWSSession(t)
	log := &[]string{}
	b := &fakeBackend{log: log, area: layout.Rect{Width: 120, Height: 32}, paneExists: true, daemonUp: true,
		view: View{WorkspaceID: ws1}}
	d := NewDispatcherFor(s, b, View{WorkspaceID: ws1})

	r := &fakeResponder{log: log, wants: true}
	d.Dispatch(CmdWorkspaceFocus, params(t, WorkspaceParams{ID: ws2}), r)
	if r.failCall {
		t.Fatalf("workspace.focus failed: %s", r.errMsg)
	}

	if len(b.viewMoves) != 1 || b.viewMoves[0] != ws2 {
		t.Fatalf("view moves = %v, want one move to %s", b.viewMoves, ws2)
	}
	if s.ActiveWorkspaceID() != ws1 {
		t.Fatalf("workspace.focus moved the session default to %s", s.ActiveWorkspaceID())
	}

	// An unknown workspace is still an error — the view must not be pointed at
	// something that does not exist by a command the user typed.
	r = &fakeResponder{log: log, wants: true}
	d.Dispatch(CmdWorkspaceFocus, params(t, WorkspaceParams{ID: "w-nope"}), r)
	if !r.failCall {
		t.Fatal("workspace.focus accepted an unknown workspace")
	}

	// The empty id is not an unknown workspace: it is "no workspace", which
	// clears the pin and puts the view back to following the primary. It has to
	// reach the backend, because only the backend knows which connection asked.
	r = &fakeResponder{log: log, wants: true}
	d.Dispatch(CmdWorkspaceFocus, params(t, WorkspaceParams{}), r)
	if r.failCall {
		t.Fatalf("workspace.focus with no id failed: %s", r.errMsg)
	}
	if len(b.viewMoves) != 2 || b.viewMoves[1] != "" {
		t.Fatalf("view moves = %v, want a second move to \"\" (un-pin)", b.viewMoves)
	}
	if s.ActiveWorkspaceID() != ws1 {
		t.Fatalf("un-pinning moved the session default to %s", s.ActiveWorkspaceID())
	}
}

// --- 4. the drift guard ----------------------------------------------------------

// viewportImplicit names the Session methods that resolve their workspace from
// s.active. Every one of them has an explicit "…In" twin, and the dispatcher
// must use the twin: reaching for the default form is how a command silently
// starts acting on the primary window's workspace instead of the one whose user
// typed it — a bug with no error message and no failing test of its own.
//
// This reads the dispatcher's own source, in the spirit of
// TestCommandSpecsRouted: the next command added has to answer the question.
var viewportImplicit = map[string]string{
	"FocusedPane":        "FocusedPaneIn(d.ws())",
	"VisiblePaneIDs":     "VisiblePaneIDsIn(d.ws())",
	"ActiveWorkspace":    "d.viewWorkspace()",
	"ActiveIndex":        "d.viewWorkspace()",
	"ActiveWorkspaceID":  "d.viewWorkspaceID()",
	"FocusPane":          "FocusPaneView + Backend.SetViewWorkspace",
	"RevealPane":         "RevealPaneView + Backend.SetViewWorkspace",
	"FocusWorkspace":     "Backend.SetViewWorkspace",
	"FocusPaneDirection": "FocusPaneDirectionIn(d.ws(), …)",
	"SwapPaneDirection":  "SwapPaneDirectionIn(d.ws(), …)",
	"SwapPanes":          "SwapPanesIn(d.ws(), …)",
	"CyclePane":          "CyclePaneIn(d.ws(), …)",
	"FocusLastPane":      "FocusLastPaneIn(d.ws())",
	"ToggleZoom":         "ToggleZoomIn(d.ws(), …)",
	"ResizeBorder":       "ResizeBorderIn(d.ws(), …)",
	"SplitPane":          "SplitPaneOnIn(d.ws(), …)",
	"SplitPaneOn":        "SplitPaneOnIn(d.ws(), …)",
	"SplitPaneWith":      "SplitPaneWithIn(d.ws(), …)",
	"ClosePane":          "ClosePaneIn(d.ws(), …)",
	"CreateTab":          "CreateTabInOn(targetWS, …)",
	"CloseTab":           "CloseTabIn(d.ws(), …)",
	"FocusTab":           "FocusTabIn(d.ws(), …)",
	"RenameTab":          "RenameTabIn(d.ws(), …)",
	"MoveTab":            "MoveTabIn(d.ws(), …)",
	"ResolvePaneTarget":  "ResolvePaneTargetIn(d.ws(), …)",
	"Info":               "InfoIn(d.ws())",
	"ListWorkspaces":     "ListWorkspacesIn(d.ws())",
	"ListTabs":           "ListTabsIn(d.ws(), …)",
	"ListPanes":          "ListPanesIn(d.ws())",
	"PaneInfoFor":        "PaneInfoForIn(d.ws(), …)",
}

func TestDispatcherUsesNoViewportImplicitSessionCall(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "commands.go", nil, 0)
	if err != nil {
		t.Fatalf("parse commands.go: %v", err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// Match `d.session.<Name>` exactly: an inner selector whose base is the
		// identifier `d` and whose field is `session`.
		inner, ok := sel.X.(*ast.SelectorExpr)
		if !ok || inner.Sel.Name != "session" {
			return true
		}
		if id, ok := inner.X.(*ast.Ident); !ok || id.Name != "d" {
			return true
		}
		if want, bad := viewportImplicit[sel.Sel.Name]; bad {
			t.Errorf("commands.go calls d.session.%s, whose workspace is implicit (s.active). "+
				"A command must act in the window that issued it — use %s instead.",
				sel.Sel.Name, want)
		}
		return true
	})
}

// --- moving a tab between workspaces ----------------------------------------------

// A tab travels with its panes and their terminals. Pane ids are unchanged —
// they are session-global — while the tab's number and its panes' public
// handles are per workspace and therefore reissued on arrival.
func TestMoveTabToAnotherWorkspace(t *testing.T) {
	s, ws1, ws2 := twoWSSession(t)
	src := s.WorkspaceByID(ws1)
	// Give ws1 a second tab so moving one does not empty it.
	if _, _, err := s.CreateTabIn(ws1); err != nil {
		t.Fatalf("CreateTabIn: %v", err)
	}
	moving := src.Tabs[1]
	num, _ := src.PublicTabNumber(1)
	panes := append([]layout.PaneID(nil), moving.Layout.PaneIDs()...)
	dstTabsBefore := len(s.WorkspaceByID(ws2).Tabs)

	newNum, err := s.MoveTabTo(ws1, num, ws2)
	if err != nil {
		t.Fatalf("MoveTabTo: %v", err)
	}

	dst := s.WorkspaceByID(ws2)
	if len(dst.Tabs) != dstTabsBefore+1 {
		t.Fatalf("destination has %d tabs, want %d", len(dst.Tabs), dstTabsBefore+1)
	}
	if len(src.Tabs) != 1 {
		t.Fatalf("source kept %d tabs, want 1", len(src.Tabs))
	}
	if dst.Tabs[len(dst.Tabs)-1] != moving {
		t.Fatal("the destination did not receive the tab itself")
	}
	// The destination switches to it: moving a tab somewhere is how you say you
	// want to work on it there.
	if got, _ := dst.PublicTabNumber(dst.ActiveTabIndex()); got != newNum {
		t.Fatalf("destination active tab = %d, want the arriving %d", got, newNum)
	}
	// Pane ids survive; their public handles are reissued in the new workspace.
	for _, id := range panes {
		if s.PaneWorkspace(id) != dst {
			t.Fatalf("pane %d did not travel with its tab", id)
		}
		pub, ok := dst.PublicPaneID(id)
		if !ok || !strings.HasPrefix(pub, ws2+":") {
			t.Fatalf("pane %d public handle = %q, want one in %s", id, pub, ws2)
		}
		if _, stale := src.PublicPaneID(id); stale {
			t.Fatalf("pane %d still has a handle in the workspace it left", id)
		}
	}
}

// Moving a workspace's last tab empties it, and an empty workspace is dropped —
// the same rule tab.close follows.
func TestMoveLastTabDropsTheEmptiedWorkspace(t *testing.T) {
	s, ws1, ws2 := twoWSSession(t)
	src := s.WorkspaceByID(ws1)
	if len(src.Tabs) != 1 {
		t.Fatalf("test setup: %s has %d tabs, want 1", ws1, len(src.Tabs))
	}
	num, _ := src.PublicTabNumber(0)

	if _, err := s.MoveTabTo(ws1, num, ws2); err != nil {
		t.Fatalf("MoveTabTo: %v", err)
	}
	if s.WorkspaceByID(ws1) != nil {
		t.Fatalf("%s was emptied but not dropped", ws1)
	}
	if s.ActiveWorkspaceID() != ws2 {
		t.Fatalf("active workspace = %q after the source was dropped, want %q",
			s.ActiveWorkspaceID(), ws2)
	}
}

func TestMoveTabToRefusals(t *testing.T) {
	s, ws1, ws2 := twoWSSession(t)
	num, _ := s.WorkspaceByID(ws1).PublicTabNumber(0)

	if _, err := s.MoveTabTo(ws1, num, "w-nope"); err == nil {
		t.Error("moving to an unknown workspace should fail")
	}
	if _, err := s.MoveTabTo(ws1, num, ws1); err == nil {
		t.Error("moving a tab into its own workspace should fail")
	}
	if _, err := s.MoveTabTo(ws1, 999, ws2); err == nil {
		t.Error("moving an unknown tab should fail")
	}

	// The last tab of the last workspace has nowhere to go, and emptying it
	// would leave nothing to be in.
	single := newTestSession(t)
	only := single.Workspaces()[0].ID
	if _, err := single.MoveTabTo(only, 1, only); err == nil {
		t.Error("the last tab of the last workspace should not be movable")
	}
}

// The command defaults its source to the issuing window's workspace: dragging a
// tab out of a window's own strip is the case it exists for.
func TestDispatchMoveTabToWorkspaceDefaultsToTheView(t *testing.T) {
	s, ws1, ws2 := twoWSSession(t)
	if _, _, err := s.CreateTabIn(ws1); err != nil {
		t.Fatalf("CreateTabIn: %v", err)
	}
	num, _ := s.WorkspaceByID(ws1).PublicTabNumber(1)

	log := &[]string{}
	b := &fakeBackend{log: log, area: layout.Rect{Width: 120, Height: 32}, paneExists: true, daemonUp: true,
		view: View{WorkspaceID: ws1}}
	// The session default is ws1 here, so aim the view at it explicitly and move
	// the tab to ws2 — proving the source came from the view and the
	// destination from the params.
	d := NewDispatcherFor(s, b, View{WorkspaceID: ws1})
	r := &fakeResponder{log: log, wants: true}
	d.Dispatch(CmdTabMoveToWorkspace, params(t, MoveTabToWorkspaceParams{Workspace: ws2, Num: num}), r)
	if r.failCall {
		t.Fatalf("tab.move_to_workspace failed: %s", r.errMsg)
	}
	if len(s.WorkspaceByID(ws2).Tabs) != 3 {
		t.Fatalf("%s has %d tabs, want 3", ws2, len(s.WorkspaceByID(ws2).Tabs))
	}

	// A missing destination is a refusal, not a default: there is no sensible
	// workspace to guess at for a move.
	r = &fakeResponder{log: log, wants: true}
	d.Dispatch(CmdTabMoveToWorkspace, params(t, MoveTabToWorkspaceParams{Num: num}), r)
	if !r.failCall {
		t.Error("tab.move_to_workspace without a destination should fail")
	}
}
