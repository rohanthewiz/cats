// Package app is the WS2 orchestrator's domain layer: the session state (an
// ordered set of workspaces, WS1) plus the command table (§7 of
// ai_docs/phase-c-ws9-protocol.md) that mutates it. It sits ABOVE the daemon
// seam (internal/orchestration) — it owns *what* the session looks like and
// *how* commands change it, never *how* PTYs are driven. That keeps it pure:
// no daemon, no I/O, no goroutines, so it unit-tests like the layout/workspace
// models it composes (the Rust src/app actions are the spec).
//
// The orchestrator runtime (the event-loop actor in cmd/catway) owns exactly
// one Session and is its only caller, so Session needs no synchronization.
package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/rohanthewiz/cats/internal/flags"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// Session is the multi-workspace session state. active indexes the workspace
// whose active tab is the current viewport (§8). All panes across all
// workspaces/tabs are live PTYs on the daemon; only the viewport's panes stream
// frames to the browser.
type Session struct {
	spawner    workspace.PaneSpawner
	cwd        string
	workspaces []*workspace.Workspace
	active     int
}

// NewSession starts a session with one workspace (one tab, one pane).
func NewSession(spawner workspace.PaneSpawner, cwd string) (*Session, error) {
	ws, err := workspace.New(spawner, cwd, workspace.SpawnSpec{})
	if err != nil {
		return nil, err
	}
	return &Session{spawner: spawner, cwd: cwd, workspaces: []*workspace.Workspace{ws}}, nil
}

// --- Queries -----------------------------------------------------------------

// Workspaces returns the ordered workspaces (for BuildLayout / the sidebar).
func (s *Session) Workspaces() []*workspace.Workspace { return s.workspaces }

// ActiveIndex is the active workspace's position.
func (s *Session) ActiveIndex() int { return s.active }

// ActiveWorkspace returns the active workspace.
func (s *Session) ActiveWorkspace() *workspace.Workspace { return s.workspaces[s.active] }

// Cwd is the session's default working directory for new panes.
func (s *Session) Cwd() string { return s.cwd }

// SetCwd replaces the session's default working directory. The runtime calls it
// after restoring a snapshot, whose saved cwd may no longer be usable — an old
// snapshot taken from a GUI launch carries "/", and a directory can be deleted
// between runs (cf. cmd/catway's healStartDirs).
func (s *Session) SetCwd(cwd string) { s.cwd = cwd }

// FocusedPane resolves the session-default view's focused pane: the active
// workspace's active tab's focused leaf. A caller that has a window wants
// FocusedPaneIn instead — with several windows open there is no single
// "focused pane" any more, only the one each window has (see view.go).
func (s *Session) FocusedPane() (layout.PaneID, bool) {
	return s.FocusedPaneIn("")
}

// AllPaneIDs lists every pane across all workspaces and tabs — the panes the
// daemon must hold PTYs for.
func (s *Session) AllPaneIDs() []layout.PaneID {
	var ids []layout.PaneID
	for _, ws := range s.workspaces {
		for _, tab := range ws.Tabs {
			ids = append(ids, tab.Layout.PaneIDs()...)
		}
	}
	return ids
}

// PaneNeighbourhood answers "how close is that pane to this one" as two sets:
// the panes sharing id's tab, and the panes sharing its workspace (the tab set
// included). id itself is in both.
//
// It exists for pane.open_file's "nearest editor wins" rule, which needs the
// distinction and would otherwise rebuild it from Workspaces() and
// FindTabIndexForPane at every call site that ever wants it. An unknown pane
// yields two empty sets rather than an error: "nothing is near it" is the right
// answer for a pane that is not in the model, and every caller of a
// neighbourhood is choosing, not validating.
func (s *Session) PaneNeighbourhood(id layout.PaneID) (tab, workspace map[layout.PaneID]bool) {
	tab, workspace = map[layout.PaneID]bool{}, map[layout.PaneID]bool{}
	ws := s.PaneWorkspace(id)
	if ws == nil {
		return tab, workspace
	}
	own, ok := ws.FindTabIndexForPane(id)
	if !ok {
		return tab, workspace
	}
	for i, t := range ws.Tabs {
		for _, pid := range t.Layout.PaneIDs() {
			workspace[pid] = true
			if i == own {
				tab[pid] = true
			}
		}
	}
	return tab, workspace
}

