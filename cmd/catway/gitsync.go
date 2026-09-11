//go:build ghostty

package main

import (
	"context"
	"sync"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/gitsync"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// Whether each workspace's repository is level with its remote, for the dot on
// its sidebar row.
//
// The question the dot answers is "is what I am about to build on top of still
// the current state of this project?" — the one thing about a checkout that
// cannot be learned from inside it, because the answer lives on another
// machine. A session that keeps five workspaces open all day is a session where
// four of them are quietly going stale, and nothing on screen said so until the
// next pull produced a merge nobody was expecting.
//
// Shape of the thing, and why:
//
//	runWorkspaceGit   own goroutine, 2-minute ticker
//	  -> o.post       hop onto the loop to read the session (sole state owner)
//	     gather       (workspace id, directory) for the LOCAL workspaces
//	  -> go           hop back off: each resolve forks git and may hit the net
//	     gitsync.Resolve
//	  -> o.post       hop back on to store the results and broadcast a change
//
// Three hops rather than one because the two ends have opposite requirements:
// the session may only be read on the loop goroutine, and the resolution may
// only happen off it. Everything else here — the busy flag, the change check —
// exists to keep that round trip from costing anything when nothing moved.

const (
	// gitSyncInterval paces the poll. Two minutes is the user-facing number:
	// long enough that a session holding a dozen workspaces is not opening a
	// dozen network connections a minute, short enough that "someone pushed"
	// surfaces while it is still the thing you were about to build on.
	gitSyncInterval = 2 * time.Minute
	// gitSyncStartDelay holds the first sweep back until the session has
	// settled. At startup the workspaces are still being restored and their
	// panes spawned, and a sweep that ran into the middle of that would resolve
	// against half a session and then be redone two minutes later anyway.
	gitSyncStartDelay = 10 * time.Second
	// gitSyncParallel bounds how many repositories are resolved at once. More
	// than one because an unreachable remote costs gitsync's full network
	// timeout and would otherwise hold up every workspace behind it; a small
	// number because each one forks several git processes, and the point of the
	// bound is that a session with twenty workspaces does not briefly become
	// sixty processes.
	gitSyncParallel = 4
)

// gitSyncTarget is one workspace to resolve: its public id and the directory
// that IS the workspace, as far as the sidebar is concerned.
type gitSyncTarget struct {
	ws  string
	dir string
}

// runWorkspaceGit is the poll's pacer (own goroutine, started by main). It owns
// nothing: every access to session state happens inside the closures it posts.
func (o *orch) runWorkspaceGit() {
	time.Sleep(gitSyncStartDelay)
	t := time.NewTicker(gitSyncInterval)
	defer t.Stop()
	for {
		o.post(o.startWorkspaceGitSweep)
		<-t.C
	}
}

// startWorkspaceGitSweep (loop goroutine) collects what to resolve and hands it
// to a worker goroutine. It also prunes the cache here rather than after the
// resolve: a workspace that has been CLOSED must stop being reported
// immediately, not two minutes later when its absence finally comes back.
func (o *orch) startWorkspaceGitSweep() {
	targets := o.gitSyncTargets()
	// Anything the session no longer holds — closed, or moved to another host —
	// leaves the cache now. Its row is already gone or already drawing the plain
	// dot, so this is only about not re-broadcasting a stale row later.
	live := make(map[string]bool, len(targets))
	for _, t := range targets {
		live[t.ws] = true
	}
	dropped := false
	for id := range o.wsGit {
		if !live[id] {
			delete(o.wsGit, id)
			dropped = true
		}
	}
	if dropped {
		o.broadcastWorkspaceGit()
	}
	if o.wsGitBusy || len(targets) == 0 {
		return
	}
	o.wsGitBusy = true
	go o.resolveWorkspaceGit(targets)
}

// gitSyncTargets (loop goroutine) is the workspaces worth asking about: the ones
// whose start directory is a path on THIS machine.
//
// The host check is the same one paneCwd makes before handing a directory to a
// cathost, and it matters for the same reason: a workspace pinned to another
// box names a path in that box's filesystem, and resolving it here would report
// on whatever local directory happens to share the name — which, for a
// monorepo checked out in the same place on both machines, is a plausible and
// completely wrong answer. A remote workspace is left out entirely rather than
// guessed at. (Resolving it properly means asking its cathost, the way
// pane_branch does; that is a protocol addition, not something this can fake.)
//
// Sleeping and locked workspaces are included. Neither says anything about the
// repository — a workspace put to bed is exactly the one most likely to have
// gone stale while you were not looking, which makes it the row that most needs
// the dot.
func (o *orch) gitSyncTargets() []gitSyncTarget {
	var out []gitSyncTarget
	for _, ws := range o.session.Workspaces() {
		dir := o.workspaceDir(ws)
		if dir == "" || !o.workspaceHostOwns(ws, localHostID) {
			continue
		}
		out = append(out, gitSyncTarget{ws: ws.ID, dir: dir})
	}
	return out
}

// workspaceDir is the directory a workspace's row stands for. It is
// IdentityCwd — the same field the row's NAME is derived from — so the dot and
// the label are always talking about the same checkout. A workspace with no
// identity cwd (older snapshots, and tests) has no directory to report on;
// falling back to the daemon's own cwd would paint every such row with the
// state of whatever directory catway happened to be started in.
func (o *orch) workspaceDir(ws *workspace.Workspace) string {
	if ws == nil {
		return ""
	}
	return ws.IdentityCwd
}

// gitSyncResult pairs a workspace with what the sweep found for it. Named
// rather than anonymous because it crosses a goroutine boundary: the worker
// builds it, the loop consumes it, and an anonymous struct shared between two
// function signatures is a change waiting to go wrong in one of them.
type gitSyncResult struct {
	ws string
	st gitsync.Status
}

// resolveWorkspaceGit (worker goroutine) resolves every target and posts the
// answers back in one batch. One batch rather than one post per workspace
// because the rollup message is sent whole anyway: per-workspace posts would
// broadcast the same list up to len(targets) times as the sweep filled in.
//
// The fan-out is a buffered channel as a semaphore plus a WaitGroup, and each
// worker writes only its own slot of the results slice — so there is no lock,
// and the results come back in target order rather than completion order.
func (o *orch) resolveWorkspaceGit(targets []gitSyncTarget) {
	results := make([]gitSyncResult, len(targets))
	sem := make(chan struct{}, gitSyncParallel)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t gitSyncTarget) {
			defer func() { <-sem; wg.Done() }()
			results[i] = gitSyncResult{ws: t.ws, st: gitsync.Resolve(context.Background(), t.dir)}
		}(i, t)
	}
	wg.Wait()
	o.post(func() { o.applyWorkspaceGit(results) })
}

