package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// errTestDrop stands in for a drop failure in tests.
var errTestDrop = errors.New("drop failed")

// newModelInTemp builds a manager whose project and global backlogs are backed
// by fresh files under t.TempDir(), so form saves, toggles, and deletes actually
// persist somewhere isolated and auto-cleaned. The project store has a path, so
// available() is true — the in-project launch where scope defaults matter.
func newModelInTemp(t *testing.T) (model, *store, *store) {
	t.Helper()
	dir := t.TempDir()
	project := &store{scope: scopeProject, path: filepath.Join(dir, "project", "todos.json")}
	global := &store{scope: scopeGlobal, path: filepath.Join(dir, "global", "todos.json")}
	m := newModel(RunContext{WorkDir: filepath.Join(dir, "project")}, project, global, nil)
	return m, project, global
}

// TestSaveFormAddsTodo walks the add flow the way a keypress would: beginAdd
// enters the form, the prompt gets text, saveForm persists. It checks the todo
// reaches both the in-memory store and disk, that a blank title is derived from
// the prompt's first line, and that the UI returns to the list.
func TestSaveFormAddsTodo(t *testing.T) {
	m, project, _ := newModelInTemp(t)

	next, _ := m.beginAdd()
	m = next.(model)
	if m.stage != stageForm {
		t.Fatalf("beginAdd stage = %v, want stageForm", m.stage)
	}
	// In a project, a new todo defaults to the project backlog.
	if m.formScope != scopeProject {
		t.Errorf("formScope = %v, want scopeProject in an available project", m.formScope)
	}

	// Leave the title blank so it gets derived from the prompt's first line.
	m.promptArea.SetValue("Wire up the export button\nplus follow-up details")
	next, _ = m.saveForm()
	m = next.(model)

	if m.stage != stageList {
		t.Fatalf("after save stage = %v, want stageList", m.stage)
	}
	if len(project.todos) != 1 {
		t.Fatalf("project has %d todos, want 1", len(project.todos))
	}
	if got := project.todos[0].Title; got != "Wire up the export button" {
		t.Errorf("derived title = %q, want the prompt's first line", got)
	}

	// Confirm it persisted, not just mutated in memory.
	reloaded := &store{scope: scopeProject, path: project.path}
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.todos) != 1 || reloaded.todos[0].Prompt != "Wire up the export button\nplus follow-up details" {
		t.Errorf("disk todos = %+v, want the saved prompt", reloaded.todos)
	}
}

func TestSaveFormRejectsEmptyPrompt(t *testing.T) {
	m, project, _ := newModelInTemp(t)
	next, _ := m.beginAdd()
	m = next.(model)

	// Only whitespace in the prompt — saveForm should refuse and stay on the form.
	m.promptArea.SetValue("   \n  ")
	next, _ = m.saveForm()
	m = next.(model)

	if m.stage != stageForm {
		t.Errorf("stage = %v, want to stay on stageForm for an empty prompt", m.stage)
	}
	if m.formErr == "" {
		t.Error("expected a formErr for an empty prompt")
	}
	if len(project.todos) != 0 {
		t.Errorf("an empty prompt added %d todos, want 0", len(project.todos))
	}
}

// TestBeginAddDefaultsToGlobalOutsideProject pins the scope default: with no
// project store available, a new todo can only go to the global backlog.
func TestBeginAddDefaultsToGlobalOutsideProject(t *testing.T) {
	project := &store{scope: scopeProject, path: ""} // unavailable: no project
	global := &store{scope: scopeGlobal, path: filepath.Join(t.TempDir(), "todos.json")}
	m := newModel(RunContext{}, project, global, nil)

	next, _ := m.beginAdd()
	m = next.(model)
	if m.formScope != scopeGlobal {
		t.Errorf("formScope = %v, want scopeGlobal when no project is available", m.formScope)
	}
}