// VisiblePaneIDs lists panes in the current viewport (active workspace's active
// tab) — the only panes whose frames stream to the browser (§8). A zoomed tab
// shows only its focused pane, so that is the whole viewport.
func (s *Session) VisiblePaneIDs() []layout.PaneID {
	return s.VisiblePaneIDsIn("")
}

// PublicPaneID resolves a pane's public handle ("w1:p3") from whichever
// workspace owns it.
func (s *Session) PublicPaneID(id layout.PaneID) (string, bool) {
	for _, ws := range s.workspaces {
		if pub, ok := ws.PublicPaneID(id); ok {
			return pub, true
		}
	}
	return "", false
}

// PaneByPublicID is the reverse of PublicPaneID: it resolves a public handle
// back to the internal pane id. It accepts the two handle forms panes are given
// in their environment (CATS_PANE_ID): the public "w1:p3" form, and the
// "p_<raw>" fallback that embeds the internal id directly (cats's
// apply_pane_env emits it when no public id is known). Reports false for a
// handle that resolves to no live pane.
func (s *Session) PaneByPublicID(handle string) (layout.PaneID, bool) {
	if raw, ok := strings.CutPrefix(handle, "p_"); ok {
		n, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return 0, false
		}
		id := layout.PaneID(n)
		if _, ws := s.workspaceIndexOf(id); ws == nil {
			return 0, false
		}
		return id, true
	}
	for _, ws := range s.workspaces {
		for _, tab := range ws.Tabs {
			for _, id := range tab.Layout.PaneIDs() {
				if pub, ok := ws.PublicPaneID(id); ok && pub == handle {
					return id, true
				}
			}
		}
	}
	return 0, false
}

// --- Pane commands (§7) ------------------------------------------------------

// FocusPane focuses a pane within its owning tab (browser click-to-focus).
func (s *Session) FocusPane(id layout.PaneID) error {
	wsID, err := s.FocusPaneView(id)
	if err != nil {
		return err
	}
	// The view-less form also moves the session default, which is what a
	// caller with no window means by "focus this": there is one viewport and
	// this is it. A windowed caller uses FocusPaneView and moves its own view.
	if i, ok := s.workspaceIndexByID(wsID); ok {
		s.active = i
	}
	return nil
}

// RevealPane brings a pane into the active viewport and focuses it: it makes the
// pane's owning workspace active, switches that workspace to the pane's tab, and
// focuses the pane within the tab. Unlike FocusPane (click-to-focus, always
// already within the current viewport), RevealPane may cross workspace AND tab
// boundaries — the agents sidebar is global (§8), so agent.focus can target a
// pane the browser cannot currently see.
func (s *Session) RevealPane(id layout.PaneID) error {
	wsID, err := s.RevealPaneView(id)
	if err != nil {
		return err
	}
	if i, ok := s.workspaceIndexByID(wsID); ok {
		s.active = i
	}
	return nil
}

// FocusPaneDirection moves focus to the nearest pane in the given cardinal
// direction within the active tab, resolving neighbours from the viewport
// geometry (area). It reports whether focus actually moved: false with no error
// means no pane lies that way (a no-op). Like FocusPane it stays within the
// current viewport, so it never changes the active workspace/tab.
func (s *Session) FocusPaneDirection(nav layout.NavDirection, area layout.Rect) (bool, error) {
	return s.FocusPaneDirectionIn("", nav, area)
}

// CyclePane moves focus to the next (next=true) or previous pane in the active
// tab's in-order pane list, wrapping around. Reports whether focus moved (false
// only when the tab has a single pane). Like FocusPane it stays within the
// viewport.
func (s *Session) CyclePane(next bool) bool {
	return s.CyclePaneIn("", next)
}

