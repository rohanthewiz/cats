//go:build ghostty

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/layout"
)

// A workspace created at a checkout path is found by workspaceForPath (identity
// cwd match, symlink-canonicalized), and its panes spawn in the checkout via
// paneCwd — the cwd-threading seam worktree.create/open relies on.
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

	if got := o.workspaceForPath(dir); got != id {
		t.Fatalf("workspaceForPath(%s) = %q, want %q", dir, got, id)
	}
	if got := o.workspaceForPath(filepath.Join(dir, "elsewhere")); got != "" {
		t.Fatalf("workspaceForPath on a non-checkout path = %q, want \"\"", got)
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
// the local-only refusals, which never reach a goroutine.
type hostGuardResponder struct {
	ok, fail bool
	data     any
	errMsg   string
}

func (*hostGuardResponder) WantsReply() bool  { return true }
func (r *hostGuardResponder) OK(data any)     { r.ok, r.data = true, data }
func (r *hostGuardResponder) Fail(msg string) { r.fail, r.errMsg = true, msg }

// git runs as a subprocess of this catway, so every worktree verb acts on this
// machine's disk. Anchored on a pane whose repository lives on another box it
// would either fail confusingly or find a same-named checkout here and act on
// it — so the command is refused up front, naming the host that made it remote.
func TestWorktreeCommandsRefuseRemotePanes(t *testing.T) {
	o, localPane, remotePane, _, _ := twoHostOrch(t)

	r := &hostGuardResponder{}
	o.StartWorktreeList(r, app.WorktreeListParams{Pane: &remotePane})
	if !r.fail || !strings.Contains(r.errMsg, "local-host only") {
		t.Fatalf("worktree.list on a remote pane: fail=%v err=%q", r.fail, r.errMsg)
	}

	r = &hostGuardResponder{}
	o.StartWorktreeCreate(r, app.WorktreeCreateParams{Pane: &remotePane})
	if !r.fail || !strings.Contains(r.errMsg, "local-host only") {
		t.Fatalf("worktree.create on a remote pane: fail=%v err=%q", r.fail, r.errMsg)
	}

	// A remote workspace's checkout is not this machine's to delete either.
	remoteWS := o.session.PaneWorkspace(layout.PaneID(remotePane))
	r = &hostGuardResponder{}
	o.StartWorktreeRemove(r, app.WorktreeRemoveParams{Workspace: remoteWS.ID})
	if !r.fail || !strings.Contains(r.errMsg, "local-host only") {
		t.Fatalf("worktree.remove on a remote workspace: fail=%v err=%q", r.fail, r.errMsg)
	}

	// The local pane still reaches git — the guard is about the host, not about
	// worktrees being disabled once a remote host exists. (Its repo lookup fails
	// on a temp dir, which is the *next* check, not this one.)
	r = &hostGuardResponder{}
	o.StartWorktreeList(r, app.WorktreeListParams{Pane: &localPane})
	if r.fail && strings.Contains(r.errMsg, "local-host only") {
		t.Fatalf("a local pane must not be refused: %q", r.errMsg)
	}
}

// A remote workspace can hold the very same path string as a local checkout and
// mean a different directory, so it must never be reported as the workspace
// open on that checkout — worktree.open would focus it instead of opening the
// local one.
func TestWorkspaceForPathIgnoresRemoteWorkspaces(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)
	dir := t.TempDir()

	remote, err := o.session.CreateWorkspaceAtOn(dir, testRemoteHost)
	if err != nil {
		t.Fatalf("CreateWorkspaceAtOn: %v", err)
	}
	if got := o.workspaceForPath(dir); got != "" {
		t.Fatalf("workspaceForPath matched the remote workspace %q (want none)", got)
	}
	local, err := o.session.CreateWorkspaceAt(dir)
	if err != nil {
		t.Fatalf("CreateWorkspaceAt: %v", err)
	}
	if got := o.workspaceForPath(dir); got != local {
		t.Fatalf("workspaceForPath = %q, want the local workspace %q (remote is %q)", got, local, remote)
	}
}
