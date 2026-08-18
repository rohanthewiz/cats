//go:build ghostty

package main

import (
	"path/filepath"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/worktree"
)

// The worktree commands (app.Backend seam, WS8 dialogs).
//
// git is a subprocess acting on a filesystem, so the one question every one of
// these commands has to answer first is *which machine's disk*. The answer is
// always the same: the machine the addressed pane runs on, or — for remove —
// the one the workspace's checkout belongs to. That is why they are no longer
// refused for a pane on another host: the refusal was never about worktrees, it
// was about this process being the only thing that could run git.
//
// So every operation goes to a cathost, including the local one. There is one
// implementation of the sequence (worktree.Do, shared by both processes) and
// one code path here to reach it, which is what keeps a remote worktree from
// slowly diverging from a local one. The exception is a local daemon that
// cannot answer — an older cathost, or a test orch with no connection at all —
// where catway runs the same function in-process; on this machine that is a
// legitimate fallback, and on another machine there is nothing to fall back to.
//
// Each command therefore has the same shape: resolve the host and the request
// on the loop goroutine, hand it to runWorktreeOp, and apply the model effects
// in the callback, which is back on the loop. The git work never touches the
// orchestrator's goroutine at either end.

// StartWorktreeList lists the repo's checkouts, anchored on the addressed (or
// focused) pane's cwd. The workspace-membership match runs back on the loop so
// it reflects the model at reply time, not request time.
func (o *orch) StartWorktreeList(r app.Responder, p app.WorktreeListParams) {
	host := o.paneHostID(o.anchorPane(p.Pane))
	o.runWorktreeOp(r, host, worktree.OpRequest{
		Op:   worktree.OpList,
		Cwd:  o.anchorPaneCwd(p.Pane),
		Root: o.worktreeDir,
	}, func(res worktree.OpResult) {
		if res.Error != "" {
			r.Fail(res.Error)
			return
		}
		r.OK(o.worktreeListResult(host, res))
	})
}

// StartWorktreeCreate creates a new branch + checkout (`git worktree add -b`)
// and opens a new workspace on it, focused and named after the branch.
//
// The branch name is generated here rather than by whoever runs git, because it
// is the name the workspace takes and the value the command reports back: an
// answer that depended on which machine ran the operation would be a different
// answer for the same request.
func (o *orch) StartWorktreeCreate(r app.Responder, p app.WorktreeCreateParams) {
	host := o.paneHostID(o.anchorPane(p.Pane))
	branch := p.Branch
	if branch == "" {
		branch = worktree.GeneratedBranchSlug(time.Now().UnixMicro())
	}
	o.runWorktreeOp(r, host, worktree.OpRequest{
		Op:     worktree.OpCreate,
		Cwd:    o.anchorPaneCwd(p.Pane),
		Path:   p.Path,
		Branch: branch,
		Root:   o.worktreeDir,
	}, func(res worktree.OpResult) {
		if res.Error != "" {
			r.Fail(res.Error)
			return
		}
		// Pinned to the host that just created it, for the same reason a
		// checkout is pinned in open: the directory exists on that machine's
		// disk, so the workspace rooted on it has to spawn its panes there even
		// in a session whose default host is a different one.
		id, err := o.session.CreateWorkspaceAtOn(res.Path, host)
		if err != nil {
			r.Fail("checkout created, but opening a workspace failed: " + err.Error())
			return
		}
		_ = o.session.RenameWorkspace(id, branch)
		o.applyModel()
		r.OK(app.WorktreeCreateResult{Workspace: id, Branch: branch, Path: res.Path})
	})
}

// StartWorktreeOpen focuses the workspace already open on the checkout, or
// creates a new one rooted there.
//
// The already-open check runs twice: once here on the path as it was given —
// the browser passes an absolute path straight out of worktree.list, which is
// the common case and needs no round trip — and once on the path the answering
// machine resolved, since a "~/…" checkout typed by hand is only a real path
// after that machine expanded it.
func (o *orch) StartWorktreeOpen(r app.Responder, p app.WorktreeOpenParams) {
	host := o.paneHostID(o.anchorPane(p.Pane))
	if o.focusWorkspaceOnPath(r, p.Path, host) {
		return
	}
	o.runWorktreeOp(r, host, worktree.OpRequest{Op: worktree.OpStat, Path: p.Path}, func(res worktree.OpResult) {
		if res.Error != "" {
			r.Fail(res.Error)
			return
		}
		if o.focusWorkspaceOnPath(r, res.Path, host) {
			return
		}
		id, err := o.session.CreateWorkspaceAtOn(res.Path, host)
		if err != nil {
			r.Fail(err.Error())
			return
		}
		o.applyModel()
		r.OK(app.WorktreeOpenResult{Workspace: id})
	})
}

