// Package plugin is the cats plugin host: it installs, links, lists, and
// removes plugins, and resolves their actions into launchable argv vectors.
//
// The model is deliberately thin — a plugin is a directory holding a
// cats-plugin.toml manifest (the same shape as herdr's herdr-plugin.toml,
// which cats-todo was originally written against) plus whatever the manifest's
// build step produces. The host's whole job is directory management:
//
//	~/.config/cats/plugins/<id>/     ← real dir (install) or symlink (link)
//
// Unlike herdr, cats has no server-side action runner or plugin-pane
// lifecycle: an action is just an argv that `catctl plugin run` launches in a
// fresh tab via the §7 tab.create command's spawn params (command/cwd/env).
// The plugin process then talks back over the control socket like any other
// automation client (CATS_CONTROL_SOCKET is exported to every pane). That
// keeps the server plugin-agnostic — it never parses a manifest — and means a
// plugin binary is equally runnable by hand, which is how cats-todo already
// works.
package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// ManifestName is the manifest file every plugin root must carry.
const ManifestName = "cats-plugin.toml"

// Manifest mirrors herdr-plugin.toml so a herdr plugin ports by renaming the
// file and swapping the id/command names. Herdr's [[panes]] table is
// deliberately absent: cats actions run their TUI directly in the tab that
// launches them, so a separate server-run pane entrypoint has no meaning here.
type Manifest struct {
	ID          string `toml:"id"`
	Name        string `toml:"name"`
	Version     string `toml:"version"`
	Description string `toml:"description"`
	// MinCatsVersion is carried for forward compatibility but not enforced —
	// cats has no single server version constant yet; enforcing against the
	// wrong number would be worse than not enforcing.
	MinCatsVersion string `toml:"min_cats_version"`
	// Platforms limits where the plugin installs ("linux", "macos"; empty =
	// everywhere). herdr's names are kept, with Go's GOOS values accepted too.
	Platforms []string    `toml:"platforms"`
	Build     []BuildStep `toml:"build"`
	Actions   []Action    `toml:"actions"`
}

// BuildStep is one [[build]] entry: a command run in the plugin root at
// install/link time (typically a `go build` producing ./bin/<tool>).
type BuildStep struct {
	Command []string `toml:"command"`
}

// Action is one [[actions]] entry: a launchable entrypoint. Command paths are
// relative to the plugin root (herdr convention: "./bin/tool"), resolved by
// ActionArgv at launch time.
type Action struct {
	ID      string   `toml:"id"`
	Title   string   `toml:"title"`
	Command []string `toml:"command"`
}

// idPattern bounds what an id may look like. The id doubles as the install
// directory name, so anything that could escape the plugins root (separators,
// "..") or hide the entry (leading dot) is rejected outright rather than
// sanitized — a manifest author should see the mistake, not a mangled id.
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// LoadManifest reads and validates dir/cats-plugin.toml.
func LoadManifest(dir string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return m, err
	}
	if err := toml.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", ManifestName, err)
	}
	if err := m.Validate(); err != nil {
		return m, fmt.Errorf("%s: %w", ManifestName, err)
	}
	return m, nil
}

// Validate enforces the invariants the rest of the host relies on (id usable
// as a directory name, unique action ids). Zero actions is legal: a plugin
// can exist purely to ship passive assets — UI themes in themes/*.yaml — and
// `catctl plugin run` already reports cleanly when there is nothing to run.
func (m Manifest) Validate() error {
	switch {
	case m.ID == "":
		return fmt.Errorf("missing id")
	case !idPattern.MatchString(m.ID) || strings.Contains(m.ID, ".."):
		return fmt.Errorf("invalid id %q (letters, digits, '.', '_', '-'; no leading dot or '..')", m.ID)
	case m.Version == "":
		return fmt.Errorf("missing version")
	}
	seen := map[string]bool{}
	for i, a := range m.Actions {
		if a.ID == "" {
			return fmt.Errorf("actions[%d]: missing id", i)
		}
		if seen[a.ID] {
			return fmt.Errorf("duplicate action id %q", a.ID)
		}
		seen[a.ID] = true
		if len(a.Command) == 0 || strings.TrimSpace(a.Command[0]) == "" {
			return fmt.Errorf("action %q: command must name a program", a.ID)
		}
	}
	for i, b := range m.Build {
		if len(b.Command) == 0 || strings.TrimSpace(b.Command[0]) == "" {
			return fmt.Errorf("build[%d]: command must name a program", i)
		}
	}
	return nil
}

// SupportsPlatform reports whether the manifest allows goos. herdr spells
// darwin "macos", so both names are accepted; an empty list allows everything.
func (m Manifest) SupportsPlatform(goos string) bool {
	if len(m.Platforms) == 0 {
		return true
	}
	names := []string{goos}
	if goos == "darwin" {
		names = append(names, "macos")
	}
	for _, p := range m.Platforms {
		if slices.Contains(names, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// FindAction resolves an action by id; id "" picks the first action (the
// common single-action plugin needs no action name at all).
func (m Manifest) FindAction(id string) (Action, bool) {
	if id == "" {
		if len(m.Actions) == 0 {
			return Action{}, false
		}
		return m.Actions[0], true
	}
	for _, a := range m.Actions {
		if a.ID == id {
			return a, true
		}
	}
	return Action{}, false
}

// currentPlatformSupported is SupportsPlatform against this build's GOOS,
// split out so tests can exercise the mapping without cross-compiling.
func (m Manifest) currentPlatformSupported() bool {
	return m.SupportsPlatform(runtime.GOOS)
}
