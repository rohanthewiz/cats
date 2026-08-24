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
// The bin dir is pinned alongside it: install/link/uninstall now reconcile bin
// links, and without the pin they would read (and could write) the user's real
// ~/.cats/bin.
func testRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "plugins")
	t.Setenv(DirEnvVar, root)
	t.Setenv(BinDirEnvVar, filepath.Join(t.TempDir(), "bin"))
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
		{"empty action command", func(m *Manifest) { m.Actions[0].Command = nil }, "must name a program"},
		{"dup action ids", func(m *Manifest) {
			m.Actions = append(m.Actions, Action{ID: "a", Command: []string{"x"}})
		}, "duplicate action id"},
		{"empty build command", func(m *Manifest) { m.Build = []BuildStep{{}} }, "build[0]"},
		{"completion without a binary", func(m *Manifest) {
			m.Completions = []Completion{{Command: []string{"./x"}}}
		}, "missing binary"},
		{"completion binary with a path separator", func(m *Manifest) {
			m.Completions = []Completion{{Binary: "bin/x", Flags: []string{"-a"}}}
		}, "invalid binary"},
		{"completion with nothing to offer", func(m *Manifest) {
			m.Completions = []Completion{{Binary: "x"}}
		}, "needs a command"},
		{"duplicate completion binaries", func(m *Manifest) {
			m.Completions = []Completion{
				{Binary: "x", Flags: []string{"-a"}},
				{Binary: "x", Command: []string{"./y"}},
			}
		}, "duplicate completion binary"},
		{"absolute bin path", func(m *Manifest) { m.Bin = []string{"/usr/bin/x"} }, "must be relative"},
		{"bin path escaping the plugin", func(m *Manifest) { m.Bin = []string{"../x"} }, "stay inside"},
		{"empty bin path", func(m *Manifest) { m.Bin = []string{" "} }, "empty path"},
		{"bin base name with shell syntax", func(m *Manifest) { m.Bin = []string{"bin/a b"} }, "invalid name"},
		{"duplicate bin base names", func(m *Manifest) {
			m.Bin = []string{"./bin/x", "./other/x"}
		}, "duplicate bin name"},
		{"unknown shell", func(m *Manifest) { m.Shell = map[string]string{"csh": "a.csh"} }, "unknown shell"},
		{"shell path escaping the plugin", func(m *Manifest) {
			m.Shell = map[string]string{"zsh": "../evil.zsh"}
		}, "stay inside"},
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

