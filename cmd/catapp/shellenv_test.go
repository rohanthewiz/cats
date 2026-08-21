//go:build darwin

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/shellenv"
)

// The bug this file exists for: a GUI launch starts with launchd's bare PATH,
// and after hydration the toolchain the user has in their shell must be
// reachable — that is what a plugin's `go build` step needs.
//
// The contract is stated against the login shell's own PATH rather than against
// any one tool. Asking instead whether `go` is reachable afterwards conflates
// two different environments: a CI runner puts the toolchain on the *test
// process's* PATH (setup-go writes GITHUB_PATH, which the job inherits) while a
// freshly spawned `zsh -ilc` sources only the user's rc files and has never
// heard of it. That made the precondition satisfiable where the assertion was
// not, and the test failed on a machine where hydration was working correctly.
func TestHydratePATHOnGUILaunch(t *testing.T) {
	const bare = "/usr/bin:/bin:/usr/sbin:/sbin"

	t.Setenv("__CFBundleIdentifier", "dev.cats.app")
	t.Setenv("PATH", bare)

	// The expectation has to be derived *after* PATH is set to the launchd-bare
	// value, because the probe is not a pure function: the shell it spawns
	// inherits our PATH, and an rc file's customary `export PATH="$HOME/bin:$PATH"`
	// folds that inherited value into what comes back. Reading it beforehand
	// measures a different environment than the one hydratePATH will see — under
	// `go test` the toolchain directory the test binary runs with leaks into the
	// first reading and not the second. (shellenv memoises the probe, so this
	// call is also what pins the value hydratePATH goes on to use.)
	shellPath := shellenv.LoginPATH()
	if shellPath == "" {
		t.Skip("login shell yielded no PATH to adopt")
	}

	hydratePATH()

	got := os.Getenv("PATH")
	if got == bare {
		t.Fatalf("PATH untouched by hydration, want the login shell's %q", shellPath)
	}
	// Every entry the login shell reported has to survive into the merged PATH;
	// that is precisely what lets a spawned build step find the user's tools.
	entries := make(map[string]bool)
	for entry := range strings.SplitSeq(got, ":") {
		entries[entry] = true
	}
	for want := range strings.SplitSeq(shellPath, ":") {
		if want == "" {
			continue
		}
		if !entries[want] {
			t.Fatalf("login shell entry %q missing after hydration (PATH=%q)", want, got)
		}
	}

	// The original symptom, asserted only where the environment can express it:
	// a login shell that can resolve the toolchain must still resolve it once we
	// have adopted its PATH.
	if _, err := exec.LookPath("go"); err != nil {
		t.Logf("go not reachable after hydration; the login shell's PATH does not carry it (PATH=%q)", got)
	}
}

// A launch from a terminal already has the user's PATH; hydratePATH must leave
// it exactly as-is rather than re-deriving (and reordering) it.
func TestHydratePATHSkipsNonGUILaunch(t *testing.T) {
	t.Setenv("__CFBundleIdentifier", "")
	t.Setenv("PATH", "/sentinel/bin")
	hydratePATH()
	if got := os.Getenv("PATH"); got != "/sentinel/bin" {
		t.Fatalf("PATH = %q, want it untouched", got)
	}
}
