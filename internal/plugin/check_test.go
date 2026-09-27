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