// Completions are enumerated across installed plugins and resolved by the
// command name a shell would type, so `catctl completion <shell>` can register
// them and `__complete --for` can find them again.
func TestCompletionsLookup(t *testing.T) {
	testRoot(t)
	checkout := writePlugin(t, validManifest+`
[[completions]]
binary = "demo"
command = ["./bin/demo", "__complete"]

[[completions]]
binary = "demo-lite"
subcommands = ["add", "rm"]
flags = ["--force"]
`)
	if _, err := Link(checkout, nil); err != nil {
		t.Fatalf("link: %v", err)
	}

	all := Completions()
	if len(all) != 2 {
		t.Fatalf("Completions() = %+v, want 2", all)
	}

	bc, ok := LookupCompletion("demo")
	if !ok || !bc.Completion.Dynamic() {
		t.Fatalf("LookupCompletion(demo) = %+v, %v; want a dynamic entry", bc, ok)
	}
	// A manifest-relative argv only means something anchored to the plugin root:
	// the completer runs while the shell sits in an unrelated directory.
	argv := CompletionArgv(bc.Plugin, bc.Completion)
	if want := filepath.Join(bc.Plugin.Dir, "bin", "demo"); argv[0] != want {
		t.Errorf("CompletionArgv[0] = %q, want %q", argv[0], want)
	}
	if len(argv) != 2 || argv[1] != "__complete" {
		t.Errorf("CompletionArgv = %v, want the trailing __complete preserved", argv)
	}

	lite, ok := LookupCompletion("demo-lite")
	if !ok || lite.Completion.Dynamic() {
		t.Fatalf("LookupCompletion(demo-lite) = %+v, %v; want a static entry", lite, ok)
	}
	if _, ok := LookupCompletion("nothing"); ok {
		t.Error("LookupCompletion(nothing) found something")
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

// A dev link whose checkout was deleted (or moved — e.g. a plugin extracted to
// its own repo) still occupies its id. It must surface in List as a broken
// linked entry rather than vanish: hiding it produced the contradiction of
// Install saying "already installed" while the dialog said "no plugins
// installed", with nothing to uninstall in between. Install/Link must name the
// broken link in their refusal, and Uninstall must clear it.
func TestBrokenDevLink(t *testing.T) {
	root := testRoot(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// The id the link squats on matches validManifest's, so Install/Link below
	// collide with it.
	gone := filepath.Join(t.TempDir(), "moved-away")
	if err := os.Symlink(gone, filepath.Join(root, "acme.demo")); err != nil {
		t.Fatal(err)
	}
	// A plain file squatter is still skipped — only dirs and symlinks list.
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	list, err := List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "acme.demo" || list[0].Err == nil || !list[0].Linked {
		t.Fatalf("list = %+v, want one broken linked entry", list)
	}

	// Linking a live checkout under the occupied id names the broken link.
	checkout := writePlugin(t, validManifest)
	if _, err := Link(checkout, nil); err == nil || !strings.Contains(err.Error(), "broken dev link") {
		t.Fatalf("link over broken link err = %v, want broken dev link refusal", err)
	}

	// So does Install (full clone path, against a local upstream).
	if _, err := exec.LookPath("git"); err == nil {
		git := gitIn(t, checkout)
		git("init", "-q", "-b", "main")
		git("add", ".")
		git("commit", "-q", "-m", "plugin")
		if _, err := Install(checkout, "", nil); err == nil || !strings.Contains(err.Error(), "broken dev link") {
			t.Fatalf("install over broken link err = %v, want broken dev link refusal", err)
		}
	}

	// Uninstall clears the squatter; a retry of the link then succeeds.
	if msg, err := Uninstall("acme.demo"); err != nil || !strings.Contains(msg, "unlinked") {
		t.Fatalf("uninstall = %q, %v; want unlinked", msg, err)
	}
	if _, err := Link(checkout, nil); err != nil {
		t.Fatalf("link after clearing broken link: %v", err)
	}
}

// TestBuildStepSeesInvokingDirectory pins the contract runBuildStep exists for:
// a build step runs in the plugin root but is told, via InstallCwdEnvVar, where
// the user was standing when they ran the installer. A step that wants to set
// something up in the user's project has no other way to find it.
func TestBuildStepSeesInvokingDirectory(t *testing.T) {
	testRoot(t)
	// The build step records both directories so the test can tell them apart.
	checkout := writePlugin(t, `
id = "acme.cwd"
name = "Cwd"
version = "0.1.0"

[[build]]
command = ["sh", "-c", "printf '%s\n%s\n' \"$PWD\" \"$CATS_PLUGIN_INSTALL_CWD\" > .dirs"]

[[actions]]
id = "demo"
title = "Demo"
command = ["./bin/demo"]
`)

	if _, err := Link(checkout, nil); err != nil {
		t.Fatalf("link: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(checkout, ".dirs"))
	if err != nil {
		t.Fatalf("build step did not run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("build step recorded %q, want two directories", string(data))
	}
	stepCwd, invokedFrom := lines[0], lines[1]

	realCheckout, _ := filepath.EvalSymlinks(checkout)
	if got, _ := filepath.EvalSymlinks(stepCwd); got != realCheckout {
		t.Errorf("build step ran in %s, want the plugin root %s", stepCwd, realCheckout)
	}
	wd, _ := os.Getwd()
	realWd, _ := filepath.EvalSymlinks(wd)
	if got, _ := filepath.EvalSymlinks(invokedFrom); got != realWd {
		t.Errorf("%s = %s, want the host's working directory %s", InstallCwdEnvVar, invokedFrom, realWd)
	}
}

// A manifest with zero actions validates: a plugin may ship only passive
// assets (e.g. UI themes under themes/), with nothing to run.
func TestValidateAllowsAssetOnlyPlugin(t *testing.T) {
	m := Manifest{ID: "just-themes", Version: "1.0.0"}
	if err := m.Validate(); err != nil {
		t.Fatalf("asset-only manifest should validate: %v", err)
	}
	if _, ok := m.FindAction(""); ok {
		t.Fatal("FindAction on an asset-only plugin should report no action")
	}
}

// binManifest declares a bin entry (and a build step that creates it) so the
// farm tests exercise the full link/re-link/uninstall reconciliation.
const binManifest = `
id = "acme.tool"
version = "0.1.0"
bin = ["./bin/tool"]

[shell]
zsh = "shell/tool.zsh"

[[build]]
command = ["sh", "-c", "mkdir -p bin && echo bin > bin/tool"]
`

// The bin farm across a linked plugin's life: link creates the symlink (aimed
// at the stable <root>/<id> path, not the checkout), a manifest change drops
// the stale name on re-link, and uninstall clears only what the plugin owns.
func TestBinLinkLifecycle(t *testing.T) {
	root := testRoot(t)
	binDir, _ := BinDir()
	checkout := writePlugin(t, binManifest)

	if _, err := Link(checkout, nil); err != nil {
		t.Fatalf("link: %v", err)
	}
	link := filepath.Join(binDir, "tool")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("bin link missing after link: %v", err)
	}
	// Stable path through the plugins root, so rebuilds and moved checkouts
	// never invalidate the farm.
	if want := filepath.Join(root, "acme.tool", "bin/tool"); target != want {
		t.Fatalf("link target = %s, want %s", target, want)
	}

	// Entries the plugin does not own survive every reconciliation.
	foreignLink := filepath.Join(binDir, "other")
	if err := os.Symlink("/usr/bin/true", foreignLink); err != nil {
		t.Fatal(err)
	}
	foreignFile := filepath.Join(binDir, "notalink")
	if err := os.WriteFile(foreignFile, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A re-link after the manifest renames the tool: the old name is ours and
	// stale, so it goes; the new name appears.
	renamed := strings.ReplaceAll(binManifest, `"./bin/tool"`, `"./bin/tool2"`)
	renamed = strings.ReplaceAll(renamed, "bin/tool\"]", "bin/tool2\"]")
	if err := os.WriteFile(filepath.Join(checkout, ManifestName), []byte(renamed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Link(checkout, nil); err != nil {
		t.Fatalf("re-link: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("stale bin link should be removed on re-link")
	}
	if _, err := os.Readlink(filepath.Join(binDir, "tool2")); err != nil {
		t.Fatalf("renamed bin link missing: %v", err)
	}

	if _, err := Uninstall("acme.tool"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(binDir, "tool2")); !os.IsNotExist(err) {
		t.Fatal("uninstall should remove the plugin's bin link")
	}
	for _, p := range []string{foreignLink, foreignFile} {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("uninstall touched foreign entry %s: %v", p, err)
		}
	}
}

// A name already taken by something the plugin does not own is left alone —
// warned about, never overwritten.
func TestBinLinkRespectsForeignEntry(t *testing.T) {
	testRoot(t)
	binDir, _ := BinDir()
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	squatter := filepath.Join(binDir, "tool")
	if err := os.WriteFile(squatter, []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if _, err := Link(writePlugin(t, binManifest), &out); err != nil {
		t.Fatalf("link: %v", err)
	}
	if data, _ := os.ReadFile(squatter); string(data) != "mine" {
		t.Fatal("foreign entry was overwritten")
	}
	if !strings.Contains(out.String(), "not owned") {
		t.Fatalf("expected a not-owned warning, got %q", out.String())
	}
}

// RemoveBinLinks works from link targets alone: a plugin whose manifest has
// been broken still cleans its links up on uninstall.
func TestBinLinksRemovedForBrokenManifest(t *testing.T) {
	root := testRoot(t)
	binDir, _ := BinDir()
	checkout := writePlugin(t, binManifest)
	if _, err := Link(checkout, nil); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ManifestName), []byte("not toml ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall("acme.tool"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(binDir, "tool")); !os.IsNotExist(err) {
		t.Fatal("bin link should be removed even with a broken manifest")
	}
	if _, err := os.Lstat(filepath.Join(root, "acme.tool")); !os.IsNotExist(err) {
		t.Fatal("plugin entry should be gone")
	}
}
