//go:build ghostty

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile writes content at path, creating parents. Fixtures here are the
// on-disk shapes git itself produces, since reading them is the whole feature.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitBranchNormalCheckout(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")

	if got := gitBranch(root); got != "main" {
		t.Errorf("gitBranch(repo root) = %q, want %q", got, "main")
	}
	// A pane's cwd is usually somewhere under the checkout, not at its top.
	deep := filepath.Join(root, "cmd", "catway", "web")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(deep); got != "main" {
		t.Errorf("gitBranch(subdir) = %q, want %q", got, "main")
	}
}

func TestGitBranchSlashedName(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/feature/pane-header\n")
	if got := gitBranch(root); got != "feature/pane-header" {
		t.Errorf("gitBranch = %q, want the full name below refs/heads/", got)
	}
}

// A linked worktree's .git is a file pointing at the real git directory; its
// HEAD is the one that names the branch the pane is actually on. Reading the
// main checkout's HEAD instead would report the wrong branch, which is exactly
// the confusion the header exists to remove.
func TestGitBranchLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "repo")
	writeFile(t, filepath.Join(main, ".git", "HEAD"), "ref: refs/heads/main\n")
	wtGitDir := filepath.Join(main, ".git", "worktrees", "fix")
	writeFile(t, filepath.Join(wtGitDir, "HEAD"), "ref: refs/heads/fix/crash\n")

	checkout := filepath.Join(root, "wt", "fix")
	writeFile(t, filepath.Join(checkout, ".git"), "gitdir: "+wtGitDir+"\n")

	if got := gitBranch(checkout); got != "fix/crash" {
		t.Errorf("gitBranch(worktree) = %q, want %q", got, "fix/crash")
	}
}

// git writes the pointer relative to the checkout when the two share an
// ancestor, so a relative gitdir must resolve against the file's own directory.
func TestGitBranchRelativeGitdirPointer(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "repo", ".git", "worktrees", "fix", "HEAD"), "ref: refs/heads/fix\n")
	checkout := filepath.Join(root, "wt")
	writeFile(t, filepath.Join(checkout, ".git"), "gitdir: ../repo/.git/worktrees/fix\n")

	if got := gitBranch(checkout); got != "fix" {
		t.Errorf("gitBranch(relative pointer) = %q, want %q", got, "fix")
	}
}

func TestGitBranchDetachedHEAD(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "9f1c4a2b8e0d6f37a5c1b9e2d4086f7a3c5e1b90\n")
	if got := gitBranch(root); got != "@9f1c4a2" {
		t.Errorf("gitBranch(detached) = %q, want %q", got, "@9f1c4a2")
	}
}

// Everything that isn't a readable repository is the same answer — no label —
// so the header says nothing rather than something it can't stand behind.
func TestGitBranchNotARepo(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "notes")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"no repository anywhere above": plain,
		"empty cwd":                    "",
		"relative cwd":                 "some/relative/path",
		"nonexistent directory":        filepath.Join(root, "gone"),
	} {
		if got := gitBranch(dir); got != "" {
			t.Errorf("%s: gitBranch = %q, want \"\"", name, got)
		}
	}
}

// A .git that is neither a directory nor a valid pointer ends the walk where it
// is: an enclosing repository's branch would be a wrong answer, not a fallback.
func TestGitBranchStopsAtUnreadableBoundary(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/outer\n")
	inner := filepath.Join(root, "vendored")
	writeFile(t, filepath.Join(inner, ".git"), "not a gitdir pointer\n")

	if got := gitBranch(inner); got != "" {
		t.Errorf("gitBranch(broken inner .git) = %q, want \"\" rather than the outer repo's branch", got)
	}
}

func TestHeadBranchMalformed(t *testing.T) {
	dir := t.TempDir()
	for name, head := range map[string]string{
		"empty file":           "",
		"whitespace only":      "   \n",
		"truncated sha":        "9f1c4a\n", // shorter than the abbreviation shown
		"not hex, not a ref":   "garbage-here\n",
		"symbolic ref with no": "ref:\n",
	} {
		p := filepath.Join(dir, "HEAD")
		writeFile(t, p, head)
		if got := headBranch(p); got != "" {
			t.Errorf("%s: headBranch = %q, want \"\"", name, got)
		}
	}
	// A HEAD pointing outside refs/heads is legal in git's format but never
	// written by a checkout; it degrades to the ref's last segment rather than
	// printing a full ref path into a one-row strip.
	p := filepath.Join(dir, "HEAD")
	writeFile(t, p, "ref: refs/remotes/origin/main\n")
	if got := headBranch(p); got != "main" {
		t.Errorf("headBranch(non-heads ref) = %q, want %q", got, "main")
	}
}

// The read is bounded so a HEAD that is not a HEAD (a symlink to something huge)
// cannot be pulled into memory. The bounded prefix simply fails to parse.
func TestHeadBranchBoundedRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "HEAD")
	writeFile(t, p, string(make([]byte, gitFileMaxBytes*4)))
	if got := headBranch(p); got != "" {
		t.Errorf("headBranch(oversized) = %q, want \"\"", got)
	}
}
