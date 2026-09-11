// Package gitsync answers one question about a checkout: is its trunk branch —
// main, or master where that is still the name — level with the remote, ahead
// of it, or behind it?
//
// It exists because the sidebar's workspace rows are the only place in cats
// that names a repository as a whole, and "somebody pushed while I was in
// here" is the fact that most often makes the next commit painful. A dot on
// the row is cheap; finding out the same thing by hand is a `git fetch` and a
// `git status` in a pane you were not otherwise going to open.
//
// # Why this forks git, where internal/gitbranch reads files
//
// gitbranch answers "which branch" from .git/HEAD because it sweeps every pane
// every ten seconds and the answer is two syscalls away. Nothing here can be
// that cheap: the whole question is about a sha on ANOTHER machine, so a
// network round trip is unavoidable, and once a process is being forked anyway
// the file-reading version of the rest (packed-refs, worktree commondir,
// per-branch remote config, include directives) is a reimplementation of git's
// own lookup rules for no saving. So this shells out, and the sweep that calls
// it runs on a two-minute timer rather than a ten-second one.
//
// # Why ls-remote and not fetch
//
// `git ls-remote` asks the remote what it has and writes nothing. `git fetch`
// would be the obvious way to get exact counts, but it mutates a repository the
// user is actively working in — new objects, moved remote-tracking refs, a
// reflog entry — every two minutes, forever, in the background. A status
// indicator must not change the thing it reports on.
//
// The cost of not fetching is that a repository which is BEHIND does not have
// the remote's commits locally, so they cannot be counted; Status says "behind"
// without a number. Ahead is countable, because those commits are right here.
package gitsync

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// State is what the indicator draws. The zero value is Unknown, which is the
// honest answer for a directory that is not a repository, has no remote, or
// could not be reached — all three of which render the same way (no colour),
// because none of them is a fact about the user's work.
type State string

const (
	Unknown State = ""
	// Synced: the trunk's local tip and the remote's are the same commit.
	Synced State = "synced"
	// Ahead: the remote's tip is an ancestor of ours — we have commits to push
	// and they have nothing we lack.
	Ahead State = "ahead"
	// Behind: the remote has commits we do not. This deliberately swallows the
	// DIVERGED case (both sides have their own commits): the indicator's job is
	// to say what has to happen next, and in both states the next thing is a
	// pull. Diverged is the worse of the two, and a user who is told "behind"
	// and pulls finds that out immediately; one who is told "ahead" and pushes
	// finds out via a rejected push, having already decided they were fine.
	Behind State = "behind"
)

// Status is one repository's answer. Branch and Remote are carried so the
// tooltip can say which pair was actually compared — "main vs origin" is not a
// safe assumption in a tree that still uses master, or that pushes a fork.
type Status struct {
	State  State
	Branch string // the trunk examined: "main", or "master"
	Remote string // the remote it was compared against, usually "origin"
	// Ahead is how many commits the local trunk has that the remote's tip does
	// not, and is only ever set for State == Ahead — see the package comment on
	// why the behind direction has no number.
	Ahead int
}

const (
	// localTimeout bounds the object-database commands. These are local disk
	// work; a second-scale bound is only there so a wedged filesystem (a stale
	// network mount, a repository mid-gc) cannot pin a sweep goroutine forever.
	localTimeout = 10 * time.Second
	// remoteTimeout bounds the one command that talks to the network. Generous,
	// because a cold ssh handshake to a slow forge is legitimately seconds, and
	// the sweep behind this runs every two minutes — there is no queue to hold
	// up. What it actually guards against is the pathological case: a host that
	// accepts the connection and then says nothing.
	remoteTimeout = 30 * time.Second
)

// trunkNames are the branch names treated as "the" branch of a repository, in
// preference order. Only these two: the point of the indicator is the shared
// mainline everyone pushes to, and a repository whose mainline is called
// something else is rare enough that guessing (the remote HEAD, the longest
// branch, …) would be a worse answer more often than no answer.
var trunkNames = []string{"main", "master"}

// Resolve reports dir's trunk sync state. Every failure path — not a
// repository, no main or master, no remote, network down, git absent — returns
// the zero Status, because the row has nothing trustworthy to say in any of
// them and a wrong colour is worse than no colour.
//
// The flow, and what each step costs:
//
//	for-each-ref   local   is there a main/master, and at what sha?
//	config         local   which remote does that branch push to?
//	ls-remote      NETWORK what sha does the remote have for it?
//	  shas equal  -> synced, and the walk stops here (the common case: 3 forks)
//	cat-file -e    local   do we even have their commit?  no -> behind
//	rev-list       local   how far apart, in each direction?
func Resolve(ctx context.Context, dir string) Status {
	branch, local := trunk(ctx, dir)
	if branch == "" {
		return Status{}
	}
	remote := remoteFor(ctx, dir, branch)
	st := Status{Branch: branch, Remote: remote}

	sha := remoteTip(ctx, dir, remote, branch)
	if sha == "" {
		// No answer from the remote: it has no such branch, does not exist, or
		// could not be reached. None of those is a statement about the user's
		// commits, so the dot stays uncoloured rather than claiming "ahead".
		return st
	}
	if sha == local {
		st.State = Synced
		return st
	}
	// A sha the object database does not hold cannot be counted against, and
	// its absence is itself the answer: those commits are only on the remote.
	if !hasCommit(ctx, dir, sha) {
		st.State = Behind
		return st
	}
	ahead, behind, ok := divergence(ctx, dir, local, sha)
	switch {
	case !ok:
		return st // rev-list failed; better to say nothing than to guess
	case behind > 0:
		st.State = Behind // including diverged — see the Behind comment
	case ahead > 0:
		st.State, st.Ahead = Ahead, ahead
	default:
		// Different shas that are zero apart in both directions is not a shape
		// git produces, but the arithmetic above would fall through to "synced"
		// silently; naming the case keeps that honest.
		st.State = Synced
	}
	return st
}

