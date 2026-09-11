//go:build ghostty

package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/gitsync"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// The sweep's own logic is what these pin — who is asked about each workspace,
// what gets cached, and when a rollup is worth broadcasting. Resolution itself
// is internal/gitsync's business and is tested there against real repositories;
// here the answers are injected, so no test forks git or touches a network.

func newGitSyncOrch(t *testing.T) *orch {
	t.Helper()
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	return o
}

// planFor is the plan entry for one workspace, and whether there was one at all.
func planFor(p gitSyncPlan, ws string) (t gitSyncTarget, viaHost, found bool) {
	for _, x := range p.viaHost {
		if x.ws == ws {
			return x, true, true
		}
	}
	for _, x := range p.local {
		if x.ws == ws {
			return x, false, true
		}
	}
	return gitSyncTarget{}, false, false
}

// ---- planning ---------------------------------------------------------------

// A workspace with no identity cwd is not asked about at all. Falling back to
// the daemon's own working directory would paint every such row with the state
// of wherever catway happened to be started.
func TestGitSyncPlanSkipsWorkspacesWithNoDirectory(t *testing.T) {
	o := newGitSyncOrch(t)
	for _, ws := range o.session.Workspaces() {
		ws.IdentityCwd = ""
	}
	if p := o.gitSyncPlan(); !p.empty() {
		t.Fatalf("plan = %+v, want nothing", p)
	}
}

// With no cathost able to answer, a LOCAL workspace still resolves in process —
// the fallback that covers an older local daemon, or one not yet connected.
func TestGitSyncPlanFallsBackToLocalResolution(t *testing.T) {
	o := newGitSyncOrch(t)
	dir := t.TempDir()
	ws := o.session.Workspaces()[0]
	ws.IdentityCwd = dir

	tgt, viaHost, found := planFor(o.gitSyncPlan(), ws.ID)
	if !found {
		t.Fatalf("workspace %s was not planned", ws.ID)
	}
	if viaHost {
		t.Fatal("a host with no git_sync capability was asked anyway")
	}
	// The identity cwd, the same field the row's NAME comes from — so the dot
	// and the label always describe the same checkout.
	if tgt.dir != dir {
		t.Fatalf("dir = %q, want %q", tgt.dir, dir)
	}
}

// The whole point of the slice: a workspace whose directory lives on another
// machine is resolved THERE. It used to be skipped, because git ran as a
// subprocess of this process and could only ever read this disk.
func TestGitSyncPlanSendsRemoteWorkspacesToTheirHost(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)
	o.hosts[testRemoteHost].setFeatures([]string{orchestration.FeatureGitSync})

	// twoHostOrch's second workspace is the one pinned to the remote host.
	var remoteWS string
	for _, ws := range o.session.Workspaces() {
		if ws.HostID == testRemoteHost {
			remoteWS = ws.ID
			ws.IdentityCwd = "/srv/repo" // a path on THAT filesystem
		}
	}
	if remoteWS == "" {
		t.Fatal("no remote workspace in the fixture")
	}

	tgt, viaHost, found := planFor(o.gitSyncPlan(), remoteWS)
	if !found || !viaHost {
		t.Fatalf("remote workspace planned viaHost=%v found=%v, want both true", viaHost, found)
	}
	if tgt.host != testRemoteHost {
		t.Fatalf("host = %q, want %q", tgt.host, testRemoteHost)
	}
	if tgt.dir != "/srv/repo" {
		t.Fatalf("dir = %q — the remote path must travel untouched", tgt.dir)
	}
}

// A remote host too old to answer is skipped rather than resolved here: its
// path names a directory on that machine, and for a monorepo checked out at the
// same place on both boxes, reading it locally is a plausible and completely
// wrong answer.
func TestGitSyncPlanSkipsRemoteHostsThatCannotAnswer(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)
	// Deliberately a capability this sweep does not use: the host is connected
	// and useful, just not for this.
	o.hosts[testRemoteHost].setFeatures([]string{orchestration.FeatureWorktree})

	for _, ws := range o.session.Workspaces() {
		if ws.HostID == testRemoteHost {
			ws.IdentityCwd = "/srv/repo"
			if _, _, found := planFor(o.gitSyncPlan(), ws.ID); found {
				t.Fatal("a host that cannot answer was planned anyway")
			}
		}
	}
	// The local workspaces are untouched — the filter is per host, not a switch
	// that turns the whole sweep off.
	if o.gitSyncPlan().empty() {
		t.Fatal("the local workspaces were dropped along with the remote one")
	}
}