// SwapPaneDirection swaps the focused pane with its nearest neighbour in the
// given direction within the active tab: the focused pane travels to the
// neighbour's slot and keeps focus. Reports whether a swap happened (false with
// no error means no neighbour that way). Needs the viewport geometry to resolve
// the neighbour.
func (s *Session) SwapPaneDirection(nav layout.NavDirection, area layout.Rect) (bool, error) {
	return s.SwapPaneDirectionIn("", nav, area)
}

// SwapPanes exchanges two panes' slots in the active tab's layout (the
// drag-reorder drop), preserving split shape and ratios. Reports whether a
// swap happened; both panes must be distinct members of the active tab.
func (s *Session) SwapPanes(a, b layout.PaneID) (bool, error) {
	return s.SwapPanesIn("", a, b)
}

// ToggleZoom flips the active tab's zoom. When zooming, target (or the focused
// pane if nil) becomes the sole visible pane at full size; when already zoomed,
// it unzooms (target ignored). Reports the resulting zoom state.
func (s *Session) ToggleZoom(target *layout.PaneID) (bool, error) {
	return s.ToggleZoomIn("", target)
}

// ResizeBorder sets the first-child ratio of the split identified by path
// (decoded from the wire border id) in the active tab, changing the sizes of
// the panes either side. A path that resolves to no split is a silent no-op.
func (s *Session) ResizeBorder(path []bool, ratio float32) error {
	return s.ResizeBorderIn("", path, ratio)
}

// FocusLastPane toggles focus back to the active tab's previously-focused pane
// (LastPane). Reports whether focus moved. A focus-only change, like FocusPane.
func (s *Session) FocusLastPane() bool {
	return s.FocusLastPaneIn("")
}

// RenamePane pins (or clears, with "") a pane's custom title, overriding the
// terminal-reported one. The pane may live in any workspace/tab.
func (s *Session) RenamePane(id layout.PaneID, name string) error {
	if st := s.paneState(id); st != nil {
		st.CustomName = name
		return nil
	}
	return fmt.Errorf("unknown pane %d", id)
}

// SetPaneFlag pins (or clears, with nil) a pane's user flag. The pane may live
// in any workspace/tab — a flag is most often set from the global AGENTS list,
// which spans the whole session.
//
// Reports whether the flag actually changed, so a re-set of the identical flag
// can skip the broadcast and the save the way SetWorkspaceLock does. Note that
// "identical" includes the timestamp (flags.Flag.Equal), so re-flagging with the
// same kind and note IS a change: it is a deliberate "still true, as of now",
// and the timestamp is the only record of it.
func (s *Session) SetPaneFlag(id layout.PaneID, f *flags.Flag) (bool, error) {
	st := s.paneState(id)
	if st == nil {
		return false, fmt.Errorf("unknown pane %d", id)
	}
	if st.Flag.Equal(f) {
		return false, nil
	}
	st.Flag = f.Clone()
	return true, nil
}

// PaneFlag returns a pane's user flag (nil when unflagged or unknown).
func (s *Session) PaneFlag(id layout.PaneID) *flags.Flag {
	if st := s.paneState(id); st != nil {
		return st.Flag
	}
	return nil
}

// PaneCustomName returns a pane's custom title and whether the pane exists.
func (s *Session) PaneCustomName(id layout.PaneID) (string, bool) {
	if st := s.paneState(id); st != nil {
		return st.CustomName, true
	}
	return "", false
}

// PaneWorkspace returns the workspace owning a pane (nil when unknown) — the
// runtime resolves per-workspace spawn cwds through it.
func (s *Session) PaneWorkspace(id layout.PaneID) *workspace.Workspace {
	_, ws := s.workspaceIndexOf(id)
	return ws
}

// paneState finds a pane's viewport state across every workspace and tab.
func (s *Session) paneState(id layout.PaneID) *workspace.PaneState {
	for _, ws := range s.workspaces {
		for _, tab := range ws.Tabs {
			if st := tab.Panes[id]; st != nil {
				return st
			}
		}
	}
	return nil
}

