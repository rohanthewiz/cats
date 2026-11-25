//go:build ghostty

package main

import (
	"context"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/gitclone"
)

// cloneTimeout bounds a workspace.create clone. Generous, because a large
// repository over a slow link is a legitimate ten minutes — but finite, so a
// remote that accepts the connection and then says nothing cannot leave the
// command (and the toast waiting on it) open forever.
const cloneTimeout = 15 * time.Minute

// StartWorkspaceClone is workspace.create for a repository that is not on disk
// yet (app.Backend seam): clone, then open a workspace on the checkout.
func (o *orch) StartWorkspaceClone(r app.Responder, c app.WorkspaceClone) {
	o.startWorkspaceClone(nil, r, c)
}

// startWorkspaceClone is StartWorkspaceClone with the issuing window carried
// through, so the new workspace opens in the window that asked for it — see
// startWorktreeCreate. Loop-goroutine only.
//
// The clone runs in this process rather than through a cathost (where the
// worktree commands go): the dispatcher only sends a clone for a local
// workspace, and git here is the same git a local cathost would run.
//
// Nothing in the model changes until the clone has landed. A workspace created
// up front would be rooted on a directory that does not exist yet — its first
// pane would fall back somewhere else — and a failed clone would leave it
// behind to be cleaned up by hand.
func (o *orch) startWorkspaceClone(c *client, r app.Responder, req app.WorkspaceClone) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), cloneTimeout)
		defer cancel()
		err := gitclone.Clone(ctx, req.URL, req.Dest)
		o.post(func() {
			if err != nil {
				r.Fail(err.Error())
				return
			}
			id, err := o.session.CreateWorkspaceAtOn(req.Dest, req.Host)
			if err != nil {
				r.Fail("cloned into " + req.Dest + ", but opening a workspace failed: " + err.Error())
				return
			}
			if req.Name != "" {
				_ = o.session.RenameWorkspace(id, req.Name)
			}
			o.setViewWorkspace(c, id)
			o.applyModel()
			r.OK(app.WorkspaceCreateResult{ID: id})
		})
	}()
}