// applyWorkspaceGit (loop goroutine) stores a sweep's answers and broadcasts the
// rollup if anything actually changed.
//
// The change check is what makes the two-minute poll free in the steady state:
// a session where nobody has pushed resolves to the same set of states forever,
// and re-sending an identical list to every window every two minutes would
// re-render the whole workspace list for nothing.
func (o *orch) applyWorkspaceGit(results []gitSyncResult) {
	o.wsGitBusy = false
	changed := false
	for _, r := range results {
		prev, had := o.wsGit[r.ws]
		if r.st.State == gitsync.Unknown {
			// A workspace that has STOPPED having an answer (its remote went
			// away, the network is down) drops back to the plain dot rather
			// than keeping the last colour: a stale "in sync" is the one
			// reading that would actively mislead.
			if had {
				delete(o.wsGit, r.ws)
				changed = true
			}
			continue
		}
		next := browserproto.WorkspaceGitInfo{
			Workspace: r.ws,
			Sync:      string(r.st.State),
			Branch:    r.st.Branch,
			Remote:    r.st.Remote,
			Ahead:     r.st.Ahead,
		}
		if !had || prev != next {
			o.wsGit[r.ws] = next
			changed = true
		}
	}
	if changed {
		o.broadcastWorkspaceGit()
	}
}

// workspaceGitMsg is the rollup as the browser sees it, in session order so the
// list matches the sidebar it annotates (map iteration cannot supply that, and
// an order that reshuffled between sweeps would defeat any future diffing on
// the client).
func (o *orch) workspaceGitMsg() browserproto.WorkspaceGit {
	items := make([]browserproto.WorkspaceGitInfo, 0, len(o.wsGit))
	for _, ws := range o.session.Workspaces() {
		if it, ok := o.wsGit[ws.ID]; ok {
			items = append(items, it)
		}
	}
	// An entry the session no longer lists (a workspace closed between the
	// sweep landing and this send) is dropped by the walk itself.
	return browserproto.NewWorkspaceGit(items)
}

func (o *orch) broadcastWorkspaceGit() { o.broadcast(o.workspaceGitMsg()) }
