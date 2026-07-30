package pathpick

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExpand(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	t.Setenv("PATHPICK_TEST_DIR", "/opt/x")
	base := "/base/dir"
	for _, tc := range []struct{ in, want string }{
		{"", base},   // empty is "here"
		{"  ", base}, // ... whitespace included
		{"~", home},  // bare tilde
		{"~/projs/", filepath.Join(home, "projs")},
		{"/usr/lo", "/usr/lo"},             // a half-typed absolute path survives intact
		{"sub/deep", "/base/dir/sub/deep"}, // relative resolves against base
		{"./sub/", "/base/dir/sub"},
		{"..", "/base"},
		{"$PATHPICK_TEST_DIR/y", "/opt/x/y"}, // env references expand
	} {
		if got := Expand(tc.in, base); got != tc.want {
			t.Errorf("Expand(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// With no base to resolve against, a relative path still comes back cleaned
// rather than joined onto the process cwd (which is nobody's "here").
func TestExpandNoBase(t *testing.T) {
	if got := Expand("sub/x", ""); got != "sub/x" {
		t.Errorf("Expand with no base = %q", got)
	}
}

func TestSubdirs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"beta", "alpha", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink to a directory is a directory for picking purposes; one to a
	// file is not.
	if err := os.Symlink(filepath.Join(root, "alpha"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "file.txt"), filepath.Join(root, "flink")); err != nil {
		t.Fatal(err)
	}

	names, truncated, err := Subdirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("small dir reported truncated")
	}
	// Hidden names are kept — the front-end decides when to show them.
	want := []string{".hidden", "alpha", "beta", "link"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("Subdirs = %v, want %v", names, want)
	}
}

// A missing or non-directory path is an error, not an empty listing: the caller
// distinguishes "nothing here" from "that isn't a directory".
func TestSubdirsBadPath(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(root, "nope"), file} {
		if _, _, err := Subdirs(p); err == nil {
			t.Errorf("Subdirs(%q) succeeded, want error", p)
		}
	}
}

// writeState writes a cdx-shaped state file with the given entries.
func writeState(t *testing.T, ents []entry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	data, err := json.Marshal(map[string]any{"entries": ents, "stack": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Recents come back in cdx's frecency order — recency outweighs a raw count —
// with vanished directories pruned and the limit honoured.
func TestRecents(t *testing.T) {
	const now = 1_000_000
	root := t.TempDir()
	live := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	hot, warm, cold := live("hot"), live("warm"), live("cold")
	gone := filepath.Join(root, "deleted")

	path := writeState(t, []entry{
		{Path: cold, Count: 40, Last: now - 30*86400}, // many visits, long ago: w=0.25 → 10
		{Path: warm, Count: 8, Last: now - 2*86400},   // last week-ish: w=0.5 → 4
		{Path: hot, Count: 5, Last: now - 60},         // within the hour: w=4 → 20
		{Path: gone, Count: 99, Last: now},            // top score, but no longer exists
		{Path: "relative/path", Count: 99, Last: now}, // not absolute: ignored
	})

	got := recents(path, now, 10)
	want := []string{hot, cold, warm}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recents = %v, want %v", got, want)
	}
	if got := recents(path, now, 2); !reflect.DeepEqual(got, want[:2]) {
		t.Errorf("recents(limit 2) = %v, want %v", got, want[:2])
	}
}

// No cdx on the machine, or a state file it cannot parse, means no recents —
// never an error the picker has to render.
func TestRecentsMissingOrCorrupt(t *testing.T) {
	dir := t.TempDir()
	if got := recents(filepath.Join(dir, "absent.json"), 100, 10); got != nil {
		t.Errorf("missing state = %v, want nil", got)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := recents(bad, 100, 10); got != nil {
		t.Errorf("corrupt state = %v, want nil", got)
	}
}