// PaneHost reports the cathost a pane's terminal belongs to, "" for the
// backend's default host (and for an unknown pane — the caller resolves "" to
// the default either way, so there is nothing an ok flag would add).
func (s *Session) PaneHost(id layout.PaneID) string {
	if st := s.paneState(id); st != nil {
		return st.HostID
	}
	return ""
}

// SetPaneHost records the cathost a pane's terminal now lives on. It is the
// one writer of that field outside of pane creation, and it exists for exactly
// one caller: detaching a host has to move the panes it held onto another
// machine, and the move has to outlive the process — a session file still
// naming a host nobody is attached to would put those panes back on a ghost at
// the next restore. Reports false for an unknown pane.
//
// Only a pane's host moves this way, never its workspace's: PaneState.HostID is
// where a terminal *is*, while Workspace.HostID is a policy for new panes, and
// a workspace pinned to a host that comes back should go on preferring it.
func (s *Session) SetPaneHost(id layout.PaneID, hostID string) bool {
	st := s.paneState(id)
	if st == nil {
		return false
	}
	st.HostID = hostID
	return true
}

// SplitPane splits target (the focused pane if nil) in dir, focusing the new
// pane, and returns its id.
func (s *Session) SplitPane(target *layout.PaneID, dir layout.Direction) (layout.PaneID, error) {
	return s.SplitPaneWith(target, dir, workspace.SpawnSpec{})
}

// SplitPaneOn is SplitPane placing the new pane on a named cathost ("" = the
// machine the split pane itself is on — see Workspace.splitHost, which is what
// fills it in). It exists so the §7 dispatcher can route pane.split's host
// param without building a workspace.SpawnSpec of its own — the host is the
// only spec field a command may set, the rest being the backend's business via
// StageSpawn.
func (s *Session) SplitPaneOn(target *layout.PaneID, dir layout.Direction, hostID string) (layout.PaneID, error) {
	return s.SplitPaneWith(target, dir, workspace.SpawnSpec{HostID: hostID})
}

// SplitPaneWith is SplitPane with an explicit spawn spec — today only its
// HostID matters (which machine the new pane lands on), the rest of the spec
// being the backend's business via StageSpawn. An empty spec reproduces
// SplitPane exactly.
func (s *Session) SplitPaneWith(target *layout.PaneID, dir layout.Direction, spec workspace.SpawnSpec) (layout.PaneID, error) {
	return s.SplitPaneWithIn("", target, dir, spec)
}

// ClosePane closes target (the focused pane if nil), returning the closed id.
// The session always keeps at least one pane; closing a workspace's last pane
// drops that workspace (when another remains).
func (s *Session) ClosePane(target *layout.PaneID) (layout.PaneID, error) {
	return s.ClosePaneIn("", target)
}

// --- Tab commands (§7) — the active workspace unless one is named ------------
//
// The tab verbs default to the active workspace because that is what a user
// driving the UI means by "a new tab". The …In variants take an explicit
// workspace id so a caller can put a tab somewhere it is not looking — the
// fan-out an "in all workspaces" plugin launch needs, which would otherwise
// have to focus each workspace in turn and leave the viewport wherever the
// loop ended. Passing "" is the active-workspace case, so the two forms are
// the same code path and cannot drift.

// WorkspaceByID resolves a workspace from its public id ("w2"); "" means the
// active one. nil when no workspace carries that id — callers turn that into
// their own error, since the phrasing differs (a refused command vs. a skipped
// element of a fan-out).
func (s *Session) WorkspaceByID(id string) *workspace.Workspace {
	if id == "" {
		return s.ActiveWorkspace()
	}
	i, ok := s.workspaceIndexByID(id)
	if !ok {
		return nil
	}
	return s.workspaces[i]
}

