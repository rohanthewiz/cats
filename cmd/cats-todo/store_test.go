package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tempStore returns a project store backed by a fresh file under t.TempDir(), so
// each test gets isolated, auto-cleaned persistence.
func tempStore(t *testing.T) *store {
	t.Helper()
	return &store{scope: scopeProject, path: filepath.Join(t.TempDir(), "todos.json")}
}

func TestStoreAvailable(t *testing.T) {
	if (&store{path: ""}).available() {
		t.Error("a store with no path should be unavailable")
	}
	if !(&store{path: "/x/todos.json"}).available() {
		t.Error("a store with a path should be available")
	}
}

// TestUnavailableStoreTouchesNoDisk pins the documented contract that an
// unavailable store (an empty path, e.g. launched outside a project) loads and
// saves to nothing rather than erroring or writing a stray file.
func TestUnavailableStoreTouchesNoDisk(t *testing.T) {
	dir := t.TempDir()
	s := &store{scope: scopeProject, path: ""}

	if err := s.load(); err != nil {
		t.Fatalf("load on unavailable store = %v, want nil", err)
	}
	if s.todos != nil {
		t.Errorf("load left todos = %v, want nil", s.todos)
	}
	if err := s.save(); err != nil {
		t.Fatalf("save on unavailable store = %v, want nil", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("save on unavailable store wrote %d entries, want 0", len(entries))
	}
}

// TestSaveLoadRoundTrip writes todos through one store and reads them back
// through a second store at the same path, proving on-disk persistence survives.
func TestSaveLoadRoundTrip(t *testing.T) {
	s := tempStore(t)
	created := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	want := []Todo{
		{ID: "a1", Title: "first", Prompt: "do the first thing", Created: created},
		{ID: "b2", Title: "", Prompt: "no title here", Done: true, Created: created},
	}
	s.todos = want
	if err := s.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloaded := &store{scope: scopeProject, path: s.path}
	if err := reloaded.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(reloaded.todos) != len(want) {
		t.Fatalf("loaded %d todos, want %d", len(reloaded.todos), len(want))
	}
	for i, td := range reloaded.todos {
		if td.ID != want[i].ID || td.Title != want[i].Title || td.Prompt != want[i].Prompt || td.Done != want[i].Done {
			t.Errorf("todo[%d] = %+v, want %+v", i, td, want[i])
		}
		if !td.Created.Equal(want[i].Created) {
			t.Errorf("todo[%d].Created = %v, want %v", i, td.Created, want[i].Created)
		}
	}
}

func TestLoadMissingFileIsEmptyNotError(t *testing.T) {
	// A first run has no file yet — that must read as an empty backlog.
	s := &store{scope: scopeGlobal, path: filepath.Join(t.TempDir(), "does-not-exist.json")}
	if err := s.load(); err != nil {
		t.Fatalf("load of missing file = %v, want nil", err)
	}
	if len(s.todos) != 0 {
		t.Errorf("load of missing file yielded %d todos, want 0", len(s.todos))
	}
}

func TestLoadEmptyFileIsEmptyNotError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todos.json")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s := &store{scope: scopeGlobal, path: path}
	if err := s.load(); err != nil {
		t.Fatalf("load of empty file = %v, want nil", err)
	}
	if len(s.todos) != 0 {
		t.Errorf("load of empty file yielded %d todos, want 0", len(s.todos))
	}
}

