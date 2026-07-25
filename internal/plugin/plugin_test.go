package plugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// validManifest is a minimal manifest with one build step that stamps a file,
// so tests can assert the build actually ran.
const validManifest = `
id = "acme.demo"
name = "Demo"
version = "0.1.0"
description = "test plugin"
platforms = ["linux", "macos"]

[[build]]
command = ["sh", "-c", "echo built > .built"]

[[actions]]
id = "demo"
title = "Demo Action"
command = ["./bin/demo", "--ui"]
`

// writePlugin lays out a plugin checkout in a fresh temp dir.
func writePlugin(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// testRoot points the package at a scratch plugins root via the env override.
func testRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "plugins")
	t.Setenv(DirEnvVar, root)
	return root
}

func TestManifestValidate(t *testing.T) {
	base := Manifest{
		ID:      "acme.demo",
		Version: "1.0",
		Actions: []Action{{ID: "a", Command: []string{"./bin/x"}}},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(m *Manifest)
		want string
	}{
		{"missing id", func(m *Manifest) { m.ID = "" }, "missing id"},
		{"path separator in id", func(m *Manifest) { m.ID = "a/b" }, "invalid id"},
		{"dotdot id", func(m *Manifest) { m.ID = "a..b" }, "invalid id"},
		{"leading dot id", func(m *Manifest) { m.ID = ".hidden" }, "invalid id"},
		{"missing version", func(m *Manifest) { m.Version = "" }, "missing version"},
		{"no actions", func(m *Manifest) { m.Actions = nil }, "at least one"},
		{"empty action command", func(m *Manifest) { m.Actions[0].Command = nil }, "must name a program"},
		{"dup action ids", func(m *Manifest) {
			m.Actions = append(m.Actions, Action{ID: "a", Command: []string{"x"}})
		}, "duplicate action id"},
		{"empty build command", func(m *Manifest) { m.Build = []BuildStep{{}} }, "build[0]"},
	}
	for _, tc := range cases {
		m := base
		m.Actions = append([]Action(nil), base.Actions...)
		tc.mut(&m)
		err := m.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

func TestSupportsPlatform(t *testing.T) {
	m := Manifest{Platforms: []string{"macos", "linux"}}
	// herdr spells darwin "macos" — both must resolve.
	if !m.SupportsPlatform("darwin") || !m.SupportsPlatform("linux") {
		t.Fatal("darwin/linux should be supported")
	}
	if m.SupportsPlatform("windows") {
		t.Fatal("windows should not be supported")
	}
	if !(Manifest{}).SupportsPlatform("plan9") {
		t.Fatal("empty platform list should allow everything")
	}
}

func TestFindAction(t *testing.T) {
	m := Manifest{Actions: []Action{
		{ID: "first", Command: []string{"a"}},
		{ID: "second", Command: []string{"b"}},
	}}
	if a, ok := m.FindAction(""); !ok || a.ID != "first" {
		t.Fatalf("default action = %+v ok=%v, want first", a, ok)
	}
	if a, ok := m.FindAction("second"); !ok || a.ID != "second" {
		t.Fatalf("named action = %+v ok=%v, want second", a, ok)
	}
	if _, ok := m.FindAction("missing"); ok {
		t.Fatal("missing action should not resolve")
	}
}

func TestResolveSourceURL(t *testing.T) {
	cases := map[string]string{
		"rohanthewiz/cats-todo":      "https://github.com/rohanthewiz/cats-todo.git",
		"https://gitlab.com/x/y.git": "https://gitlab.com/x/y.git",
		"git@github.com:x/y.git":     "git@github.com:x/y.git",
		"/abs/path/repo":             "/abs/path/repo",
		"./rel/repo":                 "./rel/repo",
		"../sibling/repo":            "../sibling/repo",
		"not-a-shorthand":            "not-a-shorthand",
		"owner/repo/extra":           "owner/repo/extra", // 3 segments: not the shorthand
	}
	for in, want := range cases {
		if got := resolveSourceURL(in); got != want {
			t.Errorf("resolveSourceURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// A "~" path must resolve here, not in a shell: the plugins dialog spawns
// `catctl plugin link <path>` as raw argv with no shell in between.
func TestExpandTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := map[string]string{
		"~":            home,
		"~/src/thing":  filepath.Join(home, "src/thing"),
		"~user/thing":  "~user/thing", // another user's home: left alone
		"/abs/path":    "/abs/path",
		"./rel":        "./rel",
		"plain":        "plain",
		"a/~/embedded": "a/~/embedded", // only a *leading* tilde expands
	}
	for in, want := range cases {
		if got := expandTilde(in); got != want {
			t.Errorf("expandTilde(%q) = %q, want %q", in, got, want)
		}
	}
}

// Link accepts a ~-path end to end (the dialog's local-checkout route).
func TestLinkTildePath(t *testing.T) {
	testRoot(t)
	checkout := writePlugin(t, validManifest)
	// Present the same checkout as "~/<base>" by pointing HOME at its parent.
	t.Setenv("HOME", filepath.Dir(checkout))

	inst, err := Link("~/"+filepath.Base(checkout), nil)
	if err != nil {
		t.Fatalf("link ~-path: %v", err)
	}
	realCheckout, _ := filepath.EvalSymlinks(checkout)
	if !inst.Linked || inst.Dir != realCheckout {
		t.Fatalf("linked = %+v, want linked at %s", inst, realCheckout)
	}
}

// A relative path — "./dir" or "../dir" — resolves against the process cwd,
// which for a dialog-spawned link tab is the focused pane's cwd. Running from a
// sibling directory makes ".." the only way to reach the checkout, so a broken
// resolution cannot pass by accident.
func TestLinkRelativeParentPath(t *testing.T) {
	testRoot(t)
	checkout := writePlugin(t, validManifest)
	sibling := filepath.Join(filepath.Dir(checkout), "sibling")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sibling)

	inst, err := Link(filepath.Join("..", filepath.Base(checkout)), nil)
	if err != nil {
		t.Fatalf("link ../ path: %v", err)
	}
	realCheckout, _ := filepath.EvalSymlinks(checkout)
	if !inst.Linked || inst.Dir != realCheckout {
		t.Fatalf("linked = %+v, want linked at %s", inst, realCheckout)
	}
}

// Link → List → Get → ActionArgv → Uninstall is the local development loop.
func TestLinkLifecycle(t *testing.T) {
	root := testRoot(t)
	checkout := writePlugin(t, validManifest)

	inst, err := Link(checkout, nil)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	// EvalSymlinks-normalize the expectation: on macOS the temp dir itself
	// lives behind /var → /private/var.
	realCheckout, _ := filepath.EvalSymlinks(checkout)
	if !inst.Linked || inst.Dir != realCheckout || inst.ID != "acme.demo" {
		t.Fatalf("linked = %+v, want linked at %s", inst, realCheckout)
	}
	// The build step ran in the checkout, not the plugins root.
	if _, err := os.Stat(filepath.Join(checkout, ".built")); err != nil {
		t.Fatalf("build step did not run: %v", err)
	}

	// Re-link of the same checkout is idempotent; a different checkout with
	// the same id is a conflict.
	if _, err := Link(checkout, nil); err != nil {
		t.Fatalf("re-link: %v", err)
	}
	other := writePlugin(t, validManifest)
	if _, err := Link(other, nil); err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("conflicting link err = %v, want already installed", err)
	}

	list, err := List()
	if err != nil || len(list) != 1 || list[0].ID != "acme.demo" || list[0].Err != nil {
		t.Fatalf("list = %+v (err %v), want the one linked plugin", list, err)
	}

	got, err := Get("acme.demo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	a, _ := got.FindAction("")
	argv := ActionArgv(got, a)
	// Relative command paths anchor to the checkout dir (the pane's cwd will
	// be the user's project, so a relative argv[0] would never resolve).
	if want := filepath.Join(realCheckout, "bin/demo"); argv[0] != want || argv[1] != "--ui" {
		t.Fatalf("argv = %v, want [%s --ui]", argv, want)
	}

	msg, err := Uninstall("acme.demo")
	if err != nil || !strings.Contains(msg, "unlinked") {
		t.Fatalf("uninstall = %q, %v; want unlinked", msg, err)
	}
	// The checkout survives; only the symlink is gone.
	if _, err := os.Stat(filepath.Join(checkout, ManifestName)); err != nil {
		t.Fatal("uninstalling a linked plugin must not touch the checkout")
	}
	if _, err := os.Lstat(filepath.Join(root, "acme.demo")); !os.IsNotExist(err) {
		t.Fatal("symlink should be removed")
	}
	if _, err := Uninstall("acme.demo"); err == nil {
		t.Fatal("double uninstall should error")
	}
}

// gitIn returns a runner for git commands in dir. Identity comes in via -c so
// the tests stay independent of the user's gitconfig.
func gitIn(t *testing.T, dir string) func(args ...string) {
	t.Helper()
	return func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Args = append([]string{"git", "-c", "user.email=t@t", "-c", "user.name=t",
			"-c", "commit.gpgsign=false"}, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// Install exercises the full clone → validate → build → rename path against a
// local git repo standing in for GitHub.
func TestInstallFromLocalRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := testRoot(t)

	// A local upstream repo holding the plugin.
	repo := writePlugin(t, validManifest)
	git := gitIn(t, repo)
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-q", "-m", "plugin")

	inst, err := Install(repo, "", nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if inst.Linked || inst.ID != "acme.demo" || inst.Source != repo {
		t.Fatalf("installed = %+v, want unlinked acme.demo from %s", inst, inst.Source)
	}
	dest := filepath.Join(root, "acme.demo")
	if inst.Dir != dest {
		t.Fatalf("dir = %s, want %s", inst.Dir, dest)
	}
	// Build ran in the installed copy; provenance was recorded.
	if _, err := os.Stat(filepath.Join(dest, ".built")); err != nil {
		t.Fatalf("build step did not run in install dir: %v", err)
	}
	if inst.Ref != "" {
		t.Fatalf("ref = %q, want empty", inst.Ref)
	}

	// Second install of the same id must refuse.
	if _, err := Install(repo, "", nil); err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("re-install err = %v, want already installed", err)
	}
	// No .installing-* temp dirs left behind.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".installing-") {
			t.Fatalf("leftover temp dir %s", e.Name())
		}
	}

	if msg, err := Uninstall("acme.demo"); err != nil || !strings.Contains(msg, "removed") {
		t.Fatalf("uninstall = %q, %v; want removed", msg, err)
	}
}

// Update covers the whole in-place refresh loop against a local upstream:
// no-op when upstream is unchanged, fetch + rebuild when it moves, and a
// rollback to the previous version when upstream publishes a manifest whose
// id no longer matches the install dir.
func TestUpdate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := testRoot(t)

	repo := writePlugin(t, validManifest)
	git := gitIn(t, repo)
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-q", "-m", "v1")

	if _, err := Install(repo, "", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	dest := filepath.Join(root, "acme.demo")

	// Upstream unchanged: no rebuild, updated=false.
	inst, updated, err := Update("acme.demo", nil)
	if err != nil || updated {
		t.Fatalf("no-op update = (%+v, %v, %v), want updated=false", inst, updated, err)
	}

	// Upstream releases 0.2.0 with a new build step; update must land both
	// the manifest and the artifacts of the re-run build.
	v2 := strings.Replace(validManifest, `version = "0.1.0"`, `version = "0.2.0"`, 1)
	v2 = strings.Replace(v2, "echo built > .built", "echo built2 > .built2", 1)
	if err := os.WriteFile(filepath.Join(repo, ManifestName), []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-aqm", "v2")

	inst, updated, err = Update("acme.demo", nil)
	if err != nil || !updated || inst.Version != "0.2.0" {
		t.Fatalf("update = (v%s, %v, %v), want updated v0.2.0", inst.Version, updated, err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".built2")); err != nil {
		t.Fatalf("new build step did not run: %v", err)
	}
	// Provenance survives the reset (it is untracked in the clone).
	if inst.Source != repo {
		t.Fatalf("source after update = %q, want %q", inst.Source, repo)
	}

	// Upstream changes the plugin id: the install dir would lie about its
	// contents, so update refuses and restores the previous version.
	v3 := strings.Replace(v2, `id = "acme.demo"`, `id = "acme.other"`, 1)
	if err := os.WriteFile(filepath.Join(repo, ManifestName), []byte(v3), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-aqm", "v3 id change")

	if _, _, err := Update("acme.demo", nil); err == nil || !strings.Contains(err.Error(), "changed the plugin id") {
		t.Fatalf("id-change update err = %v, want id-change refusal", err)
	}
	got, err := Get("acme.demo")
	if err != nil || got.Version != "0.2.0" {
		t.Fatalf("after rollback: %+v, %v; want v0.2.0 intact", got, err)
	}
}

// Linked plugins are the developer's own checkout — Update must refuse rather
// than hard-reset their working tree.
func TestUpdateLinkedRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	testRoot(t)
	checkout := writePlugin(t, validManifest)
	if _, err := Link(checkout, nil); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, _, err := Update("acme.demo", nil); err == nil || !strings.Contains(err.Error(), "linked") {
		t.Fatalf("update of linked plugin err = %v, want linked refusal", err)
	}
}

// A broken entry (directory without a manifest) surfaces in List with Err set
// instead of vanishing.
func TestListBrokenEntry(t *testing.T) {
	root := testRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "broken.one"), 0o755); err != nil {
		t.Fatal(err)
	}
	list, err := List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Err == nil || list[0].ID != "broken.one" {
		t.Fatalf("list = %+v, want one broken entry", list)
	}
}
