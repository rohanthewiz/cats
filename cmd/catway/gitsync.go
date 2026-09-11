//go:build ghostty

package main

import (
	"context"
	"sync"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/gitsync"
	"github.com/rohanthewiz/cats/internal/orchestration"
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
// # Where the question is asked
//
// A directory is only meaningful on the machine that holds it, so the question
// goes to that machine — the same argument that moved branch resolution onto
// the daemons (see gitbranch.go and the protocol's v3 note). A workspace pinned
// to devbox names a path in devbox's filesystem, and answering it here would
// report on whatever local directory happens to share the name, which for a
// monorepo checked out at the same place on both boxes is a plausible and
// completely wrong answer.
//
// So a cathost advertising FeatureGitSync is asked, for EVERY host including
// the local one — one code path, one answer, one environment doing the
// resolving. The in-process resolve survives only as the fallback for the local
// machine when its cathost cannot answer (an older build, or no connection yet);
// for any other host there is nothing here that could stand in, so the workspace
// is left out rather than guessed at.
//
// # Shape of one sweep
//
//	runWorkspaceGit   own goroutine, 2-minute ticker
//	  -> o.post       hop onto the loop: the session is read here and nowhere else
//	     gitSyncPlan  split the workspaces into "ask the host" and "do it here"
//	  ├─> d.send      one request_git_sync per remote target, id-correlated
//	  │     ...          each answers on its own schedule through pendingReqs
//	  └─> go          the local fallback batch, bounded fan-out, off the loop
//	  -> o.post       every answer lands back on the loop as a collect()
//	     apply        when the last one is in: store, and broadcast if it moved
//
// The hops are forced: the session may only be read on the loop goroutine, and
// neither git nor a daemon round trip may happen on it. Everything else here —
// the generation counter, the outstanding count, the change check — exists so
// that a sweep where nothing moved costs one message to nobody.

const (
	// gitSyncInterval paces the poll. Two minutes is the user-facing number:
	// long enough that a session holding a dozen workspaces is not opening a
	// dozen network connections a minute, short enough that "someone pushed"
	// surfaces while it is still the thing you were about to build on.
	gitSyncInterval = 2 * time.Minute
	// gitSyncStartDelay holds the first sweep back until the session has
	// settled. At startup the workspaces are still being restored, their panes
	// spawned and the cathosts still being dialled — and a sweep that ran into
	// the middle of that would resolve against half a session, with every remote
	// workspace skipped for a host that was about to connect.
	gitSyncStartDelay = 10 * time.Second
	// gitSyncParallel bounds how many repositories the LOCAL fallback resolves
	// at once. More than one because an unreachable remote costs gitsync's full
	// network timeout and would otherwise hold up every workspace behind it; a
	// small number because each one forks several git processes, and the point
	// of the bound is that a session with twenty workspaces does not briefly
	// become sixty processes.
	//
	// The host-asked targets need no such bound here: each is one small message,
	// and the work happens on the machine that owns the directory — which is
	// also the machine whose resources it should be costing.
	gitSyncParallel = 4
)

// gitSyncTarget is one workspace to resolve: its public id, the directory that
// IS the workspace as far as the sidebar is concerned, and the host that owns
// that directory.
type gitSyncTarget struct {
	ws   string
	dir  string
	host string
}

// gitSyncPlan is one sweep's division of labour. Split at planning time, on the
// loop, because that is the only place the host roster may be read — and
// because "can this host answer?" must be decided once, up front, rather than
// re-asked per target as hosts connect and drop underneath the sweep.
type gitSyncPlan struct {
	// viaHost are answered by a cathost that advertises FeatureGitSync.
	viaHost []gitSyncTarget
	// local are answered in this process: the fallback for the local machine
	// when its own cathost cannot.
	local []gitSyncTarget
}

// targets is every workspace the sweep will produce an answer for, in either
// direction. What is NOT in it is what the cache prune takes out.
func (p gitSyncPlan) targets() []gitSyncTarget {
	return append(append([]gitSyncTarget(nil), p.viaHost...), p.local...)
}

func (p gitSyncPlan) empty() bool { return len(p.viaHost)+len(p.local) == 0 }

// gitSweep is one pass's bookkeeping. It exists because a sweep's answers now
// arrive from two places on two schedules — a batch from the local worker, one
// message per remote workspace — and the rollup is only worth building once they
// are all in.
type gitSweep struct {
	// gen identifies this sweep. An answer carrying a stale generation is
	// dropped rather than folded into the sweep that replaced it: the two
	// describe different sets of workspaces, and a late reply from an abandoned
	// pass would otherwise decrement the wrong counter.
	gen uint64
	// outstanding is how many answers are still to come. It is decided up front
	// and only ever decremented, so the sweep cannot end early.
	outstanding int
	// results accumulates in arrival order. Order does not matter: the rollup is
	// rebuilt in session order at send time.
	results []gitSyncResult
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

// startWorkspaceGitSweep (loop goroutine) plans one pass and sets it going.
//
// The cache prune happens here rather than after the answers land, because a
// workspace that has been CLOSED — or whose host has dropped, taking the only
// thing that could answer for it — must stop being reported immediately, not
// two minutes later when its absence finally comes back.
func (o *orch) startWorkspaceGitSweep() {
	plan := o.gitSyncPlan()
	o.pruneWorkspaceGit(plan.targets())
	if o.wsGitSweep != nil || plan.empty() {
		// A sweep still in flight is left to finish. With an unreachable remote
		// one pass can outlast the interval, and starting a second would double
		// the load on exactly the host that is already not answering.
		return
	}
	o.wsGitGen++
	sw := &gitSweep{gen: o.wsGitGen, outstanding: len(plan.viaHost)}
	if len(plan.local) > 0 {
		sw.outstanding++ // the local batch answers once, for all of its targets
	}
	o.wsGitSweep = sw
	for _, t := range plan.viaHost {
		o.requestWorkspaceGit(sw.gen, t)
	}
	if len(plan.local) > 0 {
		go o.resolveWorkspaceGit(sw.gen, plan.local)
	}
}

// pruneWorkspaceGit drops cached states for workspaces this sweep will not
// answer for, and republishes if that changed anything.
func (o *orch) pruneWorkspaceGit(targets []gitSyncTarget) {
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
}

// gitSyncPlan (loop goroutine) decides, per workspace, who answers for it.
//
// Sleeping and locked workspaces are included. Neither says anything about the
// repository — a workspace put to bed is exactly the one most likely to have
// gone stale while you were not looking, which makes it the row that most needs
// the dot.
//
// A workspace is left out entirely when nobody can answer for it: no start
// directory, or a host that is down or too old to have the capability. Left out
// means its dot goes back to uncoloured, which is the honest rendering of "we
// cannot currently find out" — the alternative is a colour that stops tracking
// reality the moment a link drops.
func (o *orch) gitSyncPlan() gitSyncPlan {
	var p gitSyncPlan
	for _, ws := range o.session.Workspaces() {
		dir := o.workspaceDir(ws)
		if dir == "" {
			continue
		}
		host := o.workspaceHostID(ws)
		if !o.workspaceHostOwns(ws, host) {
			// The workspace names a host that has left the roster, so its
			// directory belongs to a filesystem nothing here can reach — the
			// state a detach produces (see workspaceHostOwns).
			continue
		}
		t := gitSyncTarget{ws: ws.ID, dir: dir, host: host}
		switch {
		case o.hostByID(host).supports(orchestration.FeatureGitSync):
			p.viaHost = append(p.viaHost, t)
		case host == localHostID:
			p.local = append(p.local, t)
		}
	}
	return p
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

// requestWorkspaceGit (loop goroutine) asks one cathost about one directory.
//
// It rides the same pending machinery as read, capture and the worktree
// commands, which is what makes the failure modes free: a host that drops
// mid-sweep is failed through flushPendingFor, and a daemon that simply never
// answers is failed by the registered timer. Either way the sweep gets its
// answer — an Unknown one — and cannot be left waiting forever on a reply that
// is not coming.
func (o *orch) requestWorkspaceGit(gen uint64, t gitSyncTarget) {
	o.nextGitSyncReq++
	id := o.nextGitSyncReq
	o.registerPending(gitSyncResponder{orch: o, gen: gen, ws: t.ws}, gitSyncKey(t.host, id))
	o.hostByID(t.host).send(orchestration.NewRequestGitSync(id, t.dir))
}

// gitSyncResponder turns one daemon reply into one collected result.
//
// A failure — host dropped, request timed out, reply malformed — is folded into
// the Unknown status rather than surfaced. There is no user waiting on this: it
// is a background poll, and the honest rendering of "could not find out" is the
// same uncoloured dot as "not a repository". Reporting it would put a toast in
// somebody's browser every two minutes for a machine that is merely asleep.
type gitSyncResponder struct {
	orch *orch
	gen  uint64
	ws   string
}

// WantsReply is always true: the sweep is the caller, and it is always there to
// receive. (The interface's false case is for a browser cmd sent with no id.)
func (r gitSyncResponder) WantsReply() bool { return true }

func (r gitSyncResponder) OK(data any) {
	st, _ := data.(gitsync.Status) // a wrong-typed reply reads as Unknown
	r.orch.collectWorkspaceGit(r.gen, []gitSyncResult{{ws: r.ws, st: st}})
}

func (r gitSyncResponder) Fail(string) {
	r.orch.collectWorkspaceGit(r.gen, []gitSyncResult{{ws: r.ws, st: gitsync.Status{}}})
}

// resolveWorkspaceGit (worker goroutine) is the local fallback: resolve every
// target in this process and post the answers back in one batch.
//
// The fan-out is a buffered channel as a semaphore plus a WaitGroup, and each
// worker writes only its own slot of the results slice — so there is no lock,
// and the results come back in target order rather than completion order.
func (o *orch) resolveWorkspaceGit(gen uint64, targets []gitSyncTarget) {
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
	o.post(func() { o.collectWorkspaceGit(gen, results) })
}

// collectWorkspaceGit (loop goroutine) folds one arrival into the sweep in
// flight, and applies the whole thing once the last answer is in.
//
// Waiting for all of them rather than applying each as it lands is what keeps
// one sweep to one broadcast. Applying incrementally would be correct — the
// change check would still suppress the no-op rows — but at startup, when every
// workspace changes at once, it would send one full rollup per workspace to
// every window.
func (o *orch) collectWorkspaceGit(gen uint64, results []gitSyncResult) {
	sw := o.wsGitSweep
	if sw == nil || sw.gen != gen {
		return // an answer from a sweep that has already been abandoned
	}
	sw.results = append(sw.results, results...)
	if sw.outstanding--; sw.outstanding > 0 {
		return
	}
	o.wsGitSweep = nil
	o.applyWorkspaceGit(sw.results)
}

// applyWorkspaceGit (loop goroutine) stores a sweep's answers and broadcasts the
// rollup if anything actually changed.
//
// The change check is what makes the two-minute poll free in the steady state:
// a session where nobody has pushed resolves to the same set of states forever,
// and re-sending an identical list to every window every two minutes would
// re-render the whole workspace list for nothing.
func (o *orch) applyWorkspaceGit(results []gitSyncResult) {
	changed := false
	for _, r := range results {
		prev, had := o.wsGit[r.ws]
		if r.st.State == gitsync.Unknown {
			// A workspace that has STOPPED having an answer (its remote went
			// away, its host dropped, the network is down) drops back to the
			// plain dot rather than keeping the last colour: a stale "in sync"
			// is the one reading that would actively mislead.
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

// gitSyncResponder satisfies app.Responder; pin it at compile time rather than
// discovering a signature drift through a registerPending call site.
var _ app.Responder = gitSyncResponder{}
