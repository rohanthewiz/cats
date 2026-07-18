//go:build ghostty

package main

import (
	"strconv"

	"github.com/rohanthewiz/herdr-web/internal/app"
	"github.com/rohanthewiz/herdr-web/internal/browserproto"
	"github.com/rohanthewiz/herdr-web/internal/layout"
	"github.com/rohanthewiz/herdr-web/internal/orchestration"
)

// Agent notifications (WS6) — the port of herdr's agent-notification decisions
// (app/actions.rs). A pane_agent state transition that warrants attention
// becomes a browserproto notify (toast + permission-gated native notification
// in the front-end) and a pane_notify control-API event. Suppression is the
// front-end's job: unlike herdr, which knows its host terminal's focus, the
// gateway serves many browsers and each knows its own focus/visibility — so
// the server always sends and each client decides (herdr's rule: suppress when
// the pane is on screen and the window is focused). Loop-goroutine only.

// onPaneAgent caches a pane's agent chrome, forwards it to browsers and event
// subscribers, and emits a notification on a notification-worthy transition.
func (o *orch) onPaneAgent(ev orchestration.PaneAgent) {
	rt := o.panes[ev.PaneID]
	if rt == nil {
		return
	}
	prevState, prevAgent := "unknown", ""
	if rt.agent != nil {
		prevState, prevAgent = rt.agent.State, rt.agent.Agent
	}
	rt.agent = &ev
	if o.visible[ev.PaneID] {
		o.broadcast(browserproto.NewPaneAgent(ev.PaneID, ev.Agent, ev.State, true))
	}
	o.broadcast(o.agentsMsg())
	o.emitEvent(app.EventPaneAgent, ev.PaneID, app.PaneAgentEvent{Pane: ev.PaneID, Agent: ev.Agent, State: ev.State})

	kind := notifyKind(prevState, prevAgent, ev.State, ev.Agent)
	if kind == "" {
		return
	}
	msg := ev.Agent + " " + notifyEventText(kind)
	n := browserproto.NewNotify(kind, msg, o.notifyContext(ev.PaneID))
	n.Pane = ev.PaneID
	n.Pub, _ = o.session.PublicPaneID(layout.PaneID(ev.PaneID))
	o.broadcast(n)
	o.emitEvent(app.EventPaneNotify, ev.PaneID,
		app.PaneNotifyEvent{Pane: ev.PaneID, Agent: ev.Agent, Kind: kind, Message: msg})
}

// notifyKind classifies an agent state transition (herdr's
// notification_toast_for_state_change_with_agent_labels):
//   - any change into blocked ⇒ "attention" — the agent is waiting on the user;
//   - a completion into idle ⇒ "finished" — from working/blocked, or from
//     unknown when it is the same agent (detection briefly lost it mid-run).
//
// A pane with no detected agent never notifies, and an unchanged state is
// never a transition (resync replays are deduped by this).
func notifyKind(prevState, prevAgent, state, agent string) string {
	if agent == "" || state == prevState {
		return ""
	}
	switch {
	case state == "blocked":
		return "attention"
	case state == "idle" && (prevState == "working" || prevState == "blocked"):
		return "finished"
	case state == "idle" && prevState == "unknown" && prevAgent != "" && prevAgent == agent:
		return "finished"
	}
	return ""
}

// notifyEventText is the human phrase for a notification kind.
func notifyEventText(kind string) string {
	if kind == "attention" {
		return "needs attention"
	}
	return "finished"
}

// notifyContext locates a pane and renders herdr's notification context:
// "workspace · N", plus "· tab" when the workspace has more than one tab.
func (o *orch) notifyContext(pid uint32) string {
	for i, ws := range o.session.Workspaces() {
		tabIdx, ok := ws.FindTabIndexForPane(layout.PaneID(pid))
		if !ok {
			continue
		}
		ctx := ws.DisplayName() + " · " + strconv.Itoa(i+1)
		if len(ws.Tabs) > 1 {
			ctx += " · " + ws.Tabs[tabIdx].DisplayName()
		}
		return ctx
	}
	return ""
}
