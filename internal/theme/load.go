package theme

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/rohanthewiz/cats/internal/plugin"
)

// Theme files: one YAML document per theme.
//
//	name: my-theme        # optional — defaults to the file name's stem
//	label: My Theme
//	dark: true            # optional — auto-detected from bg's luminance
//	colors:
//	  bg: "#101418"
//	  fg: "#d4dae2"
//	  ...                 # only the eight required keys are mandatory
//	font: "JetBrains Mono, monospace"   # optional
//
// User themes live in the config home ($XDG_CONFIG_HOME/cats/themes, falling
// back to ~/.config/cats/themes). Plugin themes are discovered by convention
// from <plugins-root>/<id>/themes/*.yaml — a directory scan, not a manifest
// field, so the server keeps its "never parses a plugin manifest" property.

// UserDir is where the user's own theme files live. "" when no config home is
// resolvable (no $XDG_CONFIG_HOME and no home dir) — callers then skip user
// themes rather than fail.
func UserDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "cats", "themes")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "cats", "themes")
}

// fileTheme mirrors Theme for decoding, with Dark as a pointer so an absent
// key is distinguishable from an explicit false and can be auto-detected.
type fileTheme struct {
	Name   string            `yaml:"name"`
	Label  string            `yaml:"label"`
	Dark   *bool             `yaml:"dark"`
	Colors map[string]string `yaml:"colors"`
	Font   string            `yaml:"font"`
}

// loadFile reads one theme file. The file's stem names the theme when the
// document doesn't; an explicit name must agree with nothing — files are the
// unit of storage, names the unit of identity, and Registry resolves clashes.
func loadFile(path, source string) (Theme, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Theme{}, err
	}
	var ft fileTheme
	if err := yaml.Unmarshal(data, &ft); err != nil {
		return Theme{}, fmt.Errorf("%s: %w", path, err)
	}
	t := Theme{Name: ft.Name, Label: ft.Label, Colors: ft.Colors, Font: ft.Font, Source: source}
	if t.Name == "" {
		t.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if ft.Dark != nil {
		t.Dark = *ft.Dark
	} else {
		t.Dark = DarkBG(t.Colors["bg"])
	}
	if err := t.Validate(); err != nil {
		return Theme{}, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

// LoadDir loads every *.yaml/*.yml theme in dir, tagging each with source. A
// missing directory is an empty result. Broken files are skipped and reported
// in warns so one bad theme can't take the set down.
func LoadDir(dir, source string) (themes []Theme, warns []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			warns = append(warns, err)
		}
		return nil, warns
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if ext := filepath.Ext(name); ext != ".yaml" && ext != ".yml" {
			continue
		}
		t, err := loadFile(filepath.Join(dir, name), source)
		if err != nil {
			warns = append(warns, err)
			continue
		}
		themes = append(themes, t)
	}
	sort.Slice(themes, func(i, j int) bool { return themes[i].Name < themes[j].Name })
	return themes, warns
}

// loadPluginThemes scans <plugins-root>/<id>/themes/ for every installed
// plugin. Entries under the root may be symlinks (linked dev checkouts), so
// each is Stat'ed rather than trusting ReadDir's type bit.
func loadPluginThemes() (themes []Theme, warns []error) {
	root, err := plugin.Root()
	if err != nil {
		return nil, []error{err}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			warns = append(warns, err)
		}
		return nil, warns
	}
	for _, e := range entries {
		id := e.Name()
		if strings.HasPrefix(id, ".") {
			continue
		}
		dir := filepath.Join(root, id, "themes")
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue // no themes shipped — the common case
		}
		ts, ws := LoadDir(dir, "plugin:"+id)
		themes = append(themes, ts...)
		warns = append(warns, ws...)
	}
	return themes, warns
}

// Registry returns every available theme: built-ins in presentation order,
// then plugin themes, then user themes (each group name-sorted). On a name
// clash the later source wins — user beats plugin beats builtin — so a user
// can restyle a built-in by saving a theme under its name.
func Registry() (themes []Theme, warns []error) {
	all := BuiltIns()
	pt, pw := loadPluginThemes()
	all = append(all, pt...)
	warns = append(warns, pw...)
	if dir := UserDir(); dir != "" {
		ut, uw := LoadDir(dir, SourceUser)
		all = append(all, ut...)
		warns = append(warns, uw...)
	}
	// Later entries shadow earlier ones in place, keeping the winner at the
	// original position so built-in ordering survives a user override.
	index := map[string]int{}
	for _, t := range all {
		if i, ok := index[t.Name]; ok {
			themes[i] = t
			continue
		}
		index[t.Name] = len(themes)
		themes = append(themes, t)
	}
	return themes, warns
}

// Save writes t as a user theme file (<UserDir>/<name>.yaml), creating the
// directory as needed, and returns the path. Colors are saved as authored —
// sparse themes stay sparse, so a hand-derived file round-trips small.
func Save(t Theme) (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	dir := UserDir()
	if dir == "" {
		return "", errors.New("save theme: no resolvable config home")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("save theme: %w", err)
	}
	data, err := yaml.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("marshal theme %s: %w", t.Name, err)
	}
	path := filepath.Join(dir, t.Name+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("save theme: %w", err)
	}
	return path, nil
}

// Delete removes the user theme file for name (both .yaml and .yml spellings).
// Only user themes are deletable — built-ins live in the binary and plugin
// themes belong to their plugin's directory.
func Delete(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("theme name %q: invalid", name)
	}
	dir := UserDir()
	if dir == "" {
		return errors.New("delete theme: no resolvable config home")
	}
	deleted := false
	for _, ext := range []string{".yaml", ".yml"} {
		err := os.Remove(filepath.Join(dir, name+ext))
		if err == nil {
			deleted = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("delete theme %s: %w", name, err)
		}
	}
	if !deleted {
		return fmt.Errorf("delete theme %s: no such user theme", name)
	}
	return nil
}