// The local cathost is asked too, when it can answer: one code path and one
// environment doing the resolving, which is the same argument that put branch
// resolution on the daemons.
func TestGitSyncPlanPrefersTheLocalHostWhenItCanAnswer(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)
	o.hosts[o.defaultHost].setFeatures([]string{orchestration.FeatureGitSync})
	ws := o.session.Workspaces()[0]
	ws.IdentityCwd = t.TempDir()

	_, viaHost, found := planFor(o.gitSyncPlan(), ws.ID)
	if !found || !viaHost {
		t.Fatalf("local workspace planned viaHost=%v found=%v, want both true", viaHost, found)
	}
}

// ---- the round trip ---------------------------------------------------------

// A remote workspace's directory travels to its host as a request_git_sync, and
// the host's answer becomes the row's colour.
func TestWorkspaceGitAsksTheHostAndUsesItsAnswer(t *testing.T) {
	o, _, _, _, pdRemote := twoHostOrch(t)
	go o.run()
	d := o.hosts[testRemoteHost]
	d.setFeatures([]string{orchestration.FeatureGitSync})

	var remoteWS string
	syncPost(o, func() {
		for _, ws := range o.session.Workspaces() {
			if ws.HostID == testRemoteHost {
				remoteWS, ws.IdentityCwd = ws.ID, "/srv/repo"
			} else {
				ws.IdentityCwd = "" // keep the local half out of this sweep
			}
		}
		o.startWorkspaceGitSweep()
	})

	var req orchestration.RequestGitSync
	if err := json.Unmarshal(pdRemote.expect(t, orchestration.MsgRequestGitSync), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req.Dir != "/srv/repo" {
		t.Fatalf("dir = %q, want the remote path untouched", req.Dir)
	}

	d.dispatch(orchestration.MsgGitSyncResult, mustJSON(t, orchestration.NewGitSyncResult(req.ID,
		gitsync.Status{State: gitsync.Behind, Branch: "main", Remote: "origin"})))
	syncPost(o, func() {}) // the dispatch's closure is ahead of this one

	var got browserproto.WorkspaceGit
	syncPost(o, func() { got = o.workspaceGitMsg() })
	if len(got.Workspaces) != 1 {
		t.Fatalf("rollup = %+v, want the remote workspace's row", got.Workspaces)
	}
	want := browserproto.WorkspaceGitInfo{
		Workspace: remoteWS, Sync: browserproto.GitBehind, Branch: "main", Remote: "origin",
	}
	if got.Workspaces[0] != want {
		t.Fatalf("row = %+v, want %+v", got.Workspaces[0], want)
	}
}

// A host that never answers must not wedge the poll: the pending machinery's
// failure path (a drop, or the request timing out) resolves the sweep with an
// Unknown, which both clears the in-flight sweep and leaves the dot uncoloured.
func TestWorkspaceGitSurvivesAHostThatNeverAnswers(t *testing.T) {
	o, _, _, _, pdRemote := twoHostOrch(t)
	go o.run()
	d := o.hosts[testRemoteHost]
	d.setFeatures([]string{orchestration.FeatureGitSync})

	syncPost(o, func() {
		for _, ws := range o.session.Workspaces() {
			if ws.HostID == testRemoteHost {
				ws.IdentityCwd = "/srv/repo"
			} else {
				ws.IdentityCwd = ""
			}
		}
		// A stale colour to prove the failure CLEARS it rather than leaving it:
		// a "in sync" that outlives the link is the one reading that misleads.
		o.wsGit["w-stale"] = browserproto.WorkspaceGitInfo{Workspace: "w-stale", Sync: browserproto.GitSynced}
		o.startWorkspaceGitSweep()
	})
	pdRemote.expect(t, orchestration.MsgRequestGitSync)

	// The host drops — the same path a real disconnect takes.
	syncPost(o, func() { o.flushPendingFor(testRemoteHost, "host gone") })
	syncPost(o, func() {}) // let the responder's own post land

	var inFlight *gitSweep
	var rows int
	syncPost(o, func() { inFlight, rows = o.wsGitSweep, len(o.workspaceGitMsg().Workspaces) })
	if inFlight != nil {
		t.Fatal("a dropped host left the sweep in flight — the poll would never run again")
	}
	if rows != 0 {
		t.Fatalf("rollup has %d rows, want none", rows)
	}
}

// A host coming back asks about its workspaces at once rather than waiting out
// the poll. Every sweep it was down for skipped them — nothing here can answer
// for a directory on its filesystem — so without this a reconnected host's rows
// stay blank for up to two minutes for no visible reason.
func TestHostConnectAsksAboutItsWorkspacesAtOnce(t *testing.T) {
	o, _, _, _, pdRemote := twoHostOrch(t)
	go o.run()
	d := o.hosts[testRemoteHost]
	syncPost(o, func() {
		for _, ws := range o.session.Workspaces() {
			if ws.HostID == testRemoteHost {
				ws.IdentityCwd = "/srv/repo"
			} else {
				ws.IdentityCwd = ""
			}
		}
	})

	// Down, or too old: the sweep plans nothing and asks nobody.
	syncPost(o, o.startWorkspaceGitSweep)
	var inFlight *gitSweep
	syncPost(o, func() { inFlight = o.wsGitSweep })
	if inFlight != nil {
		t.Fatal("a host that cannot answer still started a sweep")
	}

	// The capability arriving is what a completed handshake installs; the post
	// below is the rest of that path.
	d.setFeatures([]string{orchestration.FeatureGitSync})
	syncPost(o, o.startWorkspaceGitSweep)

	var req orchestration.RequestGitSync
	if err := json.Unmarshal(pdRemote.expect(t, orchestration.MsgRequestGitSync), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req.Dir != "/srv/repo" {
		t.Fatalf("dir = %q, want the reconnected host's workspace", req.Dir)
	}
}

// ---- collection -------------------------------------------------------------

// A sweep applies once, when its LAST answer is in. Applying each arrival would
// be correct but would send one full rollup per workspace at startup, when every
// row changes at once.
func TestCollectWorkspaceGitWaitsForEveryAnswer(t *testing.T) {
	o := newGitSyncOrch(t)
	ws := o.session.Workspaces()[0].ID
	o.wsGitGen++
	o.wsGitSweep = &gitSweep{gen: o.wsGitGen, outstanding: 2}

	o.collectWorkspaceGit(o.wsGitGen, []gitSyncResult{{ws: ws, st: gitsync.Status{State: gitsync.Synced, Branch: "main"}}})
	if o.wsGitSweep == nil {
		t.Fatal("the sweep ended on its first answer of two")
	}
	if len(o.wsGit) != 0 {
		t.Fatal("results were applied before the sweep finished")
	}
	o.collectWorkspaceGit(o.wsGitGen, []gitSyncResult{{ws: "w-other", st: gitsync.Status{}}})
	if o.wsGitSweep != nil {
		t.Fatal("the sweep did not end on its last answer")
	}
	if len(o.wsGit) != 1 {
		t.Fatalf("cache = %+v, want the one known state", o.wsGit)
	}
}

// A late answer from an abandoned sweep is dropped rather than folded into its
// successor: the two describe different sets of workspaces, and counting it
// would end the new sweep early.
func TestCollectWorkspaceGitIgnoresStaleGenerations(t *testing.T) {
	o := newGitSyncOrch(t)
	o.wsGitGen = 7
	o.wsGitSweep = &gitSweep{gen: 7, outstanding: 1}
	o.collectWorkspaceGit(6, []gitSyncResult{{ws: "w1", st: gitsync.Status{State: gitsync.Synced}}})
	if o.wsGitSweep == nil {
		t.Fatal("an answer from an older sweep ended the current one")
	}
	if len(o.wsGit) != 0 {
		t.Fatalf("cache = %+v, want nothing from a stale sweep", o.wsGit)
	}
}

// A sweep already in flight is left to finish. With an unreachable remote one
// pass can outlast the interval, and starting a second would double the load on
// exactly the host that is already not answering.
func TestStartSweepDoesNotStackSweeps(t *testing.T) {
	o := newGitSyncOrch(t)
	o.session.Workspaces()[0].IdentityCwd = t.TempDir()
	first := &gitSweep{gen: 99, outstanding: 1}
	o.wsGitSweep = first
	o.startWorkspaceGitSweep()
	if o.wsGitSweep != first {
		t.Fatal("a second sweep started on top of the one in flight")
	}
}

// ---- caching ----------------------------------------------------------------

// Applying a sweep caches only the workspaces that had an answer; an Unknown
// result leaves nothing behind, which is what lets the client draw the plain
// dot for "not a repo / no remote / could not reach it".
func TestApplyWorkspaceGitCachesOnlyKnownStates(t *testing.T) {
	o := newGitSyncOrch(t)
	ws := o.session.Workspaces()[0].ID
	o.applyWorkspaceGit([]gitSyncResult{
		{ws: ws, st: gitsync.Status{State: gitsync.Ahead, Branch: "main", Remote: "origin", Ahead: 2}},
		{ws: "w-nope", st: gitsync.Status{}},
	})
	if _, ok := o.wsGit["w-nope"]; ok {
		t.Fatal("an unknown state was cached")
	}
	msg := o.workspaceGitMsg()
	if len(msg.Workspaces) != 1 {
		t.Fatalf("rollup = %+v, want one row", msg.Workspaces)
	}
	want := browserproto.WorkspaceGitInfo{
		Workspace: ws, Sync: browserproto.GitAhead, Branch: "main", Remote: "origin", Ahead: 2,
	}
	if msg.Workspaces[0] != want {
		t.Fatalf("row = %+v, want %+v", msg.Workspaces[0], want)
	}
	if msg.T != browserproto.MsgWorkspaceGit {
		t.Fatalf("message type = %q", msg.T)
	}
}

// A workspace that STOPS having an answer drops its colour rather than keeping
// the last one: a stale "in sync" is the single reading that would actively
// mislead, since it is the one that says "go ahead".
func TestApplyWorkspaceGitForgetsWorkspacesThatLoseTheirAnswer(t *testing.T) {
	o := newGitSyncOrch(t)
	ws := o.session.Workspaces()[0].ID
	o.applyWorkspaceGit([]gitSyncResult{{ws: ws, st: gitsync.Status{State: gitsync.Synced, Branch: "main"}}})
	if len(o.workspaceGitMsg().Workspaces) != 1 {
		t.Fatal("the first sweep did not cache anything")
	}
	o.applyWorkspaceGit([]gitSyncResult{{ws: ws, st: gitsync.Status{}}})
	if got := o.workspaceGitMsg().Workspaces; len(got) != 0 {
		t.Fatalf("rollup = %+v, want empty", got)
	}
}

// The rollup is ordered by the session's workspace order, so it matches the
// sidebar it annotates. Map iteration cannot supply that, and an order that
// reshuffled between sweeps would defeat any diffing on the client.
func TestWorkspaceGitMsgFollowsSessionOrder(t *testing.T) {
	o := newGitSyncOrch(t)
	first := o.session.Workspaces()[0].ID
	second, err := o.session.CreateWorkspaceAt(t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspaceAt: %v", err)
	}
	// Applied in the reverse of the session's order, so a rollup that merely
	// preserved insertion order would fail this.
	o.applyWorkspaceGit([]gitSyncResult{
		{ws: second, st: gitsync.Status{State: gitsync.Behind, Branch: "main"}},
		{ws: first, st: gitsync.Status{State: gitsync.Synced, Branch: "main"}},
	})
	got := o.workspaceGitMsg().Workspaces
	if len(got) != 2 || got[0].Workspace != first || got[1].Workspace != second {
		t.Fatalf("rollup order = %+v, want %s then %s", got, first, second)
	}
}

// A workspace nobody can answer for any more — closed, or its host gone — stops
// being reported immediately rather than two minutes later: the sweep prunes on
// the way in, before it asks anybody anything.
func TestStartSweepPrunesWorkspacesNobodyAnswersFor(t *testing.T) {
	o := newGitSyncOrch(t)
	o.wsGit["w-gone"] = browserproto.WorkspaceGitInfo{Workspace: "w-gone", Sync: browserproto.GitSynced}
	// A sweep in flight, so this prunes and then declines to start a second —
	// which is exactly the interleaving the prune has to survive.
	o.wsGitSweep = &gitSweep{gen: 1, outstanding: 1}
	o.startWorkspaceGitSweep()
	if _, ok := o.wsGit["w-gone"]; ok {
		t.Fatal("a workspace nobody answers for kept its cached state")
	}
}
