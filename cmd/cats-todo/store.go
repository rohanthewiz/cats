package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// errTodoNotFound reports a mutation aimed at a todo that no longer exists —
// typically it was deleted or completed from another manager pane after this
// pane last loaded. Surfacing it keeps a stale pane honest instead of quietly
// claiming success.
var errTodoNotFound = errors.New("todo not found (changed from another pane?)")

// Todo is one saved prompt of future work.
type Todo struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Prompt  string    `json:"prompt"`
	Done    bool      `json:"done"`
	Created time.Time `json:"created"`
}

// scope marks where a todo lives: with the project (committed in the repo) or in
// the user's global backlog.
type scope int

const (
	// scopeProject is a todo stored in <workDir>/.cats-todo/todos.json — it
	// travels with the repo and is only visible when you launch from that project.
	scopeProject scope = iota
	// scopeGlobal is a todo stored in the user's cats-todo config dir, visible
	// from anywhere.
	scopeGlobal
)

func (s scope) String() string {
	if s == scopeProject {
		return "Project"
	}
	return "Global"
}

// projectConfigDirName is the directory a repo gets for its cats-todo config.
const projectConfigDirName = ".cats-todo"

// configDirEnvVar overrides the global-backlog config directory outright —
// mostly for tests and for anyone who keeps their backlog somewhere synced.
const configDirEnvVar = "CATS_TODO_CONFIG_DIR"

// configBaseDir returns the root config directory for cats-todo's global
// backlog: the CATS_TODO_CONFIG_DIR override when set, else
// $XDG_CONFIG_HOME/cats-todo, then ~/.config/cats-todo.
func configBaseDir() (string, error) {
	if d := os.Getenv(configDirEnvVar); d != "" {
		return d, nil
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "cats-todo"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "cats-todo"), nil
}

// globalTodosPath returns the path to the global backlog file.
func globalTodosPath() (string, error) {
	base, err := configBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "todos.json"), nil
}

// projectTodosPath returns the project backlog file within workDir, or "" when
// workDir is unknown (no project scope is available in that case).
func projectTodosPath(workDir string) string {
	if workDir == "" {
		return ""
	}
	return filepath.Join(workDir, projectConfigDirName, "todos.json")
}

// store is a file-backed list of todos for one scope. A store with an empty path
// is "unavailable" (e.g. the project store when launched outside a project): it
// loads and saves to nothing and holds no todos.
type store struct {
	scope scope
	path  string
	todos []Todo
}

// available reports whether this scope has a backing file (a project store has
// none when there is no project directory).
func (s *store) available() bool { return s.path != "" }

// load reads the store's file into s.todos. A missing file is not an error — it
// just yields an empty list, which is the natural first-run state.
func (s *store) load() error {
	s.todos = nil
	if s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, &s.todos)
}

// save writes s.todos back to its file, creating the parent directory as needed.
// It writes to a temp file in the same directory and renames it over the target,
// so a crash mid-write can never truncate the backlog — the file is always
// either the old contents or the new, never half of one. It is a no-op for an
// unavailable store.
func (s *store) save() error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.todos, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".todos-*.tmp")
	if err != nil {
		return err
	}
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Chmod(0o644)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// reload refreshes s.todos from disk so a mutation starts from the latest
// state. Every manager pane shares the global backlog (and long-lived panes can
// share a project one), so mutating from a stale in-memory copy would silently
// clobber what another pane wrote. An unavailable store keeps its in-memory
// list — load would wipe it.
func (s *store) reload() error {
	if s.path == "" {
		return nil
	}
	return s.load()
}

// add appends a new todo and persists.
func (s *store) add(t Todo) error {
	if err := s.reload(); err != nil {
		return err
	}
	s.todos = append(s.todos, t)
	return s.save()
}

// update replaces the todo with the same ID (title/prompt) and persists.
func (s *store) update(t Todo) error {
	if err := s.reload(); err != nil {
		return err
	}
	for i := range s.todos {
		if s.todos[i].ID == t.ID {
			s.todos[i].Title = t.Title
			s.todos[i].Prompt = t.Prompt
			return s.save()
		}
	}
	return errTodoNotFound
}

// delete removes the todo with id and persists.
func (s *store) delete(id string) error {
	if err := s.reload(); err != nil {
		return err
	}
	for i, t := range s.todos {
		if t.ID == id {
			s.todos = append(s.todos[:i], s.todos[i+1:]...)
			return s.save()
		}
	}
	return errTodoNotFound
}

// toggle flips the done flag of the todo with id and persists.
func (s *store) toggle(id string) error {
	if err := s.reload(); err != nil {
		return err
	}
	for i := range s.todos {
		if s.todos[i].ID == id {
			s.todos[i].Done = !s.todos[i].Done
			return s.save()
		}
	}
	return errTodoNotFound
}

// setDone sets the done flag of the todo with id to done and persists. Unlike
// toggle it is idempotent (a no-op when already in that state) — used to
// auto-complete a todo after a "run" drop without risk of reopening one that
// was already done.
func (s *store) setDone(id string, done bool) error {
	if err := s.reload(); err != nil {
		return err
	}
	for i := range s.todos {
		if s.todos[i].ID == id {
			if s.todos[i].Done == done {
				return nil
			}
			s.todos[i].Done = done
			return s.save()
		}
	}
	return errTodoNotFound
}

// move shifts the todo with id one step toward the front (delta < 0) or back
// (delta > 0) of the backlog, swapping only with neighbors in the same done
// state — mirroring the list view, which shows open todos first and done todos
// last within each scope. Array order is the backlog's priority order, so the
// swap is persisted. Moving past the edge of the group is a quiet no-op.
func (s *store) move(id string, delta int) error {
	if err := s.reload(); err != nil {
		return err
	}
	cur := -1
	for i := range s.todos {
		if s.todos[i].ID == id {
			cur = i
			break
		}
	}
	if cur < 0 {
		return errTodoNotFound
	}
	step := 1
	if delta < 0 {
		step = -1
	}
	for j := cur + step; j >= 0 && j < len(s.todos); j += step {
		if s.todos[j].Done == s.todos[cur].Done {
			s.todos[cur], s.todos[j] = s.todos[j], s.todos[cur]
			return s.save()
		}
	}
	return nil
}

// clearDone removes every done todo and persists, returning how many were
// removed. Zero removals skip the save.
func (s *store) clearDone() (int, error) {
	if err := s.reload(); err != nil {
		return 0, err
	}
	kept := s.todos[:0]
	removed := 0
	for _, t := range s.todos {
		if t.Done {
			removed++
			continue
		}
		kept = append(kept, t)
	}
	s.todos = kept
	if removed == 0 {
		return 0, nil
	}
	return removed, s.save()
}

// find returns a copy of the todo with id, and whether it was found.
func (s *store) find(id string) (Todo, bool) {
	for _, t := range s.todos {
		if t.ID == id {
			return t, true
		}
	}
	return Todo{}, false
}

// loadStores builds and loads the project and global stores for a launch
// context. The project store is keyed off ctx.WorkDir; when that is empty (no
// project) the project store is unavailable and only the global backlog shows.
func loadStores(ctx RunContext) (project *store, global *store, err error) {
	project = &store{scope: scopeProject, path: projectTodosPath(ctx.WorkDir)}
	if err = project.load(); err != nil {
		return nil, nil, err
	}

	gp, err := globalTodosPath()
	if err != nil {
		return nil, nil, err
	}
	global = &store{scope: scopeGlobal, path: gp}
	if err = global.load(); err != nil {
		return nil, nil, err
	}
	return project, global, nil
}
