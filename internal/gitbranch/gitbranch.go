// Package gitbranch resolves "which branch is this directory on" by reading
// git's own files, with no git process and no repository library.
//
// It lives in its own package because two different machines ask the question:
// catway asks it for a pane on its own box (the historical path), and cathost
// asks it for every pane it owns — which is the only way a pane on another
// machine can ever carry a branch, since the path in that pane belongs to the
// remote filesystem and means nothing here (see the pane_branch event).
//
// Resolution is deliberately a file read, not `git rev-parse`: reading
// .git/HEAD is two syscalls and no process, against a fork+exec per pane per
// sweep. Everything git puts in HEAD that matters here is in that one file —
// the symbolic ref while a branch is checked out, a raw sha while detached.
package gitbranch

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// FileMaxBytes bounds the reads. HEAD and a worktree's .git pointer are
	// both a single short line; anything larger is not one of them, and the
	// bound is what keeps a stray symlink to a huge (or endless) file from
	// being read into memory.
	FileMaxBytes = 4 << 10
	// walkMaxDepth bounds the climb toward the filesystem root. Reaching the
	// root ends the walk on its own; this only guards against a pathological
	// path (symlink loops resolved by the OS into a very deep name).
	walkMaxDepth = 64
	// DetachedPrefix marks a HEAD that names a commit instead of a branch, so a
	// detached pane reads as "@a1b2c3d" rather than as a branch whose name
	// happens to look like a hash.
	DetachedPrefix = "@"
	// shortSHALen is how much of a detached HEAD's sha is shown — git's own
	// default abbreviation.
	shortSHALen = 7
)

// Resolve reports the branch checked out in dir's repository: the branch name
// while HEAD is symbolic, "@<short sha>" while it is detached, and "" when dir
// is not in a repository or the repository cannot be read.
//
// Unreadable is deliberately indistinguishable from "not a repo" here: both mean
// the header has nothing trustworthy to say, and a permissions error on someone
// else's checkout is not worth a distinct label in a one-row strip.
func Resolve(dir string) string {
	gitDir := FindGitDir(dir)
	if gitDir == "" {
		return ""
	}
	return HeadBranch(filepath.Join(gitDir, "HEAD"))
}

// FindGitDir walks up from dir to the first .git entry and returns the git
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
func FindGitDir(dir string) string {
	d := filepath.Clean(dir)
	if !filepath.IsAbs(d) {
		return "" // a relative cwd is not something this can resolve against
	}
	for i := 0; i < walkMaxDepth; i++ {
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

// HeadBranch parses a git HEAD file into a display label.
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
func HeadBranch(path string) string {
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
		return DetachedPrefix + head[:shortSHALen]
	}
	return ""
}

// readSmall reads at most FileMaxBytes from path, returning "" on any failure.
// The bound (rather than os.ReadFile) is what makes this safe to point at a
// path the user controls the contents of.
func readSmall(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, FileMaxBytes))
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
