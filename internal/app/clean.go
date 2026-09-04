package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// workspace.clean / workspace.sleep / workspace.wake — the dispatcher half.
//
// The model (workspace/sleep.go, session sleep.go) knows how to put a
// workspace to bed and how to bring it back; it has no idea what any pane is
// doing. That is the runtime's knowledge, reached through Backend.PaneActivity,
// and this file is where the two meet: it classifies every pane of the target
// workspace, decides what clean or sleep may do with each, and then drives the
// session and the backend to do it.
//
//	pane activity            clean / sleep verdict
//	───────────────────────  ─────────────────────────────────────────────
//	runtime unknown          keep   (host down, or not yet spawned: no say)
//	exited                   close
//	agent, not idle          keep   (mid-turn, or waiting on the user)
//	agent, idle              leave: keep · park: close + park · command: send
//	foreground job / exec'd  keep   (a build, an editor, a plugin)
//	shell at its prompt      close
//
// clean acts on that verdict and stops. sleep first checks that the verdict
// leaves nothing behind, and refuses — closing nothing — if it does, naming
// the panes in the way; a workspace that would be left empty is not emptied
// pane by pane but put to sleep whole (workspace.Sleep closes everything and
// leaves the placeholder), which is also what clean does when its verdict is
// "everything goes" and another workspace is awake to switch to.

// PaneActivity is the runtime's account of what a pane is doing, for the
// idle test above. Known is false when the backend has nothing to say — no
// runtime for the pane, or its host disconnected — and the pane is then left
// alone: a clean issued during a host outage must not read "no answer" as
// "nothing running" and close every pane on that host.
type PaneActivity struct {
	Known  bool
	Exited bool
	// Busy: a foreground job is running in the shell, or the pane was spawned
	// to exec a command rather than a shell (a plugin, a resumed agent that
	// detection has not named). Only meaningful with no Agent.
	Busy       bool
	Agent      string // detected agent label, "" for none
	AgentState string // "idle" | "working" | "blocked" | …; only with Agent
	// Session is the agent's resumable conversation, nil when the runtime
	// does not know one. Pane is filled in by the dispatcher.
	Session *workspace.ParkedAgent
}

// cleanVerdict is the classification of one workspace, before anything is
// done to it.
type cleanVerdict struct {
	closable []layout.PaneID // idle: close (the placeholder route when everything goes)
	park     []parkedPane    // idle agents to close, with the ref to park first
	send     []layout.PaneID // idle agents to type the command into and leave
	kept     []layout.PaneID // busy, or an idle agent under "leave"
}

type parkedPane struct {
	pane layout.PaneID
	ref  workspace.ParkedAgent
}

// Agents modes of CleanWorkspaceParams.
const (
	agentsLeave   = "leave"
	agentsPark    = "park"
	agentsCommand = "command"
)

// classifyWorkspace walks every pane of ws and sorts each into the verdict.
// Pure: it asks the backend and the session, and changes nothing. Panes are
// visited in a fixed order (tabs in order, pane ids ascending) so the result,
// and the kept-panes list a refusal reports, are deterministic.
func (d *Dispatcher) classifyWorkspace(ws *workspace.Workspace, agents string, command string) cleanVerdict {
	var v cleanVerdict
	for _, tab := range ws.Tabs {
		ids := tab.Layout.PaneIDs()
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			act := d.backend.PaneActivity(uint32(id))
			switch {
			case !act.Known:
				v.kept = append(v.kept, id)
			case act.Exited:
				v.closable = append(v.closable, id)
			case act.Agent != "":
				if act.AgentState != "idle" {
					v.kept = append(v.kept, id)
					break
				}
				switch agents {
				case agentsPark:
					if act.Session == nil {
						v.kept = append(v.kept, id) // nothing to resume from: as "leave"
						break
					}
					ref := *act.Session
					ref.Pane, _ = ws.PublicPaneID(id)
					v.park = append(v.park, parkedPane{pane: id, ref: ref})
				case agentsCommand:
					v.send = append(v.send, id)
				default:
					v.kept = append(v.kept, id)
				}
			case act.Busy:
				v.kept = append(v.kept, id)
			default:
				v.closable = append(v.closable, id)
			}
		}
	}
	return v
}

