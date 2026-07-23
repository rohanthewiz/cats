package main

import (
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
