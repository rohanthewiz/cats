package startdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Usable walks its candidates in order and never answers "/" or a directory
// that isn't there — the two ways a saved or inherited cwd goes bad.
func TestUsable(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	missing := filepath.Join(dir, "gone")
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		in   []string
		want string
	}{
		{"first usable wins", []string{dir, home}, dir},
		{"skips root", []string{"/", dir}, dir},
		{"skips empty", []string{"", dir}, dir},
		{"skips missing", []string{missing, dir}, dir},
		{"skips a file", []string{file, dir}, dir},
		{"falls back to home", []string{"/", missing}, home},
		{"no candidates is home", nil, home},
	} {
		if got := Usable(tc.in...); got != tc.want {
			t.Errorf("%s: Usable(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// Resolve expands what a user types and verifies it lands on a real directory.
func TestResolve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sub := filepath.Join(home, "proj")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, in, base, want string
	}{
		{"empty is no opinion", "", home, ""},
		{"absolute", sub, "", sub},
		{"tilde alone", "~", "", home},
		{"tilde prefix", "~/proj", "", sub},
		{"env var", "$HOME/proj", "", sub},
		{"relative to base", "proj", home, sub},
		{"trimmed and cleaned", "  ~/proj/../proj  ", "", sub},
		{"explicit root is honoured", "/", "", "/"},
	} {
		got, err := Resolve(tc.in, tc.base)
		if err != nil {
			t.Errorf("%s: Resolve(%q, %q): %v", tc.name, tc.in, tc.base, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: Resolve(%q, %q) = %q, want %q", tc.name, tc.in, tc.base, got, tc.want)
		}
	}
}

// A path that doesn't exist (or isn't a directory) is an error, not a silent
// fallback: the workspace must not open somewhere the user didn't ask for.
func TestResolveRejects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	file := filepath.Join(home, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Resolve(filepath.Join(home, "nope"), ""); err == nil ||
		!strings.Contains(err.Error(), "no such directory") {
		t.Errorf("missing directory: got %v, want a no-such-directory error", err)
	}
	if _, err := Resolve(file, ""); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("plain file: got %v, want a not-a-directory error", err)
	}
	if _, err := Resolve("proj", ""); err == nil {
		t.Error("relative path with no base: want an error")
	}
}
