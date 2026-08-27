package app

import (
	"strings"
	"testing"
)

// These tests drive the read-only §7 query commands (session.get, *.list,
// pane.get) through the same dispatcher + harness as the mutating commands. A
// query must answer straight from the Session and run NO backend effect, so
// every case also asserts the effect log holds only the responder's "ok".

// dataAs decodes a responder's OK payload into T via its concrete type. The
// dispatcher hands the responder the typed result struct directly (no JSON round
// trip), so a type assertion is enough.
func okData[T any](t *testing.T, r *fakeResponder) T {
	t.Helper()
	if !r.okCall || r.failCall {
		t.Fatalf("query should ack ok: ok=%v fail=%v (%q)", r.okCall, r.failCall, r.errMsg)
	}
	v, ok := r.data.(T)
	if !ok {
		t.Fatalf("result type = %T, want %T", r.data, *new(T))
	}
	return v
}

// wantOnlyOK asserts the query drove no backend effect — the log is just "ok".
func wantOnlyOK(t *testing.T, log []string) {
	t.Helper()
	if len(log) != 1 || log[0] != "ok" {
		t.Fatalf("a query must run no effects, log = %v", log)
	}
}

// session.get snapshots the whole session: one workspace, one pane, the focused
// pane's public handle, and the session cwd.
func TestDispatchSessionGet(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdSessionGet, noParams(), r)

	got := okData[SessionInfoResult](t, r)
	wantOnlyOK(t, *h.log)
	if got.ActiveWorkspace != h.s.ActiveWorkspace().ID {
		t.Fatalf("active workspace = %q, want %q", got.ActiveWorkspace, h.s.ActiveWorkspace().ID)
	}
	if got.Workspaces != 1 || got.Panes != 1 {
		t.Fatalf("counts = %d workspaces / %d panes, want 1/1", got.Workspaces, got.Panes)
	}
	if got.Cwd != h.s.Cwd() {
		t.Fatalf("cwd = %q, want %q", got.Cwd, h.s.Cwd())
	}
	focused, _ := h.s.FocusedPane()
	wantHandle, _ := h.s.PublicPaneID(focused)
	if got.FocusedPane != wantHandle {
		t.Fatalf("focused pane = %q, want %q", got.FocusedPane, wantHandle)
	}
}

// workspace.list reflects added workspaces and marks exactly the active one.
func TestDispatchWorkspaceList(t *testing.T) {
	h := newCmdHarness(t)
	if _, err := h.s.CreateWorkspace(); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	r := h.resp()

	h.d.Dispatch(CmdWorkspaceList, noParams(), r)

	got := okData[WorkspaceListResult](t, r)
	wantOnlyOK(t, *h.log)
	if len(got.Workspaces) != 2 {
		t.Fatalf("workspaces = %d, want 2", len(got.Workspaces))
	}
	active := 0
	for i, ws := range got.Workspaces {
		if ws.ID != h.s.Workspaces()[i].ID {
			t.Fatalf("workspace[%d] id = %q, want %q", i, ws.ID, h.s.Workspaces()[i].ID)
		}
		if ws.Active {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("exactly one workspace must be active, got %d", active)
	}
	if !got.Workspaces[h.s.ActiveIndex()].Active {
		t.Fatalf("the active-index workspace must carry the active flag")
	}
}

// tab.list defaults to the active workspace and echoes its resolved id; after a
// tab.create it lists both tabs and marks the active one.
func TestDispatchTabListDefault(t *testing.T) {
	h := newCmdHarness(t)
	if _, err := h.s.CreateTab(); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	r := h.resp()

	h.d.Dispatch(CmdTabList, noParams(), r)

	got := okData[TabListResult](t, r)
	wantOnlyOK(t, *h.log)
	if got.Workspace != h.s.ActiveWorkspace().ID {
		t.Fatalf("resolved workspace = %q, want %q", got.Workspace, h.s.ActiveWorkspace().ID)
	}
	if len(got.Tabs) != 2 {
		t.Fatalf("tabs = %d, want 2", len(got.Tabs))
	}
	activeIdx := h.s.ActiveWorkspace().ActiveTabIndex()
	if !got.Tabs[activeIdx].Active {
		t.Fatalf("the active tab must carry the active flag")
	}
}

// tab.list for an unknown workspace fails without touching the backend.
func TestDispatchTabListUnknownWorkspace(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdTabList, params(t, TabListParams{Workspace: "wZZ"}), r)

	if !r.failCall || r.okCall {
		t.Fatalf("unknown workspace must fail, ok=%v fail=%v", r.okCall, r.failCall)
	}
}

