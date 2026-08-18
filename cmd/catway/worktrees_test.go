//go:build ghostty

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/worktree"
)

// A workspace created at a checkout path is found by workspaceForPathOn
// (identity cwd match, symlink-canonicalized), and its panes spawn in the
// checkout via paneCwd — the cwd-threading seam worktree.create/open relies on.
func TestWorktreeWorkspaceCwd(t *testing.T) {
	o, err := newOrch("", "/base")
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	dir := t.TempDir()
	id, err := o.session.CreateWorkspaceAt(dir)
	if err != nil {
		t.Fatalf("CreateWorkspaceAt: %v", err)
	}

	if got := o.workspaceForPathOn(dir, localHostID); got != id {
		t.Fatalf("workspaceForPathOn(%s) = %q, want %q", dir, got, id)
	}
	if got := o.workspaceForPathOn(filepath.Join(dir, "elsewhere"), localHostID); got != "" {
		t.Fatalf("workspaceForPathOn on a non-checkout path = %q, want \"\"", got)
	}

	// The new workspace is active; its pane must inherit the checkout cwd.
	pid, ok := o.session.FocusedPane()
	if !ok {
		t.Fatal("no focused pane after CreateWorkspaceAt")
	}
	if got := o.paneCwd(uint32(pid)); canonPath(got) != canonPath(dir) {
		t.Fatalf("paneCwd = %q, want %q", got, dir)
	}
}

// hostGuardResponder captures one synchronous Start* resolution — enough for
// the refusals, which never reach a goroutine.
type hostGuardResponder struct {
	ok, fail bool
	data     any
	errMsg   string
}

func (*hostGuardResponder) WantsReply() bool  { return true }
func (r *hostGuardResponder) OK(data any)     { r.ok, r.data = true, data }
func (r *hostGuardResponder) Fail(msg string) { r.fail, r.errMsg = true, msg }

// The whole point of the slice: a worktree command anchored on a pane that runs
// on another machine is executed *there*. It used to be refused, because git
// runs as a subprocess of this process and could only ever act on this disk.
func TestWorktreeListRunsOnThePanesHost(t *testing.T) {
	o, _, remotePane, _, pdRemote := twoHostOrch(t)
	go o.run()
	d := o.hosts[testRemoteHost]
	d.setFeatures([]string{orchestration.FeatureWorktree})

	r := &hostGuardResponder{}
	syncPost(o, func() {
		o.StartWorktreeList(r, app.WorktreeListParams{Pane: &remotePane})
	})
	if r.ok || r.fail {
		t.Fatalf("a remote worktree.list must not resolve before the daemon answers: %+v", r)
	}

	payload := pdRemote.expect(t, orchestration.MsgRequestWorktree)
	var req orchestration.RequestWorktree
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req.Req.Op != worktree.OpList {
		t.Errorf("op = %q, want %q", req.Req.Op, worktree.OpList)
	}
	// The anchor is the remote pane's own directory, and the configured worktree
	// root travels UNEXPANDED: "~" is the remote user's home.
	if req.Req.Cwd == "" {
		t.Error("the anchor cwd did not travel")
	}
	o.worktreeDir = "~/.cats/worktrees"
	r2 := &hostGuardResponder{}
	syncPost(o, func() { o.StartWorktreeList(r2, app.WorktreeListParams{Pane: &remotePane}) })
	var req2 orchestration.RequestWorktree
	if err := json.Unmarshal(pdRemote.expect(t, orchestration.MsgRequestWorktree), &req2); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req2.Req.Root != "~/.cats/worktrees" {
		t.Errorf("root = %q, want the untouched configured value", req2.Req.Root)
	}
	if req2.ID == req.ID {
		t.Errorf("two in-flight requests share id %d — they could not be told apart", req.ID)
	}

	// The daemon's answer, matched by id, becomes the command's result. Note the
	// SECOND request is answered first, which is exactly why the id exists.
	d.dispatch(orchestration.MsgWorktreeResult, mustJSON(t, orchestration.NewWorktreeResult(req2.ID, worktree.OpResult{
		Checkout: "/srv/repo",
		Root:     "/home/remote/.cats/worktrees",
		Entries: []worktree.Entry{
			{Path: "/srv/repo", Branch: "main"},
			{Path: "/srv/wt/feature", Branch: "feature"},
			{Path: "/srv/bare", IsBare: true},
		},
	})))
	syncPost(o, func() {}) // the dispatch's closure is ahead of this one

	if r.ok || r.fail {
		t.Fatalf("the FIRST request was resolved by the second one's reply: %+v", r)
	}
	res, isRes := r2.data.(app.WorktreeListResult)
	if !r2.ok || !isRes {
		t.Fatalf("no result after the daemon answered: %+v", r2)
	}
	if res.RepoRoot != "/srv/repo" || res.RepoName != "repo" {
		t.Fatalf("repo = %q/%q, want the remote machine's answer", res.RepoRoot, res.RepoName)
	}
	// The root the *answering* machine expanded, not this one's home.
	if res.WorktreeRoot != "/home/remote/.cats/worktrees" {
		t.Fatalf("worktree root = %q, want the remote home", res.WorktreeRoot)
	}
	if len(res.Worktrees) != 2 {
		t.Fatalf("worktrees = %+v, want the bare entry dropped", res.Worktrees)
	}
	if !res.Worktrees[0].Current || res.Worktrees[1].Current {
		t.Fatalf("current flags = %+v, want the anchor's own checkout marked", res.Worktrees)
	}
}

