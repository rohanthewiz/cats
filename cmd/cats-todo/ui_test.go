package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newTestModel builds a manager model with empty project and global backlogs and
// no cats control socket, suitable for driving Update directly. The project store gets a
// path so available() is true — matching a real in-project launch, the context
// under which the startup crash reproduced.
func newTestModel() model {
	project := &store{scope: scopeProject, path: "/tmp/cats-todo-test/project/todos.json"}
	global := &store{scope: scopeGlobal, path: "/tmp/cats-todo-test/global/todos.json"}
	return newModel(RunContext{WorkDir: "/tmp/cats-todo-test/project"}, project, global, nil)
}

// TestWindowSizeMsgNeverPanics is a regression test (inherited from herdr-todo)
// for a bug that closed the manager pane on launch: applySizes resized the
// form's textarea/textinput and the
// target picker unconditionally, but those are zero-value until their stage is
// entered (built in beginAdd/beginEdit/beginDrop). The first WindowSizeMsg lands
// while the model is still on the list, so applySizes called textarea.SetWidth on
// a nil-initialized model and panicked — Bubble Tea unwound, the process exited,
// and cats tore the pane down. A WindowSizeMsg must be safe on every stage,
// whether or not that stage's inputs have been built yet.
func TestWindowSizeMsgNeverPanics(t *testing.T) {
	// A width whose usable area (width-4) clears applySizes' w >= 20 guard, so the
	// resize actually reaches the inputs rather than returning early.
	resize := tea.WindowSizeMsg{Width: 120, Height: 40}

	t.Run("list stage (where the first resize lands)", func(t *testing.T) {
		m := newTestModel()
		if m.stage != stageList {
			t.Fatalf("initial stage = %v, want stageList", m.stage)
		}
		m.Update(resize) // before the fix this panicked on the zero-value textarea
	})

	t.Run("form stage (textarea built and resizable)", func(t *testing.T) {
		next, _ := newTestModel().beginAdd()
		m := next.(model)
		if m.stage != stageForm {
			t.Fatalf("beginAdd stage = %v, want stageForm", m.stage)
		}
		m.Update(resize)
	})

	t.Run("target stage (picker built without a socket)", func(t *testing.T) {
		m := newTestModel()
		// beginDrop needs a control socket, so build the picker the way it would but
		// with a nil client (buildTargets degrades to just the new-session target).
		m.targets, m.targetList = m.buildTargets()
		m.stage = stageTarget
		m.Update(resize)
	})
}

// TestScopeNote pins the header's scope line: the project basename must always
// lead (the workspace label used to replace it, which hid launches that landed
// in the wrong directory), the workspace label rides along when it adds
// information, and under width pressure the label — never the project — is
// what compacts and then disappears.
func TestScopeNote(t *testing.T) {
	// Wide enough that the title + note + label all fit uncompacted.
	const roomy = 120

	build := func(wsLabel string, width int) model {
		m := newTestModel()
		m.ctx.WorkspaceLabel = wsLabel
		m.width = width
		return m
	}

	t.Run("project and workspace both shown", func(t *testing.T) {
		got := build("pers", roomy).scopeNote()
		if got != "project + global · ws:pers" {
			t.Fatalf("scopeNote = %q, want project first with ws suffix", got)
		}
	})

	t.Run("no workspace label", func(t *testing.T) {
		if got := build("", roomy).scopeNote(); got != "project + global" {
			t.Fatalf("scopeNote = %q, want bare project note", got)
		}
	})

	t.Run("label matching the project is dropped as redundant", func(t *testing.T) {
		// newTestModel's WorkDir basename is "project".
		if got := build("project", roomy).scopeNote(); got != "project + global" {
			t.Fatalf("scopeNote = %q, want suffix deduped", got)
		}
	})

	t.Run("tight width compacts the label, not the project", func(t *testing.T) {
		got := build("a-rather-long-workspace-name", 60).scopeNote()
		if !strings.HasPrefix(got, "project + global · ws:") {
			t.Fatalf("scopeNote = %q, project note must survive compaction intact", got)
		}
		if !strings.HasSuffix(got, "…") {
			t.Fatalf("scopeNote = %q, want a truncated (…) workspace label", got)
		}
	})

	t.Run("no room at all drops the label entirely", func(t *testing.T) {
		if got := build("pers", 40).scopeNote(); got != "project + global" {
			t.Fatalf("scopeNote = %q, want label dropped, project kept", got)
		}
	})

	t.Run("zero width (before the first resize) shows everything", func(t *testing.T) {
		got := build("a-rather-long-workspace-name", 0).scopeNote()
		if got != "project + global · ws:a-rather-long-workspace-name" {
			t.Fatalf("scopeNote = %q, want full label when width is unknown", got)
		}
	})
}
