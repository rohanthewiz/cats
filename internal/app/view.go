package app

// Views: the per-window half of the viewport.
//
// A session used to have exactly one viewport — one active workspace, and
// through it one active tab and one focused pane — shared by every connected
// client. That made a second window a second *view of the same thing*: it
// followed the first window's workspace switches, fought it over pane sizes,
// and typed into whichever pane the first window had focused.
//
// A View is the fix, and it is deliberately not a session concept: it is the
// workspace one caller is looking at, passed down from whoever holds the
// window (a WebSocket connection, in catway's case). The session keeps its
// `active` workspace as the *default* for callers with no window — catctl, a
// hook action, a runbook step — and as the thing persistence means by "where
// you were".
//
//	window A ──► View{"w1"} ─┐
//	window B ──► View{"w2"} ─┼─► Dispatcher ──► Session (workspaces/tabs/panes)
//	catctl    ──► View{""}   ─┘                     ▲
//	                                                └─ "" resolves to s.active
//
// Every Session method that used to read s.active implicitly has an explicit
// "…In(workspaceID)" form here; the original keeps its name and delegates with
// "" so a view-less caller behaves exactly as before. That is the same shape
// the mobile/remote work introduced for CreateTabIn / RenameTabIn — finished.

import (
	"errors"
	"fmt"
	"slices"

	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// View is the viewport a command was issued from: which workspace the issuing
// window is showing. The zero value ("" workspace) is the view-less caller,
// which resolves to the session's active workspace at every use.
//
// It is a struct rather than a bare string so the per-view facts a later phase
// wants (its own grid, its own focus) can join it without touching every
// constructor. Copied by value — a dispatcher must never be able to mutate the
// window's state behind the backend's back; moving a view is a Backend effect
// (SetViewWorkspace), which is the seam that knows *which* window asked.
type View struct {
	// WorkspaceID is the public workspace id ("w2") this window shows, or ""
	// for "whatever the session's active workspace is".
	WorkspaceID string
}

// --- workspace resolution -----------------------------------------------------

// viewWorkspace resolves the workspace a view names, falling back to the active
// one for "" and — deliberately — for an id that no longer exists.
//
// The fallback is not laziness: a window's workspace can be closed out from
// under it by another window (decision 8 says closing a *window* never mutates
// the session, but closing a *workspace* is a normal command), and the right
// behaviour for the orphaned window is to show something rather than to start
// refusing every command with "unknown workspace w4". Commands that must
// validate a user-supplied workspace id still do so themselves — this resolves
// a view, not a parameter.
func (s *Session) viewWorkspace(wsID string) *workspace.Workspace {
	return s.workspaces[s.viewWorkspaceIndex(wsID)]
}

// viewWorkspaceIndex is viewWorkspace's index half, for the queries that report
// "is this the active one".
//
// A sleeping workspace resolves like a closed one: it has nothing to show (its
// one pane has no terminal), so a window still pointing at it after a sleep
// from elsewhere falls back to the active workspace rather than driving a
// pane that does not run.
func (s *Session) viewWorkspaceIndex(wsID string) int {
	if wsID == "" {
		return s.active
	}
	if i, ok := s.workspaceIndexByID(wsID); ok && !s.workspaces[i].Asleep {
		return i
	}
	return s.active
}

// ResolveViewWorkspace reports the workspace id a view actually shows — the
// view's own id when it names a live workspace, else the session's active one.
// The runtime uses it to build a view's layout and to answer "which workspace
// is this window on" in the client census.
func (s *Session) ResolveViewWorkspace(wsID string) string {
	return s.viewWorkspace(wsID).ID
}

// WorkspaceIDAt is the public id of the workspace at an index, "" when out of
// range — the reverse of workspaceIndexByID, for the runtime's primary-view
// bookkeeping (s.active → a view's workspace id).
func (s *Session) WorkspaceIDAt(idx int) string {
	if idx < 0 || idx >= len(s.workspaces) {
		return ""
	}
	return s.workspaces[idx].ID
}

// ActiveWorkspaceID is the session-default workspace's public id — what a
// view-less caller is looking at, and what persistence restores to.
func (s *Session) ActiveWorkspaceID() string { return s.ActiveWorkspace().ID }

// --- viewport queries ---------------------------------------------------------

// FocusedPaneIn resolves one view's focused pane: its workspace's active tab's
// focused leaf. This is the composition Session.FocusedPane has always done,
// with the workspace supplied instead of assumed.
func (s *Session) FocusedPaneIn(wsID string) (layout.PaneID, bool) {
	return s.viewWorkspace(wsID).FocusedPaneID()
}

// VisiblePaneIDsIn lists the panes one view streams: its workspace's active
// tab's panes, or just the focused pane when that tab is zoomed. The session's
// visible set is now the union of these over every view (decision 6), which the
// runtime composes — the model has no idea how many windows there are.
func (s *Session) VisiblePaneIDsIn(wsID string) []layout.PaneID {
	tab := s.viewWorkspace(wsID).ActiveTab()
	if tab == nil {
		return nil
	}
	if tab.Zoomed {
		return []layout.PaneID{tab.Layout.Focused()}
	}
	return tab.Layout.PaneIDs()
}

// ResolvePaneTargetIn is ResolvePaneTarget for a view: the named pane, or the
// view's focused pane when target is nil.
func (s *Session) ResolvePaneTargetIn(wsID string, target *layout.PaneID) (layout.PaneID, error) {
	if target != nil {
		if _, ws := s.workspaceIndexOf(*target); ws == nil {
			return 0, fmt.Errorf("unknown pane %d", *target)
		}
		return *target, nil
	}
	id, ok := s.FocusedPaneIn(wsID)
	if !ok {
		return 0, errors.New("no focused pane")
	}
	return id, nil
}

// --- focus, without moving the session's active workspace ---------------------

// FocusPaneView focuses a pane within its owning tab and reports the id of the
// workspace that owns it, WITHOUT touching s.active.
//
// It is the half of FocusPane that is genuinely session state (which pane its
// tab considers focused); which *window* now shows that workspace is the
// caller's business, and the caller is the only one that knows whether it has a
// window at all. FocusPane keeps the old behaviour by doing both.
func (s *Session) FocusPaneView(id layout.PaneID) (string, error) {
	_, ws := s.workspaceIndexOf(id)
	if ws == nil {
		return "", fmt.Errorf("unknown pane %d", id)
	}
	tabIdx, _ := ws.FindTabIndexForPane(id)
	ws.Tabs[tabIdx].Layout.FocusPane(id)
	return ws.ID, nil
}

// RevealPaneView is FocusPaneView with RevealPane's extra reach: it also
// switches the owning workspace to the pane's tab, so a pane in a background
// tab becomes visible. Like FocusPaneView it leaves s.active alone and returns
// the workspace the caller's view must move to in order to see it.
func (s *Session) RevealPaneView(id layout.PaneID) (string, error) {
	_, ws := s.workspaceIndexOf(id)
	if ws == nil {
		return "", fmt.Errorf("unknown pane %d", id)
	}
	tabIdx, _ := ws.FindTabIndexForPane(id)
	ws.SwitchTab(tabIdx)
	ws.Tabs[tabIdx].Layout.FocusPane(id)
	return ws.ID, nil
}

// FocusPaneDirectionIn is FocusPaneDirection scoped to a view's workspace.
func (s *Session) FocusPaneDirectionIn(wsID string, nav layout.NavDirection, area layout.Rect) (bool, error) {
	tab := s.viewWorkspace(wsID).ActiveTab()
	if tab == nil {
		return false, errors.New("no active tab")
	}
	panes := tab.Layout.Panes(area)
	focused := focusedInfo(panes)
	if focused == nil {
		return false, errors.New("no focused pane")
	}
	target, ok := layout.FindInDirection(focused, nav, panes)
	if !ok {
		return false, nil // no neighbour in that direction
	}
	tab.Layout.FocusPane(target)
	return true, nil
}

// SwapPaneDirectionIn is SwapPaneDirection scoped to a view's workspace.
func (s *Session) SwapPaneDirectionIn(wsID string, nav layout.NavDirection, area layout.Rect) (bool, error) {
	tab := s.viewWorkspace(wsID).ActiveTab()
	if tab == nil {
		return false, errors.New("no active tab")
	}
	panes := tab.Layout.Panes(area)
	focused := focusedInfo(panes)
	if focused == nil {
		return false, errors.New("no focused pane")
	}
	target, ok := layout.FindInDirection(focused, nav, panes)
	if !ok {
		return false, nil
	}
	tab.Layout.SwapPanes(focused.ID, target)
	return true, nil
}

// focusedInfo picks the focused pane out of a laid-out tab's pane list. Both
// directional commands need it and neither owns it.
func focusedInfo(panes []layout.PaneInfo) *layout.PaneInfo {
	for i := range panes {
		if panes[i].IsFocused {
			return &panes[i]
		}
	}
	return nil
}

// SwapPanesIn is SwapPanes scoped to a view's workspace.
func (s *Session) SwapPanesIn(wsID string, a, b layout.PaneID) (bool, error) {
	tab := s.viewWorkspace(wsID).ActiveTab()
	if tab == nil {
		return false, errors.New("no active tab")
	}
	return tab.Layout.SwapPanes(a, b), nil
}

// CyclePaneIn is CyclePane scoped to a view's workspace.
func (s *Session) CyclePaneIn(wsID string, next bool) bool {
	tab := s.viewWorkspace(wsID).ActiveTab()
	if tab == nil {
		return false
	}
	ids := tab.Layout.PaneIDs()
	if len(ids) < 2 {
		return false
	}
	pos := slices.Index(ids, tab.Layout.Focused())
	if pos < 0 {
		pos = 0
	}
	n := len(ids)
	step := 1
	if !next {
		step = -1
	}
	tab.Layout.FocusPane(ids[(pos+step+n)%n])
	return true
}

// FocusLastPaneIn is FocusLastPane scoped to a view's workspace.
func (s *Session) FocusLastPaneIn(wsID string) bool {
	tab := s.viewWorkspace(wsID).ActiveTab()
	if tab == nil {
		return false
	}
	return tab.Layout.FocusLast()
}

// ToggleZoomIn is ToggleZoom scoped to a view's workspace.
func (s *Session) ToggleZoomIn(wsID string, target *layout.PaneID) (bool, error) {
	tab := s.viewWorkspace(wsID).ActiveTab()
	if tab == nil {
		return false, errors.New("no active tab")
	}
	if !tab.Zoomed && target != nil {
		if !slices.Contains(tab.Layout.PaneIDs(), *target) {
			return false, fmt.Errorf("pane %d not in the active tab", *target)
		}
		tab.Layout.FocusPane(*target)
	}
	tab.Zoomed = !tab.Zoomed
	return tab.Zoomed, nil
}

// ResizeBorderIn is ResizeBorder scoped to a view's workspace.
func (s *Session) ResizeBorderIn(wsID string, path []bool, ratio float32) error {
	tab := s.viewWorkspace(wsID).ActiveTab()
	if tab == nil {
		return errors.New("no active tab")
	}
	tab.Layout.SetRatioAt(path, ratio)
	return nil
}

// --- pane lifecycle -----------------------------------------------------------

// SplitPaneWithIn is SplitPaneWith with the view supplied: a nil target means
// "the pane this window has focused", not "the pane the session has focused".
// Once a target is resolved the split itself is workspace-agnostic — the new
// pane lands beside its sibling, wherever that lives.
func (s *Session) SplitPaneWithIn(wsID string, target *layout.PaneID, dir layout.Direction, spec workspace.SpawnSpec) (layout.PaneID, error) {
	id, err := s.ResolvePaneTargetIn(wsID, target)
	if err != nil {
		return 0, err
	}
	_, ws := s.workspaceIndexOf(id)
	// The only pane a sleeping workspace has is its placeholder, and a split
	// off it would be a second pane with no terminal (see CreateTabInWith).
	if ws.Asleep {
		return 0, workspaceAsleepErr(ws.ID)
	}
	_, np, err := ws.SplitPane(id, dir, true, spec)
	if err != nil {
		return 0, err
	}
	return np.PaneID, nil
}

// SplitPaneOnIn is SplitPaneOn with the view supplied.
func (s *Session) SplitPaneOnIn(wsID string, target *layout.PaneID, dir layout.Direction, hostID string) (layout.PaneID, error) {
	return s.SplitPaneWithIn(wsID, target, dir, workspace.SpawnSpec{HostID: hostID})
}

// ClosePaneIn is ClosePane with the view supplied.
func (s *Session) ClosePaneIn(wsID string, target *layout.PaneID) (layout.PaneID, error) {
	id, err := s.ResolvePaneTargetIn(wsID, target)
	if err != nil {
		return 0, err
	}
	if s.totalPanes() <= 1 {
		return 0, errors.New("cannot close the last pane")
	}
	idx, ws := s.workspaceIndexOf(id)
	if ws.ClosePane(id) {
		s.dropWorkspace(idx)
	}
	return id, nil
}

// --- tab commands -------------------------------------------------------------

// FocusTabIn is FocusTab scoped to a view's workspace: the window switches
// tabs, and a window on another workspace never notices.
func (s *Session) FocusTabIn(wsID string, num int) error {
	ws := s.viewWorkspace(wsID)
	idx, ok := s.tabIndexByNumber(ws, num)
	if !ok {
		return fmt.Errorf("unknown tab %d", num)
	}
	ws.SwitchTab(idx)
	return nil
}

// CloseTabIn is CloseTab scoped to a view's workspace. Closing the workspace's
// last tab still drops the workspace (when another remains) — the window that
// issued it is left pointing at a workspace that no longer exists, which
// viewWorkspace resolves to the active one, exactly as a window whose workspace
// somebody else closed.
func (s *Session) CloseTabIn(wsID string, num *int) error {
	ws := s.viewWorkspace(wsID)
	idx := ws.ActiveTabIndex()
	if num != nil {
		i, ok := s.tabIndexByNumber(ws, *num)
		if !ok {
			return fmt.Errorf("unknown tab %d", *num)
		}
		idx = i
	}
	if len(ws.Tabs) > 1 {
		ws.CloseTab(idx)
		return nil
	}
	if len(s.workspaces) <= 1 {
		return errors.New("cannot close the last tab")
	}
	wsIdx, _ := s.workspaceIndexByID(ws.ID)
	s.dropWorkspace(wsIdx)
	return nil
}

// MoveTabIn is MoveTab scoped to a view's workspace.
func (s *Session) MoveTabIn(wsID string, num, insertIdx int) (bool, error) {
	ws := s.viewWorkspace(wsID)
	srcIdx, ok := s.tabIndexByNumber(ws, num)
	if !ok {
		return false, fmt.Errorf("unknown tab %d", num)
	}
	if insertIdx < 0 || insertIdx > len(ws.Tabs) {
		return false, fmt.Errorf("bad insert index %d", insertIdx)
	}
	return ws.MoveTab(srcIdx, insertIdx), nil
}

// --- moving a tab between workspaces -------------------------------------------

// MoveTabTo moves a tab from one workspace to another, keeping its panes and
// their terminals exactly as they are. It is what "drag this tab into that
// window" means once a window is a view on a workspace: the tab moves in the
// model, and both windows see it because each is rendering its own workspace.
//
// srcWS/dstWS are public workspace ids; "" means the caller's default view (the
// same rule every …In form follows). num is the tab's public number in the
// SOURCE workspace — tab numbering is per workspace, so the returned number is
// a different one: the tab is renumbered on arrival, as a new tab would be, and
// so are its panes' public ids (see Workspace.AdoptTab for what that costs).
//
// Moving a workspace's LAST tab empties it, and an empty workspace is dropped —
// the same rule CloseTab follows, and for the same reason: a workspace with no
// tabs has nothing to show. The destination switches to the arriving tab, since
// moving a tab somewhere is how you say you want to work on it there.
func (s *Session) MoveTabTo(srcWS string, num int, dstWS string) (int, error) {
	src, dst := s.viewWorkspace(srcWS), s.WorkspaceByID(dstWS)
	if dst == nil {
		return 0, fmt.Errorf("unknown workspace %s", dstWS)
	}
	if src == dst {
		return 0, errors.New("the tab is already in that workspace")
	}
	if dst.Asleep {
		return 0, workspaceAsleepErr(dst.ID)
	}
	idx, ok := s.tabIndexByNumber(src, num)
	if !ok {
		return 0, fmt.Errorf("unknown tab %d", num)
	}
	// The source is about to lose its last tab AND it is the last workspace:
	// there would be nothing left to be in. Refused rather than silently
	// half-done, which is what dropping the workspace would be here.
	if len(src.Tabs) == 1 && len(s.workspaces) <= 1 {
		return 0, errors.New("cannot move the last tab out of the last workspace")
	}
	tab, ok := src.DetachTab(idx)
	if !ok {
		return 0, fmt.Errorf("unknown tab %d", num)
	}
	dstIdx := dst.AdoptTab(tab)
	dst.SwitchTab(dstIdx)
	if len(src.Tabs) == 0 {
		if i, found := s.workspaceIndexByID(src.ID); found {
			s.dropWorkspace(i)
		}
	}
	newNum, _ := dst.PublicTabNumber(dstIdx)
	return newNum, nil
}
