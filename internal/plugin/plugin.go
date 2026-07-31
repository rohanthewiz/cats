package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// DirEnvVar overrides the plugins root — primarily for tests and scratch
// setups that must not touch the real ~/.config tree (the same pattern as
// cats-todo's CATS_TODO_CONFIG_DIR).
const DirEnvVar = "CATS_PLUGINS_DIR"

// Launch-time environment for a plugin's pane (set by `catctl plugin run` via
// tab.create's env params, on top of the CATS_PANE_ID / CATS_CONTROL_SOCKET
// every pane gets): which plugin is running and where its files live, so a
// binary can find its own assets without argv[0] games.
const (
	IDEnvVar      = "CATS_PLUGIN_ID"
	DirPathEnvVar = "CATS_PLUGIN_DIR"
)

// sourceMetaName records install provenance inside an installed plugin's dir.
// Dot-prefixed so List's entry scan (and the user's eye) skips it naturally;
// absent for linked plugins, whose "source" is the symlink itself.
const sourceMetaName = ".cats-plugin-source.json"

// sourceMeta is what Install leaves behind for `plugin list` display and a
// future `plugin update` (the URL is enough to re-fetch, the ref to re-pin).
type sourceMeta struct {
	Source string `json:"source"` // what the user typed (owner/repo or URL)
	URL    string `json:"url"`    // the resolved clone URL
	Ref    string `json:"ref,omitempty"`
}

// Root resolves the plugins directory: $CATS_PLUGINS_DIR >
// $XDG_CONFIG_HOME/cats/plugins > ~/.config/cats/plugins — the same
// config-home chain as config.DefaultPath, because a plugin set is
// configuration (what the user chose to have), not state (what the server
// remembers).
func Root() (string, error) {
	if dir := os.Getenv(DirEnvVar); dir != "" {
		return dir, nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "cats", "plugins"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".config", "cats", "plugins"), nil
}

// Installed is one plugin as the host sees it on disk.
type Installed struct {
	Manifest
	// Dir is the path launches resolve against: the entry under the plugins
	// root for installs, or the symlink target for linked checkouts (so
	// relative action commands hit the real working tree).
	Dir    string
	Linked bool
	Source string // install source ("" for linked plugins)
	Ref    string
	// Err is set instead of Manifest when the entry exists but its manifest
	// is missing or invalid — List surfaces the breakage rather than silently
	// hiding the directory.
	Err error
}

