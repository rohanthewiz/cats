//go:build darwin

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A login shell's PATH arrives wrapped in whatever an interactive rc file
// printed; only the fenced value may survive.
func TestBetweenMarkers(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare", shellEnvMarker + "/usr/bin:/bin" + shellEnvMarker, "/usr/bin:/bin"},
		{"rc noise around it", "Sourcing custom configs...\n" + shellEnvMarker + "/opt/go/bin" + shellEnvMarker + "\n$ ", "/opt/go/bin"},
		{"no markers", "command not found", ""},
		{"unterminated", shellEnvMarker + "/usr/bin", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := betweenMarkers(tc.in, shellEnvMarker); got != tc.want {
				t.Fatalf("betweenMarkers = %q, want %q", got, tc.want)
			}
		})
	}
}

// The shell's PATH wins on order, but an inherited-only entry must not be lost.
func TestMergePATH(t *testing.T) {
	got := mergePATH("/opt/go/bin:/usr/bin:/bin", "/usr/bin:/bin:/managed/only:")
	want := "/opt/go/bin:/usr/bin:/bin:/managed/only"
	if got != want {
		t.Fatalf("mergePATH = %q, want %q", got, want)
	}
}

// The shell invocation itself: whatever the user's login shell is, the fenced
// PATH must come back parseable through real rc files.
func TestLoginShellPATH(t *testing.T) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("no usable login shell: %v", err)
	}
	got := loginShellPATH()
	if got == "" {
		t.Fatalf("loginShellPATH returned nothing for %s", shell)
	}
	if !strings.Contains(got, "/bin") {
		t.Fatalf("loginShellPATH = %q, expected something PATH-shaped", got)
	}
}

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
	// value, because loginShellPATH is not a pure function: the shell it spawns
	// inherits our PATH, and an rc file's customary `export PATH="$HOME/bin:$PATH"`
	// folds that inherited value into what comes back. Reading it beforehand
	// measures a different environment than the one hydratePATH will see — under
	// `go test` the toolchain directory the test binary runs with leaks into the
	// first reading and not the second.
	shellPath := loginShellPATH()
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