// pane.list enumerates every pane with its addressing id + public handle, and
// marks the focused/visible panes after a split.
func TestDispatchPaneList(t *testing.T) {
	h := newCmdHarness(t)
	// A split gives us a two-pane viewport; run it through the dispatcher, then
	// list on a fresh responder so the query's own effect log is clean.
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH}), h.resp())
	*h.log = (*h.log)[:0]

	r := h.resp()
	h.d.Dispatch(CmdPaneList, noParams(), r)

	got := okData[PaneListResult](t, r)
	wantOnlyOK(t, *h.log)
	if len(got.Panes) != 2 {
		t.Fatalf("panes = %d, want 2", len(got.Panes))
	}
	focused, _ := h.s.FocusedPane()
	var focusedSeen, visible int
	for _, p := range got.Panes {
		if p.Handle == "" {
			t.Fatalf("pane %d missing public handle", p.Pane)
		}
		if p.Visible {
			visible++
		}
		if p.Pane == uint32(focused) {
			if !p.Focused {
				t.Fatalf("the session-focused pane must report focused=true")
			}
			focusedSeen++
		}
	}
	if focusedSeen != 1 {
		t.Fatalf("exactly one listed pane must match the focused pane, got %d", focusedSeen)
	}
	if visible != 2 {
		t.Fatalf("both split panes should be visible, got %d", visible)
	}
}

// pane.get with no params returns the focused pane; with an explicit id returns
// that pane; an unknown id fails.
func TestDispatchPaneGet(t *testing.T) {
	h := newCmdHarness(t)
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitV}), h.resp())
	*h.log = (*h.log)[:0]

	// Default target ⇒ the focused pane.
	r := h.resp()
	h.d.Dispatch(CmdPaneGet, noParams(), r)
	got := okData[PaneInfo](t, r)
	wantOnlyOK(t, *h.log)
	focused, _ := h.s.FocusedPane()
	if got.Pane != uint32(focused) || !got.Focused {
		t.Fatalf("default pane.get = %+v, want focused pane %d", got, focused)
	}

	// Explicit id ⇒ that pane. Pick a pane that is not the focused one.
	var other uint32
	for _, id := range h.s.AllPaneIDs() {
		if id != focused {
			other = uint32(id)
			break
		}
	}
	*h.log = (*h.log)[:0]
	r = h.resp()
	h.d.Dispatch(CmdPaneGet, params(t, OptPaneParams{Pane: &other}), r)
	got = okData[PaneInfo](t, r)
	if got.Pane != other {
		t.Fatalf("explicit pane.get = %d, want %d", got.Pane, other)
	}

	// Unknown id ⇒ fail.
	bogus := uint32(9999)
	r = h.resp()
	h.d.Dispatch(CmdPaneGet, params(t, OptPaneParams{Pane: &bogus}), r)
	if !r.failCall || r.okCall {
		t.Fatalf("unknown pane.get must fail, ok=%v fail=%v", r.okCall, r.failCall)
	}
}

