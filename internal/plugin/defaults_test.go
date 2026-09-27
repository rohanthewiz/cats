package plugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seedRepo builds a local git repo holding a plugin whose build step records
// the invoking-directory and headless env vars, so a test can check how the
// build was run.
func seedRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := writePlugin(t, `
id = "acme.seed"
name = "Seed"
version = "1.2.3"

[[build]]
command = ["sh", "-c", "printf '%s' \"$CATS_PLUGIN_INSTALL_CWD\" > .install-cwd; printf '%s' \"$CATS_PLUGIN_BUILD_HEADLESS\" > .headless"]
`)
	git := gitIn(t, repo)
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-q", "-m", "plugin")
	return repo
}

func readSeedState(t *testing.T, root string) defaultsState {
	t.Helper()
	st, err := readDefaultsState(filepath.Join(root, defaultsStateName))
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	return st
}

// A fresh machine (no plugins root) gets the defaults installed once. After
// the user uninstalls one, later starts leave it uninstalled.
func TestSeedDefaultsFreshInstall(t *testing.T) {
	t.Setenv(NoDefaultsEnvVar, "")
	root := testRoot(t)
	defs := []DefaultPlugin{{ID: "acme.seed", Source: seedRepo(t)}}

	outs, err := SeedDefaults(defs, nil)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(outs) != 1 || !outs[0].Installed || outs[0].Version != "1.2.3" {
		t.Fatalf("outcomes = %+v, want one install of v1.2.3", outs)
	}
	if st := readSeedState(t, root); len(st.Pending) != 0 {
		t.Fatalf("pending after success = %+v, want none", st.Pending)
	}
	// Headless: the build step saw no invoking directory, and was told so.
	if b, err := os.ReadFile(filepath.Join(root, "acme.seed", ".install-cwd")); err != nil || len(b) != 0 {
		t.Fatalf("install cwd seen by build = %q (%v), want empty", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "acme.seed", ".headless")); err != nil || string(b) != "1" {
		t.Fatalf("%s seen by build = %q (%v), want 1", HeadlessEnvVar, b, err)
	}
	// The state file stays out of the plugin listing.
	if ps, _ := List(); len(ps) != 1 || ps[0].ID != "acme.seed" {
		t.Fatalf("List = %+v, want just acme.seed", ps)
	}

	if _, err := Uninstall("acme.seed"); err != nil {
		t.Fatal(err)
	}
	outs, err = SeedDefaults(defs, nil)
	if err != nil || outs != nil {
		t.Fatalf("second pass = %+v, %v; want nothing done", outs, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "acme.seed")); err == nil {
		t.Fatal("an uninstalled default came back")
	}
}

// A machine that already has a plugins root is an existing install: nothing
// is seeded, and the verdict is recorded so it never changes.
func TestSeedDefaultsExistingInstall(t *testing.T) {
	t.Setenv(NoDefaultsEnvVar, "")
	root := testRoot(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	defs := []DefaultPlugin{{ID: "acme.seed", Source: "/nonexistent/never-cloned"}}

	outs, err := SeedDefaults(defs, nil)
	if err != nil || outs != nil {
		t.Fatalf("seed = %+v, %v; want nothing done", outs, err)
	}
	if st := readSeedState(t, root); len(st.Pending) != 0 {
		t.Fatalf("pending = %+v, want none", st.Pending)
	}
}

// The opt-out does nothing at all: no install, and no state file, so removing
// the opt-out later still seeds a machine that is otherwise fresh.
func TestSeedDefaultsOptOut(t *testing.T) {
	root := testRoot(t)
	t.Setenv(NoDefaultsEnvVar, "1")

	outs, err := SeedDefaults([]DefaultPlugin{{ID: "acme.seed", Source: "/nonexistent"}}, nil)
	if err != nil || outs != nil {
		t.Fatalf("seed = %+v, %v; want nothing done", outs, err)
	}
	if _, err := os.Lstat(root); err == nil {
		t.Fatal("opt-out still created the plugins root")
	}
}

// A failing default is retried on later passes, then dropped after
// maxSeedAttempts, so an unbuildable plugin does not clone on every start.
func TestSeedDefaultsRetriesThenGivesUp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv(NoDefaultsEnvVar, "")
	root := testRoot(t)
	defs := []DefaultPlugin{{ID: "acme.gone", Source: filepath.Join(t.TempDir(), "no-such-repo")}}

	for attempt := 1; attempt <= maxSeedAttempts; attempt++ {
		outs, err := SeedDefaults(defs, nil)
		if err != nil {
			t.Fatalf("pass %d: %v", attempt, err)
		}
		if len(outs) != 1 || outs[0].Err == nil || outs[0].Attempt != attempt {
			t.Fatalf("pass %d outcomes = %+v, want failed attempt %d", attempt, outs, attempt)
		}
		last := attempt == maxSeedAttempts
		if outs[0].GaveUp != last {
			t.Fatalf("pass %d GaveUp = %v, want %v", attempt, outs[0].GaveUp, last)
		}
		st := readSeedState(t, root)
		if last && len(st.Pending) != 0 {
			t.Fatalf("still pending after giving up: %+v", st.Pending)
		}
		if !last && (len(st.Pending) != 1 || !strings.Contains(st.Pending[0].LastError, "clone")) {
			t.Fatalf("pass %d pending = %+v, want one entry with the clone error", attempt, st.Pending)
		}
	}
	if outs, _ := SeedDefaults(defs, nil); outs != nil {
		t.Fatalf("pass after giving up = %+v, want nothing done", outs)
	}
}

// A pending default that turns up installed by other means (by hand, or by a
// peer sync) is taken as done, with no clone.
func TestSeedDefaultsAlreadyPresent(t *testing.T) {
	t.Setenv(NoDefaultsEnvVar, "")
	root := testRoot(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeDefaultsState(filepath.Join(root, defaultsStateName), defaultsState{
		Pending: []pendingDefault{{DefaultPlugin: DefaultPlugin{ID: "acme.demo", Source: "/nonexistent"}, Attempts: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Link(writePlugin(t, validManifest), nil); err != nil {
		t.Fatalf("link: %v", err)
	}

	outs, err := SeedDefaults(nil, nil)
	if err != nil || len(outs) != 1 || !outs[0].Present {
		t.Fatalf("seed = %+v, %v; want one Present outcome", outs, err)
	}
	if st := readSeedState(t, root); len(st.Pending) != 0 {
		t.Fatalf("pending = %+v, want none", st.Pending)
	}
}

// A plain Install is not headless: its build sees the invoking directory and
// no HeadlessEnvVar, so a one-time step still runs for a person.
func TestInstallIsNotHeadless(t *testing.T) {
	t.Setenv(HeadlessEnvVar, "") // a stray value in the test env would mask the check
	root := testRoot(t)
	if _, err := Install(seedRepo(t), "", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "acme.seed", ".headless")); err != nil || len(b) != 0 {
		t.Fatalf("%s seen by build = %q (%v), want empty", HeadlessEnvVar, b, err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "acme.seed", ".install-cwd")); err != nil || len(b) == 0 {
		t.Fatalf("install cwd seen by build = %q (%v), want the host's directory", b, err)
	}
}
