//go:build ghostty

package main

import (
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/gitbranch"
)

// Which branch each pane is sitting on, for its header strip.
//
// The question the row answers is "if I type here, what am I about to change?"
// — which, in a session that runs several agents over several worktrees of the
// same repo, the path alone does not answer: worktree checkouts are named after
// their branch only until someone switches inside one, and two panes deep in the
// same monorepo look identical by path while one is on main.
//
// Resolution is deliberately a file read, not `git rev-parse`: reading
// .git/HEAD is two syscalls and no process, against a fork+exec per pane per
// sweep. Everything git puts in HEAD that matters here is in that one file —
// the symbolic ref while a branch is checked out, a raw sha while detached.
//
// It runs off the loop goroutine (the fs may be a network mount) and posts the
// answer back, the same shape as agentmodel.go's transcript reads.
//
// This is the *fallback* path from protocol v3 on. A v3 cathost resolves the
// branch where the directory actually is and pushes it as a pane_branch event
// (applyPaneBranch below), which is the only arrangement that can work for a
// pane on another machine. What remains here is the case where the daemon
// cannot: a v2 cathost, which by construction is one on this machine.

const (
	// branchRefreshInterval is the minimum gap between HEAD reads for one pane.
	// Short, because unlike a model a branch changes by a keystroke the user
	// just made and expects to see reflected; the read is cheap enough that the
	// floor is only there to keep a burst of cd's from stacking reads.
	branchRefreshInterval = 3 * time.Second
	// branchSweepInterval paces the background refresh, which is what catches a
	// `git checkout` in a pane that never moves — there is no OSC event for a
	// branch change, so nothing else would notice it.
	branchSweepInterval = 10 * time.Second
)

// refreshPaneBranch resolves rt's branch in the background when one is due. A
// pane with no reported cwd yet (no OSC 7 since it spawned) has nothing to
// resolve against, and one that had a branch and then lost its cwd drops it
// rather than keeping a label for a directory nobody can name.
func (o *orch) refreshPaneBranch(rt *paneRuntime) {
	// Two panes are not ours to resolve. A remote pane's cwd is a path on its
	// own machine; reading it here would report a branch from a directory that
	// merely shares a name (see orch.paneIsLocal). And any pane whose host
	// speaks v3 answers for itself — resolving it here as well would race the
	// host's own answer with a staler one from a filesystem that isn't the
	// pane's. Neither case clears an existing branch that a host event set:
	// only a pane with no cwd, or a local pane on a v2 host that has left its
	// repository, ends up dropping the label below.
	if o.hostOf(rt).resolvesBranch() {
		return
	}
	if rt.cwd == "" || !o.paneIsLocal(rt.id) {
		if rt.branch != "" {
			rt.branch = ""
			o.sendVisible(rt.id, browserproto.NewPaneBranch(rt.id, ""))
		}
		return
	}
	if rt.branchBusy || time.Since(rt.branchAt) < branchRefreshInterval {
		return
	}
	rt.branchBusy = true
	pid, cwd := rt.id, rt.cwd
	go func() {
		branch := gitBranch(cwd)
		o.post(func() { o.setPaneBranch(pid, cwd, branch) })
	}()
}

// setPaneBranch records a resolved branch and republishes the pane's chrome when
// it actually changed. Broadcast is gated on visibility like every other chrome
// field; an off-screen pane still keeps rt.branch current so broadcastPaneChrome
// has the right answer the moment the viewport reaches it.
func (o *orch) setPaneBranch(pid uint32, cwd, branch string) {
	rt := o.panes[pid]
	if rt == nil {
		return
	}
	rt.branchBusy = false
	rt.branchAt = time.Now()
	if rt.cwd != cwd {
		// The pane moved while the read was in flight, so this answer describes
		// a directory it has left. Drop it and re-read where the pane actually
		// is — the refresh that the cd itself asked for was swallowed by the
		// branchBusy guard, so without this the stale label would sit there
		// until the next sweep. Clearing branchAt reopens the throttle for that
		// one re-read.
		rt.branchAt = time.Time{}
		o.refreshPaneBranch(rt)
		return
	}
	if branch == rt.branch {
		return
	}
	rt.branch = branch
	o.sendVisible(pid, browserproto.NewPaneBranch(pid, branch))
}

// runPaneBranches is the periodic refresh pacer (own goroutine, started by
// main). It sweeps every pane rather than only the visible ones: an off-screen
// pane's branch costs two syscalls to keep current, and paying them on the timer
// means a viewport switch shows the right branch immediately instead of the one
// the pane was on when it last left the screen. Each pass is throttled per pane
// by refreshPaneBranch, so a pane that just resolved costs nothing.
func (o *orch) runPaneBranches() {
	t := time.NewTicker(branchSweepInterval)
	defer t.Stop()
	for range t.C {
		o.post(func() {
			for _, rt := range o.panes {
				o.refreshPaneBranch(rt)
			}
		})
	}
}

// applyPaneBranch records a branch a cathost resolved for one of its own panes
// and republishes the pane's chrome when it changed. It is deliberately simpler
// than setPaneBranch: there is no in-flight read to race against a cd, because
// the host resolved this against the cwd it holds — the same value it derived
// the pane_cwd event from — so the answer cannot describe a directory the pane
// has already left in a way this side could detect.
func (o *orch) applyPaneBranch(pid uint32, branch string) {
	rt := o.panes[pid]
	if rt == nil || rt.branch == branch {
		return
	}
	rt.branch = branch
	rt.branchAt = time.Now() // keeps the local fallback quiet if the host later drops to v2
	o.sendVisible(pid, browserproto.NewPaneBranch(pid, branch))
}

// --- resolution (no orch state; runs off the loop goroutine) -----------------

// gitBranch resolves dir's branch on this machine. The reading itself lives in
// internal/gitbranch, because cathost needs exactly the same answer for the
// panes it owns and two copies of git's on-disk layout would be one too many.
func gitBranch(dir string) string { return gitbranch.Resolve(dir) }