// worktree.create on a remote pane creates the checkout there and roots the new
// workspace on that host — a workspace pinned to the machine whose disk holds
// the directory, not to the session's default.
func TestWorktreeCreateOpensTheWorkspaceOnThatHost(t *testing.T) {
	o, _, remotePane, _, pdRemote := twoHostOrch(t)
	go o.run()
	d := o.hosts[testRemoteHost]
	d.setFeatures([]string{orchestration.FeatureWorktree})

	r := &hostGuardResponder{}
	syncPost(o, func() {
		o.StartWorktreeCreate(r, app.WorktreeCreateParams{Pane: &remotePane, Branch: "feature/x"})
	})
	var req orchestration.RequestWorktree
	if err := json.Unmarshal(pdRemote.expect(t, orchestration.MsgRequestWorktree), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	// The branch is named here, not by whoever runs git: it is the value the
	// command reports back and the name the workspace takes.
	if req.Req.Op != worktree.OpCreate || req.Req.Branch != "feature/x" {
		t.Fatalf("request = %+v, want a create of feature/x", req.Req)
	}

	d.dispatch(orchestration.MsgWorktreeResult, mustJSON(t,
		orchestration.NewWorktreeResult(req.ID, worktree.OpResult{Path: "/srv/wt/feature-x"})))
	syncPost(o, func() {}) // the dispatch's closure is ahead of this one

	res, isRes := r.data.(app.WorktreeCreateResult)
	if !r.ok || !isRes {
		t.Fatalf("no result after the daemon answered: %+v", r)
	}
	if res.Path != "/srv/wt/feature-x" || res.Branch != "feature/x" {
		t.Fatalf("result = %+v", res)
	}
	var found bool
	for _, ws := range o.session.Workspaces() {
		if ws.ID != res.Workspace {
			continue
		}
		found = true
		if ws.HostID != testRemoteHost {
			t.Fatalf("workspace host = %q, want %q — its checkout is on that disk", ws.HostID, testRemoteHost)
		}
		if ws.IdentityCwd != "/srv/wt/feature-x" {
			t.Fatalf("workspace cwd = %q, want the remote checkout", ws.IdentityCwd)
		}
	}
	if !found {
		t.Fatalf("no workspace %q", res.Workspace)
	}
}

// A host that cannot run the operation is refused by name, with the reason:
// an un-upgraded cathost is a different fix from an unreachable one. There is
// no in-process fallback for another machine — running git here would act on
// this disk, which is the bug the capability exists to prevent.
func TestWorktreeRefusesAHostThatCannotRunIt(t *testing.T) {
	o, localPane, remotePane, _, _ := twoHostOrch(t)

	r := &hostGuardResponder{}
	o.StartWorktreeList(r, app.WorktreeListParams{Pane: &remotePane})
	if !r.fail || !strings.Contains(r.errMsg, "cannot run git-worktree commands") {
		t.Fatalf("worktree.list on an incapable host: fail=%v err=%q", r.fail, r.errMsg)
	}

	o.hosts[testRemoteHost].setConn(nil)
	r = &hostGuardResponder{}
	o.StartWorktreeCreate(r, app.WorktreeCreateParams{Pane: &remotePane})
	if !r.fail || !strings.Contains(r.errMsg, "is not connected") {
		t.Fatalf("worktree.create on a disconnected host: fail=%v err=%q", r.fail, r.errMsg)
	}

	// The local pane falls back to running git in this process, which is
	// asynchronous — nothing resolves inline, which is how this test tells the
	// refusal did not fire for it.
	r = &hostGuardResponder{}
	o.StartWorktreeList(r, app.WorktreeListParams{Pane: &localPane})
	if r.ok || r.fail {
		t.Fatalf("a local worktree.list must not be refused: %+v", r)
	}
}

// The local fallback is the whole command, not a stub: with no cathost able to
// answer, catway runs the same worktree.Do against a real repository.
func TestWorktreeListFallsBackToThisProcess(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "-q", "-m", "x", "--allow-empty"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=cats", "GIT_AUTHOR_EMAIL=cats@example.invalid",
			"GIT_COMMITTER_NAME=cats", "GIT_COMMITTER_EMAIL=cats@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	o, err := newOrch("", repo)
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	go o.run()
	done := make(chan app.WorktreeListResult, 1)
	syncPost(o, func() {
		o.StartWorktreeList(chanResponder{t: t, out: done}, app.WorktreeListParams{})
	})
	res := <-done
	if canonPath(res.RepoRoot) != canonPath(repo) {
		t.Fatalf("repo root = %q, want %q", res.RepoRoot, repo)
	}
	if len(res.Worktrees) != 1 || !res.Worktrees[0].Current {
		t.Fatalf("worktrees = %+v, want the repo itself, marked current", res.Worktrees)
	}
}

// chanResponder delivers one asynchronous worktree.list result to a test.
type chanResponder struct {
	t   *testing.T
	out chan app.WorktreeListResult
}

func (chanResponder) WantsReply() bool { return true }
func (c chanResponder) OK(data any) {
	res, ok := data.(app.WorktreeListResult)
	if !ok {
		c.t.Errorf("result of an unexpected shape: %T", data)
	}
	c.out <- res
}
func (c chanResponder) Fail(msg string) {
	c.t.Errorf("worktree.list failed: %s", msg)
	c.out <- app.WorktreeListResult{}
}

// A workspace whose host has left the roster cannot have its checkout removed:
// the path names a filesystem this catway can no longer reach, and resolving
// its host to the default would run `git worktree remove` on the wrong machine
// — which, on a coincidental path match, deletes the wrong checkout. That is
// exactly the state a forced detach leaves behind.
func TestWorktreeRemoveRefusesADepartedHost(t *testing.T) {
	o, _, remotePane, _, _ := twoHostOrch(t)
	ws := o.session.PaneWorkspace(layout.PaneID(remotePane))
	delete(o.hosts, testRemoteHost)

	r := &hostGuardResponder{}
	o.StartWorktreeRemove(r, app.WorktreeRemoveParams{Workspace: ws.ID})
	if !r.fail || !strings.Contains(r.errMsg, "not attached") {
		t.Fatalf("remove on a departed host: fail=%v err=%q", r.fail, r.errMsg)
	}
}

// A path only identifies a directory together with a host: two machines can
// hold the very same path string and mean two different directories. So the
// workspace open "on this checkout" is a per-host question — worktree.open
// would otherwise focus a workspace on the wrong machine instead of opening
// the checkout the user pointed at.
func TestWorkspaceForPathIsPerHost(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)
	dir := t.TempDir()

	remote, err := o.session.CreateWorkspaceAtOn(dir, testRemoteHost)
	if err != nil {
		t.Fatalf("CreateWorkspaceAtOn: %v", err)
	}
	if got := o.workspaceForPathOn(dir, localHostID); got != "" {
		t.Fatalf("local lookup matched the remote workspace %q (want none)", got)
	}
	if got := o.workspaceForPathOn(dir, testRemoteHost); got != remote {
		t.Fatalf("remote lookup = %q, want %q", got, remote)
	}

	local, err := o.session.CreateWorkspaceAt(dir)
	if err != nil {
		t.Fatalf("CreateWorkspaceAt: %v", err)
	}
	if got := o.workspaceForPathOn(dir, localHostID); got != local {
		t.Fatalf("local lookup = %q, want the local workspace %q (remote is %q)", got, local, remote)
	}
}
