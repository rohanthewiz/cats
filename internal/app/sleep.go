package app

import (
	"errors"
	"fmt"

	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// Sleeping workspaces at the session level (the model half is
// workspace/sleep.go; the idle test that decides WHICH panes a clean closes
// is the dispatcher's, since only the runtime knows what a pane is doing).
//
// The one invariant added here: the session's active workspace is never
// asleep, and neither is any workspace a window is showing. A sleeping
// workspace has nothing to show — its single pane has no terminal — so
// SleepWorkspace moves the active index off it, refuses to sleep the last
// awake workspace, and the view resolution (viewWorkspace) treats a sleeping
// id like a closed one: a window left pointing at it falls back to the active
// workspace, exactly as when its workspace is closed under it.

// ErrLastAwake is the refusal for sleeping the only workspace still awake.
var ErrLastAwake = errors.New("cannot sleep the last awake workspace")

// SleepWorkspace puts the workspace with the given id to sleep ("" = the
// active workspace): every pane closed, one placeholder left, the flag set
// (workspace.Sleep). The backend reconciles the closed terminals off the
// daemon on the next ApplyModel, the same way it does for a closed tab.
//
// The caller has already decided the workspace is empty enough to sleep — this
// closes whatever is left without asking. A sleeping workspace is a no-op.
func (s *Session) SleepWorkspace(id string) error {
	i := s.active
	if id != "" {
		var ok bool
		if i, ok = s.workspaceIndexByID(id); !ok {
			return fmt.Errorf("unknown workspace %s", id)
		}
	}
	ws := s.workspaces[i]
	if ws.Asleep {
		return nil
	}
	// Where the active index goes if this was it: the nearest awake workspace
	// after it, else the nearest before. None means this is the last one awake,
	// and the session would have nothing to show — refused.
	next := s.nearestAwake(i)
	if next < 0 {
		return ErrLastAwake
	}
	if _, err := ws.Sleep(workspace.SpawnSpec{}); err != nil {
		return err
	}
	if i == s.active {
		s.active = next
	}
	return nil
}

// --- retiring a dead pane on the runtime's own initiative --------------------
//
// ClosePaneIn is the close a person asks for: closing a workspace's last pane
// drops the workspace, because that is what "close this" means when there is
// nothing left. The runtime's housekeeping — the clean-exit auto-close and the
// exited-pane reaper — closes panes nobody asked it to close, often in a
// workspace nobody is looking at, and there the same rule turned "the shell in
// that workspace's only pane ended" into "the workspace, its name, flag and
// place in the list are gone", with no prompt and nothing in the log. The
// workspace was not closed; it was just empty.
//
// RetirePane is the housekeeping close. It differs from ClosePaneIn only when
// the pane is its workspace's last one, and then it does what a clean does with
// a workspace it empties: sleep it, so it stays in the list and a click wakes it
// to a fresh shell.
//
//	workspace panes   another awake ws   outcome
//	───────────────   ────────────────   ─────────────────────────────────────
//	      > 1               any          pane closed (ClosePaneIn, unchanged)
//	       1                yes          workspace put to sleep (slept = true)
//	       1                no           refused — the corpse stays put
//
// The refusal in the last row is the same judgement as the session's last-pane
// refusal, one level up: sleeping needs an awake workspace to land on, and
// dropping the only awake workspace to avoid keeping a dead pane would be the
// very loss this exists to prevent. The callers already treat a refusal as
// "leave it to the next sweep", which is right — once another workspace is
// awake, the next sweep sleeps this one.

// ErrLastAwakeWorkspacePane is RetirePane's refusal for the last pane of the
// only workspace still awake.
var ErrLastAwakeWorkspacePane = errors.New("last pane of the only awake workspace")

// RetirePane closes a dead pane on the runtime's behalf (auto-close, reaper),
// sleeping its workspace instead of dropping it when the pane is the
// workspace's last. slept reports which happened, and wsID names the workspace
// either way so the caller can say where it happened.
func (s *Session) RetirePane(id layout.PaneID) (wsID string, slept bool, err error) {
	idx, ws := s.workspaceIndexOf(id)
	if ws == nil {
		return "", false, fmt.Errorf("unknown pane %d", id)
	}
	if !s.retireSleeps(ws) {
		_, err := s.ClosePaneIn("", &id)
		return ws.ID, false, err
	}
	if s.nearestAwake(idx) < 0 {
		return ws.ID, false, ErrLastAwakeWorkspacePane
	}
	if err := s.SleepWorkspace(ws.ID); err != nil {
		return ws.ID, false, err
	}
	return ws.ID, true, nil
}

// CanRetirePane reports whether RetirePane would close or sleep something for
// this pane rather than refuse. The auto-close asks it before arming, so the
// header never counts down to a close that will not happen.
func (s *Session) CanRetirePane(id layout.PaneID) bool {
	idx, ws := s.workspaceIndexOf(id)
	if ws == nil {
		return false
	}
	if s.retireSleeps(ws) {
		return s.nearestAwake(idx) >= 0
	}
	// A pane that is not its workspace's last has a sibling, so the session
	// holds at least two panes and ClosePaneIn's last-pane refusal cannot fire.
	return true
}

// retireSleeps reports whether retiring a pane of ws empties it — the pane is
// the workspace's only one. A sleeping workspace never qualifies: its one pane
// is the placeholder, which has no process to have exited, so there is nothing
// to retire and the close path's own checks apply.
func (s *Session) retireSleeps(ws *workspace.Workspace) bool {
	if ws.Asleep {
		return false
	}
	n := 0
	for _, tab := range ws.Tabs {
		n += tab.Layout.PaneCount()
	}
	return n == 1
}

// nearestAwake is the index of the awake workspace closest to i, other than i
// itself — after it first, then before — or -1 when every other workspace is
// asleep.
func (s *Session) nearestAwake(i int) int {
	for j := i + 1; j < len(s.workspaces); j++ {
		if !s.workspaces[j].Asleep {
			return j
		}
	}
	for j := i - 1; j >= 0; j-- {
		if !s.workspaces[j].Asleep {
			return j
		}
	}
	return -1
}

// WakeWorkspace wakes a sleeping workspace: the flag is cleared, the
// placeholder pane (which the backend realizes as a shell on the next
// ApplyModel) is returned, and the parked agent conversations come back for
// the caller to resume — one pane each, split off the placeholder, staged
// with the agent's resume command. woke is false for a workspace that was not
// asleep, with no error: "make sure this is awake" is a fine thing to ask of
// an awake workspace, and workspace.focus asks it of every workspace.
func (s *Session) WakeWorkspace(id string) (placeholder layout.PaneID, parked []workspace.ParkedAgent, woke bool, err error) {
	ws := s.WorkspaceByID(id)
	if ws == nil {
		return 0, nil, false, fmt.Errorf("unknown workspace %s", id)
	}
	placeholder, _ = ws.PlaceholderPane()
	parked, woke = ws.Wake()
	return placeholder, parked, woke, nil
}

// ParkAgentIn records a resumable agent conversation on a workspace, ahead of
// the clean that closes its pane. Reports whether it was newly parked.
func (s *Session) ParkAgentIn(id string, a workspace.ParkedAgent) (bool, error) {
	ws := s.WorkspaceByID(id)
	if ws == nil {
		return false, fmt.Errorf("unknown workspace %s", id)
	}
	return ws.ParkAgent(a), nil
}

// workspaceAsleepErr phrases the refusal for putting something new into a
// sleeping workspace — a tab, a split, a moved tab. The way in is named, since
// the refusal is otherwise a dead end: the workspace is right there in the
// list, just not running.
func workspaceAsleepErr(id string) error {
	return fmt.Errorf("workspace %s is asleep — wake it first (workspace.wake)", id)
}

// indexOfWorkspace is the position of ws in the session order, -1 when it is
// not this session's (which cannot happen for a workspace the caller just
// resolved through WorkspaceByID).
func (s *Session) indexOfWorkspace(ws *workspace.Workspace) int {
	for i, w := range s.workspaces {
		if w == ws {
			return i
		}
	}
	return -1
}
