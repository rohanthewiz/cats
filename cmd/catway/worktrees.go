//go:build ghostty

package main

import (
	"os"
	"path/filepath"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/worktree"
)

// The worktree commands (app.Backend seam, WS8 dialogs). Each Start* runs on
// the loop goroutine, resolves what it needs from loop-owned state (pane cwds,
// the worktree root), then runs the git subprocess work on its own goroutine and
// posts the model mutation + Responder resolution back onto the loop — the
// StartRead/StartCapture shape, so git can never stall the orchestrator.
// worktree.open needs no git and resolves synchronously.

// StartWorktreeList lists the repo's checkouts, anchored on the addressed (or
// focused) pane's cwd. The workspace-membership match runs back on the loop so
// it reflects the model at reply time, not request time.
func (o *orch) StartWorktreeList(r app.Responder, p app.WorktreeListParams) {
	if err := o.requireLocalAnchor(p.Pane); err != "" {
		r.Fail(err)
		return
	}
	cwd := o.anchorPaneCwd(p.Pane)
	go func() {
		checkout, err := worktree.RepoRoot(cwd)
		if err != nil {
			o.post(func() { r.Fail("not a git worktree: " + err.Error()) })
			return
		}
		entries, err := worktree.List(checkout)
		if err != nil {
			o.post(func() { r.Fail(err.Error()) })
			return
		}
		o.post(func() { r.OK(o.worktreeListResult(checkout, entries)) })
	}()
}

// StartWorktreeCreate creates a new branch + checkout (`git worktree add -b`)
// and opens a new workspace on it, focused and named after the branch.
func (o *orch) StartWorktreeCreate(r app.Responder, p app.WorktreeCreateParams) {
	if err := o.requireLocalAnchor(p.Pane); err != "" {
		r.Fail(err)
		return
	}
	cwd := o.anchorPaneCwd(p.Pane)
	branch := p.Branch
	if branch == "" {
		branch = worktree.GeneratedBranchSlug(time.Now().UnixMicro())
	}
	root := o.worktreeDir
	go func() {
		src, err := worktree.RepoRoot(cwd)
		if err != nil {
			o.post(func() { r.Fail("not a git worktree: " + err.Error()) })
			return
		}
		path := p.Path
		if path == "" {
			// The default checkout path keys on the *main* repo's name, which
			// the porcelain list resolves even from a linked worktree.
			entries, err := worktree.List(src)
			if err != nil {
				o.post(func() { r.Fail(err.Error()) })
				return
			}
			path = worktree.DefaultCheckoutPath(root, filepath.Base(mainWorktreePath(entries, src)), branch)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			o.post(func() { r.Fail(err.Error()) })
			return
		}
		if err := worktree.Run(worktree.AddCommand(src, path, branch, "HEAD")); err != nil {
			o.post(func() { r.Fail(err.Error()) })
			return
		}
		o.post(func() {
			id, err := o.session.CreateWorkspaceAt(path)
			if err != nil {
				r.Fail("checkout created, but opening a workspace failed: " + err.Error())
				return
			}
			_ = o.session.RenameWorkspace(id, branch)
			o.applyModel()
			r.OK(app.WorktreeCreateResult{Workspace: id, Branch: branch, Path: path})
		})
	}()
}

// StartWorktreeOpen focuses the workspace already open on the checkout, or
// creates a new one rooted there. No git involved — synchronous on the loop.
func (o *orch) StartWorktreeOpen(r app.Responder, p app.WorktreeOpenParams) {
	if id := o.workspaceForPath(p.Path); id != "" {
		if err := o.session.FocusWorkspace(id); err != nil {
			r.Fail(err.Error())
			return
		}
		o.applyModel()
		r.OK(app.WorktreeOpenResult{Workspace: id, AlreadyOpen: true})
		return
	}
	if st, err := os.Stat(p.Path); err != nil || !st.IsDir() {
		r.Fail("worktree path is not a directory: " + p.Path)
		return
	}
	// Pinned to the local host, not left to the default: the checkout was just
	// stat'ed on this machine's disk, so the workspace rooted on it has to spawn
	// its panes here even in a session whose default host is a remote one.
	id, err := o.session.CreateWorkspaceAtOn(p.Path, localHostID)
	if err != nil {
		r.Fail(err.Error())
		return
	}
	o.applyModel()
	r.OK(app.WorktreeOpenResult{Workspace: id})
}

// StartWorktreeRemove deletes a workspace's checkout folder (`git worktree
// remove`, never the branch) and closes the workspace on success. A dirty
// checkout without force fails with the "dirty_worktree_requires_force:" prefix
// the front-end escalates on.
func (o *orch) StartWorktreeRemove(r app.Responder, p app.WorktreeRemoveParams) {
	var path string
	for _, ws := range o.session.Workspaces() {
		if ws.ID == p.Workspace {
			// Its checkout is on the machine its panes run on; `git worktree
			// remove` here would either miss or, on a coincidental path match,
			// delete the wrong checkout.
			if id := o.workspaceHostID(ws); id != localHostID {
				r.Fail(worktreeRemoteErr(id))
				return
			}
			path = ws.IdentityCwd
			break
		}
	}
	if path == "" {
		r.Fail("unknown workspace " + p.Workspace)
		return
	}
	force := p.Force
	wsID := p.Workspace
	go func() {
		// Remove must run from the main worktree — git refuses to remove the
		// checkout it is running inside of.
		entries, err := worktree.List(path)
		if err != nil {
			o.post(func() { r.Fail(err.Error()) })
			return
		}
		main := mainWorktreePath(entries, path)
		if err := worktree.Run(worktree.RemoveCommand(main, path, force)); err != nil {
			msg := err.Error()
			o.post(func() {
				if !force && worktree.IsDirtyRemoveError(msg) {
					r.Fail("dirty_worktree_requires_force: " + msg)
					return
				}
				r.Fail(msg)
			})
			return
		}
		o.post(func() {
			id := wsID
			if err := o.session.CloseWorkspace(&id); err != nil {
				r.Fail("checkout removed, but closing the workspace failed: " + err.Error())
				return
			}
			o.applyModel()
			r.OK(nil)
		})
	}()
}

