# Session: broken plugin dev-link fix + cats-todo global launch action

Session ID: `7cbbef7b-28e3-4b06-9185-1e37c6ac7010`
Date: 2026-07-26

## Part 1 — "already installed" vs "no plugins installed" (cats `7ecf3bf`, pushed)

Installing `rohanthewiz/cats-todo` failed with `plugin "rohanthewiz.cats-todo"
is already installed` while the plugins dialog showed "no plugins installed".

**Root cause:** `~/.config/cats/plugins/rohanthewiz.cats-todo` was a dev-link
symlink pointing at `~/projs/go/cats/cmd/cats-todo` — deleted when cats-todo
moved to its own repo. `Install` Lstat'd the entry (sees the dead link →
"already installed") while `List`'s salvage guard Stat'd it (follows the link,
fails → entry silently skipped → invisible everywhere, including uninstall).

**Fix (`internal/plugin/`):**

- `plugin.go` `List`: questionable entries are judged by `Lstat` — directories
  *and* symlinks (broken included) surface as broken entries with `Err` set;
  only plain files squatting in the root are skipped. `load`'s partial result
  (Dir, Linked) is kept, so a broken link lists as broken+linked. The dialog
  backend (`cmd/catway/plugins.go` → `info.Broken`) and catctl already rendered
  broken entries, so both light up with no further changes.
- `install.go`: new `occupiedErr` shared by `Install` and `Link` — when the id
  is squatted by a broken symlink the error names the dead target and points at
  `catctl plugin uninstall <id>` instead of the misleading "already installed".
- `plugin_test.go` `TestBrokenDevLink`: lists as broken linked entry, blocks
  install/link with the named error, clears via uninstall, plain files stay
  hidden.
- `go mod tidy`: dropped the charm deps (bubbletea/lipgloss/bubbles/fuzzy)
  orphaned by the cats-todo removal.

Cleared the stale link and did the real install: `rohanthewiz.cats-todo`
v0.1.0 from GitHub, `todo` action registered. The server re-reads the plugins
root per dialog open, so no restart was needed for the install to show; the
List fix itself needs a server rebuild/restart to be live.

## Part 2 — launch project AND global todo from the plugin manager (cats-todo `9d9f806`)

Request: the manager dialog should be able to start the *local project* todo
as well as the *global* one, even if the project backlog is empty.

All changes in `~/projs/go/cats-todo` (no cats-side change needed — the
dialog's "run" button already becomes a picker when a plugin has >1 action):

- `cats-todo -g|--global`: global-only launch mode. `gatherRunContext` skips
  the project-root walk (`RunContext.GlobalOnly`); `loadStores` withholds the
  project store; pane titles `todo: global`. `WorkDir` is still gathered so
  new-session drops root at the pane's directory. The pre-existing global-only
  rendering (header "global only", add defaults to global scope) takes over.
- `cats-plugin.toml`: second action `todo-global` ("Cats Todo — global only",
  `./bin/cats-todo --global`). The project action stays **first** = default
  for a bare `catctl plugin run`.
- "Even if empty" needed no code: a store is available by path, not contents,
  so the project action scopes to the current project (nearest `.cats-todo/`,
  else repo root, else cwd) whether or not a backlog exists yet.
- Version 0.2.0 (manifest + main.go const); README + help text updated.
- Tests: context walk-skip under `--global`, `todo: global` pane title,
  `loadStores` global-only subtest. Build/vet/tests clean.

## State at session end

- cats: fix committed `7ecf3bf` and pushed by the user.
- cats-todo: `9d9f806` on `main`.
- **User will update the installed plugin themselves** (push cats-todo, then
  the dialog's update button or `catctl plugin update rohanthewiz.cats-todo`).
  Until then the installed copy is v0.1.0 with the single action.
- Chrome extension wasn't connected this session — verification went through
  `catctl`, which exercises the same `plugin.List` the dialog uses.