// NewTabNeighborPane returns the pane a tab created right now would land next
// to: the root pane of the active workspace's *active* tab, since CreateTab
// inserts directly to its right. The runtime resolves that pane's live cwd so
// the new tab opens where its left-hand neighbor is working (the dispatcher's
// inheritedTabCwd). False when the workspace somehow holds no tabs.
func (s *Session) NewTabNeighborPane() (layout.PaneID, bool) {
	return s.NewTabNeighborPaneIn("")
}

// NewTabNeighborPaneIn is NewTabNeighborPane for a named workspace ("" = the
// active one). False for an unknown workspace as well as an empty one: both
// mean "no neighbor to inherit from", which is exactly what the caller does
// with it.
func (s *Session) NewTabNeighborPaneIn(wsID string) (layout.PaneID, bool) {
	ws := s.WorkspaceByID(wsID)
	if ws == nil || len(ws.Tabs) == 0 {
		return 0, false
	}
	tab := ws.ActiveTab()
	if tab == nil {
		return 0, false
	}
	return tab.RootPane, true
}

// CreateTab opens a tab beside the active workspace's current tab and switches
// to it. Returns the new tab's public number.
func (s *Session) CreateTab() (int, error) {
	num, _, err := s.CreateTabIn("")
	return num, err
}

// CreateTabIn opens a tab beside the current tab of the named workspace ("" =
// the active one) and makes it that workspace's active tab. Returns the new
// tab's public number and
// its root pane. The tab spawns in the workspace's identity cwd (a worktree
// workspace's checkout), falling back to the session cwd — the dispatcher layers
// the neighbor tab's live cwd over that when it knows one.
//
// The root pane is returned rather than left to the caller to look up, because
// FocusedPane — how the dispatcher used to find it — reports the *viewport's*
// pane, which is the new one only when the target workspace happens to be the
// active one. Returning it here keeps the answer right for both cases.
func (s *Session) CreateTabIn(wsID string) (int, layout.PaneID, error) {
	return s.CreateTabInWith(wsID, workspace.SpawnSpec{})
}

// CreateTabInOn is CreateTabIn placing the tab's root pane on a named cathost
// ("" = the workspace's own default host) — the tab.create counterpart of
// SplitPaneOn, and the same reasoning for its existence.
func (s *Session) CreateTabInOn(wsID, hostID string) (int, layout.PaneID, error) {
	return s.CreateTabInWith(wsID, workspace.SpawnSpec{HostID: hostID})
}

// CreateTabInWith is CreateTabIn with an explicit spawn spec (its HostID picks
// the machine; an empty spec falls back to the workspace's own host, which is
// what CreateTabIn asks for).
func (s *Session) CreateTabInWith(wsID string, spec workspace.SpawnSpec) (int, layout.PaneID, error) {
	ws := s.WorkspaceByID(wsID)
	if ws == nil {
		return 0, 0, fmt.Errorf("unknown workspace %s", wsID)
	}
	cwd := ws.IdentityCwd
	if cwd == "" {
		cwd = s.cwd
	}
	idx, err := ws.CreateTab(cwd, spec)
	if err != nil {
		return 0, 0, err
	}
	ws.SwitchTab(idx)
	num, _ := ws.PublicTabNumber(idx)
	return num, ws.Tabs[idx].RootPane, nil
}

// CloseTab closes a tab (the active tab if num is nil) of the active workspace.
// Closing a workspace's last tab drops the workspace (when another remains).
func (s *Session) CloseTab(num *int) error {
	return s.CloseTabIn("", num)
}

// FocusTab switches the active workspace to the tab with the given public
// number (a viewport change).
func (s *Session) FocusTab(num int) error {
	return s.FocusTabIn("", num)
}

// RenameTab pins (or clears, with "") a tab's display name.
func (s *Session) RenameTab(num int, name string) error {
	return s.RenameTabIn("", num, name)
}

