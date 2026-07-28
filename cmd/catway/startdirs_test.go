//go:build ghostty

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
)

// A snapshot outlives the launch that wrote it, so a session first saved from a
// GUI launch carries "/" everywhere. healStartDirs must scrub that on restore —
// otherwise every workspace created afterwards is rooted at the filesystem root,
// forever, no matter how catway is started next.
func TestHealStartDirsReplacesRoot(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	sess, err := app.NewSession(modelSpawner{}, "/")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.CreateWorkspaceAt("/"); err != nil {
		t.Fatalf("CreateWorkspaceAt: %v", err)
	}

	healStartDirs(sess, root)

	if sess.Cwd() != root {
		t.Fatalf("session cwd = %q, want the launch directory %q", sess.Cwd(), root)
	}
	for _, ws := range sess.Workspaces() {
		if ws.IdentityCwd != root {
			t.Fatalf("workspace %s identity cwd = %q, want %q", ws.ID, ws.IdentityCwd, root)
		}
	}
	// And a new workspace now inherits the healed default.
	id, err := sess.CreateWorkspace()
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	for _, ws := range sess.Workspaces() {
		if ws.ID == id && ws.IdentityCwd != root {
			t.Fatalf("new workspace rooted at %q, want %q", ws.IdentityCwd, root)
		}
	}
}

// A directory that no longer exists is as unusable as "/", and when the launch
// directory is itself unusable the whole session falls back to $HOME. Workspaces
// on a still-valid checkout are left alone.
func TestHealStartDirsKeepsUsableDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	checkout := filepath.Join(home, "checkout")
	if err := os.Mkdir(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	sess, err := app.NewSession(modelSpawner{}, filepath.Join(home, "deleted"))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.CreateWorkspaceAt(checkout); err != nil {
		t.Fatalf("CreateWorkspaceAt: %v", err)
	}

	healStartDirs(sess, "/") // GUI launch: nothing usable from the launcher either

	if sess.Cwd() != home {
		t.Fatalf("session cwd = %q, want the home directory %q", sess.Cwd(), home)
	}
	if got := sess.Workspaces()[1].IdentityCwd; got != checkout {
		t.Fatalf("usable workspace cwd = %q, want it untouched (%q)", got, checkout)
	}
}

// Saved per-pane cwds get the same treatment: an unusable one is dropped so the
// pane respawns in its (healed) workspace directory instead of at "/".
func TestHealPaneCwds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwds := map[uint32]string{1: "/", 2: home, 3: filepath.Join(home, "gone")}
	got := healPaneCwds(cwds)

	if len(got) != 1 || got[2] != home {
		t.Fatalf("healed pane cwds = %v, want only pane 2 at %q", got, home)
	}
}
