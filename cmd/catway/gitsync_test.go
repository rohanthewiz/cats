//go:build ghostty

package main

import (
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/gitsync"
)

// The sweep's own logic is what these pin — which workspaces get asked about,
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

// A workspace with no identity cwd is not asked about at all. Falling back to
// the daemon's own working directory would paint every such row with the state
// of wherever catway happened to be started.
func TestGitSyncTargetsSkipWorkspacesWithNoDirectory(t *testing.T) {
	o := newGitSyncOrch(t)
	for _, ws := range o.session.Workspaces() {
		ws.IdentityCwd = ""
	}
	if got := o.gitSyncTargets(); len(got) != 0 {
		t.Fatalf("targets = %+v, want none", got)
	}
}

// A workspace pinned to another machine names a path in that machine's
// filesystem. Resolving it here would report on whatever local directory
// happens to share the name, which for a project checked out in the same place
// on both boxes is a plausible and completely wrong answer.
func TestGitSyncTargetsSkipRemoteWorkspaces(t *testing.T) {
	o := newGitSyncOrch(t)
	dir := t.TempDir()
	id, err := o.session.CreateWorkspaceAtOn(dir, "devbox")
	if err != nil {
		t.Fatalf("CreateWorkspaceAtOn: %v", err)
	}
	for _, tgt := range o.gitSyncTargets() {
		if tgt.ws == id {
			t.Fatalf("a workspace on another host was targeted: %+v", tgt)
		}
	}
	// The local workspaces are still there — the filter is the host, not the
	// presence of any remote workspace at all.
	if len(o.gitSyncTargets()) == 0 {
		t.Fatal("no local workspaces targeted")
	}
}

// A local workspace is targeted at its identity cwd, the same directory its
// row's NAME is derived from — so the dot and the label always describe the
// same checkout.
func TestGitSyncTargetsUseIdentityCwd(t *testing.T) {
	o := newGitSyncOrch(t)
	dir := t.TempDir()
	ws := o.session.Workspaces()[0]
	ws.IdentityCwd = dir

	var found bool
	for _, tgt := range o.gitSyncTargets() {
		if tgt.ws == ws.ID {
			found = true
			if tgt.dir != dir {
				t.Fatalf("dir = %q, want %q", tgt.dir, dir)
			}
		}
	}
	if !found {
		t.Fatalf("workspace %s was not targeted", ws.ID)
	}
}

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

// The busy flag is cleared by applying a sweep's results, so the next tick can
// start one. Without it a single slow sweep would wedge the poll forever.
func TestApplyWorkspaceGitClearsBusy(t *testing.T) {
	o := newGitSyncOrch(t)
	o.wsGitBusy = true
	o.applyWorkspaceGit(nil)
	if o.wsGitBusy {
		t.Fatal("busy flag survived the sweep")
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

// A workspace that has been CLOSED stops being reported immediately rather than
// two minutes later: the sweep prunes the cache on the way in, before it starts
// resolving anything.
func TestStartSweepPrunesClosedWorkspaces(t *testing.T) {
	o := newGitSyncOrch(t)
	o.wsGit["w-gone"] = browserproto.WorkspaceGitInfo{Workspace: "w-gone", Sync: browserproto.GitSynced}
	// Busy, so the sweep prunes and then declines to start a second resolve —
	// which is exactly the interleaving this has to survive.
	o.wsGitBusy = true
	o.startWorkspaceGitSweep()
	if _, ok := o.wsGit["w-gone"]; ok {
		t.Fatal("a closed workspace kept its cached state")
	}
}