// flag.list is the flagged subset of workspace.list and pane.list, in those
// lists' order, with every unflagged row dropped.
func TestDispatchFlagList(t *testing.T) {
	h := newCmdHarness(t)
	h.d.Dispatch(CmdPaneSplit, params(t, SplitParams{Direction: SplitH}), h.resp())

	// Flag the active workspace and exactly one of the two panes, so the result
	// proves it FILTERS rather than merely echoing the two lists.
	h.d.Dispatch(CmdWorkspaceFlag, params(t, FlagWorkspaceParams{Kind: "warn", Note: "flaky here"}), h.resp())
	focused, _ := h.s.FocusedPane()
	h.d.Dispatch(CmdPaneFlag, params(t, FlagPaneParams{Pane: uint32(focused), Kind: "followup", Note: "come back"}), h.resp())
	*h.log = (*h.log)[:0]

	r := h.resp()
	h.d.Dispatch(CmdFlagList, noParams(), r)
	got := okData[FlagListResult](t, r)
	wantOnlyOK(t, *h.log)

	if len(got.Workspaces) != 1 || got.Workspaces[0].Flag != "warn" || got.Workspaces[0].FlagNote != "flaky here" {
		t.Fatalf("workspaces = %+v, want the one warn-flagged workspace", got.Workspaces)
	}
	if len(got.Panes) != 1 {
		t.Fatalf("panes = %+v, want only the flagged one of two", got.Panes)
	}
	p := got.Panes[0]
	if p.Pane != uint32(focused) || p.Flag != "followup" || p.FlagNote != "come back" {
		t.Fatalf("pane row = %+v, want the followup flag on pane %d", p, focused)
	}
	if p.FlagAtMs == 0 {
		t.Fatal("the row must carry when the flag was set")
	}
	// The rows are the SAME rows the per-scope lists produce: a client must not
	// have to reconcile two descriptions of one pane.
	if p.Handle == "" {
		t.Fatal("a flagged pane row must carry its public handle, as pane.list does")
	}

	// A kind narrows it, and the narrowing applies to both scopes.
	r = h.resp()
	h.d.Dispatch(CmdFlagList, params(t, FlagListParams{Kind: "followup"}), r)
	got = okData[FlagListResult](t, r)
	if len(got.Workspaces) != 0 || len(got.Panes) != 1 {
		t.Fatalf("filtered by followup = %d workspaces / %d panes, want 0/1",
			len(got.Workspaces), len(got.Panes))
	}
	// Empty rather than null: a client loops over these without a nil check.
	if got.Workspaces == nil || got.Panes == nil {
		t.Fatal("both slices must be non-nil even when empty")
	}
}

// An unknown kind is REFUSED rather than answered with an empty listing — the
// one wrong answer a listing can give, because it is indistinguishable from the
// truthful one.
func TestDispatchFlagListRejectsUnknownKind(t *testing.T) {
	h := newCmdHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdFlagList, params(t, FlagListParams{Kind: "folloup"}), r)

	if !r.failCall || !strings.Contains(r.errMsg, "unknown flag kind") {
		t.Fatalf("fail=%v msg=%q, want a bad-params error naming the unknown kind", r.failCall, r.errMsg)
	}
	if len(*h.log) != 1 || (*h.log)[0] != "fail" {
		t.Fatalf("a params failure must run no effects, log=%v", *h.log)
	}
}

// A custom glyph filters exactly like a named kind: both halves of the
// vocabulary are one string field, so the listing needs no second code path.
func TestDispatchFlagListFiltersCustomGlyph(t *testing.T) {
	h := newCmdHarness(t)
	focused, _ := h.s.FocusedPane()
	h.d.Dispatch(CmdPaneFlag, params(t, FlagPaneParams{Pane: uint32(focused), Kind: "🍕", Note: "lunch build"}), h.resp())

	r := h.resp()
	h.d.Dispatch(CmdFlagList, params(t, FlagListParams{Kind: "🍕"}), r)
	got := okData[FlagListResult](t, r)
	if len(got.Panes) != 1 || got.Panes[0].Flag != "🍕" {
		t.Fatalf("panes = %+v, want the one 🍕-flagged pane", got.Panes)
	}

	// And a different glyph matches nothing, rather than matching every custom.
	r = h.resp()
	h.d.Dispatch(CmdFlagList, params(t, FlagListParams{Kind: "🌮"}), r)
	if got := okData[FlagListResult](t, r); len(got.Panes) != 0 {
		t.Fatalf("filtering on another glyph returned %+v", got.Panes)
	}
}
