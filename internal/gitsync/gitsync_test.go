package gitsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tests drive real repositories rather than fakes: every interesting case
// here (packed refs, a worktree's split git directory, what ls-remote prints
// for a branch that does not exist) is a fact about git's behaviour, and a fake
// would only pin this package's assumptions about it rather than the behaviour
// itself. The "remote" is a bare repository on disk, so nothing touches the
// network — ls-remote over a path is the same code path with a local transport.

// run executes a git command and fails the test if it does not succeed.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// A deterministic identity and no user/system config: a developer's global
	// gitconfig (a default branch of "trunk", a commit template, a signing key
	// with no agent running) would otherwise leak into the fixtures.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// commit writes a file and commits it, so each call moves the branch tip.
func commit(t *testing.T, dir, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "f")
	run(t, dir, "commit", "-m", msg)
}

// fixture builds a bare "remote" and a clone of it whose main branch has one
// commit, and returns the clone's path. The bare path is returned too so a test
// can push into it from a second clone — which is how "somebody else pushed"
// is simulated without any network.
func fixture(t *testing.T) (clone, bare string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	bare = filepath.Join(root, "remote.git")
	run(t, root, "init", "--bare", "--initial-branch=main", bare)

	seed := filepath.Join(root, "seed")
	run(t, root, "init", "--initial-branch=main", seed)
	commit(t, seed, "one")
	run(t, seed, "remote", "add", "origin", bare)
	run(t, seed, "push", "-u", "origin", "main")

	clone = filepath.Join(root, "work")
	run(t, root, "clone", bare, clone)
	return clone, bare
}

// otherClone is a second working copy of the same bare repository — the stand-in
// for a colleague, or for the same project checked out on another machine.
func otherClone(t *testing.T, bare string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "other")
	run(t, filepath.Dir(dir), "clone", bare, dir)
	return dir
}

func TestResolveSynced(t *testing.T) {
	clone, _ := fixture(t)
	got := Resolve(context.Background(), clone)
	if got.State != Synced {
		t.Fatalf("state = %q, want %q", got.State, Synced)
	}
	if got.Branch != "main" || got.Remote != "origin" {
		t.Errorf("compared %s vs %s, want main vs origin", got.Branch, got.Remote)
	}
	if got.Ahead != 0 {
		t.Errorf("ahead = %d on a synced repo, want 0", got.Ahead)
	}
}

func TestResolveAheadCountsCommits(t *testing.T) {
	clone, _ := fixture(t)
	commit(t, clone, "two")
	commit(t, clone, "three")
	got := Resolve(context.Background(), clone)
	if got.State != Ahead {
		t.Fatalf("state = %q, want %q", got.State, Ahead)
	}
	// The count is the whole reason ahead carries a number: "you have unpushed
	// work" is much less useful than how much of it there is.
	if got.Ahead != 2 {
		t.Errorf("ahead = %d, want 2", got.Ahead)
	}
}

func TestResolveBehind(t *testing.T) {
	clone, bare := fixture(t)
	other := otherClone(t, bare)
	commit(t, other, "theirs")
	run(t, other, "push", "origin", "main")

	got := Resolve(context.Background(), clone)
	if got.State != Behind {
		t.Fatalf("state = %q, want %q", got.State, Behind)
	}
	// Deliberately uncounted: the poll never fetches, so their commit is not in
	// this object database to be counted. A number here would mean the poll had
	// started writing to the user's repository behind their back.
	if got.Ahead != 0 {
		t.Errorf("ahead = %d on a behind repo, want 0", got.Ahead)
	}
}

// Diverged — commits on both sides — reports as behind, because a pull is what
// has to happen next in both cases and "ahead" would invite a push that gets
// rejected. This is the case the Behind doc comment argues for; pin it.
func TestResolveDivergedReadsAsBehind(t *testing.T) {
	clone, bare := fixture(t)
	other := otherClone(t, bare)
	commit(t, other, "theirs")
	run(t, other, "push", "origin", "main")
	commit(t, clone, "mine")

	if got := Resolve(context.Background(), clone); got.State != Behind {
		t.Fatalf("state = %q, want %q", got.State, Behind)
	}
}

// A repository whose local main is behind but whose objects ARE present (the
// user fetched by hand, or reset back) still reads as behind: the comparison is
// against the remote's tip, not against what happens to be in the object store.
func TestResolveBehindWithObjectsPresent(t *testing.T) {
	clone, bare := fixture(t)
	other := otherClone(t, bare)
	commit(t, other, "theirs")
	run(t, other, "push", "origin", "main")
	run(t, clone, "fetch", "origin")

	if got := Resolve(context.Background(), clone); got.State != Behind {
		t.Fatalf("state = %q, want %q", got.State, Behind)
	}
}