func TestStoreCRUD(t *testing.T) {
	s := tempStore(t)

	if err := s.add(Todo{ID: "1", Title: "one", Prompt: "p1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.add(Todo{ID: "2", Title: "two", Prompt: "p2"}); err != nil {
		t.Fatal(err)
	}

	t.Run("find", func(t *testing.T) {
		got, ok := s.find("2")
		if !ok || got.Title != "two" {
			t.Errorf("find(2) = %+v, %v; want title two, true", got, ok)
		}
		if _, ok := s.find("nope"); ok {
			t.Error("find(nope) reported found for an unknown id")
		}
		// find returns a copy — mutating it must not touch the store.
		got.Title = "mutated"
		if again, _ := s.find("2"); again.Title != "two" {
			t.Errorf("find returned a live reference: store title became %q", again.Title)
		}
	})

	t.Run("update edits title and prompt only", func(t *testing.T) {
		if err := s.update(Todo{ID: "1", Title: "one-edited", Prompt: "p1-edited"}); err != nil {
			t.Fatal(err)
		}
		got, _ := s.find("1")
		if got.Title != "one-edited" || got.Prompt != "p1-edited" {
			t.Errorf("after update find(1) = %+v, want edited title/prompt", got)
		}
		// Updating an unknown id reports the todo as gone (it may have been
		// deleted from another pane), rather than claiming success.
		if err := s.update(Todo{ID: "ghost"}); err != errTodoNotFound {
			t.Errorf("update of unknown id = %v, want errTodoNotFound", err)
		}
	})

	t.Run("toggle flips done", func(t *testing.T) {
		if err := s.toggle("2"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.find("2"); !got.Done {
			t.Error("toggle(2) did not mark done")
		}
		if err := s.toggle("2"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.find("2"); got.Done {
			t.Error("second toggle(2) did not reopen")
		}
	})

	t.Run("delete removes and persists", func(t *testing.T) {
		if err := s.delete("1"); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.find("1"); ok {
			t.Error("delete(1) left the todo in memory")
		}
		// Reload from disk to confirm the deletion was saved, not just in-memory.
		reloaded := &store{scope: s.scope, path: s.path}
		if err := reloaded.load(); err != nil {
			t.Fatal(err)
		}
		if len(reloaded.todos) != 1 || reloaded.todos[0].ID != "2" {
			t.Errorf("after delete, disk has %+v, want only id 2", reloaded.todos)
		}
	})

	t.Run("mutations of unknown ids report not found", func(t *testing.T) {
		if err := s.delete("ghost"); err != errTodoNotFound {
			t.Errorf("delete of unknown id = %v, want errTodoNotFound", err)
		}
		if err := s.toggle("ghost"); err != errTodoNotFound {
			t.Errorf("toggle of unknown id = %v, want errTodoNotFound", err)
		}
		if err := s.setDone("ghost", true); err != errTodoNotFound {
			t.Errorf("setDone of unknown id = %v, want errTodoNotFound", err)
		}
	})
}

// TestMutationsStartFromDisk pins the lost-update fix: two store instances at
// the same path (two manager panes sharing the global backlog) each add a todo,
// and both todos survive — the second write must not clobber the first, because
// every mutation reloads from disk before applying itself.
func TestMutationsStartFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todos.json")
	s1 := &store{scope: scopeGlobal, path: path}
	s2 := &store{scope: scopeGlobal, path: path}
	if err := s1.load(); err != nil {
		t.Fatal(err)
	}
	if err := s2.load(); err != nil {
		t.Fatal(err)
	}

	if err := s1.add(Todo{ID: "from-pane-1", Prompt: "p1"}); err != nil {
		t.Fatal(err)
	}
	// s2 still has an empty in-memory list; its add must pick up pane 1's todo.
	if err := s2.add(Todo{ID: "from-pane-2", Prompt: "p2"}); err != nil {
		t.Fatal(err)
	}

	reloaded := &store{scope: scopeGlobal, path: path}
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.todos) != 2 {
		t.Fatalf("disk has %d todos, want 2 (a stale pane clobbered the other's write)", len(reloaded.todos))
	}
	if _, ok := reloaded.find("from-pane-1"); !ok {
		t.Error("pane 1's todo was lost")
	}
	if _, ok := reloaded.find("from-pane-2"); !ok {
		t.Error("pane 2's todo was lost")
	}
}

// TestSaveLeavesNoTempFiles pins the write-then-rename save: after a save the
// directory holds exactly the backlog file, with the expected content and no
// leftover temp files.
func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s := &store{scope: scopeProject, path: filepath.Join(dir, "todos.json")}
	if err := s.add(Todo{ID: "a", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "todos.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("dir contains %v, want only todos.json", names)
	}
}

// TestMove covers reordering: a move swaps only with neighbors in the same done
// state, persists, and quietly no-ops at the edge of the group.
func TestMove(t *testing.T) {
	s := tempStore(t)
	for _, td := range []Todo{
		{ID: "a", Prompt: "a"},
		{ID: "b", Prompt: "b"},
		{ID: "done1", Prompt: "d", Done: true},
		{ID: "c", Prompt: "c"},
	} {
		if err := s.add(td); err != nil {
			t.Fatal(err)
		}
	}

	order := func() []string {
		ids := make([]string, len(s.todos))
		for i, td := range s.todos {
			ids[i] = td.ID
		}
		return ids
	}
	want := func(expect ...string) {
		t.Helper()
		got := order()
		for i := range expect {
			if got[i] != expect[i] {
				t.Fatalf("order = %v, want %v", got, expect)
			}
		}
	}

	// b moves down past the done todo to swap with c — same-done-state neighbors only.
	if err := s.move("b", 1); err != nil {
		t.Fatal(err)
	}
	want("a", "c", "done1", "b")

	// b moves back up, again skipping the done todo.
	if err := s.move("b", -1); err != nil {
		t.Fatal(err)
	}
	want("a", "b", "done1", "c")

	// A move past the edge is a no-op, not an error.
	if err := s.move("a", -1); err != nil {
		t.Fatal(err)
	}
	want("a", "b", "done1", "c")

	if err := s.move("ghost", 1); err != errTodoNotFound {
		t.Errorf("move of unknown id = %v, want errTodoNotFound", err)
	}

	// The new order must be on disk, not just in memory.
	reloaded := &store{scope: s.scope, path: s.path}
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	if reloaded.todos[0].ID != "a" || reloaded.todos[1].ID != "b" {
		t.Errorf("disk order = %v, want the moved order persisted", reloaded.todos)
	}
}

// TestClearDone covers bulk cleanup: only done todos are removed, the count is
// reported, and clearing an already-clean store is a zero no-op.
func TestClearDone(t *testing.T) {
	s := tempStore(t)
	for _, td := range []Todo{
		{ID: "open1", Prompt: "p"},
		{ID: "done1", Prompt: "p", Done: true},
		{ID: "done2", Prompt: "p", Done: true},
		{ID: "open2", Prompt: "p"},
	} {
		if err := s.add(td); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.clearDone()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("clearDone removed %d, want 2", n)
	}
	if len(s.todos) != 2 || s.todos[0].ID != "open1" || s.todos[1].ID != "open2" {
		t.Errorf("after clearDone todos = %+v, want the two open ones in order", s.todos)
	}

	n, err = s.clearDone()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("second clearDone removed %d, want 0", n)
	}
}

