package workspace

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/layout"
)

// TestSleepReplacesLayoutWithPlaceholder: sleeping closes every pane and tab,
// hands their terminals back, and leaves exactly one placeholder pane numbered
// past everything the workspace has had.
func TestSleepReplacesLayoutWithPlaceholder(t *testing.T) {
	sp := &fakeSpawner{}
	ws, err := New(sp, "/cats-test/ws", SpawnSpec{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mustSplitFocused(t, ws, layout.Vertical)
	if _, err := ws.CreateTab("/cats-test/ws", SpawnSpec{}); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	// p1, p2 in tab 1; p3 in tab 2 — three terminals live.
	if got := len(sp.spawned); got != 3 {
		t.Fatalf("spawned before sleep = %d, want 3", got)
	}

	closed, err := ws.Sleep(SpawnSpec{})
	if err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if len(closed) != 3 {
		t.Fatalf("Sleep returned %d terminals, want 3", len(closed))
	}
	if !ws.Asleep {
		t.Fatal("workspace not marked asleep")
	}
	if len(ws.Tabs) != 1 || ws.Tabs[0].Layout.PaneCount() != 1 {
		t.Fatalf("sleeping workspace has %d tabs / panes, want one of each", len(ws.Tabs))
	}
	ph, ok := ws.PlaceholderPane()
	if !ok {
		t.Fatal("PlaceholderPane not reported")
	}
	// Numbers are never reused: the placeholder is p4 in tab 3.
	if n, _ := ws.PublicPaneNumber(ph); n != 4 {
		t.Fatalf("placeholder pane number = %d, want 4", n)
	}
	if ws.Tabs[0].Number != 3 {
		t.Fatalf("placeholder tab number = %d, want 3", ws.Tabs[0].Number)
	}
	if len(ws.PublicPaneNumbers) != 1 {
		t.Fatalf("stale pane numbers survive sleep: %v", ws.PublicPaneNumbers)
	}
	// The placeholder was spawned through the seam with the workspace's cwd
	// and the public handle the backend keys its env on.
	last := sp.spawned[len(sp.spawned)-1]
	if last.Cwd != "/cats-test/ws" || last.PublicPaneID != ws.ID+":p4" {
		t.Fatalf("placeholder spawn spec = %+v", last)
	}

	// Sleeping again is a no-op.
	again, err := ws.Sleep(SpawnSpec{})
	if err != nil || again != nil {
		t.Fatalf("second Sleep = %v, %v; want nil, nil", again, err)
	}
}

// TestWakeReturnsParkedAgentsOnce: wake clears the flag and hands the parked
// refs back exactly once; a second wake is a no-op with nothing to resume.
func TestWakeReturnsParkedAgentsOnce(t *testing.T) {
	ws := testWorkspace(t, "ws")
	ref := ParkedAgent{Source: "hook", Agent: "claude", Kind: "id", Value: "abc", Pane: "w1:p1"}
	if !ws.ParkAgent(ref) {
		t.Fatal("first ParkAgent refused")
	}
	if ws.ParkAgent(ref) {
		t.Fatal("duplicate ParkAgent accepted")
	}
	if _, err := ws.Sleep(SpawnSpec{}); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if _, ok := ws.PlaceholderPane(); !ok {
		t.Fatal("no placeholder while asleep")
	}

	parked, woke := ws.Wake()
	if !woke || ws.Asleep {
		t.Fatalf("Wake: woke=%v asleep=%v", woke, ws.Asleep)
	}
	if len(parked) != 1 || parked[0] != ref {
		t.Fatalf("parked = %+v, want [%+v]", parked, ref)
	}
	if ws.ParkedAgents != nil {
		t.Fatalf("parked refs survive wake: %+v", ws.ParkedAgents)
	}
	if _, ok := ws.PlaceholderPane(); ok {
		t.Fatal("awake workspace reports a placeholder")
	}
	if p, woke := ws.Wake(); woke || p != nil {
		t.Fatalf("second Wake = %v, %v; want nil, false", p, woke)
	}
}

// TestSleepSnapshotRoundTrip: the sleep flag and parked refs survive
// Snapshot/Restore, and an awake workspace with nothing parked serializes
// without either key (old files stay byte-identical).
func TestSleepSnapshotRoundTrip(t *testing.T) {
	ws := testWorkspace(t, "ws")
	ws.ParkAgent(ParkedAgent{Source: "hook", Agent: "codex", Kind: "id", Value: "s1"})
	if _, err := ws.Sleep(SpawnSpec{}); err != nil {
		t.Fatalf("Sleep: %v", err)
	}

	snap := ws.Snapshot()
	restored, err := Restore(&fakeSpawner{}, snap)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !restored.Asleep {
		t.Fatal("restored workspace is not asleep")
	}
	if len(restored.ParkedAgents) != 1 || restored.ParkedAgents[0].Value != "s1" {
		t.Fatalf("restored parked = %+v", restored.ParkedAgents)
	}
	// The snapshot's slice is a copy: parking more after the fact must not
	// reach into a snapshot already taken.
	ws.ParkAgent(ParkedAgent{Source: "hook", Agent: "codex", Kind: "id", Value: "s2"})
	if len(snap.ParkedAgents) != 1 {
		t.Fatal("snapshot shares the live parked slice")
	}

	awake := testWorkspace(t, "awake")
	b, err := json.Marshal(awake.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"asleep"`, `"parked_agents"`} {
		if strings.Contains(string(b), key) {
			t.Fatalf("awake snapshot carries %s: %s", key, b)
		}
	}
}
