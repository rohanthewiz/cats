//go:build ghostty

package main

import (
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/cats/internal/config"
)

// A sleeping workspace's placeholder gets no grid, so syncDaemon spawns
// nothing for it; waking it puts the placeholder back in the desired set.
func TestDesiredGridsSkipSleepingWorkspaces(t *testing.T) {
	dir := t.TempDir()
	o, err := newOrchHosts(config.EffectiveHosts(filepath.Join(dir, "local.sock"), nil), dir)
	if err != nil {
		t.Fatalf("newOrchHosts: %v", err)
	}
	id, err := o.session.CreateWorkspace()
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	ws := o.session.WorkspaceByID(id)
	if err := o.session.SleepWorkspace(id); err != nil {
		t.Fatalf("SleepWorkspace: %v", err)
	}
	placeholder, ok := ws.PlaceholderPane()
	if !ok {
		t.Fatal("no placeholder after sleep")
	}

	grids := o.desiredGrids()
	if _, has := grids[uint32(placeholder)]; has {
		t.Fatalf("sleeping workspace's placeholder %d has a desired grid", placeholder)
	}
	if len(grids) != 1 {
		t.Fatalf("desired grids = %v, want just the awake workspace's pane", grids)
	}
	// Nothing the daemon is not holding can be vouched for: the placeholder
	// is Unknown, which is what keeps a clean from closing it as "idle".
	if act := o.PaneActivity(uint32(placeholder)); act.Known {
		t.Fatalf("placeholder activity = %+v, want unknown", act)
	}

	if _, _, woke, err := o.session.WakeWorkspace(id); err != nil || !woke {
		t.Fatalf("WakeWorkspace: woke=%v err=%v", woke, err)
	}
	if _, has := o.desiredGrids()[uint32(placeholder)]; !has {
		t.Fatal("awake placeholder has no desired grid")
	}
}

// A window pinned to a workspace that goes to sleep resolves to the active
// workspace, as it would if the workspace had been closed under it.
func TestViewWSFallsThroughSleepingWorkspace(t *testing.T) {
	dir := t.TempDir()
	o, err := newOrchHosts(config.EffectiveHosts(filepath.Join(dir, "local.sock"), nil), dir)
	if err != nil {
		t.Fatalf("newOrchHosts: %v", err)
	}
	id, err := o.session.CreateWorkspace()
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	c := &client{}
	c.view.ws = id
	if got := o.viewWS(c); got != id {
		t.Fatalf("awake view resolves to %s, want %s", got, id)
	}
	if err := o.session.SleepWorkspace(id); err != nil {
		t.Fatalf("SleepWorkspace: %v", err)
	}
	if got, want := o.viewWS(c), o.session.ActiveWorkspaceID(); got != want || got == id {
		t.Fatalf("view on a sleeping workspace resolves to %s, want the active %s", got, want)
	}
}