// cleanWorkspace is the shared body of workspace.clean (sleep false) and
// workspace.sleep (sleep true). See the file comment for the two verbs'
// difference; the params are the same.
func (d *Dispatcher) cleanWorkspace(dec ParamDecoder, r Responder, sleep bool) {
	var p CleanWorkspaceParams
	if err := decodeOptional(dec, &p); err != nil {
		r.Fail("bad params: " + err.Error())
		return
	}
	agents := p.Agents
	if agents == "" {
		agents = agentsLeave
	}
	switch agents {
	case agentsLeave, agentsPark, agentsCommand:
	default:
		r.Fail(fmt.Sprintf("bad params: agents %q (want leave, park or command)", p.Agents))
		return
	}
	if agents == agentsCommand && strings.TrimSpace(p.Command) == "" {
		r.Fail("bad params: agents \"command\" needs a command")
		return
	}
	target := p.ID
	if target == "" {
		target = d.viewWorkspaceID()
	}
	ws := d.session.WorkspaceByID(target)
	if ws == nil {
		r.Fail(fmt.Sprintf("unknown workspace %s", target))
		return
	}
	verb := "clean"
	if sleep {
		verb = "sleep"
	}
	// Already asleep: nothing runs there, so there is nothing to clean and
	// sleep is already true. Acked as such rather than refused — "make sure
	// this is asleep" is a fine thing to ask twice.
	if ws.Asleep {
		r.OK(CleanWorkspaceResult{Asleep: true})
		return
	}
	// A workspace is only ever emptied by sleeping it, and sleeping needs
	// another workspace awake to land on. Checked before any pane is touched:
	// a sleep that closed half the panes and then failed would be the worst
	// of both.
	canSleep := d.session.nearestAwake(d.session.indexOfWorkspace(ws)) >= 0
	if sleep && !canSleep {
		r.Fail(ErrLastAwake.Error())
		return
	}

	v := d.classifyWorkspace(ws, agents, p.Command)
	if sleep && len(v.kept) > 0 {
		r.Fail(fmt.Sprintf("cannot sleep workspace %s: %s still busy (%s)", ws.ID,
			plural(len(v.kept), "pane"), strings.Join(d.publicIDs(v.kept), ", ")))
		return
	}

	res := CleanWorkspaceResult{Kept: len(v.kept) + len(v.send), KeptPanes: d.publicIDs(append(append([]layout.PaneID{}, v.kept...), v.send...))}
	// Park before anything closes: the ref must be on the workspace before the
	// pane it came from is gone, or a save between the two would lose it.
	for _, pp := range v.park {
		if ok, _ := d.session.ParkAgentIn(ws.ID, pp.ref); ok {
			res.Parked++
		}
		v.closable = append(v.closable, pp.pane)
	}
	for _, id := range v.send {
		if err := d.backend.SendInput(uint32(id), p.Command, true); err == nil {
			res.Sent++
		}
	}

	switch {
	case len(v.kept) == 0 && len(v.send) == 0 && canSleep:
		// Everything goes. Rather than closing the panes one by one — which
		// would drop the workspace on its last pane — the workspace is put to
		// sleep whole, which is what an empty workspace IS.
		if err := d.session.SleepWorkspace(ws.ID); err != nil {
			r.Fail(fmt.Sprintf("%s workspace %s: %v", verb, ws.ID, err))
			return
		}
		res.Closed = len(v.closable)
		res.Asleep = true
	case len(v.kept) == 0 && len(v.send) == 0:
		// Everything goes but nothing else is awake (clean only; sleep was
		// refused above). Close all but one so the user keeps a shell to stand
		// in — the last idle pane in walk order, which for a single-tab
		// workspace is the one furthest from the root.
		last := v.closable[len(v.closable)-1]
		for _, id := range v.closable[:len(v.closable)-1] {
			if _, err := d.session.ClosePaneIn("", &id); err == nil {
				res.Closed++
			}
		}
		res.Kept++
		res.KeptPanes = append(res.KeptPanes, d.publicIDs([]layout.PaneID{last})...)
	default:
		// Something stays, so the idle panes close individually. A tab whose
		// last pane goes closes with it; the workspace cannot empty because a
		// kept pane remains.
		for _, id := range v.closable {
			if _, err := d.session.ClosePaneIn("", &id); err == nil {
				res.Closed++
			}
		}
	}
	d.backend.ApplyModel()
	r.OK(res)
}

// wakeWorkspace is workspace.wake: the placeholder becomes a shell and each
// parked agent gets a pane of its own, staged to resume.
func (d *Dispatcher) wakeWorkspace(dec ParamDecoder, r Responder) {
	var p WorkspaceParams
	if err := dec.Decode(&p); err != nil {
		r.Fail("bad params: " + err.Error())
		return
	}
	if d.session.WorkspaceByID(p.ID) == nil {
		r.Fail(fmt.Sprintf("unknown workspace %s", p.ID))
		return
	}
	// A workspace that was awake acks without an ApplyModel: nothing changed.
	if !d.wakeIfAsleep(p.ID) {
		r.OK(nil)
		return
	}
	d.backend.ApplyModel()
	r.OK(nil)
}

// wakeIfAsleep wakes the named workspace when it is asleep, resuming its
// parked agents, and reports whether anything happened. It is the step every
// path INTO a workspace takes first — workspace.wake itself, and
// workspace.focus, whose click on a sleeping row is the everyday way a
// workspace comes back. The caller owns the ApplyModel that realizes the new
// panes; an unknown id is a no-op (the caller has validated it).
func (d *Dispatcher) wakeIfAsleep(wsID string) bool {
	placeholder, parked, woke, err := d.session.WakeWorkspace(wsID)
	if err != nil || !woke {
		return false
	}
	ws := d.session.WorkspaceByID(wsID)
	for _, a := range parked {
		// One pane per conversation, split off the placeholder so they share
		// its tab. The pane is created first — the resume rides the spawn
		// plan keyed by its id — and taken back if the backend cannot resume
		// this ref here (the agent's transcript lives on another host, say):
		// a plain shell standing where an agent was promised is worse than
		// nothing, since it looks like the agent lost its memory.
		id, err := d.session.SplitPaneWithIn(wsID, &placeholder, layout.Vertical, workspace.SpawnSpec{HostID: ws.HostID})
		if err != nil {
			continue
		}
		if !d.backend.StageResume(uint32(id), a) {
			_, _ = d.session.ClosePaneIn("", &id)
		}
	}
	return true
}

// publicIDs renders pane ids as their public handles ("w2:p3"), for messages.
func (d *Dispatcher) publicIDs(ids []layout.PaneID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if pub, ok := d.session.PublicPaneID(id); ok {
			out = append(out, pub)
		}
	}
	return out
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
