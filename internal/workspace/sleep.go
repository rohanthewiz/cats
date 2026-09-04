package workspace

import (
	"fmt"

	"github.com/rohanthewiz/cats/internal/layout"
)

// Sleeping workspaces.
//
// A workspace is normally either fully alive — every pane a live PTY on its
// cathost, restart or not — or gone, along with its name, flag, lock and todo
// state. Sleep is the state in between: the workspace keeps its place in the
// list and everything the user pinned to it, and holds no terminal at all.
//
//	awake                              asleep
//	┌──────────────────────┐  Sleep   ┌──────────────────────┐
//	│ tab 1: p1 | p2       │ ───────► │ tab N: pK            │  one placeholder
//	│ tab 2: p3            │          │  (no PTY behind it)  │  pane, no PTY
//	└──────────────────────┘  ◄─────  └──────────────────────┘
//	                          Wake
//
// The layout is deliberately NOT kept across sleep. A sleeping workspace holds
// exactly one tab with one root pane, allocated fresh at sleep time, so every
// invariant the rest of the model relies on ("a workspace has a tab", "a tab
// has a focused pane", "every pane has a public number") holds without a
// single special case, and the backend has one rule to add: spawn nothing for
// a sleeping workspace. On wake that placeholder becomes the first shell; the
// backend creates its PTY exactly as it would for any pane it has not yet
// realized. Pane numbers keep counting up (numbers are never reused), so the
// placeholder gets a fresh public handle rather than resurrecting a closed one.
//
// What sleep does keep is the one thing worth more than a layout: which agent
// conversations were running. A ParkedAgent is the resume ref of an agent
// whose pane was closed by a clean or a sleep with parking on; Wake hands them
// back so the caller can resume each one in a pane of its own.

// ParkedAgent is a resumable agent conversation whose pane was closed while
// the workspace was cleaned or put to sleep. The four ref fields are exactly
// what the runtime persists per pane for restart-resume (persist.AgentSession),
// so wake can hand them to the same resume path a cold restart uses. Pane is
// the public handle the agent used to live at ("w1:p3"), kept only so the
// sidebar can say where it came from.
type ParkedAgent struct {
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"` // "id" | "path"
	Value  string `json:"value"`
	Pane   string `json:"pane,omitempty"`
}

// Sleep closes every tab and pane of the workspace and replaces them with one
// placeholder tab holding one root pane, then marks the workspace asleep. It
// returns the TerminalIDs that were attached to the closed panes, so the
// caller can despawn them (the model never touches terminals itself).
//
// The placeholder is spawned through the seam like any pane — the backend
// decides what to do with a spawn in a sleeping workspace, and catway's
// spawner is model-only, so nothing runs until wake. Sleeping an already
// sleeping workspace is a no-op that returns nothing.
func (w *Workspace) Sleep(spec SpawnSpec) ([]TerminalID, error) {
	if w.Asleep {
		return nil, nil
	}
	var closed []TerminalID
	for _, t := range w.Tabs {
		for id, st := range t.Panes {
			closed = append(closed, st.AttachedTerminalID)
			w.unregisterPane(id)
		}
	}
	// One fresh tab, numbered past every tab this workspace has had, with its
	// root pane numbered past every pane — the same allocation CreateTab does,
	// so a wake-then-split continues the sequence the user already knows.
	number := w.nextPublicTabNumber
	w.nextPublicTabNumber++
	paneNumber := w.nextPublicPaneNumber
	spec.PublicPaneID = publicPaneIDForNumber(w.ID, paneNumber)
	spec = w.defaultHost(spec)
	tab, err := NewTab(w.spawner, number, w.IdentityCwd, spec)
	if err != nil {
		return nil, fmt.Errorf("workspace %s: sleep placeholder: %w", w.ID, err)
	}
	w.Tabs = []*Tab{tab}
	w.activeTab = 0
	w.registerNewPaneWithNumber(tab.RootPane, paneNumber) // also bumps the counter
	w.Asleep = true
	return closed, nil
}

// Wake clears the sleep flag and hands back the parked agents, clearing them
// from the workspace: once the caller has resumed them they are live panes
// again, and a ref that stayed parked would resume the same conversation a
// second time on the next wake. The placeholder pane is left where it is —
// the backend realizes it now that the workspace is awake. Returns false for
// a workspace that was not asleep.
func (w *Workspace) Wake() (parked []ParkedAgent, woke bool) {
	if !w.Asleep {
		return nil, false
	}
	w.Asleep = false
	parked = w.ParkedAgents
	w.ParkedAgents = nil
	return parked, true
}

// ParkAgent records a resumable agent conversation on the workspace. Called by
// a clean or sleep that is about to close the agent's pane; the pane itself is
// closed by the caller through the ordinary close path. A ref already parked
// (same four fields) is not parked twice — two panes on one conversation would
// otherwise resume it twice on wake.
func (w *Workspace) ParkAgent(a ParkedAgent) bool {
	for _, p := range w.ParkedAgents {
		if p.Source == a.Source && p.Agent == a.Agent && p.Kind == a.Kind && p.Value == a.Value {
			return false
		}
	}
	w.ParkedAgents = append(w.ParkedAgents, a)
	return true
}

// PlaceholderPane is the root pane of a sleeping workspace — the pane that
// becomes its first shell on wake. ok is false for an awake workspace, which
// has no such distinguished pane.
func (w *Workspace) PlaceholderPane() (layout.PaneID, bool) {
	if !w.Asleep || len(w.Tabs) == 0 {
		return 0, false
	}
	return w.Tabs[0].RootPane, true
}
