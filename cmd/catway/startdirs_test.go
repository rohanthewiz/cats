//go:build ghostty

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/config"
)

// localOnlyHosts is the roster a session with no hosts: block has — one local,
// default host — which is what the restore-time repairs saw before hosts
// existed at all. Every heal test that is not about remote panes uses it.
func localOnlyHosts() []config.Host { return config.EffectiveHosts("/tmp/cathost.sock", nil) }

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

	healStartDirs(sess, root, localOnlyHosts())

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

	healStartDirs(sess, "/", localOnlyHosts()) // GUI launch: nothing usable from the launcher either

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

	sess, err := app.NewSession(modelSpawner{}, home)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	cwds := map[uint32]string{1: "/", 2: home, 3: filepath.Join(home, "gone")}
	got := healPaneCwds(cwds, sess, localOnlyHosts())

	if len(got) != 1 || got[2] != home {
		t.Fatalf("healed pane cwds = %v, want only pane 2 at %q", got, home)
	}
}

// A remote pane's saved cwd names a directory on the machine its PTY lives on.
// Every question healPaneCwds asks — does it exist, is it a directory — would be
// asked of this machine's disk instead, and the answer for a path like
// /home/dev/proj on a laptop is "no", every single restart. So the repair skips
// remote panes entirely: their cwds round-trip untouched, and the pane respawns
// where it was.
func TestHealPaneCwdsSkipsRemotePanes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sess, err := app.NewSession(modelSpawner{}, home)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// A workspace on another host, so its root pane resolves as remote.
	if _, err := sess.CreateWorkspaceAtOn(home, testRemoteHost); err != nil {
		t.Fatalf("CreateWorkspaceAtOn: %v", err)
	}
	remote := uint32(sess.ActiveWorkspace().Tabs[0].RootPane)
	local := uint32(sess.Workspaces()[0].Tabs[0].RootPane)

	gone := filepath.Join(home, "gone") // unusable *here*, real over there
	cwds := map[uint32]string{local: gone, remote: gone}
	got := healPaneCwds(cwds, sess, twoHostConfig())

	if _, ok := got[local]; ok {
		t.Fatalf("the local pane's unusable cwd should have been dropped: %v", got)
	}
	if got[remote] != gone {
		t.Fatalf("remote pane cwd = %q, want it untouched (%q)", got[remote], gone)
	}
}

// The workspace half of the same rule: a workspace pinned to a remote host keeps
// its identity cwd exactly as saved, while a local one is still repaired.
func TestHealStartDirsSkipsRemoteWorkspaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sess, err := app.NewSession(modelSpawner{}, home)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.CreateWorkspaceAtOn("/srv/build", testRemoteHost); err != nil {
		t.Fatalf("CreateWorkspaceAtOn: %v", err)
	}
	if _, err := sess.CreateWorkspaceAt("/"); err != nil { // local, and unusable
		t.Fatalf("CreateWorkspaceAt: %v", err)
	}

	healStartDirs(sess, home, twoHostConfig())

	if got := sess.Workspaces()[1].IdentityCwd; got != "/srv/build" {
		t.Fatalf("remote workspace cwd = %q, want it untouched", got)
	}
	if got := sess.Workspaces()[2].IdentityCwd; got != home {
		t.Fatalf("local workspace cwd = %q, want it healed to %q", got, home)
	}
}

// twoHostConfig is localOnlyHosts plus the remote host the multi-host tests use,
// so the heal repairs see the same roster the orchestrator would.
func twoHostConfig() []config.Host {
	return config.EffectiveHosts("/tmp/cathost.sock", []config.Host{
		{ID: testRemoteHost, Addr: "unix:///tmp/remote.sock"},
	})
}
