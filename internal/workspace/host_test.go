package workspace

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/layout"
)

// hostSpawner records the host each spawn was asked for, so the tests can check
// what the *backend* would have been told — the thing that actually decides
// which machine a PTY lands on — rather than only the model's bookkeeping.
type hostSpawner struct{ hosts map[layout.PaneID]string }

func newHostSpawner() *hostSpawner { return &hostSpawner{hosts: map[layout.PaneID]string{}} }

func (h *hostSpawner) Spawn(spec SpawnSpec) (TerminalID, error) {
	h.hosts[spec.PaneID] = spec.HostID
	return TerminalID("term_" + EncodePublicNumber(int(spec.PaneID))), nil
}
func (h *hostSpawner) Despawn(TerminalID) {}

// A workspace's host is the default for every pane created in it — the root
// pane, later splits, and later tabs alike. That default is the whole point of
// the workspace-level field: one choice at creation puts everything that grows
// out of the workspace on the same machine.
func TestWorkspaceHostIsPaneDefault(t *testing.T) {
	sp := newHostSpawner()
	ws, err := New(sp, "/tmp/wsroot", SpawnSpec{HostID: "devbox"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ws.HostID != "devbox" {
		t.Fatalf("workspace host = %q, want devbox", ws.HostID)
	}
	root := ws.Tabs[0].RootPane

	split, err := ws.SplitFocused(layout.Horizontal, SpawnSpec{})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	tabIdx, err := ws.CreateTab("/tmp/tab2", SpawnSpec{})
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	tabRoot := ws.Tabs[tabIdx].RootPane

	for _, id := range []layout.PaneID{root, split.PaneID, tabRoot} {
		if got := sp.hosts[id]; got != "devbox" {
			t.Errorf("pane %d spawned on host %q, want devbox", id, got)
		}
		st, ok := ws.PaneStateFor(id)
		if !ok || st.HostID != "devbox" {
			t.Errorf("pane %d state host = %+v, want devbox", id, st)
		}
	}
}

// The workspace default must not overwrite a caller's explicit choice: putting
// one pane on another machine is the finer-grained half of the same feature.
func TestExplicitPaneHostBeatsWorkspaceDefault(t *testing.T) {
	sp := newHostSpawner()
	ws, err := New(sp, "/tmp/wsroot", SpawnSpec{HostID: "devbox"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	np, err := ws.SplitFocused(layout.Horizontal, SpawnSpec{HostID: "buildbox"})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if got := sp.hosts[np.PaneID]; got != "buildbox" {
		t.Fatalf("split spawned on %q, want buildbox", got)
	}
	if st, _ := ws.PaneStateFor(np.PaneID); st.HostID != "buildbox" {
		t.Fatalf("split pane state host = %q, want buildbox", st.HostID)
	}
	if ws.HostID != "devbox" {
		t.Fatalf("a per-pane host must not move the workspace, got %q", ws.HostID)
	}
}

// A split lands beside the pane it split, not on the workspace's default. This
// is the difference between "another terminal here" and "another terminal
// somewhere else that happens to share a layout": a guest pane's split must be
// able to see the same filesystem, or the inherited cwd is meaningless and the
// two panes only look adjacent.
func TestSplitInheritsTheSplitPanesHost(t *testing.T) {
	sp := newHostSpawner()
	ws, err := New(sp, "/tmp/wsroot", SpawnSpec{}) // unpinned: default-host workspace
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	root := ws.Tabs[0].RootPane

	if _, _, err := ws.SplitPane(root, layout.Horizontal, true, SpawnSpec{HostID: "devbox"}); err != nil {
		t.Fatalf("split onto devbox: %v", err)
	}

	// Splitting the guest with no host named: devbox, not the workspace's "".
	beside, err := ws.SplitFocused(layout.Vertical, SpawnSpec{})
	if err != nil {
		t.Fatalf("split of the guest: %v", err)
	}
	if got := sp.hosts[beside.PaneID]; got != "devbox" {
		t.Errorf("split of a guest pane spawned on %q, want devbox", got)
	}
	if st, _ := ws.PaneStateFor(beside.PaneID); st.HostID != "devbox" {
		t.Errorf("split of a guest pane state host = %q, want devbox", st.HostID)
	}

	// Splitting the root the other way still gives the default host, so the
	// rule tracks the source pane rather than the last host used.
	_, local, err := ws.SplitPane(root, layout.Vertical, false, SpawnSpec{})
	if err != nil {
		t.Fatalf("split of the root: %v", err)
	}
	if got := sp.hosts[local.PaneID]; got != "" {
		t.Errorf("split of a default-host pane spawned on %q, want the default", got)
	}

	// A workspace pinned elsewhere does not override the neighbour either: the
	// workspace default is for tabs and new workspaces, which have no
	// neighbouring pane to ask.
	pinned, err := New(sp, "/tmp/pinned", SpawnSpec{HostID: "buildbox"})
	if err != nil {
		t.Fatalf("New pinned: %v", err)
	}
	pinnedRoot := pinned.Tabs[0].RootPane
	_, guest2, err := pinned.SplitPane(pinnedRoot, layout.Horizontal, true, SpawnSpec{HostID: "devbox"})
	if err != nil {
		t.Fatalf("split onto devbox: %v", err)
	}
	_, beside2, err := pinned.SplitPane(guest2.PaneID, layout.Vertical, true, SpawnSpec{})
	if err != nil {
		t.Fatalf("split of the guest: %v", err)
	}
	if got := sp.hosts[beside2.PaneID]; got != "devbox" {
		t.Errorf("split of a guest in a pinned workspace spawned on %q, want devbox", got)
	}

	// And a new TAB in that workspace still takes the workspace's host — the
	// two rules are meant to differ.
	tabIdx, err := pinned.CreateTab("/tmp/pinned", SpawnSpec{})
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if got := sp.hosts[pinned.Tabs[tabIdx].RootPane]; got != "buildbox" {
		t.Errorf("new tab spawned on %q, want the workspace's buildbox", got)
	}
}

// Restore respawns each pane on the host it was recorded on — per pane, not per
// workspace, so the odd-one-out pane comes back where its process lived rather
// than being quietly migrated to the workspace's default.
func TestRestoreRespawnsOnRecordedHost(t *testing.T) {
	ws, err := New(newHostSpawner(), "/tmp/wsroot", SpawnSpec{HostID: "devbox"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	np, err := ws.SplitFocused(layout.Horizontal, SpawnSpec{HostID: "buildbox"})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	root := ws.Tabs[0].RootPane

	sp := newHostSpawner()
	restored, err := Restore(sp, ws.Snapshot())
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored.HostID != "devbox" {
		t.Fatalf("restored workspace host = %q, want devbox", restored.HostID)
	}
	if got := sp.hosts[root]; got != "devbox" {
		t.Errorf("root respawned on %q, want devbox", got)
	}
	if got := sp.hosts[np.PaneID]; got != "buildbox" {
		t.Errorf("split respawned on %q, want buildbox", got)
	}
}

// A single-host session names no host anywhere, so the snapshot must carry no
// "host" key at all — the additive-field rule that lets an old state file (and
// an old catway) keep working.
func TestSnapshotOmitsEmptyHost(t *testing.T) {
	ws, err := New(recordingSpawner(), "/tmp/wsroot", SpawnSpec{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := ws.SplitFocused(layout.Horizontal, SpawnSpec{}); err != nil {
		t.Fatalf("split: %v", err)
	}
	b, err := json.Marshal(ws.Snapshot())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"host"`) {
		t.Fatalf("host key emitted for a host-less workspace: %s", b)
	}
}

// An old snapshot (no host anywhere) restores every pane onto the empty host,
// which every backend reads as "the default one".
func TestRestoreOldSnapshotHasNoHost(t *testing.T) {
	ws, err := New(recordingSpawner(), "/tmp/wsroot", SpawnSpec{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sp := newHostSpawner()
	restored, err := Restore(sp, ws.Snapshot())
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored.HostID != "" {
		t.Fatalf("restored host = %q, want empty", restored.HostID)
	}
	for id, host := range sp.hosts {
		if host != "" {
			t.Fatalf("pane %d respawned on %q, want the default host", id, host)
		}
	}
}
