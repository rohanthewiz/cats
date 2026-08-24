package shellenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A login shell's PATH arrives wrapped in whatever an interactive rc file
// printed; only the fenced value may survive.
func TestBetween(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare", Marker + "/usr/bin:/bin" + Marker, "/usr/bin:/bin"},
		{"rc noise around it", "Sourcing custom configs...\n" + Marker + "/opt/go/bin" + Marker + "\n$ ", "/opt/go/bin"},
		{"no markers", "command not found", ""},
		{"unterminated", Marker + "/usr/bin", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Between(tc.in, Marker); got != tc.want {
				t.Fatalf("Between = %q, want %q", got, tc.want)
			}
		})
	}
}

// pinCatsBin points the cats bin dir somewhere the test controls. Every Merge
// assertion needs it: without the pin the result depends on whether the real
// ~/.cats/bin happens to exist on the machine running the tests.
func pinCatsBin(t *testing.T, dir string) {
	t.Helper()
	t.Setenv(CatsBinEnvVar, dir)
}

// The shell's PATH wins on order, but an inherited-only entry must not be lost.
func TestMerge(t *testing.T) {
	pinCatsBin(t, filepath.Join(t.TempDir(), "absent"))
	got := Merge("/opt/go/bin:/usr/bin:/bin", "/usr/bin:/bin:/managed/only:")
	want := "/opt/go/bin:/usr/bin:/bin:/managed/only"
	if got != want {
		t.Fatalf("Merge = %q, want %q", got, want)
	}
}

// An existing cats bin dir leads the merged PATH; a missing one contributes
// nothing — its existence is the feature switch.
func TestMergeCatsBin(t *testing.T) {
	bin := t.TempDir()
	pinCatsBin(t, bin)
	if got, want := Merge("/usr/bin:/bin", "/bin"), bin+":/usr/bin:/bin"; got != want {
		t.Fatalf("Merge with cats bin = %q, want %q", got, want)
	}
	// Already listed by the login shell: the dedupe keeps one copy, in front.
	if got, want := Merge("/usr/bin:"+bin, "/bin"), bin+":/usr/bin:/bin"; got != want {
		t.Fatalf("Merge with cats bin already present = %q, want %q", got, want)
	}
	pinCatsBin(t, filepath.Join(bin, "absent"))
	if got, want := Merge("/usr/bin:/bin", "/bin"), "/usr/bin:/bin"; got != want {
		t.Fatalf("Merge with absent cats bin = %q, want %q", got, want)
	}
}

// The one subtle coupling Lookup must not have: a machine where the login-shell
// probe fails (login == "") still has to hand panes the cats bin dir, or plugin
// binaries vanish exactly where PATH help is scarcest.
func TestLookupInjectsCatsBinWithoutLoginPATH(t *testing.T) {
	bin := t.TempDir()
	pinCatsBin(t, bin)
	// Lookup consults LoginPATH(), which is memoised per process and may hold
	// the real login PATH — that is fine: the assertion is that the cats bin
	// dir is in envPATH, whatever else the merge picked up.
	_, envPATH := Lookup("cats-no-such-program-xyzzy")
	if !strings.Contains(":"+envPATH+":", ":"+bin+":") {
		t.Fatalf("envPATH %q does not contain cats bin %q", envPATH, bin)
	}
}

// The shell invocation itself: whatever the user's login shell is, the fenced
// PATH must come back parseable through real rc files.
func TestLoginPATH(t *testing.T) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("no usable login shell: %v", err)
	}
	got := LoginPATH()
	if got == "" {
		t.Fatalf("LoginPATH returned nothing for %s", shell)
	}
	if !strings.Contains(got, "/bin") {
		t.Fatalf("LoginPATH = %q, expected something PATH-shaped", got)
	}
}

// The heart of the daemon-side fix: a program that our own bare PATH cannot see
// still has to be found, because that is exactly the position cathost is in
// after a GUI launch. The login shell's PATH is simulated with a temp directory
// the rc files know nothing about — the assertion is about lookIn, which is the
// part Lookup contributes over exec.LookPath.
func TestLookInFindsWhatTheProcessPATHCannot(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "cats-fake-agent")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	if _, err := exec.LookPath("cats-fake-agent"); err == nil {
		t.Fatal("precondition: the bare PATH must not resolve the fake agent")
	}

	got, err := lookIn(dir, "cats-fake-agent")
	if err != nil {
		t.Fatalf("lookIn: %v", err)
	}
	if got != prog {
		t.Fatalf("lookIn = %q, want %q", got, prog)
	}
	// The swap is temporary: the process PATH must be exactly as we left it, or
	// every later spawn inherits a search list it never asked for.
	if p := os.Getenv("PATH"); p != "/usr/bin:/bin:/usr/sbin:/sbin" {
		t.Fatalf("PATH left as %q after lookIn", p)
	}
}

// A name that is already a path is a decision the caller made; Lookup passes it
// through rather than searching for something with the same base name.
func TestLookupPassesPathsThrough(t *testing.T) {
	got, _ := Lookup("/opt/agents/claude")
	if got != "/opt/agents/claude" {
		t.Fatalf("Lookup = %q, want the path unchanged", got)
	}
}

// A resolvable name comes back absolute, so the child's own PATH — which we may
// be replacing — cannot change which binary gets exec'd.
func TestLookupAbsolutisesAResolvableName(t *testing.T) {
	got, _ := Lookup("sh")
	if !filepath.IsAbs(got) {
		t.Fatalf("Lookup(\"sh\") = %q, want an absolute path", got)
	}
}