// master is honoured where a repository still uses it, and preferred over
// nothing — the whole point of having two trunk names.
func TestResolveMasterTrunk(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	run(t, root, "init", "--bare", "--initial-branch=master", bare)
	seed := filepath.Join(root, "seed")
	run(t, root, "init", "--initial-branch=master", seed)
	commit(t, seed, "one")
	run(t, seed, "remote", "add", "origin", bare)
	run(t, seed, "push", "-u", "origin", "master")

	got := Resolve(context.Background(), seed)
	if got.State != Synced || got.Branch != "master" {
		t.Fatalf("got %q on %q, want synced on master", got.State, got.Branch)
	}
}

// Where both names exist, main wins: a repository that renamed master to main
// and left the old branch behind is still pushing to main.
func TestResolvePrefersMainOverMaster(t *testing.T) {
	clone, _ := fixture(t)
	run(t, clone, "branch", "master", "main")
	commit(t, clone, "two") // moves main only, so the two disagree
	got := Resolve(context.Background(), clone)
	if got.Branch != "main" {
		t.Fatalf("branch = %q, want main", got.Branch)
	}
	if got.State != Ahead {
		t.Errorf("state = %q, want %q — master would have read as synced", got.State, Ahead)
	}
}

// The branch's configured remote is used, not a hardcoded origin: a workspace
// that pushes main to a fork must be measured against the fork.
func TestResolveUsesConfiguredRemote(t *testing.T) {
	clone, bare := fixture(t)
	run(t, clone, "remote", "rename", "origin", "fork")
	run(t, clone, "config", "branch.main.remote", "fork")
	got := Resolve(context.Background(), clone)
	if got.State != Synced {
		t.Fatalf("state = %q, want %q", got.State, Synced)
	}
	if got.Remote != "fork" {
		t.Errorf("remote = %q, want fork", got.Remote)
	}
	_ = bare
}

// Every "nothing to say" path lands on the zero Status, which is what makes the
// sidebar draw an uncoloured dot rather than guessing.
func TestResolveUnknownPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	plain := t.TempDir()

	noRepo := filepath.Join(plain, "plain")
	if err := os.Mkdir(noRepo, 0o755); err != nil {
		t.Fatal(err)
	}

	noRemote := filepath.Join(plain, "solo")
	run(t, plain, "init", "--initial-branch=main", noRemote)
	commit(t, noRemote, "one")

	noTrunk := filepath.Join(plain, "topic")
	run(t, plain, "init", "--initial-branch=topic", noTrunk)
	commit(t, noTrunk, "one")

	for _, tc := range []struct{ name, dir string }{
		{"not a repository", noRepo},
		{"missing directory", filepath.Join(plain, "gone")},
		{"repository with no remote", noRemote},
		{"no main or master branch", noTrunk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(context.Background(), tc.dir); got.State != Unknown {
				t.Fatalf("state = %q, want unknown", got.State)
			}
		})
	}
}

// A remote that cannot be reached must fail fast and quietly, not hang: this is
// the case hardenedEnv exists for. The address is a host that does not resolve,
// over ssh, which is the shape that would otherwise sit at a passphrase prompt.
func TestResolveUnreachableRemote(t *testing.T) {
	clone, _ := fixture(t)
	run(t, clone, "remote", "set-url", "origin", "git@nonexistent.invalid:x/y.git")
	if got := Resolve(context.Background(), clone); got.State != Unknown {
		t.Fatalf("state = %q, want unknown", got.State)
	}
}

// A cancelled context stops the walk rather than running it to completion — the
// sweep's way out if the daemon is shutting down mid-poll.
func TestResolveHonoursContext(t *testing.T) {
	clone, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Resolve(ctx, clone); got.State != Unknown {
		t.Fatalf("state = %q, want unknown", got.State)
	}
}

// A linked worktree resolves against the repository it belongs to, not against
// nothing: its .git is a file pointing elsewhere, and refs/heads/main lives in
// the common directory rather than beside it. Worktree-per-branch is how cats's
// own worktree workspaces are laid out, so this is the normal case there.
func TestResolveInLinkedWorktree(t *testing.T) {
	clone, _ := fixture(t)
	wt := filepath.Join(filepath.Dir(clone), "wt")
	run(t, clone, "worktree", "add", "-b", "topic", wt)

	got := Resolve(context.Background(), wt)
	if got.State != Synced {
		t.Fatalf("state = %q, want %q", got.State, Synced)
	}
	if got.Branch != "main" {
		// The trunk, not the branch the worktree has checked out: the dot is
		// about the project's mainline, and the pane header already says which
		// branch this particular directory is on.
		t.Errorf("branch = %q, want main", got.Branch)
	}
}

// Packed refs are read as well as loose ones — `git gc` moves refs/heads/main
// into packed-refs, and a repository that has been collected must not silently
// stop reporting.
func TestResolveWithPackedRefs(t *testing.T) {
	clone, _ := fixture(t)
	run(t, clone, "pack-refs", "--all")
	if _, err := os.Stat(filepath.Join(clone, ".git", "refs", "heads", "main")); !os.IsNotExist(err) {
		t.Skip("git did not pack the ref; nothing to pin here")
	}
	if got := Resolve(context.Background(), clone); got.State != Synced {
		t.Fatalf("state = %q, want %q", got.State, Synced)
	}
}
