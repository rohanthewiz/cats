package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CheckUpdate against a local upstream: current while upstream is still,
// available (with the upstream manifest's version) once it moves, and the
// installed copy untouched by the check — HEAD, manifest and all.
func TestCheckUpdateDefaultBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := testRoot(t)
	repo := writePlugin(t, validManifest)
	git := gitIn(t, repo)
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-q", "-m", "v1")

	inst, err := Install(repo, "", nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if c := CheckUpdate(ctx, inst); c.Status != UpToDate || c.LocalSHA == "" || c.LocalSHA != c.RemoteSHA {
		t.Fatalf("unchanged upstream: %+v, want current with equal shas", c)
	}

	v2 := strings.Replace(validManifest, `version = "0.1.0"`, `version = "0.2.0"`, 1)
	if err := os.WriteFile(filepath.Join(repo, ManifestName), []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-aqm", "v2")

	c := CheckUpdate(ctx, inst)
	if c.Status != UpdateAvailable || c.LocalVersion != "0.1.0" || c.RemoteVersion != "0.2.0" {
		t.Fatalf("moved upstream: %+v, want available 0.1.0 → 0.2.0", c)
	}
	if c.RemoteSubject != "v2" {
		t.Fatalf("remote subject = %q, want the upstream commit's subject %q", c.RemoteSubject, "v2")
	}
	// The check is read-only: the installed tree still holds v1.
	got, err := Get("acme.demo")
	if err != nil || got.Version != "0.1.0" {
		t.Fatalf("after check: %+v, %v; want v0.1.0 untouched", got, err)
	}
	if head, _ := gitHead(filepath.Join(root, "acme.demo")); head != c.LocalSHA {
		t.Fatalf("HEAD moved during check: %s != %s", head, c.LocalSHA)
	}

	// And Update still lands it afterwards (the check's fetch must not have
	// left the clone in a state the real update trips over).
	if _, updated, err := Update("acme.demo", nil); err != nil || !updated {
		t.Fatalf("update after check = %v, %v; want updated", updated, err)
	}
	fresh, _ := Get("acme.demo")
	if c := CheckUpdate(ctx, fresh); c.Status != UpToDate {
		t.Fatalf("after update: %+v, want current", c)
	}
}

// A tag-pinned install compares against the tag's peeled commit, not the
// annotated tag object — otherwise every annotated-tag pin would read as
// permanently out of date.
func TestCheckUpdateAnnotatedTag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	testRoot(t)
	repo := writePlugin(t, validManifest)
	git := gitIn(t, repo)
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-q", "-m", "v1")
	git("tag", "-a", "v0.1.0", "-m", "release 0.1.0")

	inst, err := Install(repo, "v0.1.0", nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// main moving on does not matter to a tag pin.
	git("commit", "-q", "--allow-empty", "-m", "post-release work")
	if c := CheckUpdate(ctx, inst); c.Status != UpToDate {
		t.Fatalf("annotated tag pin: %+v, want current", c)
	}
	// Re-pointing the tag does.
	git("tag", "-f", "-a", "v0.1.0", "-m", "re-release")
	if c := CheckUpdate(ctx, inst); c.Status != UpdateAvailable {
		t.Fatalf("re-pointed tag: %+v, want available", c)
	}
}

// Linked and broken entries have nothing to compare against and are skipped,
// not failed; a vanished remote is a per-plugin error, not a panic.
func TestCheckUpdateSkipsAndFailures(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	testRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	checkout := writePlugin(t, validManifest)
	linked, err := Link(checkout, nil)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if c := CheckUpdate(ctx, linked); c.Status != UpdateSkipped {
		t.Fatalf("linked: %+v, want skipped", c)
	}
	if c := CheckUpdate(ctx, Installed{Manifest: Manifest{ID: "x"}, Err: os.ErrNotExist}); c.Status != UpdateSkipped {
		t.Fatalf("broken: %+v, want skipped", c)
	}
	if _, err := Uninstall("acme.demo"); err != nil {
		t.Fatal(err)
	}

	repo := writePlugin(t, validManifest)
	git := gitIn(t, repo)
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-q", "-m", "v1")
	inst, err := Install(repo, "", nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if c := CheckUpdate(ctx, inst); c.Status != UpdateCheckFailed || c.Reason == "" {
		t.Fatalf("vanished remote: %+v, want error with a reason", c)
	}
}

// A linked checkout gets a quiet "has moved on" hint (N-043): still skipped,
// so no badge and no update button, but the reason says when the branch it
// tracks has a commit it lacks. Asked with ls-remote, so the developer's
// remote-tracking ref must not move.
func TestCheckUpdateLinkedBehindHint(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	testRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// upstream (a plain repo standing in for GitHub) → the developer's clone.
	upstream := writePlugin(t, validManifest)
	up := gitIn(t, upstream)
	up("init", "-q", "-b", "main")
	up("add", ".")
	up("commit", "-q", "-m", "v1")
	checkout := filepath.Join(t.TempDir(), "dev")
	if out, err := exec.Command("git", "clone", "-q", upstream, checkout).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	dev := gitIn(t, checkout)
	dev("config", "user.email", "dev@example.com")
	dev("config", "user.name", "dev")
	linked, err := Link(checkout, nil)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	trackingBefore := gitOut(t, checkout, "rev-parse", "origin/main")

	if c := CheckUpdate(ctx, linked); c.Status != UpdateSkipped || c.Reason != "linked checkout" {
		t.Fatalf("level with upstream: %+v, want a plain skip", c)
	}

	// Upstream moves on: the hint names the branch and the new commit.
	if err := os.WriteFile(filepath.Join(upstream, "NEWS"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	up("add", "NEWS")
	up("commit", "-q", "-m", "v2")
	newTip := gitOut(t, upstream, "rev-parse", "HEAD")
	c := CheckUpdate(ctx, linked)
	if c.Status != UpdateSkipped || !strings.Contains(c.Reason, "origin/main has moved on ("+newTip[:7]+")") {
		t.Fatalf("upstream ahead: %+v, want the moved-on hint", c)
	}
	if after := gitOut(t, checkout, "rev-parse", "origin/main"); after != trackingBefore {
		t.Fatalf("the check moved origin/main from %s to %s; it must not fetch", trackingBefore, after)
	}

	// Pulled, then a local commit on top: ahead of upstream is no hint.
	dev("pull", "-q", "--ff-only")
	if err := os.WriteFile(filepath.Join(checkout, "WIP"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dev("add", "WIP")
	dev("commit", "-q", "-m", "wip")
	if c := CheckUpdate(ctx, linked); c.Reason != "linked checkout" {
		t.Fatalf("ahead of upstream: %+v, want no hint", c)
	}

	// A branch with no upstream gives no hint and no error.
	dev("checkout", "-q", "-b", "scratch")
	if c := CheckUpdate(ctx, linked); c.Status != UpdateSkipped || c.Reason != "linked checkout" {
		t.Fatalf("no upstream: %+v, want a plain skip", c)
	}
}

// gitOut runs git in dir and returns its trimmed stdout.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