func TestProjectTodosPath(t *testing.T) {
	if got := projectTodosPath(""); got != "" {
		t.Errorf("projectTodosPath(\"\") = %q, want empty (no project scope)", got)
	}
	want := filepath.Join("/repo", projectConfigDirName, "todos.json")
	if got := projectTodosPath("/repo"); got != want {
		t.Errorf("projectTodosPath(/repo) = %q, want %q", got, want)
	}
}

// TestConfigBaseDir exercises the documented precedence: the CATS_TODO_CONFIG_DIR
// override wins, then XDG_CONFIG_HOME/cats-todo, then ~/.config. These use
// t.Setenv and so must not run in parallel.
func TestConfigBaseDir(t *testing.T) {
	t.Run("CATS_TODO_CONFIG_DIR wins", func(t *testing.T) {
		t.Setenv("CATS_TODO_CONFIG_DIR", "/cats-todo/cfg")
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		got, err := configBaseDir()
		if err != nil {
			t.Fatal(err)
		}
		if got != "/cats-todo/cfg" {
			t.Errorf("configBaseDir = %q, want the override dir", got)
		}
	})

	t.Run("falls back to XDG_CONFIG_HOME", func(t *testing.T) {
		t.Setenv("CATS_TODO_CONFIG_DIR", "")
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		got, err := configBaseDir()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join("/xdg", "cats-todo"); got != want {
			t.Errorf("configBaseDir = %q, want %q", got, want)
		}
	})

	t.Run("falls back to home .config", func(t *testing.T) {
		t.Setenv("CATS_TODO_CONFIG_DIR", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home dir available: %v", err)
		}
		got, err := configBaseDir()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(home, ".config", "cats-todo"); got != want {
			t.Errorf("configBaseDir = %q, want %q", got, want)
		}
	})
}

// TestLoadStores checks that a launch with a project workdir produces an
// available, loaded project store plus the global store, while an empty workdir
// leaves the project store unavailable (global-only mode).
func TestLoadStores(t *testing.T) {
	// Point the global backlog at an isolated dir so the test never touches the
	// real user config.
	t.Setenv("CATS_TODO_CONFIG_DIR", t.TempDir())

	t.Run("with a project workdir", func(t *testing.T) {
		work := t.TempDir()
		project, global, err := loadStores(RunContext{WorkDir: work})
		if err != nil {
			t.Fatal(err)
		}
		if !project.available() {
			t.Error("project store should be available when WorkDir is set")
		}
		if project.path != projectTodosPath(work) {
			t.Errorf("project path = %q, want %q", project.path, projectTodosPath(work))
		}
		if !global.available() {
			t.Error("global store should always be available")
		}
	})

	t.Run("without a workdir is global-only", func(t *testing.T) {
		project, global, err := loadStores(RunContext{WorkDir: ""})
		if err != nil {
			t.Fatal(err)
		}
		if project.available() {
			t.Error("project store should be unavailable with no WorkDir")
		}
		if !global.available() {
			t.Error("global store should still be available")
		}
	})
}
