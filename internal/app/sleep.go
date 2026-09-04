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