// trunk finds the repository's mainline branch and its local tip. It asks for
// both candidate refs in one command rather than probing them in turn, so the
// usual (main-only) repository costs exactly one fork and the master-era one
// costs the same.
//
// A repository holding BOTH main and master — a rename that left the old branch
// behind, which is common — resolves to main, since that is the one still being
// pushed to.
func trunk(ctx context.Context, dir string) (branch, sha string) {
	out, ok := git(ctx, dir, localTimeout, "for-each-ref",
		"--format=%(refname:short) %(objectname)", "refs/heads/main", "refs/heads/master")
	if !ok {
		return "", ""
	}
	found := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, obj, cut := strings.Cut(strings.TrimSpace(line), " ")
		if cut && name != "" && obj != "" {
			found[name] = obj
		}
	}
	for _, n := range trunkNames {
		if s := found[n]; s != "" {
			return n, s
		}
	}
	return "", ""
}

// remoteFor is which remote the trunk branch pushes to: its configured upstream
// remote, falling back to origin.
//
// The fallback matters more than it looks. A branch that has never been pushed
// has no branch.<name>.remote at all, and a fresh clone's main usually does —
// so the two cases together cover both "cloned it" and "made it here and
// pushed it later". Where origin does not exist either, ls-remote simply fails
// and the whole thing lands on Unknown, which is correct: a repository with no
// remote has no remote to be in sync with.
func remoteFor(ctx context.Context, dir, branch string) string {
	if out, ok := git(ctx, dir, localTimeout, "config", "--get", "branch."+branch+".remote"); ok {
		if r := strings.TrimSpace(out); r != "" {
			return r
		}
	}
	return "origin"
}

// remoteTip asks the remote for its sha for one branch — the network step.
//
// Output is git's ls-remote table, "<sha>\t<ref>", filtered to the single ref
// asked for, so one line is expected and anything else (an empty answer for a
// branch the remote does not have) reads as "no answer".
func remoteTip(ctx context.Context, dir, remote, branch string) string {
	out, ok := git(ctx, dir, remoteTimeout, "ls-remote", "--heads", "--", remote, "refs/heads/"+branch)
	if !ok {
		return ""
	}
	sha, _, cut := strings.Cut(strings.TrimSpace(out), "\t")
	if !cut {
		return ""
	}
	return sha
}

// hasCommit reports whether the object database holds sha as a commit. The
// ^{commit} peel is what makes a sha that names some other object type (which a
// remote branch tip never is, but the check is free) answer false rather than
// true-but-unusable-in-rev-list.
func hasCommit(ctx context.Context, dir, sha string) bool {
	_, ok := git(ctx, dir, localTimeout, "cat-file", "-e", sha+"^{commit}")
	return ok
}

// divergence counts commits on each side of the fork point in one pass:
// `rev-list --left-right --count A...B` prints "<left>\t<right>", left being
// commits reachable from A (ours) but not B, right the reverse. The three-dot
// form is what makes it symmetric — two-dot would answer only one direction and
// could not tell "ahead" from "diverged".
func divergence(ctx context.Context, dir, local, remote string) (ahead, behind int, ok bool) {
	out, ok := git(ctx, dir, localTimeout, "rev-list", "--left-right", "--count", local+"..."+remote)
	if !ok {
		return 0, 0, false
	}
	l, r, cut := strings.Cut(strings.TrimSpace(out), "\t")
	if !cut {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(strings.TrimSpace(l))
	b, err2 := strconv.Atoi(strings.TrimSpace(r))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return a, b, true
}

// git runs one git command in dir under its own timeout, returning stdout and
// whether it succeeded. Stderr is discarded: every failure here is already
// folded into "the row has nothing to say", and a repository the user cannot
// read would otherwise write a line into the daemon log every two minutes.
//
// -C rather than cmd.Dir so git's own "not a repository" handling applies to a
// path that has since been deleted, instead of exec failing to chdir.
func git(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = hardenedEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// hardenedEnv is the caller's environment with every interactive path shut off.
//
// This is the difference between a background poll and a hung one. A private
// repository over https whose credential helper has expired, or over ssh with a
// passphrase-locked key, will by default sit waiting for a human at a terminal
// that does not exist — and the process would be reaped only by the timeout
// above, every two minutes, forever. Each variable closes one of those doors:
//
//	GIT_TERMINAL_PROMPT=0   no username/password prompt on the tty
//	GIT_ASKPASS / SSH_ASKPASS + *_REQUIRE=never
//	                        no GUI passphrase dialog either (an askpass set to
//	                        a program that prints nothing fails immediately)
//	GIT_SSH_COMMAND         BatchMode refuses any ssh prompt; ConnectTimeout
//	                        keeps a dead host from eating the whole budget
//
// The user's config is otherwise left entirely alone: credential helpers that
// answer without asking (the macOS keychain, a cached token) still work, which
// is what makes a private repository resolvable at all.
func hardenedEnv() []string {
	return append(environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"SSH_ASKPASS_REQUIRE=never",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10",
	)
}

// environ is os.Environ behind a variable so a test can pin the inherited
// environment (and so a test repository is never resolved against whatever
// GIT_* the developer happens to be running under).
var environ = os.Environ
