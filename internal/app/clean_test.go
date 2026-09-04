package app

import (
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// cleanHarness is a cmdHarness with a second workspace holding a known set of
// panes, so the clean/sleep tests can point the verdict at each one:
//
//	w2 tab 1:  shell (idle) | job (busy) | agent (idle, resumable)
//
// The first workspace is left as the one "awake elsewhere" that a sleep needs.
type cleanHarness struct {
	cmdHarness
	ws                *workspace.Workspace
	shell, job, agent layout.PaneID
}

func newCleanHarness(t *testing.T) cleanHarness {
	t.Helper()
	h := newCmdHarness(t)
	id, err := h.s.CreateWorkspace()
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	ws := h.s.WorkspaceByID(id)
	shell, _ := ws.FocusedPaneID()
	job, err := h.s.SplitPaneWithIn(id, &shell, layout.Vertical, workspace.SpawnSpec{})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	agent, err := h.s.SplitPaneWithIn(id, &job, layout.Vertical, workspace.SpawnSpec{})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	h.b.activity = map[uint32]PaneActivity{
		uint32(job): {Known: true, Busy: true},
		uint32(agent): {Known: true, Agent: "claude", AgentState: "idle",
			Session: &workspace.ParkedAgent{Source: "hook", Agent: "claude", Kind: "id", Value: "sess-1"}},
	}
	*h.log = nil
	return cleanHarness{cmdHarness: h, ws: ws, shell: shell, job: job, agent: agent}
}

func (h cleanHarness) has(id layout.PaneID) bool {
	_, ok := h.s.PublicPaneID(id)
	return ok
}

func (h cleanHarness) entry(t *testing.T) WorkspaceEntry {
	t.Helper()
	for _, e := range okDataFor[WorkspaceListResult](t, h.cmdHarness, CmdWorkspaceList).Workspaces {
		if e.ID == h.ws.ID {
			return e
		}
	}
	t.Fatalf("workspace %s missing from workspace.list", h.ws.ID)
	return WorkspaceEntry{}
}

// clean closes the idle shell, keeps the busy pane, and — under the default
// "leave" — keeps the idle agent too.
func TestDispatchWorkspaceCleanLeavesAgents(t *testing.T) {
	h := newCleanHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdWorkspaceClean, params(t, CleanWorkspaceParams{ID: h.ws.ID}), r)

	if !r.okCall {
		t.Fatalf("clean failed: %q", r.errMsg)
	}
	res := okData[CleanWorkspaceResult](t, r)
	if res.Closed != 1 || res.Kept != 2 || res.Asleep || res.Parked != 0 {
		t.Fatalf("result = %+v, want closed 1 kept 2 awake", res)
	}
	if h.has(h.shell) || !h.has(h.job) || !h.has(h.agent) {
		t.Fatalf("panes after clean: shell=%v job=%v agent=%v", h.has(h.shell), h.has(h.job), h.has(h.agent))
	}
	if h.ws.Asleep {
		t.Fatal("workspace went to sleep with panes still busy")
	}
	if lg := *h.log; len(lg) != 2 || lg[0] != "applyModel" || lg[1] != "ok" {
		t.Fatalf("effects = %v, want [applyModel ok]", lg)
	}
}

// A pane the backend cannot vouch for (host down, not yet spawned) is kept:
// "no answer" is never read as "nothing running".
func TestDispatchWorkspaceCleanKeepsUnknownPanes(t *testing.T) {
	h := newCleanHarness(t)
	h.b.activity[uint32(h.shell)] = PaneActivity{} // Known false
	r := h.resp()

	h.d.Dispatch(CmdWorkspaceClean, params(t, CleanWorkspaceParams{ID: h.ws.ID}), r)

	if res := okData[CleanWorkspaceResult](t, r); res.Closed != 0 || !h.has(h.shell) {
		t.Fatalf("unknown pane was closed: %+v", res)
	}
}

