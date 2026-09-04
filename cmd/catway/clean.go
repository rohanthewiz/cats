//go:build ghostty

package main

import (
	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/persist"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// The runtime half of workspace.clean / sleep / wake (app/clean.go has the
// verdict logic). Two Backend methods: what a pane is doing, and how to bring
// a parked agent back.

// PaneActivity classifies a pane for the dispatcher's idle test. Loop
// goroutine only — it reads the runtime state the daemon pumps keep current.
//
// The order of the checks is the order of certainty. A pane the daemon is
// not holding a live connection for is Unknown: the last thing we heard may
// be minutes stale, and "no answer" must never become "nothing running". An
// exit is definite. An agent's word about itself (effectiveAgent: hook first,
// detection second) beats the pgid probe, which only says *something* other
// than the shell holds the terminal — the agent is that something. Only a
// plain shell falls through to the job test, plus execCmd for the pane whose
// child is not a shell at all.
func (o *orch) PaneActivity(pane uint32) app.PaneActivity {
	rt := o.panes[pane]
	if rt == nil || !rt.created || !o.hostOf(rt).connected() {
		return app.PaneActivity{}
	}
	act := app.PaneActivity{Known: true}
	if rt.exited != nil {
		act.Exited = true
		return act
	}
	if agent, state := rt.effectiveAgent(); agent != "" {
		act.Agent, act.AgentState = agent, state
		if ref := rt.resumableSession(agent); ref != nil {
			act.Session = &workspace.ParkedAgent{Source: ref.source, Agent: ref.agent, Kind: ref.kind, Value: ref.value}
		}
		return act
	}
	act.Busy = rt.job || rt.execCmd
	return act
}

// resumableSession is the conversation a park would keep for the pane's
// agent: the hook-reported ref when it belongs to the agent actually running
// (an older agent's ref can outlive it, see refreshAgentModel), else the one
// the host read off the agent's own registry. nil when neither names this
// agent — there is then nothing a wake could resume, and the dispatcher
// leaves the pane alone rather than park a ref that would come back as
// someone else's history.
func (rt *paneRuntime) resumableSession(agent string) *agentSessionRef {
	if s := rt.agentSession; s != nil && s.agent == agent {
		return s
	}
	if s := rt.detectedSession; s != nil && s.agent == agent {
		return s
	}
	return nil
}

// StageResume arms a parked agent's resume for a pane the next applyModel
// creates, through the same plan table a cold restart uses (resume.go):
// createPane consumes resumePlans for the argv and restoredAgents to keep the
// ref live on the runtime, so the next snapshot still carries it. False when
// the ref is not resumable — an agent the plan table does not know, a
// malformed value, or a pane on a host other than this one, where the
// transcript is not (the restart path makes the same call in planResume).
func (o *orch) StageResume(pane uint32, a workspace.ParkedAgent) bool {
	argv := resumeArgv(a.Source, a.Agent, a.Kind, a.Value)
	if argv == nil || !o.paneIsLocal(pane) {
		return false
	}
	o.resumePlans[pane] = argv
	o.restoredAgents[pane] = persist.AgentSession{Source: a.Source, Agent: a.Agent, Kind: a.Kind, Value: a.Value}
	return true
}