// RenameTabIn is RenameTab scoped to a named workspace ("" = the active one).
// tab.create's title has to travel with its workspace: tab numbers are per
// workspace, so renaming "tab 2" against the viewport after creating tab 2
// somewhere else would rename the wrong tab — or, worse, silently succeed.
func (s *Session) RenameTabIn(wsID string, num int, name string) error {
	ws := s.WorkspaceByID(wsID)
	if ws == nil {
		return fmt.Errorf("unknown workspace %s", wsID)
	}
	idx, ok := s.tabIndexByNumber(ws, num)
	if !ok {
		return fmt.Errorf("unknown tab %d", num)
	}
	ws.Tabs[idx].SetCustomName(name)
	return nil
}

// MoveTab moves the active workspace's tab with public number num to the
// insertion point idx (a gap position, 0..=len: len means "to the end").
// Reports whether the order actually changed (false = a no-op move).
func (s *Session) MoveTab(num, insertIdx int) (bool, error) {
	return s.MoveTabIn("", num, insertIdx)
}

// --- Workspace commands (§7) -------------------------------------------------

// CreateWorkspace appends a new workspace (one tab, one pane) rooted at the
// session cwd and makes it active. Returns its public id ("w2").
func (s *Session) CreateWorkspace() (string, error) {
	return s.CreateWorkspaceAt(s.cwd)
}

// CreateWorkspaceAt is CreateWorkspace with an explicit root directory — the
// worktree commands open workspaces on a checkout, and the cwd becomes the
// workspace's IdentityCwd so every pane spawned in it inherits the checkout.
func (s *Session) CreateWorkspaceAt(cwd string) (string, error) {
	return s.CreateWorkspaceAtOn(cwd, "")
}

// CreateWorkspaceAtOn is CreateWorkspaceAt pinned to a cathost: the id becomes
// the workspace's HostID, so its root pane and every later pane created in it
// default to that machine. "" = the backend's default host.
func (s *Session) CreateWorkspaceAtOn(cwd, hostID string) (string, error) {
	ws, err := workspace.New(s.spawner, cwd, workspace.SpawnSpec{HostID: hostID})
	if err != nil {
		return "", err
	}
	s.workspaces = append(s.workspaces, ws)
	s.active = len(s.workspaces) - 1
	return ws.ID, nil
}

// CloseWorkspace drops a workspace (the active one if id is nil); the session
// always keeps at least one.
func (s *Session) CloseWorkspace(id *string) error {
	if len(s.workspaces) <= 1 {
		return errors.New("cannot close the last workspace")
	}
	idx := s.active
	if id != nil {
		i, ok := s.workspaceIndexByID(*id)
		if !ok {
			return fmt.Errorf("unknown workspace %s", *id)
		}
		idx = i
	}
	s.dropWorkspace(idx)
	return nil
}

// FocusWorkspace makes the workspace with the given id active (a viewport
// change).
func (s *Session) FocusWorkspace(id string) error {
	i, ok := s.workspaceIndexByID(id)
	if !ok {
		return fmt.Errorf("unknown workspace %s", id)
	}
	s.active = i
	return nil
}

// RenameWorkspace pins (or clears, with "") a workspace's display name.
func (s *Session) RenameWorkspace(id, name string) error {
	i, ok := s.workspaceIndexByID(id)
	if !ok {
		return fmt.Errorf("unknown workspace %s", id)
	}
	s.workspaces[i].SetCustomName(name)
	return nil
}

// SetWorkspaceLock opens or closes a workspace to automation ("" = the active
// workspace). A locked workspace refuses the two control-API paths that put
// something new in motion inside it — spawning a process from a supplied
// command line, and typing into its panes — so a plugin action or an agent
// launch cannot land in a workspace the user has set aside for hand work.
// Reports whether the flag actually changed, so a no-op toggle can skip the
// broadcast.
func (s *Session) SetWorkspaceLock(id string, locked bool) (bool, error) {
	i := s.active
	if id != "" {
		var ok bool
		if i, ok = s.workspaceIndexByID(id); !ok {
			return false, fmt.Errorf("unknown workspace %s", id)
		}
	}
	if s.workspaces[i].Locked == locked {
		return false, nil
	}
	s.workspaces[i].SetLocked(locked)
	return true, nil
}