// TestToggleSelected flips the highlighted todo's done flag and reports the right
// status, both directions.
func TestToggleSelected(t *testing.T) {
	m, project, _ := newModelInTemp(t)
	if err := project.add(Todo{ID: "x", Title: "task", Prompt: "do it"}); err != nil {
		t.Fatal(err)
	}
	m.rebuildList() // pick up the new todo and park the cursor on it

	next, _ := m.toggleSelected()
	m = next.(model)
	if got, _ := project.find("x"); !got.Done {
		t.Error("toggleSelected did not mark the todo done")
	}
	if m.status != "marked done" {
		t.Errorf("status = %q, want 'marked done'", m.status)
	}

	next, _ = m.toggleSelected()
	m = next.(model)
	if got, _ := project.find("x"); got.Done {
		t.Error("second toggleSelected did not reopen the todo")
	}
	if m.status != "reopened" {
		t.Errorf("status = %q, want 'reopened'", m.status)
	}
}

// TestDeleteConfirmFlow runs the two-step delete: beginDelete arms the confirm
// stage, then a "y" key carries it out and returns to the list.
func TestDeleteConfirmFlow(t *testing.T) {
	m, project, _ := newModelInTemp(t)
	if err := project.add(Todo{ID: "gone", Title: "remove me", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	m.rebuildList()

	next, _ := m.beginDelete()
	m = next.(model)
	if m.stage != stageConfirm {
		t.Fatalf("beginDelete stage = %v, want stageConfirm", m.stage)
	}
	if m.pendingTitle != "remove me" {
		t.Errorf("pendingTitle = %q, want 'remove me'", m.pendingTitle)
	}

	next, _ = m.updateConfirm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(model)
	if m.stage != stageList {
		t.Errorf("after confirm stage = %v, want stageList", m.stage)
	}
	if _, ok := project.find("gone"); ok {
		t.Error("confirmed delete did not remove the todo")
	}
	if m.status != "deleted" {
		t.Errorf("status = %q, want 'deleted'", m.status)
	}
}

// TestDeleteConfirmCancel pins that answering "n" keeps the todo.
func TestDeleteConfirmCancel(t *testing.T) {
	m, project, _ := newModelInTemp(t)
	if err := project.add(Todo{ID: "keep", Title: "keep me", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	m.rebuildList()

	next, _ := m.beginDelete()
	m = next.(model)
	next, _ = m.updateConfirm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = next.(model)
	if m.stage != stageList {
		t.Errorf("after cancel stage = %v, want stageList", m.stage)
	}
	if _, ok := project.find("keep"); !ok {
		t.Error("cancelling the confirm deleted the todo anyway")
	}
}

// hasHeading reports whether the rebuilt list contains a non-selectable group
// heading with the given name.
func hasHeading(items []listItem, name string) bool {
	for _, it := range items {
		if !it.selectable && it.name == name {
			return true
		}
	}
	return false
}

// TestRebuildListGroupsOnlyWhenBothScopesHaveTodos covers the grouping rule:
// "Project"/"Global" headings appear only when both backlogs are non-empty.
func TestRebuildListGroupsOnlyWhenBothScopesHaveTodos(t *testing.T) {
	t.Run("both populated shows headings", func(t *testing.T) {
		m, project, global := newModelInTemp(t)
		if err := project.add(Todo{ID: "p", Prompt: "proj todo"}); err != nil {
			t.Fatal(err)
		}
		if err := global.add(Todo{ID: "g", Prompt: "glob todo"}); err != nil {
			t.Fatal(err)
		}
		m.rebuildList()
		if !hasHeading(m.list.items, "Project") || !hasHeading(m.list.items, "Global") {
			t.Errorf("expected both group headings; items = %+v", m.list.items)
		}
		if len(m.rows) != 2 {
			t.Errorf("rows = %d, want 2 selectable todos", len(m.rows))
		}
	})

	t.Run("single scope shows no headings", func(t *testing.T) {
		m, _, global := newModelInTemp(t)
		if err := global.add(Todo{ID: "g", Prompt: "glob only"}); err != nil {
			t.Fatal(err)
		}
		m.rebuildList()
		if hasHeading(m.list.items, "Project") || hasHeading(m.list.items, "Global") {
			t.Errorf("expected no headings with a single populated scope; items = %+v", m.list.items)
		}
	})
}

// TestRebuildListSortsDoneToBottom pins the list order: within a scope, open
// todos keep their backlog (array) order and done todos sink below them.
func TestRebuildListSortsDoneToBottom(t *testing.T) {
	m, project, _ := newModelInTemp(t)
	for _, td := range []Todo{
		{ID: "done-first", Prompt: "d", Done: true},
		{ID: "open1", Prompt: "o1"},
		{ID: "open2", Prompt: "o2"},
	} {
		if err := project.add(td); err != nil {
			t.Fatal(err)
		}
	}
	m.rebuildList()

	got := make([]string, len(m.rows))
	for i, r := range m.rows {
		got[i] = r.id
	}
	want := []string{"open1", "open2", "done-first"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v (open first, done last)", got, want)
		}
	}
}

// TestHideDoneFoldsCompleted pins ctrl+d's fold: with hideDone set, done todos
// leave the rows entirely, and clearing it brings them back.
func TestHideDoneFoldsCompleted(t *testing.T) {
	m, project, _ := newModelInTemp(t)
	if err := project.add(Todo{ID: "open", Prompt: "o"}); err != nil {
		t.Fatal(err)
	}
	if err := project.add(Todo{ID: "done", Prompt: "d", Done: true}); err != nil {
		t.Fatal(err)
	}

	m.hideDone = true
	m.rebuildList()
	if len(m.rows) != 1 || m.rows[0].id != "open" {
		t.Errorf("hidden rows = %+v, want only the open todo", m.rows)
	}
	if m.hiddenDoneCount() != 1 {
		t.Errorf("hiddenDoneCount = %d, want 1", m.hiddenDoneCount())
	}

	m.hideDone = false
	m.rebuildList()
	if len(m.rows) != 2 {
		t.Errorf("unhidden rows = %+v, want both todos back", m.rows)
	}
}

// TestFilterMatchesDeepPromptLines pins the full-body search: a query that only
// appears past the first line of a multi-line prompt still matches.
func TestFilterMatchesDeepPromptLines(t *testing.T) {
	m, project, _ := newModelInTemp(t)
	err := project.add(Todo{ID: "deep", Title: "refactor", Prompt: "clean up the store\nand also fix the flaky websocket reconnect"})
	if err != nil {
		t.Fatal(err)
	}
	if err := project.add(Todo{ID: "other", Title: "docs", Prompt: "update the readme"}); err != nil {
		t.Fatal(err)
	}
	m.rebuildList()

	m.list.input.SetValue("websocket")
	m.list.filter()
	idx := m.list.selectedIndex()
	if idx < 0 || m.rows[idx].id != "deep" {
		t.Errorf("filtering for a deep prompt line selected row %d, want the multi-line todo", idx)
	}
}

// TestMoveSelectedKeepsHighlight pins reordering: ctrl+down swaps the todo with
// its neighbor, persists the order, and the highlight follows the moved todo.
func TestMoveSelectedKeepsHighlight(t *testing.T) {
	m, project, _ := newModelInTemp(t)
	for _, td := range []Todo{{ID: "a", Prompt: "a"}, {ID: "b", Prompt: "b"}} {
		if err := project.add(td); err != nil {
			t.Fatal(err)
		}
	}
	m.rebuildList() // cursor parks on the first row ("a")

	next, _ := m.moveSelected(1)
	m = next.(model)

	if project.todos[0].ID != "b" || project.todos[1].ID != "a" {
		t.Errorf("store order = %+v, want b then a", project.todos)
	}
	if ref, ok := m.selectedRef(); !ok || ref.id != "a" {
		t.Errorf("selected = %+v, want the highlight to follow the moved todo a", ref)
	}
}

// TestClearDoneConfirmFlow runs the bulk cleanup: ctrl+w arms the confirm stage
// with the done count, "y" removes completed todos from both scopes, and open
// todos survive. With nothing done, it short-circuits to a status message.
func TestClearDoneConfirmFlow(t *testing.T) {
	m, project, global := newModelInTemp(t)
	for _, td := range []Todo{{ID: "p-open", Prompt: "p"}, {ID: "p-done", Prompt: "p", Done: true}} {
		if err := project.add(td); err != nil {
			t.Fatal(err)
		}
	}
	if err := global.add(Todo{ID: "g-done", Prompt: "g", Done: true}); err != nil {
		t.Fatal(err)
	}
	m.rebuildList()

	next, _ := m.beginClearDone()
	m = next.(model)
	if m.stage != stageConfirm || m.confirmKind != confirmClearDone {
		t.Fatalf("beginClearDone stage/kind = %v/%v, want stageConfirm/confirmClearDone", m.stage, m.confirmKind)
	}
	if m.pendingClearCount != 2 {
		t.Errorf("pendingClearCount = %d, want 2", m.pendingClearCount)
	}

	next, _ = m.updateConfirm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(model)
	if m.stage != stageList {
		t.Errorf("after confirm stage = %v, want stageList", m.stage)
	}
	if _, ok := project.find("p-done"); ok {
		t.Error("project's done todo survived the clear")
	}
	if _, ok := global.find("g-done"); ok {
		t.Error("global's done todo survived the clear")
	}
	if _, ok := project.find("p-open"); !ok {
		t.Error("the open todo was cleared; only done todos should go")
	}
	if !strings.Contains(m.status, "cleared 2") {
		t.Errorf("status = %q, want it to report clearing 2", m.status)
	}

	// A second clear finds nothing and never enters the confirm stage.
	next, _ = m.beginClearDone()
	m = next.(model)
	if m.stage != stageList {
		t.Errorf("empty clear stage = %v, want to stay on stageList", m.stage)
	}
	if !strings.Contains(m.status, "no completed") {
		t.Errorf("status = %q, want the nothing-to-clear note", m.status)
	}
}

// TestViewStageFlow covers the read-only prompt view: ctrl+v opens it on the
// highlighted todo, its rendering carries the full body, esc returns to the
// list, and enter hands off to the drop flow (which, socket-less here, lands
// back on the list with an error status).
func TestViewStageFlow(t *testing.T) {
	m, project, _ := newModelInTemp(t)
	body := "first line\nsecond line with the details"
	if err := project.add(Todo{ID: "v", Title: "view me", Prompt: body}); err != nil {
		t.Fatal(err)
	}
	m.rebuildList()

	next, _ := m.beginView()
	m = next.(model)
	if m.stage != stageView {
		t.Fatalf("beginView stage = %v, want stageView", m.stage)
	}
	if got := m.View(); !strings.Contains(got, "second line with the details") {
		t.Errorf("view rendering lacks the prompt's later lines:\n%s", got)
	}

	next, _ = m.updateView(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.stage != stageList {
		t.Errorf("esc from view stage = %v, want stageList", m.stage)
	}

	next, _ = m.beginView()
	m = next.(model)
	next, _ = m.updateView(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.stage != stageList || !m.statusErr {
		t.Errorf("enter (drop) without a control socket: stage=%v statusErr=%v, want stageList with an error status", m.stage, m.statusErr)
	}
}

// TestChooseTargetStaysOpen pins the persistent-pane behavior: choosing a drop
// target no longer quits the program. Instead it returns to the list, marks a
// drop in flight, shows a "dropping…" status, and hands back a command that will
// perform the drop off the UI thread. The pane lives on so more prompts can drop.
func TestChooseTargetStaysOpen(t *testing.T) {
	m, project, _ := newModelInTemp(t)
	if err := project.add(Todo{ID: "d", Prompt: "drop me"}); err != nil {
		t.Fatal(err)
	}
	m.rebuildList()

	// Stand up the target picker the way beginDrop would, but without a control socket:
	// buildTargets degrades to just the new-session target (selected by default).
	m.dropTodo = todoRef{scope: scopeProject, id: "d"}
	m.targets, m.targetList = m.buildTargets()
	m.stage = stageTarget

	next, cmd := m.chooseTarget(dropPaste)
	m = next.(model)
	if m.quitting {
		t.Error("chooseTarget set quitting; the persistent pane must stay open")
	}
	if !m.dropping {
		t.Error("chooseTarget did not mark a drop in flight")
	}
	if m.stage != stageList {
		t.Errorf("stage = %v, want stageList after starting a drop", m.stage)
	}
	if m.status == "" || m.statusErr {
		t.Errorf("expected a non-error 'dropping…' status; got status=%q err=%v", m.status, m.statusErr)
	}
	if cmd == nil {
		t.Fatal("chooseTarget returned no command to perform the drop")
	}

	// The command performs the drop and reports back. With a nil socket the drop
	// fails, but the result still flows through Update, which clears the in-flight
	// flag and surfaces the error — leaving the manager usable.
	msg := cmd()
	res, ok := msg.(dropResultMsg)
	if !ok {
		t.Fatalf("drop command returned %T, want dropResultMsg", msg)
	}
	if res.err == nil {
		t.Error("expected the socket-less drop to fail")
	}
	next, _ = m.Update(res)
	m = next.(model)
	if m.dropping {
		t.Error("dropResultMsg did not clear the in-flight flag")
	}
	if !m.statusErr {
		t.Errorf("expected an error status after a failed drop; got status=%q", m.status)
	}
}

// TestRunDropMarksTodoDone pins the auto-complete behavior: a successful "run"
// drop carries markDone, and the dropResultMsg handler closes the todo out and
// notes it in the status. A "paste" drop leaves the todo open.
func TestRunDropMarksTodoDone(t *testing.T) {
	t.Run("run drop marks done", func(t *testing.T) {
		m, project, _ := newModelInTemp(t)
		if err := project.add(Todo{ID: "r", Prompt: "run me"}); err != nil {
			t.Fatal(err)
		}
		m.rebuildList()

		// A run drop reports success for an existing pane; mark it done.
		res := dropResultMsg{desc: "claude", ref: todoRef{scope: scopeProject, id: "r"}, markDone: true}
		next, _ := m.Update(res)
		m = next.(model)

		if got, _ := project.find("r"); !got.Done {
			t.Error("a successful run drop did not mark the todo done")
		}
		if !strings.Contains(m.status, "marked done") {
			t.Errorf("status = %q, want it to note 'marked done'", m.status)
		}
		// It must persist, not just mutate in memory.
		reloaded := &store{scope: scopeProject, path: project.path}
		if err := reloaded.load(); err != nil {
			t.Fatal(err)
		}
		if got, _ := reloaded.find("r"); !got.Done {
			t.Error("the auto-complete did not persist to disk")
		}
	})

	t.Run("paste drop leaves it open", func(t *testing.T) {
		m, project, _ := newModelInTemp(t)
		if err := project.add(Todo{ID: "p", Prompt: "paste me"}); err != nil {
			t.Fatal(err)
		}
		m.rebuildList()

		res := dropResultMsg{desc: "claude", ref: todoRef{scope: scopeProject, id: "p"}, markDone: false}
		next, _ := m.Update(res)
		m = next.(model)

		if got, _ := project.find("p"); got.Done {
			t.Error("a paste drop marked the todo done; it should stay open")
		}
		if strings.Contains(m.status, "marked done") {
			t.Errorf("status = %q should not claim 'marked done' for a paste drop", m.status)
		}
	})

	t.Run("failed run drop leaves it open", func(t *testing.T) {
		m, project, _ := newModelInTemp(t)
		if err := project.add(Todo{ID: "f", Prompt: "fail me"}); err != nil {
			t.Fatal(err)
		}
		m.rebuildList()

		res := dropResultMsg{desc: "claude", ref: todoRef{scope: scopeProject, id: "f"}, markDone: true, err: errTestDrop}
		next, _ := m.Update(res)
		m = next.(model)

		if got, _ := project.find("f"); got.Done {
			t.Error("a failed run drop marked the todo done; it should stay open")
		}
		if !m.statusErr {
			t.Errorf("expected an error status after a failed drop; got status=%q", m.status)
		}
	})
}

// TestChooseTargetRunMarksDone pins that a run drop carries markDone with the
// dropped todo's ref through to the result, while a paste drop does not.
func TestChooseTargetRunMarksDone(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode dropMode
		want bool
	}{
		{"run", dropRun, true},
		{"paste", dropPaste, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, project, _ := newModelInTemp(t)
			if err := project.add(Todo{ID: "d", Prompt: "drop me"}); err != nil {
				t.Fatal(err)
			}
			m.rebuildList()
			m.dropTodo = todoRef{scope: scopeProject, id: "d"}
			m.targets, m.targetList = m.buildTargets()
			m.stage = stageTarget

			_, cmd := m.chooseTarget(tc.mode)
			if cmd == nil {
				t.Fatal("chooseTarget returned no drop command")
			}
			res, ok := cmd().(dropResultMsg)
			if !ok {
				t.Fatalf("drop command returned %T, want dropResultMsg", cmd())
			}
			if res.markDone != tc.want {
				t.Errorf("markDone = %v, want %v for a %s drop", res.markDone, tc.want, tc.name)
			}
			if res.ref != (todoRef{scope: scopeProject, id: "d"}) {
				t.Errorf("ref = %+v, want the dropped todo's ref", res.ref)
			}
		})
	}
}

// TestBeginDropWhileDroppingIsRejected pins that a second drop can't start while
// one is still in flight — the guard that keeps two performDrop goroutines from
// racing on the same manager.
func TestBeginDropWhileDroppingIsRejected(t *testing.T) {
	m, project, _ := newModelInTemp(t)
	if err := project.add(Todo{ID: "d", Prompt: "drop me"}); err != nil {
		t.Fatal(err)
	}
	m.rebuildList()
	m.dropping = true

	next, _ := m.beginDrop()
	m = next.(model)
	if m.stage != stageList {
		t.Errorf("stage = %v, want to stay on stageList while a drop is in flight", m.stage)
	}
	if m.status == "" || m.statusErr {
		t.Errorf("expected an informational 'in progress' status; got status=%q err=%v", m.status, m.statusErr)
	}
}

// TestTargetDesc covers the short destination labels used in the status line.
func TestTargetDesc(t *testing.T) {
	if got := targetDesc(dropTarget{kind: targetNewSession}); got != "new Claude Code session" {
		t.Errorf("new-session desc = %q", got)
	}
	if got := targetDesc(dropTarget{kind: targetExistingPane, agent: "claude"}); got != "claude" {
		t.Errorf("existing-pane desc = %q, want the agent name", got)
	}
	if got := targetDesc(dropTarget{kind: targetExistingPane}); got != "session" {
		t.Errorf("agentless existing-pane desc = %q, want the 'session' fallback", got)
	}
}

// TestBeginDropWithoutSocketReportsError pins that dropping with no cats control socket
// surfaces a status error instead of advancing to the (unusable) target picker.
func TestBeginDropWithoutSocketReportsError(t *testing.T) {
	m, project, _ := newModelInTemp(t)
	if err := project.add(Todo{ID: "d", Prompt: "drop me"}); err != nil {
		t.Fatal(err)
	}
	m.rebuildList()

	next, _ := m.beginDrop()
	m = next.(model)
	if m.stage != stageList {
		t.Errorf("stage = %v, want to stay on stageList without a socket", m.stage)
	}
	if !m.statusErr || m.status == "" {
		t.Errorf("expected an error status; got status=%q err=%v", m.status, m.statusErr)
	}
}
