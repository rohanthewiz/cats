package app

import (
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/layout"
)

// metaTable is a PaneMeta lookup for TabDisplayName tests: a fixed map, absent
// panes yield the zero value (the Backend contract).
func metaTable(m map[layout.PaneID]PaneMeta) func(uint32) PaneMeta {
	return func(pane uint32) PaneMeta { return m[layout.PaneID(pane)] }
}

func TestTabDisplayNameLadder(t *testing.T) {
	newTab := func(t *testing.T) (*Session, layout.PaneID) {
		t.Helper()
		s := newTestSession(t)
		root, _ := s.FocusedPane()
		return s, root
	}

	name := func(s *Session, meta map[layout.PaneID]PaneMeta) string {
		ws := s.Workspaces()[0]
		return s.TabDisplayName(ws.Tabs[ws.ActiveTabIndex()], metaTable(meta))
	}

	t.Run("no signal falls back to the tab number", func(t *testing.T) {
		s, _ := newTab(t)
		if got := name(s, nil); got != "1" {
			t.Errorf("name = %q, want %q", got, "1")
		}
	})

	t.Run("cwd basename beats nothing", func(t *testing.T) {
		s, root := newTab(t)
		got := name(s, map[layout.PaneID]PaneMeta{root: {Cwd: "/Users/ra/projs/go/cats"}})
		if got != "cats" {
			t.Errorf("name = %q, want %q", got, "cats")
		}
	})

	t.Run("root cwd is no signal", func(t *testing.T) {
		s, root := newTab(t)
		if got := name(s, map[layout.PaneID]PaneMeta{root: {Cwd: "/"}}); got != "1" {
			t.Errorf("name = %q, want %q", got, "1")
		}
	})

	t.Run("OSC title beats cwd", func(t *testing.T) {
		s, root := newTab(t)
		got := name(s, map[layout.PaneID]PaneMeta{root: {Title: "todo: cats", Cwd: "/x/y"}})
		if got != "todo: cats" {
			t.Errorf("name = %q, want %q", got, "todo: cats")
		}
	})

	t.Run("agent beats title and folds in the cwd basename", func(t *testing.T) {
		s, root := newTab(t)
		got := name(s, map[layout.PaneID]PaneMeta{
			root: {Agent: "claude", Title: "⠁ churning spinner title", Cwd: "/p/cats"},
		})
		if got != "claude · cats" {
			t.Errorf("name = %q, want %q", got, "claude · cats")
		}
	})

	t.Run("agent with no cwd stands alone", func(t *testing.T) {
		s, root := newTab(t)
		got := name(s, map[layout.PaneID]PaneMeta{root: {Agent: "codex"}})
		if got != "codex" {
			t.Errorf("name = %q, want %q", got, "codex")
		}
	})

	t.Run("pane custom name beats agent", func(t *testing.T) {
		s, root := newTab(t)
		if err := s.RenamePane(root, "builder"); err != nil {
			t.Fatalf("RenamePane: %v", err)
		}
		got := name(s, map[layout.PaneID]PaneMeta{root: {Agent: "claude", Cwd: "/p/cats"}})
		if got != "builder" {
			t.Errorf("name = %q, want %q", got, "builder")
		}
	})

	t.Run("tab rename always wins", func(t *testing.T) {
		s, root := newTab(t)
		if err := s.RenameTab(1, "pinned"); err != nil {
			t.Fatalf("RenameTab: %v", err)
		}
		got := name(s, map[layout.PaneID]PaneMeta{root: {Agent: "claude", Cwd: "/p/cats"}})
		if got != "pinned" {
			t.Errorf("name = %q, want %q", got, "pinned")
		}
	})

	t.Run("best pane wins across a split, with +N", func(t *testing.T) {
		s, root := newTab(t)
		split, err := s.SplitPane(nil, layout.Horizontal)
		if err != nil {
			t.Fatalf("SplitPane: %v", err)
		}
		// The agent pane names the tab even though the shell pane is focused
		// (SplitPane focuses the new pane; the agent rung outranks cwd).
		if f, _ := s.FocusedPane(); f != split {
			t.Fatalf("focused=%d, want split %d", f, split)
		}
		got := name(s, map[layout.PaneID]PaneMeta{
			root:  {Agent: "claude", Cwd: "/p/cats"},
			split: {Cwd: "/p/elsewhere"},
		})
		if got != "claude · cats +1" {
			t.Errorf("name = %q, want %q", got, "claude · cats +1")
		}
	})

	t.Run("focused pane wins a tie", func(t *testing.T) {
		s, root := newTab(t)
		split, err := s.SplitPane(nil, layout.Horizontal)
		if err != nil {
			t.Fatalf("SplitPane: %v", err)
		}
		// Both panes are plain shells (rung 1); the focused split names the tab.
		got := name(s, map[layout.PaneID]PaneMeta{
			root:  {Cwd: "/p/alpha"},
			split: {Cwd: "/p/beta"},
		})
		if got != "beta +1" {
			t.Errorf("name = %q, want %q", got, "beta +1")
		}
	})

	t.Run("long titles are clipped", func(t *testing.T) {
		s, root := newTab(t)
		got := name(s, map[layout.PaneID]PaneMeta{root: {Title: strings.Repeat("x", 200)}})
		if want := strings.Repeat("x", autoLabelMaxRunes-1) + "…"; got != want {
			t.Errorf("name = %q (len %d), want %d runes ending in …", got, len([]rune(got)), autoLabelMaxRunes)
		}
	})
}

func TestListTabsUsesDerivedNames(t *testing.T) {
	s := newTestSession(t)
	root, _ := s.FocusedPane()
	meta := metaTable(map[layout.PaneID]PaneMeta{root: {Agent: "claude", Cwd: "/p/cats"}})

	tabs, _, ok := s.ListTabs("", meta)
	if !ok || len(tabs) != 1 {
		t.Fatalf("ListTabs: ok=%v tabs=%d", ok, len(tabs))
	}
	if tabs[0].Name != "claude · cats" {
		t.Errorf("tab name = %q, want %q", tabs[0].Name, "claude · cats")
	}

	// nil meta skips derivation — the plain custom-name-or-number contract.
	tabs, _, _ = s.ListTabs("", nil)
	if tabs[0].Name != "1" {
		t.Errorf("tab name without meta = %q, want %q", tabs[0].Name, "1")
	}
}