// focusWorkspaceOnPath focuses the workspace already open on a checkout and
// reports whether it resolved r — so a caller can treat "already open" as the
// whole answer. A focus that fails is also an answer: the workspace exists, so
// creating a second one on the same checkout is not the recovery.
func (o *orch) focusWorkspaceOnPath(r app.Responder, path, hostID string) bool {
	id := o.workspaceForPathOn(path, hostID)
	if id == "" {
		return false
	}
	if err := o.session.FocusWorkspace(id); err != nil {
		r.Fail(err.Error())
		return true
	}
	o.applyModel()
	r.OK(app.WorktreeOpenResult{Workspace: id, AlreadyOpen: true})
	return true
}

// StartWorktreeRemove deletes a workspace's checkout folder (`git worktree
// remove`, never the branch) and closes the workspace on success. A dirty
// checkout without force fails with the "dirty_worktree_requires_force:" prefix
// the front-end escalates on.
func (o *orch) StartWorktreeRemove(r app.Responder, p app.WorktreeRemoveParams) {
	var path, host string
	for _, ws := range o.session.Workspaces() {
		if ws.ID != p.Workspace {
			continue
		}
		// A workspace pinned to a host that has left the roster is the one case
		// where the checkout's machine cannot be named: workspaceHostID would
		// resolve it to the default host, and `git worktree remove` there would
		// either miss or, on a coincidental path match, delete the wrong
		// checkout. That is exactly the state a forced detach leaves behind.
		if ws.HostID != "" && o.hosts[ws.HostID] == nil {
			r.Fail("workspace " + p.Workspace + " belongs to host " + ws.HostID + ", which is not attached")
			return
		}
		path, host = ws.IdentityCwd, o.workspaceHostID(ws)
		break
	}
	if path == "" {
		r.Fail("unknown workspace " + p.Workspace)
		return
	}
	wsID := p.Workspace
	o.runWorktreeOp(r, host, worktree.OpRequest{
		Op:    worktree.OpRemove,
		Path:  path,
		Force: p.Force,
	}, func(res worktree.OpResult) {
		if res.Error != "" {
			if !p.Force && res.Dirty {
				r.Fail("dirty_worktree_requires_force: " + res.Error)
				return
			}
			r.Fail(res.Error)
			return
		}
		id := wsID
		if err := o.session.CloseWorkspace(&id); err != nil {
			r.Fail("checkout removed, but closing the workspace failed: " + err.Error())
			return
		}
		o.applyModel()
		r.OK(nil)
	})
}

// runWorktreeOp runs one operation on the machine that owns the checkout and
// calls done with its result, back on the loop goroutine. Loop-goroutine only.
//
// The daemon is preferred for every host, including the local one, so that a
// worktree command is the same command everywhere — the Phase 4 argument for
// host-side branch resolution, applied to the other thing that reads a
// repository. The in-process fallback covers a local cathost that cannot answer
// (an older build, or no connection at all); for any other host there is
// nothing here that could stand in for it, so it is refused by name.
func (o *orch) runWorktreeOp(r app.Responder, hostID string, req worktree.OpRequest, done func(worktree.OpResult)) {
	d := o.hostByID(hostID)
	if d.supports(orchestration.FeatureWorktree) {
		o.nextWorktreeReq++
		id := o.nextWorktreeReq
		// Registered before the send, and resolved by the same machinery as
		// read and capture: a host that drops mid-operation fails the command
		// through flushPendingFor rather than leaving the dialog waiting for a
		// reply that cannot come.
		o.registerPending(worktreeResponder{r: r, done: done}, hostKey(hostID, id))
		d.send(orchestration.NewRequestWorktree(id, req))
		return
	}
	if hostID != localHostID {
		r.Fail(o.hostCapabilityErr(hostID, "run git-worktree commands", orchestration.FeatureWorktree))
		return
	}
	go func() {
		res := worktree.Do(req)
		o.post(func() { done(res) })
	}()
}

// worktreeResponder turns the daemon's OpResult into the command's answer on the
// way back. The pending queue carries one Responder and knows nothing about the
// shape of what it is resolving, while what a worktree result *means* — a new
// workspace, a closed one, a dirty-checkout escalation — is entirely catway's.
type worktreeResponder struct {
	r    app.Responder
	done func(worktree.OpResult)
}

func (w worktreeResponder) WantsReply() bool { return w.r.WantsReply() }