// SetWorkspaceFlag pins (or clears, with nil) a workspace's user flag ("" = the
// active workspace, the default workspace.close and workspace.lock both take).
// Reports whether the flag actually changed — see SetPaneFlag for what
// "changed" includes.
func (s *Session) SetWorkspaceFlag(id string, f *flags.Flag) (bool, error) {
	i := s.active
	if id != "" {
		var ok bool
		if i, ok = s.workspaceIndexByID(id); !ok {
			return false, fmt.Errorf("unknown workspace %s", id)
		}
	}
	if s.workspaces[i].Flag.Equal(f) {
		return false, nil
	}
	s.workspaces[i].SetFlag(f)
	return true, nil
}

// MoveWorkspace moves the workspace with the given public id to the insertion
// point idx (a gap position, 0..=len: len means "to the end"). The active
// workspace keeps its identity across the move. Reports whether the order
// actually changed (false = a no-op move).
func (s *Session) MoveWorkspace(id string, insertIdx int) (bool, error) {
	srcIdx, ok := s.workspaceIndexByID(id)
	if !ok {
		return false, fmt.Errorf("unknown workspace %s", id)
	}
	if insertIdx < 0 || insertIdx > len(s.workspaces) {
		return false, fmt.Errorf("bad insert index %d", insertIdx)
	}
	targetIdx := insertIdx
	if srcIdx < insertIdx {
		targetIdx = insertIdx - 1
	}
	targetIdx = min(targetIdx, len(s.workspaces)-1)
	if srcIdx == targetIdx {
		return false, nil
	}
	activeWS := s.workspaces[s.active]
	ws := s.workspaces[srcIdx]
	s.workspaces = append(s.workspaces[:srcIdx], s.workspaces[srcIdx+1:]...)
	s.workspaces = append(s.workspaces[:targetIdx],
		append([]*workspace.Workspace{ws}, s.workspaces[targetIdx:]...)...)
	for i, w := range s.workspaces {
		if w == activeWS {
			s.active = i
			break
		}
	}
	return true, nil
}

// ResolvePaneTarget resolves the pane an optionally-addressed command means: the
// named pane, or the focused one when target is nil — the rule every pane command
// follows. Exported so a caller that must know the subject *before* the mutation
// runs (the dispatcher reads the split source's cwd) resolves it the same way the
// mutation will.
func (s *Session) ResolvePaneTarget(target *layout.PaneID) (layout.PaneID, error) {
	return s.resolvePaneTarget(target)
}

// --- Internal helpers --------------------------------------------------------

func (s *Session) resolvePaneTarget(target *layout.PaneID) (layout.PaneID, error) {
	return s.ResolvePaneTargetIn("", target)
}

func (s *Session) totalPanes() int {
	n := 0
	for _, ws := range s.workspaces {
		for _, tab := range ws.Tabs {
			n += tab.Layout.PaneCount()
		}
	}
	return n
}

func (s *Session) workspaceIndexOf(id layout.PaneID) (int, *workspace.Workspace) {
	for i, ws := range s.workspaces {
		if _, ok := ws.FindTabIndexForPane(id); ok {
			return i, ws
		}
	}
	return -1, nil
}

func (s *Session) workspaceIndexByID(id string) (int, bool) {
	for i, ws := range s.workspaces {
		if ws.ID == id {
			return i, true
		}
	}
	return -1, false
}

func (s *Session) tabIndexByNumber(ws *workspace.Workspace, num int) (int, bool) {
	for i, tab := range ws.Tabs {
		if tab.Number == num {
			return i, true
		}
	}
	return -1, false
}

// dropWorkspace removes the workspace at idx and keeps active valid.
func (s *Session) dropWorkspace(idx int) {
	s.workspaces = append(s.workspaces[:idx], s.workspaces[idx+1:]...)
	switch {
	case s.active >= len(s.workspaces):
		s.active = len(s.workspaces) - 1
	case idx < s.active:
		s.active--
	}
}