// sleep refuses while anything is busy, names the panes, and closes nothing.
func TestDispatchWorkspaceSleepRefusesBusy(t *testing.T) {
	h := newCleanHarness(t)
	r := h.resp()

	h.d.Dispatch(CmdWorkspaceSleep, params(t, CleanWorkspaceParams{ID: h.ws.ID}), r)

	if !r.failCall || !strings.Contains(r.errMsg, "still busy") {
		t.Fatalf("sleep with a busy pane: fail=%v msg=%q", r.failCall, r.errMsg)
	}
	jobPub, _ := h.s.PublicPaneID(h.job)
	agentPub, _ := h.s.PublicPaneID(h.agent)
	if !strings.Contains(r.errMsg, jobPub) || !strings.Contains(r.errMsg, agentPub) {
		t.Fatalf("refusal does not name the busy panes: %q", r.errMsg)
	}
	if !h.has(h.shell) {
		t.Fatal("a refused sleep closed the idle shell")
	}
	if lg := *h.log; len(lg) != 1 || lg[0] != "fail" {
		t.Fatalf("effects = %v, want [fail]", lg)
	}
}

// With the busy pane gone, "park" closes the idle agent, parks its session,
// and the workspace sleeps whole: one placeholder, no layout, asleep in
// workspace.list with the parked agent listed; the active index moved off it.
// wake then brings back a shell plus one pane per parked agent, staged to
// resume, and clears the parking.
func TestDispatchWorkspaceSleepParksAndWakeResumes(t *testing.T) {
	h := newCleanHarness(t)
	delete(h.b.activity, uint32(h.job)) // now a plain idle shell
	if err := h.s.FocusWorkspace(h.ws.ID); err != nil {
		t.Fatal(err)
	}
	r := h.resp()

	h.d.Dispatch(CmdWorkspaceSleep, params(t, CleanWorkspaceParams{ID: h.ws.ID, Agents: "park"}), r)

	if !r.okCall {
		t.Fatalf("sleep failed: %q", r.errMsg)
	}
	res := okData[CleanWorkspaceResult](t, r)
	if !res.Asleep || res.Parked != 1 || res.Closed != 3 || res.Kept != 0 {
		t.Fatalf("result = %+v, want asleep, 1 parked, 3 closed", res)
	}
	if !h.ws.Asleep || len(h.ws.Tabs) != 1 || h.ws.Tabs[0].Layout.PaneCount() != 1 {
		t.Fatalf("workspace not reduced to a sleeping placeholder: asleep=%v tabs=%d", h.ws.Asleep, len(h.ws.Tabs))
	}
	if h.s.ActiveWorkspace() == h.ws {
		t.Fatal("the active workspace is asleep")
	}
	e := h.entry(t)
	if !e.Asleep || len(e.Parked) != 1 || e.Parked[0].Agent != "claude" || e.Parked[0].Pane == "" {
		t.Fatalf("workspace.list entry = %+v", e)
	}
	// A view still pointing at it resolves to the active workspace, as a
	// closed workspace's would.
	if got := h.s.ResolveViewWorkspace(h.ws.ID); got != h.s.ActiveWorkspaceID() {
		t.Fatalf("view on a sleeping workspace resolves to %s, want the active %s", got, h.s.ActiveWorkspaceID())
	}

	*h.log = nil
	r = h.resp()
	h.d.Dispatch(CmdWorkspaceWake, params(t, WorkspaceParams{ID: h.ws.ID}), r)

	if !r.okCall {
		t.Fatalf("wake failed: %q", r.errMsg)
	}
	if h.ws.Asleep || h.ws.ParkedAgents != nil {
		t.Fatalf("after wake: asleep=%v parked=%v", h.ws.Asleep, h.ws.ParkedAgents)
	}
	if n := h.ws.Tabs[0].Layout.PaneCount(); n != 2 {
		t.Fatalf("panes after wake = %d, want shell + resumed agent", n)
	}
	if len(h.b.resumed) != 1 {
		t.Fatalf("staged resumes = %v, want one", h.b.resumed)
	}
	for _, a := range h.b.resumed {
		if a.Value != "sess-1" || a.Agent != "claude" {
			t.Fatalf("staged ref = %+v", a)
		}
	}
	// The stage precedes the applyModel that spawns the pane.
	if lg := *h.log; len(lg) != 3 || lg[0] != "stageResume" || lg[1] != "applyModel" || lg[2] != "ok" {
		t.Fatalf("wake effects = %v, want [stageResume applyModel ok]", lg)
	}
	if e := h.entry(t); e.Asleep || len(e.Parked) != 0 {
		t.Fatalf("workspace.list after wake = %+v", e)
	}
}