// anchorPaneCwd resolves the directory a pane-addressed command anchors on: the
// addressed (or focused) pane's live cwd, falling back to that pane's workspace
// identity and then the process cwd. The worktree commands anchor their repo on
// it and path.list resolves relative paths against it. Loop-goroutine only.
func (o *orch) anchorPaneCwd(pane *uint32) string {
	pid := o.anchorPane(pane)
	if rt := o.panes[pid]; rt != nil && rt.cwd != "" {
		return rt.cwd
	}
	return o.paneCwd(pid)
}

// anchorPane is which pane a pane-addressed command anchors on: the one it
// names, else the focused one. Split out of anchorPaneCwd so the host guard
// below asks about the same pane the cwd will come from — resolving the anchor
// twice by two rules is how a guard ends up protecting a different pane than
// the one that gets used.
func (o *orch) anchorPane(pane *uint32) uint32 {
	if pane != nil {
		return *pane
	}
	if id, ok := o.session.FocusedPane(); ok {
		return uint32(id)
	}
	return 0
}

// requireLocalAnchor refuses a filesystem command anchored on a remote pane,
// returning the failure text ("" = allowed). git runs as a subprocess of *this*
// process, so every worktree verb operates on this machine's disk; anchoring
// one on a pane whose repository lives on another box would at best fail with a
// confusing "not a git worktree" and at worst find a same-named checkout here
// and act on it. Remote worktrees need cathost-side git, which is not this
// slice's work.
func (o *orch) requireLocalAnchor(pane *uint32) string {
	if id := o.paneHostID(o.anchorPane(pane)); id != localHostID {
		return worktreeRemoteErr(id)
	}
	return ""
}

// worktreeRemoteErr names the host that made the command local-only, so the
// dialog (and `catctl`) can say which machine it was pointed at rather than
// just refusing.
func worktreeRemoteErr(hostID string) string {
	return "worktrees are local-host only (this one is on host " + hostID + ")"
}

// worktreeListResult assembles the worktree.list reply: entry flags plus which
// checkout the anchoring pane is in (current) and which workspaces are already
// open on each checkout. Bare entries have no working tree to open and are
// dropped. Loop-goroutine only.
func (o *orch) worktreeListResult(checkout string, entries []worktree.Entry) app.WorktreeListResult {
	repoRoot := mainWorktreePath(entries, checkout)
	res := app.WorktreeListResult{
		RepoRoot:     repoRoot,
		RepoName:     filepath.Base(repoRoot),
		WorktreeRoot: o.worktreeDir,
	}
	cur := canonPath(checkout)
	for _, e := range entries {
		if e.IsBare {
			continue
		}
		res.Worktrees = append(res.Worktrees, app.WorktreeInfo{
			Path:          e.Path,
			Branch:        e.Branch,
			Detached:      e.IsDetached,
			Prunable:      e.IsPrunable,
			Current:       canonPath(e.Path) == cur,
			OpenWorkspace: o.workspaceForPath(e.Path),
		})
	}
	return res
}

// workspaceForPath finds the workspace open on a checkout path: one whose
// identity cwd or any live pane cwd is the path (canonicalized, so a symlinked
// checkout still matches). "" when none. Loop-goroutine only.
func (o *orch) workspaceForPath(path string) string {
	cp := canonPath(path)
	for _, ws := range o.session.Workspaces() {
		// Paths only compare within one filesystem: a workspace on another host
		// can hold the very same path string and mean a different directory, so
		// a remote workspace is never the one open on a local checkout.
		if o.workspaceHostID(ws) != localHostID {
			continue
		}
		if ws.IdentityCwd != "" && canonPath(ws.IdentityCwd) == cp {
			return ws.ID
		}
		for _, tab := range ws.Tabs {
			for _, id := range tab.Layout.PaneIDs() {
				if rt := o.panes[uint32(id)]; rt != nil && rt.cwd != "" && canonPath(rt.cwd) == cp {
					return ws.ID
				}
			}
		}
	}
	return ""
}

// mainWorktreePath is the main checkout from a porcelain list (git lists it
// first); fallback covers a defensive empty list.
func mainWorktreePath(entries []worktree.Entry, fallback string) string {
	if len(entries) > 0 {
		return entries[0].Path
	}
	return fallback
}

// canonPath resolves symlinks for path comparison, falling back to a cleaned
// path when the target does not resolve (e.g. it was just removed).
func canonPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
