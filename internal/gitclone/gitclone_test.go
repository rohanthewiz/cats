package gitclone

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	ok := []struct{ spec, url, name string }{
		{"rohanthewiz/cdx", "https://github.com/rohanthewiz/cdx.git", "cdx"},
		{"  rohanthewiz/cdx  ", "https://github.com/rohanthewiz/cdx.git", "cdx"},
		{"rohanthewiz/cdx.git", "https://github.com/rohanthewiz/cdx.git", "cdx"},
		{"rohanthewiz/cdx/", "https://github.com/rohanthewiz/cdx.git", "cdx"},
		{"github.com/rohanthewiz/cdx", "https://github.com/rohanthewiz/cdx.git", "cdx"},
		{"https://github.com/rohanthewiz/cdx", "https://github.com/rohanthewiz/cdx", "cdx"},
		{"https://gitlab.com/group/sub/proj.git", "https://gitlab.com/group/sub/proj.git", "proj"},
		{"git@github.com:rohanthewiz/cdx.git", "git@github.com:rohanthewiz/cdx.git", "cdx"},
		{"devbox:repo.git", "devbox:repo.git", "repo"},
		{"ssh://git@host:2222/team/thing.git/", "ssh://git@host:2222/team/thing.git/", "thing"},
		{"/srv/git/thing.git", "/srv/git/thing.git", "thing"},
		{"./local/repo", "./local/repo", "repo"},
	}
	for _, c := range ok {
		url, name, err := Resolve(c.spec)
		if err != nil || url != c.url || name != c.name {
			t.Errorf("Resolve(%q) = %q, %q, %v; want %q, %q", c.spec, url, name, err, c.url, c.name)
		}
	}
	for _, spec := range []string{"", "   ", "justaname", "a/b/c", "--upload-pack=x", "a b/c", "owner/", "/", ".."} {
		if url, name, err := Resolve(spec); err == nil {
			t.Errorf("Resolve(%q) = %q, %q; want an error", spec, url, name)
		}
	}
}

func TestClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// Keep the user's and the machine's git config out of the test.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", src, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(src, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "README")
	git("commit", "-q", "-m", "init")

	// Parents are created; the checkout lands at dest.
	dest := filepath.Join(root, "new", "parent", "src")
	if err := Clone(context.Background(), src, dest); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README")); err != nil {
		t.Fatalf("checkout missing its file: %v", err)
	}

	// A destination that is already something is refused, and left alone.
	err := Clone(context.Background(), src, dest)
	if err == nil || !strings.HasPrefix(err.Error(), "already exists: ") {
		t.Fatalf("clone over an existing checkout: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README")); err != nil {
		t.Fatalf("existing checkout was disturbed: %v", err)
	}

	// A failed clone leaves no directory behind.
	bad := filepath.Join(root, "bad")
	if err := Clone(context.Background(), filepath.Join(root, "no-such-repo"), bad); err == nil {
		t.Fatal("clone of a missing repo succeeded")
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatalf("failed clone left %s behind (stat err = %v)", bad, err)
	}
}