// List enumerates the plugins root. A missing root is an empty list, not an
// error (nothing has been installed yet). Entries are sorted by name for
// stable CLI output.
func List() ([]Installed, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Installed
	for _, e := range entries {
		name := e.Name()
		if name[0] == '.' {
			continue
		}
		inst, err := load(root, name)
		if err != nil {
			// Report entries that could plausibly be a plugin: directories (an
			// install with a bad manifest) and symlinks — including broken ones.
			// A broken symlink is a dev link whose checkout moved or was
			// deleted; it still occupies the id (Install's already-installed
			// check Lstats it), so hiding it here would leave the user with
			// "already installed" on one side and "no plugins installed" on the
			// other, and no way to uninstall from the dialog. Lstat (not Stat)
			// so the judgment is about the entry itself, not its target. Only
			// plain files squatting in the root are skipped.
			fi, lerr := os.Lstat(filepath.Join(root, name))
			if lerr != nil || (fi.Mode()&os.ModeSymlink == 0 && !fi.IsDir()) {
				continue
			}
			// load's partial result already carries Dir and Linked as far as it
			// got; keep those and just mark the breakage.
			inst.Err = err
			inst.ID = name
		}
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Get loads one installed plugin by id.
func Get(id string) (Installed, error) {
	root, err := Root()
	if err != nil {
		return Installed{}, err
	}
	if _, err := os.Lstat(filepath.Join(root, id)); err != nil {
		return Installed{}, fmt.Errorf("plugin %q is not installed (try `catctl plugin list`)", id)
	}
	return load(root, id)
}

// load reads the entry root/<id> into an Installed.
func load(root, id string) (Installed, error) {
	entry := filepath.Join(root, id)
	inst := Installed{Dir: entry}

	fi, err := os.Lstat(entry)
	if err != nil {
		return inst, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		inst.Linked = true
		// Launches must resolve against the real checkout: a plugin binary
		// that inspects its own path (or a build that wrote into ./bin) lives
		// there, not under the plugins root.
		target, err := filepath.EvalSymlinks(entry)
		if err != nil {
			return inst, fmt.Errorf("broken link: %w", err)
		}
		inst.Dir = target
	}

	m, err := LoadManifest(inst.Dir)
	if err != nil {
		return inst, err
	}
	inst.Manifest = m

	if b, err := os.ReadFile(filepath.Join(inst.Dir, sourceMetaName)); err == nil {
		var sm sourceMeta
		if json.Unmarshal(b, &sm) == nil {
			inst.Source, inst.Ref = sm.Source, sm.Ref
		}
	}
	return inst, nil
}

// Uninstall removes root/<id> and reports what happened. A linked plugin only
// loses its symlink — the checkout it points at is the developer's working
// tree and is never touched.
func Uninstall(id string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	entry := filepath.Join(root, id)
	fi, err := os.Lstat(entry)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("plugin %q is not installed", id)
	}
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(entry)
		if err := os.Remove(entry); err != nil {
			return "", err
		}
		return fmt.Sprintf("unlinked %s (checkout left in place at %s)", id, target), nil
	}
	if err := os.RemoveAll(entry); err != nil {
		return "", err
	}
	return fmt.Sprintf("removed %s", id), nil
}

// ActionArgv resolves an action's command into an absolute-program argv:
// manifests use plugin-root-relative paths ("./bin/tool", herdr convention),
// which only mean something once anchored to the plugin's directory — the
// launched pane's cwd is the *user's* project, not the plugin root.
func ActionArgv(inst Installed, a Action) []string {
	argv := append([]string(nil), a.Command...)
	if !filepath.IsAbs(argv[0]) {
		argv[0] = filepath.Join(inst.Dir, argv[0])
	}
	return argv
}

// CompletionArgv anchors a dynamic completion's command to the plugin root, the
// same way ActionArgv anchors an action's — the completer runs while the user's
// shell sits in some unrelated directory, so "./bin/tool" has to be made
// absolute here or it resolves against the wrong tree.
func CompletionArgv(inst Installed, c Completion) []string {
	argv := append([]string(nil), c.Command...)
	if len(argv) > 0 && !filepath.IsAbs(argv[0]) {
		argv[0] = filepath.Join(inst.Dir, argv[0])
	}
	return argv
}

// LookupCompletion finds the installed plugin that claims a shell command name.
func LookupCompletion(binary string) (BinaryCompletion, bool) {
	for _, bc := range Completions() {
		if bc.Completion.Binary == binary {
			return bc, true
		}
	}
	return BinaryCompletion{}, false
}

// BinaryCompletion pairs a completion entry with the plugin that declared it —
// what the shell-script generator needs to name the command and what
// __complete needs to run it.
type BinaryCompletion struct {
	Plugin     Installed
	Completion Completion
}

// Completions enumerates every completion entry across installed plugins.
// Broken entries (Err set) are skipped — their manifest never parsed, so they
// declare nothing. Two plugins claiming one binary name is resolved first-wins
// in List's sorted order, so the winner is stable rather than
// filesystem-dependent, and the generated shell script never registers one
// command twice.
func Completions() []BinaryCompletion {
	plugins, err := List()
	if err != nil {
		return nil
	}
	var out []BinaryCompletion
	seen := map[string]bool{}
	for _, p := range plugins {
		if p.Err != nil {
			continue
		}
		for _, c := range p.Completions {
			if seen[c.Binary] {
				continue
			}
			seen[c.Binary] = true
			out = append(out, BinaryCompletion{Plugin: p, Completion: c})
		}
	}
	return out
}
