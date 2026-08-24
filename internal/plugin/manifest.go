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
	Platforms   []string     `toml:"platforms"`
	Build       []BuildStep  `toml:"build"`
	Actions     []Action     `toml:"actions"`
	Completions []Completion `toml:"completions"`
	// Bin lists plugin-root-relative paths (herdr convention: "./bin/tool")
	// whose base names the host exposes on the user's PATH by symlinking them
	// into the cats bin directory (~/.cats/bin). Declared explicitly rather
	// than derived — from bin/ contents (a build can produce helpers not meant
	// for PATH) or from [[completions]] (claiming to complete a command is not
	// a claim to provide it).
	Bin []string `toml:"bin"`
	// Shell maps a shell name ("zsh", "bash", "fish") to a plugin-root-relative
	// script that `catctl shellinit <shell>` emits a source line for. This is
	// how a plugin contributes code that must run *inside* the user's shell —
	// functions that cd, keybindings, hooks — which no spawned process can do
	// on its behalf. Emission happens at shell startup from the installed set,
	// so uninstalling a plugin needs no rc-file surgery to undo it.
	Shell map[string]string `toml:"shell"`
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

// Completion is one [[completions]] entry: the plugin's claim that it knows how
// to complete a command name in the user's shell. `catctl completion <shell>`
// emits a registration for every declared Binary, and the shell then routes
// that command's completions back through `catctl __complete --for <binary>`.
// Going through catctl rather than binding the plugin binary directly keeps one
// protocol in the shell script and lets a plugin choose either style below.
//
// Two styles, checked in this order:
//
//   - Dynamic: Command is an argv (plugin-root-relative, like an action's) that
//     speaks the completion protocol — catctl appends the words typed so far and
//     the program prints "value<TAB>description" lines plus a trailing directive
//     (":files" / ":dirs" / ":nofiles"). Descriptions and context-sensitive
//     candidates are only possible this way.
//   - Static: Subcommands and Flags are fixed candidate lists that catctl serves
//     itself. Subcommands are offered only in the first word position, Flags
//     wherever the word starts with "-". Enough for a shell-script plugin that
//     has no completion code of its own.
type Completion struct {
	Binary      string   `toml:"binary"`
	Command     []string `toml:"command"`
	Subcommands []string `toml:"subcommands"`
	Flags       []string `toml:"flags"`
}

// Dynamic reports whether the entry delegates to a program (as opposed to the
// static Subcommands/Flags lists).
func (c Completion) Dynamic() bool { return len(c.Command) > 0 }

// binaryPattern bounds a completion's binary name: it is pasted into generated
// shell scripts as a word, so anything that could carry shell syntax (spaces,
// quotes, '$', separators) is rejected at manifest load rather than escaped at
// emit time — a manifest author should see the mistake, not a mangled script.
var binaryPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

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
	seenBinary := map[string]bool{}
	for i, c := range m.Completions {
		switch {
		case c.Binary == "":
			return fmt.Errorf("completions[%d]: missing binary", i)
		case !binaryPattern.MatchString(c.Binary) || strings.Contains(c.Binary, ".."):
			return fmt.Errorf("completions[%d]: invalid binary %q (letters, digits, '.', '_', '-'; no path separators)", i, c.Binary)
		case seenBinary[c.Binary]:
			return fmt.Errorf("duplicate completion binary %q", c.Binary)
		}
		seenBinary[c.Binary] = true
		if c.Dynamic() {
			if strings.TrimSpace(c.Command[0]) == "" {
				return fmt.Errorf("completions[%q]: command must name a program", c.Binary)
			}
			continue
		}
		if len(c.Subcommands) == 0 && len(c.Flags) == 0 {
			return fmt.Errorf("completions[%q]: needs a command, or subcommands/flags to offer", c.Binary)
		}
	}
	seenBin := map[string]bool{}
	for i, b := range m.Bin {
		rel, err := localRel(b)
		if err != nil {
			return fmt.Errorf("bin[%d]: %w", i, err)
		}
		// The base name becomes a filename in the shared bin directory, so it
		// gets the same syntax bound as a completion binary — and two entries
		// sharing a base would silently fight over one link.
		base := filepath.Base(rel)
		if !binaryPattern.MatchString(base) {
			return fmt.Errorf("bin[%d]: invalid name %q (letters, digits, '.', '_', '-')", i, base)
		}
		if seenBin[base] {
			return fmt.Errorf("duplicate bin name %q", base)
		}
		seenBin[base] = true
	}
	for shell, path := range m.Shell {
		if !slices.Contains(shellNames, shell) {
			return fmt.Errorf("shell: unknown shell %q (one of %s)", shell, strings.Join(shellNames, ", "))
		}
		if _, err := localRel(path); err != nil {
			return fmt.Errorf("shell.%s: %w", shell, err)
		}
	}
	return nil
}

// shellNames are the shells `catctl shellinit` can emit for — the same set
// `catctl completion` generates.
var shellNames = []string{"bash", "zsh", "fish"}

// localRel cleans a manifest path and enforces that it stays inside the plugin
// root: these paths are joined under the plugins root (bin link targets, shell
// source lines), so an absolute path or a ".." escape would let a manifest
// reach outside the tree the host manages.
func localRel(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	rel := filepath.Clean(p)
	if filepath.IsAbs(rel) || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path %q must be relative and stay inside the plugin", p)
	}
	return rel, nil
}

// FindCompletion resolves a completion entry by the command name typed in the
// shell.
func (m Manifest) FindCompletion(binary string) (Completion, bool) {
	for _, c := range m.Completions {
		if c.Binary == binary {
			return c, true
		}
	}
	return Completion{}, false
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
