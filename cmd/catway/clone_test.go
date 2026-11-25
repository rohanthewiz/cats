//go:build ghostty

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
)

// workspace.create with a repo: the workspace appears only after the clone has
// landed, rooted in the checkout and carrying the requested name — and a clone
// that fails leaves the session exactly as it was.
func TestWorkspaceClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	for _, args := range [][]string{
		{"init", "-q", src},
		{"-C", src, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	o, err := newOrch("", root)
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	go o.run()
	run := func(c app.WorkspaceClone) *syncedResponder {
		t.Helper()
		r := &syncedResponder{done: make(chan struct{})}
		syncPost(o, func() { o.StartWorkspaceClone(r, c) })
		select {
		case <-r.done:
		case <-time.After(30 * time.Second):
			t.Fatal("clone never resolved")
		}
		return r
	}
	count := func() (n int) {
		syncPost(o, func() { n = len(o.session.Workspaces()) })
		return n
	}
	before := count()

	dest := filepath.Join(root, "work", "thing")
	r := run(app.WorkspaceClone{URL: src, Dest: dest, Name: "the thing"})
	if !r.ok {
		t.Fatalf("clone failed: %s", r.errMsg)
	}
	id := r.data.(app.WorkspaceCreateResult).ID
	var name, cwd string
	syncPost(o, func() {
		for _, ws := range o.session.Workspaces() {
			if ws.ID == id {
				name, cwd = ws.CustomName, ws.IdentityCwd
			}
		}
	})
	if cwd != dest || name != "the thing" {
		t.Fatalf("workspace %s: cwd=%q name=%q, want %q / %q", id, cwd, name, dest, "the thing")
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Fatalf("no checkout at %s: %v", dest, err)
	}

	// Cloning onto the checkout that now exists fails, and opens nothing.
	r = run(app.WorkspaceClone{URL: src, Dest: dest})
	if !r.fail || r.errMsg != "already exists: "+dest {
		t.Fatalf("second clone: fail=%v err=%q", r.fail, r.errMsg)
	}
	if got := count(); got != before+1 {
		t.Fatalf("workspaces = %d, want %d: a failed clone must not open one", got, before+1)
	}
}