// A parked ref the backend cannot resume here gets no pane: the split is
// taken back rather than left as a shell impersonating the agent.
func TestDispatchWorkspaceWakeDropsUnresumable(t *testing.T) {
	h := newCleanHarness(t)
	h.ws.ParkAgent(workspace.ParkedAgent{Source: "hook", Agent: "codex", Kind: "id", Value: "x"})
	if _, err := h.ws.Sleep(workspace.SpawnSpec{}); err != nil {
		t.Fatal(err)
	}
	h.b.resumeRefuse = map[string]bool{"codex": true}
	r := h.resp()

	h.d.Dispatch(CmdWorkspaceWake, params(t, WorkspaceParams{ID: h.ws.ID}), r)

	if !r.okCall || h.ws.Asleep {
		t.Fatalf("wake: ok=%v asleep=%v (%q)", r.okCall, h.ws.Asleep, r.errMsg)
	}
	if n := h.ws.Tabs[0].Layout.PaneCount(); n != 1 {
		t.Fatalf("panes after a refused resume = %d, want just the shell", n)
	}
}

// "command" types the command into each idle agent and leaves it; a sleep
// under that mode therefore stays awake, and says so.
func TestDispatchWorkspaceSleepCommandSendsAndStaysAwake(t *testing.T) {
	h := newCleanHarness(t)
	delete(h.b.activity, uint32(h.job))
	r := h.resp()

	h.d.Dispatch(CmdWorkspaceSleep, params(t, CleanWorkspaceParams{ID: h.ws.ID, Agents: "command", Command: "/exit"}), r)

	if !r.okCall {
		t.Fatalf("sleep(command) failed: %q", r.errMsg)
	}
	res := okData[CleanWorkspaceResult](t, r)
	if res.Asleep || res.Sent != 1 || res.Closed != 2 || res.Kept != 1 {
		t.Fatalf("result = %+v, want awake, 1 sent, 2 closed, 1 kept", res)
	}
	if h.b.lastSend.Pane != uint32(h.agent) || h.b.lastSend.Text != "/exit" || !h.b.lastSend.Submit {
		t.Fatalf("sent = %+v", h.b.lastSend)
	}
	if !h.has(h.agent) || h.has(h.shell) || h.ws.Asleep {
		t.Fatalf("panes after sleep(command): agent=%v shell=%v asleep=%v", h.has(h.agent), h.has(h.shell), h.ws.Asleep)
	}

	// The mode needs its command.
	r = h.resp()
	h.d.Dispatch(CmdWorkspaceClean, params(t, CleanWorkspaceParams{ID: h.ws.ID, Agents: "command"}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "needs a command") {
		t.Fatalf("command mode without a command: fail=%v msg=%q", r.failCall, r.errMsg)
	}
}

// The last awake workspace cannot sleep; a clean whose verdict is "everything
// goes" there keeps one shell instead of emptying the workspace.
func TestDispatchWorkspaceSleepLastAwake(t *testing.T) {
	h := newCleanHarness(t)
	h.b.activity = nil // everything idle
	first := h.s.Workspaces()[0].ID
	if err := h.s.SleepWorkspace(first); err != nil {
		t.Fatalf("sleep w1: %v", err)
	}
	r := h.resp()
	h.d.Dispatch(CmdWorkspaceSleep, params(t, CleanWorkspaceParams{ID: h.ws.ID}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "last awake") {
		t.Fatalf("sleeping the last awake workspace: fail=%v msg=%q", r.failCall, r.errMsg)
	}

	r = h.resp()
	h.d.Dispatch(CmdWorkspaceClean, params(t, CleanWorkspaceParams{ID: h.ws.ID}), r)
	res := okData[CleanWorkspaceResult](t, r)
	if res.Asleep || res.Closed != 2 || res.Kept != 1 || len(res.KeptPanes) != 1 {
		t.Fatalf("clean of the last awake workspace = %+v, want 2 closed, 1 kept", res)
	}
	if n := h.ws.Tabs[0].Layout.PaneCount(); n != 1 || h.ws.Asleep {
		t.Fatalf("after clean: panes=%d asleep=%v", n, h.ws.Asleep)
	}
}

// Focusing a sleeping workspace wakes it first, and the sleeping workspace
// refuses a new tab (a fan-out must not wake what was put to bed).
func TestDispatchWorkspaceFocusWakes(t *testing.T) {
	h := newCleanHarness(t)
	if _, err := h.ws.Sleep(workspace.SpawnSpec{}); err != nil {
		t.Fatal(err)
	}
	r := h.resp()
	h.d.Dispatch(CmdTabCreate, params(t, TabCreateParams{Workspace: h.ws.ID}), r)
	if !r.failCall || !strings.Contains(r.errMsg, "asleep") {
		t.Fatalf("tab.create into a sleeping workspace: fail=%v msg=%q", r.failCall, r.errMsg)
	}

	r = h.resp()
	h.d.Dispatch(CmdWorkspaceFocus, params(t, WorkspaceParams{ID: h.ws.ID}), r)
	if !r.okCall || h.ws.Asleep {
		t.Fatalf("focus: ok=%v asleep=%v (%q)", r.okCall, h.ws.Asleep, r.errMsg)
	}
	if got := h.b.viewMoves; len(got) != 1 || got[0] != h.ws.ID {
		t.Fatalf("view moves = %v", got)
	}

	// Clean and sleep on an already-sleeping workspace ack as asleep.
	if _, err := h.ws.Sleep(workspace.SpawnSpec{}); err != nil {
		t.Fatal(err)
	}
	r = h.resp()
	h.d.Dispatch(CmdWorkspaceClean, params(t, CleanWorkspaceParams{ID: h.ws.ID}), r)
	if res := okData[CleanWorkspaceResult](t, r); !res.Asleep || res.Closed != 0 {
		t.Fatalf("clean of a sleeping workspace = %+v", res)
	}
}

// A snapshot whose active workspace is asleep restores with the active index
// moved to an awake one; with nothing awake, the active workspace is woken.
func TestRestoreHealsSleepingActive(t *testing.T) {
	h := newCleanHarness(t)
	if _, err := h.ws.Sleep(workspace.SpawnSpec{}); err != nil {
		t.Fatal(err)
	}
	snap := h.s.Snapshot()
	snap.Active = 1 // the sleeping one, which SleepWorkspace would never leave active
	restored, err := RestoreSession(&fakeSpawner{}, snap)
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	if restored.ActiveWorkspace().Asleep || restored.ActiveIndex() != 0 {
		t.Fatalf("active after restore = %d (asleep=%v), want 0 awake", restored.ActiveIndex(), restored.ActiveWorkspace().Asleep)
	}

	for i := range snap.Workspaces {
		snap.Workspaces[i].Asleep = true
	}
	restored, err = RestoreSession(&fakeSpawner{}, snap)
	if err != nil {
		t.Fatalf("RestoreSession(all asleep): %v", err)
	}
	if restored.ActiveWorkspace().Asleep {
		t.Fatal("every workspace asleep: the active one was not woken")
	}
}
