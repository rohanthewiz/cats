//go:build ghostty

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
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
	// gitFileMaxBytes bounds the reads. HEAD and a worktree's .git pointer are
	// both a single short line; anything larger is not one of them, and the
	// bound is what keeps a stray symlink to a huge (or endless) file from
	// being read into memory.
	gitFileMaxBytes = 4 << 10
	// gitWalkMaxDepth bounds the climb toward the filesystem root. Reaching the
	// root ends the walk on its own; this only guards against a pathological
	// path (symlink loops resolved by the OS into a very deep name).
	gitWalkMaxDepth = 64
	// detachedPrefix marks a HEAD that names a commit instead of a branch, so a
	// detached pane reads as "@a1b2c3d" rather than as a branch whose name
	// happens to look like a hash.
	detachedPrefix = "@"
	// shortSHALen is how much of a detached HEAD's sha is shown — git's own
	// default abbreviation.
	shortSHALen = 7
)

// refreshPaneBranch resolves rt's branch in the background when one is due. A
// pane with no reported cwd yet (no OSC 7 since it spawned) has nothing to
// resolve against, and one that had a branch and then lost its cwd drops it
// rather than keeping a label for a directory nobody can name.
func (o *orch) refreshPaneBranch(rt *paneRuntime) {
	// A remote pane's cwd is a path on its own machine; resolving it here would
	// read this machine's repositories and label the pane with a branch from a
	// directory that merely shares a name (see orch.paneIsLocal). Branch
	// resolution moves host-side in Phase 4; until then a remote pane simply
	// carries no branch, and one that had one before it was restored onto a
	// remote host drops it through the same clearing path below.
	if rt.cwd == "" || !o.paneIsLocal(rt.id) {
		if rt.branch != "" {
			rt.branch = ""
			if o.visible[rt.id] {
				o.broadcast(browserproto.NewPaneBranch(rt.id, ""))
			}
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
	if o.visible[pid] {
		o.broadcast(browserproto.NewPaneBranch(pid, branch))
	}
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

// --- resolution (no orch state; runs off the loop goroutine) -----------------

// gitBranch reports the branch checked out in dir's repository: the branch name
// while HEAD is symbolic, "@<short sha>" while it is detached, and "" when dir
// is not in a repository or the repository cannot be read.
//
// Unreadable is deliberately indistinguishable from "not a repo" here: both mean
// the header has nothing trustworthy to say, and a permissions error on someone
// else's checkout is not worth a distinct label in a one-row strip.
func gitBranch(dir string) string {
	gitDir := findGitDir(dir)
	if gitDir == "" {
		return ""
	}
	return headBranch(filepath.Join(gitDir, "HEAD"))
}

// findGitDir walks up from dir to the first .git entry and returns the git
// directory it names, or "" if the walk leaves the filesystem without finding
// one.
//
// The two shapes .git comes in:
//
//	.git/            a normal checkout — the git directory itself
//	.git             a file, in a linked worktree or a submodule, holding
//	                 "gitdir: <path to the real git directory>"
//
// The distinction matters precisely here: a worktree's HEAD lives in the
// pointed-at directory (.git/worktrees/<name>/HEAD), and reading the main
// checkout's HEAD instead would report the *other* branch — the one the user is
// specifically not on. Which is the failure this whole feature exists to fix.
func findGitDir(dir string) string {
	d := filepath.Clean(dir)
	if !filepath.IsAbs(d) {
		return "" // a relative cwd is not something this can resolve against
	}
	for i := 0; i < gitWalkMaxDepth; i++ {
		p := filepath.Join(d, ".git")
		if fi, err := os.Lstat(p); err == nil {
			if fi.IsDir() {
				return p
			}
			return gitDirPointer(p, d)
			// A .git that is neither a directory nor a readable pointer ends
			// the walk rather than continuing upward: the repository boundary
			// is here, and an outer repository's branch would be the wrong
			// answer, not a fallback.
		}
		parent := filepath.Dir(d)
		if parent == d {
			break // filesystem root
		}
		d = parent
	}
	return ""
}

// gitDirPointer reads a ".git" file's "gitdir: <path>" line, resolving a
// relative path against the checkout that holds it (git writes relative
// pointers when the worktree and the repository share an ancestor).
func gitDirPointer(path, checkout string) string {
	line := strings.TrimSpace(readSmall(path))
	rest, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return ""
	}
	p := strings.TrimSpace(rest)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(checkout, p)
	}
	return filepath.Clean(p)
}

// headBranch parses a git HEAD file into a display label.
//
// Symbolic form is "ref: refs/heads/<name>"; the name may itself contain
// slashes ("feature/thing"), so the whole remainder after the refs/heads/ prefix
// is the branch. A ref outside refs/heads (which HEAD should never hold, but
// git's format permits) falls back to its last segment rather than printing a
// full ref path into a one-row strip.
//
// Otherwise HEAD holds a raw commit — detached, which is also what a pane sitting
// mid-rebase or mid-bisect shows. That is worth saying out loud in the header,
// since committing there is how work gets lost.
func headBranch(path string) string {
	head := strings.TrimSpace(readSmall(path))
	if head == "" {
		return ""
	}
	if ref, ok := strings.CutPrefix(head, "ref:"); ok {
		ref = strings.TrimSpace(ref)
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			return name
		}
		if i := strings.LastIndex(ref, "/"); i >= 0 {
			return ref[i+1:]
		}
		return ref
	}
	if isHex(head) && len(head) >= shortSHALen {
		return detachedPrefix + head[:shortSHALen]
	}
	return ""
}

// readSmall reads at most gitFileMaxBytes from path, returning "" on any
// failure. The bound (rather than os.ReadFile) is what makes this safe to point
// at a path the user controls the contents of.
func readSmall(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, gitFileMaxBytes))
	if err != nil {
		return ""
	}
	return string(b)
}

// isHex reports whether s is a non-empty run of hex digits — the shape of the
// raw sha a detached HEAD holds, and what separates it from a malformed file.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