func (w worktreeResponder) OK(data any) {
	res, ok := data.(worktree.OpResult)
	if !ok {
		w.r.Fail("bad worktree reply")
		return
	}
	w.done(res)
}

// Fail is a transport failure — a dropped host or a timeout — and is passed
// straight through. A git failure never arrives this way: it comes back inside
// the result, because it is an answer rather than a lost question.
func (w worktreeResponder) Fail(msg string) { w.r.Fail(msg) }

// hostCapabilityErr explains a host that cannot answer a request. The reason
// matters and is the one sentence the user sees: a cathost that has not been
// upgraded is a different fix from one that is unreachable.
func (o *orch) hostCapabilityErr(hostID, what, feature string) string {
	d := o.hostByID(hostID)
	label := d.label
	if o.hosts[hostID] == nil && hostID != "" {
		label = hostID // an id nobody is attached to: name what was asked for
	}
	if !d.connected() {
		return "host " + label + " is not connected"
	}
	return "host " + label + " cannot " + what + " (its cathost predates the " + feature + " capability)"
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
// names, else the focused one. Split out of anchorPaneCwd so the host lookup
// asks about the same pane the cwd will come from — resolving the anchor twice
// by two rules is how a command ends up running on a different machine than the
// one whose directory it was given.
func (o *orch) anchorPane(pane *uint32) uint32 {
	if pane != nil {
		return *pane
	}
	if id, ok := o.session.FocusedPane(); ok {
		return uint32(id)
	}
	return 0
}

// worktreeListResult assembles the worktree.list reply: entry flags plus which
// checkout the anchoring pane is in (current) and which workspaces are already
// open on each checkout. Bare entries have no working tree to open and are
// dropped. Loop-goroutine only.
//
// The worktree root comes back from the answering machine rather than from this
// process: the configured directory is "~/.cats/worktrees" by default, and the
// home it names is the home of the account the checkout will belong to.
func (o *orch) worktreeListResult(hostID string, res worktree.OpResult) app.WorktreeListResult {
	repoRoot := worktree.MainPath(res.Entries, res.Checkout)
	out := app.WorktreeListResult{
		RepoRoot:     repoRoot,
		RepoName:     filepath.Base(repoRoot),
		WorktreeRoot: res.Root,
		Host:         hostID,
	}
	cur := o.canonPathOn(res.Checkout, hostID)
	for _, e := range res.Entries {
		if e.IsBare {
			continue
		}
		out.Worktrees = append(out.Worktrees, app.WorktreeInfo{
			Path:          e.Path,
			Branch:        e.Branch,
			Detached:      e.IsDetached,
			Prunable:      e.IsPrunable,
			Current:       o.canonPathOn(e.Path, hostID) == cur,
			OpenWorkspace: o.workspaceForPathOn(e.Path, hostID),
		})
	}
	return out
}

// workspaceForPathOn finds the workspace open on a checkout path *on one host*:
// one whose identity cwd or any live pane cwd is the path. "" when none.
// Loop-goroutine only.
//
// The host is half the identity of a path. Two machines can hold the very same
// path string and mean two different directories, so a workspace on another
// host is never the one open on this checkout — worktree.open would otherwise
// focus it instead of opening the checkout the user pointed at.
func (o *orch) workspaceForPathOn(path, hostID string) string {
	cp := o.canonPathOn(path, hostID)
	for _, ws := range o.session.Workspaces() {
		if o.workspaceHostOwns(ws, hostID) && ws.IdentityCwd != "" &&
			o.canonPathOn(ws.IdentityCwd, hostID) == cp {
			return ws.ID
		}
		for _, tab := range ws.Tabs {
			for _, id := range tab.Layout.PaneIDs() {
				pid := uint32(id)
				if o.paneHostID(pid) != hostID {
					continue
				}
				if rt := o.panes[pid]; rt != nil && rt.cwd != "" && o.canonPathOn(rt.cwd, hostID) == cp {
					return ws.ID
				}
			}
		}
	}
	return ""
}

// canonPathOn canonicalizes a path for comparison, but only when the path is on
// this machine. EvalSymlinks resolves against the local filesystem, which says
// nothing about another host's disk — and worse, a remote path that happens to
// exist here too would be resolved into a different local path. A remote path
// compares as a cleaned string, which is all this side can honestly do.
func (o *orch) canonPathOn(path, hostID string) string {
	if hostID != localHostID {
		return filepath.Clean(path)
	}
	return canonPath(path)
}

// canonPath resolves symlinks for path comparison, falling back to a cleaned
// path when the target does not resolve (e.g. it was just removed).
func canonPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
