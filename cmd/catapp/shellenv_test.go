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
func TestHydratePATHOnGUILaunch(t *testing.T) {
	toolchain, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go on the test PATH to look for: %v", err)
	}
	t.Setenv("__CFBundleIdentifier", "dev.cats.app")
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")

	hydratePATH()

	found, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go still not on PATH after hydration (PATH=%q): %v", os.Getenv("PATH"), err)
	}
	if found != toolchain {
		t.Logf("resolved %q, shell PATH prefers it over %q", found, toolchain)
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
