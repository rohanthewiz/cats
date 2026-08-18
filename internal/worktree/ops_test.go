package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Do is the operation both catway and a remote cathost run, so these exercise
// it against real git — the parser and the command builders are covered without
// I/O elsewhere, but the *sequence* (resolve the root, list, derive the path,
// add, remove) is what a remote host is trusted to reproduce.

// testRepo builds a git repository with one commit and returns its path.
func testRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		// A machine with no committer identity configured (CI, a fresh box) must
		// not make these tests fail for a reason that has nothing to do with them.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=cats", "GIT_AUTHOR_EMAIL=cats@example.invalid",
			"GIT_COMMITTER_NAME=cats", "GIT_COMMITTER_EMAIL=cats@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "first")
	return dir
}

// The create → list → remove round trip, run the way a daemon runs it.
func TestDoCreateListRemove(t *testing.T) {
	repo := testRepo(t)
	root := t.TempDir()

	created := Do(OpRequest{Op: OpCreate, Cwd: repo, Branch: "worktree/brave-river", Root: root})
	if created.Error != "" {
		t.Fatalf("create: %s", created.Error)
	}
	// The default path keys on the MAIN repository's folder name and the branch
	// slug, under the root as this machine expanded it.
	want := DefaultCheckoutPath(root, filepath.Base(repo), "worktree/brave-river")
	if created.Path != want {
		t.Fatalf("checkout path = %q, want %q", created.Path, want)
	}
	if st, err := os.Stat(filepath.Join(created.Path, "f.txt")); err != nil || st.IsDir() {
		t.Fatalf("the checkout has no working tree: %v", err)
	}

	// A list anchored *inside the new checkout* still names the main repository
	// first — which is what the remove below has to run from.
	listed := Do(OpRequest{Op: OpList, Cwd: created.Path, Root: root})
	if listed.Error != "" {
		t.Fatalf("list: %s", listed.Error)
	}
	if listed.Checkout != canon(t, created.Path) && listed.Checkout != created.Path {
		t.Fatalf("list checkout = %q, want the anchor's own checkout %q", listed.Checkout, created.Path)
	}
	if len(listed.Entries) != 2 {
		t.Fatalf("entries = %+v, want the main repo and the new checkout", listed.Entries)
	}
	if got := MainPath(listed.Entries, ""); got != canon(t, repo) && got != repo {
		t.Fatalf("main path = %q, want the repo %q", got, repo)
	}
	if listed.Entries[1].Branch != "worktree/brave-river" {
		t.Fatalf("second entry = %+v, want the new branch", listed.Entries[1])
	}
	if listed.Root != root {
		t.Fatalf("list root = %q, want the expanded worktree root %q", listed.Root, root)
	}

	// Stat is what worktree.open asks before rooting a workspace on a path.
	if got := Do(OpRequest{Op: OpStat, Path: created.Path}); !got.IsDir || got.Error != "" {
		t.Fatalf("stat of the checkout = %+v", got)
	}
	if got := Do(OpRequest{Op: OpStat, Path: filepath.Join(created.Path, "f.txt")}); got.IsDir ||
		!strings.Contains(got.Error, "not a directory") {
		t.Fatalf("stat of a file = %+v, want a refusal", got)
	}

	if got := Do(OpRequest{Op: OpRemove, Path: created.Path}); got.Error != "" {
		t.Fatalf("remove: %s", got.Error)
	}
	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Fatalf("the checkout survived removal: %v", err)
	}
}

// A checkout with uncommitted work is refused, and the refusal is reported as
// Dirty rather than as a plain failure — that flag is what escalates the
// front-end's confirm to "delete anyway", and forcing then succeeds.
func TestDoRemoveDirtyNeedsForce(t *testing.T) {
	repo := testRepo(t)
	created := Do(OpRequest{Op: OpCreate, Cwd: repo, Branch: "wip", Root: t.TempDir()})
	if created.Error != "" {
		t.Fatalf("create: %s", created.Error)
	}
	if err := os.WriteFile(filepath.Join(created.Path, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	refused := Do(OpRequest{Op: OpRemove, Path: created.Path})
	if refused.Error == "" || !refused.Dirty {
		t.Fatalf("dirty remove = %+v, want a refusal flagged dirty", refused)
	}
	if forced := Do(OpRequest{Op: OpRemove, Path: created.Path, Force: true}); forced.Error != "" {
		t.Fatalf("forced remove: %s", forced.Error)
	}
}

// Every failure is text, never a Go error: the caller may be a process on
// another machine that can do nothing with a typed error but print it.
func TestDoRefusals(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	notARepo := t.TempDir()
	if got := Do(OpRequest{Op: OpList, Cwd: notARepo}); !strings.Contains(got.Error, "not a git worktree") {
		t.Fatalf("list outside a repo = %+v", got)
	}
	if got := Do(OpRequest{Op: OpCreate, Cwd: notARepo}); !strings.Contains(got.Error, "branch name is required") {
		t.Fatalf("create with no branch = %+v", got)
	}
	if got := Do(OpRequest{Op: OpRemove}); !strings.Contains(got.Error, "path is required") {
		t.Fatalf("remove with no path = %+v", got)
	}
	if got := Do(OpRequest{Op: "prune"}); !strings.Contains(got.Error, "unknown worktree operation") {
		t.Fatalf("unknown op = %+v", got)
	}
}

// canon resolves symlinks the way macOS's /var → /private/var TempDir needs,
// so a path comparison against git's own output is not testing the platform.
func canon(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}
