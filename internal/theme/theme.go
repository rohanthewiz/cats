// Package theme is the catway UI's named color themes: the built-in palettes,
// the user's custom theme files, and themes shipped inside plugin directories.
//
// A theme is a flat map of CSS custom-property names (without the leading
// "--") to color values — the same vocabulary config.Theme.Colors always used.
// The package's job is to answer "what full palette should the served page
// carry?" for a theme name plus per-key user overrides, so the render side
// (catway's themeStyle) stays a dumb emitter.
//
// Authoring a theme does not require all ~30 keys: only the eight core colors
// are required, and Normalize fills every missing key from a small derivation
// table (e.g. panel defaults to bg, sel-fill to a translucent accent). The
// built-ins hand-author the keys where taste matters and lean on the same
// derivations for the rest, so built-in and user themes go through one path.
//
// House style (matching the rest of the repo): stdlib errors/fmt, no serr.
package theme

import (
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultName is the theme catway uses when the config names none (or names
// one that doesn't exist): the original GoNotes-style dark green.
const DefaultName = "cats-green"

// DefaultFont is the font stack used when neither the config nor the theme
// sets one (the value index.html has always carried).
const DefaultFont = `ui-monospace, "SF Mono", Menlo, Consolas, monospace`

// Source values for Theme.Source. Plugin themes carry "plugin:<id>".
const (
	SourceBuiltin = "builtin"
	SourceUser    = "user"
)

// Theme is one named palette. Colors holds CSS custom-property values keyed by
// property name without the "--" prefix; a loaded theme may be sparse — callers
// render Normalize(t).Colors, never t.Colors directly.
type Theme struct {
	Name  string `yaml:"name" json:"name"`
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
	// Dark drives UI grouping only (light/dark sections in the picker); it has
	// no rendering effect. File themes may omit it — see Normalize.
	Dark   bool              `yaml:"dark" json:"dark"`
	Colors map[string]string `yaml:"colors" json:"colors"`
	Font   string            `yaml:"font,omitempty" json:"font,omitempty"`
	// Source records where the theme came from (builtin / user / plugin:<id>).
	// Set by the loaders, never read from a file — a file can't claim a source.
	Source string `yaml:"-" json:"source,omitempty"`
}

// requiredKeys are the colors a theme must define — everything else derives.
var requiredKeys = []string{"bg", "fg", "muted", "line", "accent", "ok", "warn", "err"}

// derivations fills absent keys from present ones, in order (later rules may
// consume earlier results, e.g. chrome ← panel2 ← panel ← bg). alpha > 0 turns
// the source color translucent — that's how the selection washes and hover
// flashes track a theme's accent instead of staying hardcoded rgba() literals.
//
//	bg ─┬─ panel ── panel2 ── chrome        fg ─┬─ fg-strong ─┬─ fg-bright
//	    ├─ term-bg                              ├─ fg-soft    └─ chrome-fg
//	    └─ accent-fg                            ├─ chrome-fg-dim
//	 line ── chrome-focus                       ├─ term-fg
//	                                            └─ hover/scroll-thumb-idle (α)
//	muted ── ws-heading ── branch
//	 accent ─ heading · accent-dim(α) · done · sel-fill(α) · cm-cursor(α) · scroll-thumb(α)
var derivations = []struct {
	key   string
	from  string
	alpha float64 // 0 = copy verbatim; >0 = rgba(source, alpha)
}{
	{"panel", "bg", 0},
	{"panel2", "panel", 0},
	{"chrome", "panel2", 0},
	{"chrome-focus", "line", 0},
	{"term-bg", "bg", 0},
	{"term-fg", "fg", 0},
	{"heading", "accent", 0},
	// The workspace group headers inside the Panes list: a second tier of
	// heading, so a theme that doesn't author one falls back to plain muted
	// label text — which is what those rows were before they were themeable.
	{"ws-heading", "muted", 0},
	// The git branch in a pane header, one more tier of secondary label. It
	// follows ws-heading (and so, for a theme that authors neither, muted)
	// because both are the same kind of thing: a warm counterweight to the
	// accent, naming where something lives rather than what it is doing.
	{"branch", "ws-heading", 0},
	{"accent-dim", "accent", 0.45},
	{"accent-fg", "bg", 0},
	{"todo", "warn", 0},
	{"done", "accent", 0},
	{"fg-strong", "fg", 0},
	{"fg-soft", "fg", 0},
	{"fg-bright", "fg-strong", 0},
	{"chrome-fg", "fg-strong", 0},
	{"chrome-fg-dim", "fg", 0},
	{"err-bg", "panel2", 0},
	{"err-fg", "err", 0},
	{"hover", "fg", 0.16},
	{"sel-fill", "accent", 0.30},
	{"cm-cursor", "accent", 0.95},
	{"scroll-thumb", "accent", 0.60},
	{"scroll-thumb-idle", "fg", 0.25},
}

// Keys is the full canonical key set (required + derived), sorted — the
// settings modal's row schema and the documented theme vocabulary.
func Keys() []string {
	out := append([]string(nil), requiredKeys...)
	for _, d := range derivations {
		out = append(out, d.key)
	}
	sort.Strings(out)
	return out
}

// namePattern bounds theme names the same way plugin ids are bounded: they
// become file names and CSS-adjacent identifiers, so keep them boring.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidName reports whether s is an acceptable theme name.
func ValidName(s string) bool { return namePattern.MatchString(s) }

// Validate checks a theme is renderable: a sane name and the required colors
// present. Color *values* are deliberately not validated beyond non-empty —
// any CSS color expression is welcome; render-side sanitizing keeps a stray
// value from breaking out of the <style> block.
func (t Theme) Validate() error {
	if !ValidName(t.Name) {
		return fmt.Errorf("theme name %q: want lowercase letters, digits, hyphens", t.Name)
	}
	for _, k := range requiredKeys {
		if strings.TrimSpace(t.Colors[k]) == "" {
			return fmt.Errorf("theme %s: missing required color %q", t.Name, k)
		}
	}
	return nil
}

// Normalize returns t with every canonical key present, filling absent ones
// from the derivation table. The input is not mutated. Label defaults to the
// name; a caller that needs Dark auto-detected from bg does that at load time
// (see loadFile), since a bool can't distinguish "false" from "absent" here.
func Normalize(t Theme) Theme {
	out := t
	out.Colors = maps.Clone(t.Colors)
	if out.Colors == nil {
		out.Colors = map[string]string{}
	}
	for _, d := range derivations {
		if strings.TrimSpace(out.Colors[d.key]) != "" {
			continue
		}
		src := out.Colors[d.from]
		if d.alpha > 0 {
			src = withAlpha(src, d.alpha)
		}
		out.Colors[d.key] = src
	}
	if out.Label == "" {
		out.Label = out.Name
	}
	return out
}

// Resolve picks the named theme out of themes (falling back to DefaultName,
// then to the first entry), normalizes it, and overlays the per-key user
// overrides. It returns the effective theme plus ok=false when name was
// non-empty but unknown — the caller logs, the user still gets a usable UI.
func Resolve(name string, overrides map[string]string, themes []Theme) (Theme, bool) {
	ok := true
	t, found := byName(themes, name)
	if !found {
		ok = name == "" // absent name is not an error, a missing theme is
		if t, found = byName(themes, DefaultName); !found && len(themes) > 0 {
			t = themes[0]
		}
	}
	t = Normalize(t)
	for k, v := range overrides {
		if strings.TrimSpace(v) != "" {
			t.Colors[k] = v
		}
	}
	return t, ok
}

func byName(themes []Theme, name string) (Theme, bool) {
	if name == "" {
		return Theme{}, false
	}
	for _, t := range themes {
		if t.Name == name {
			return t, true
		}
	}
	return Theme{}, false
}

// --- color math ----------------------------------------------------------------

// withAlpha renders a hex color as an rgba() with the given alpha. A value that
// isn't plain hex (already an rgba(), a named color) is returned unchanged —
// wrong alpha beats a broken declaration.
func withAlpha(hex string, alpha float64) string {
	r, g, b, ok := parseHex(hex)
	if !ok {
		return hex
	}
	return fmt.Sprintf("rgba(%d,%d,%d,%s)", r, g, b,
		strconv.FormatFloat(alpha, 'g', 3, 64))
}

// parseHex reads #rgb or #rrggbb.
func parseHex(s string) (r, g, b int, ok bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "#") {
		return 0, 0, 0, false
	}
	s = s[1:]
	switch len(s) {
	case 3:
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	case 6:
	default:
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v >> 16), int(v >> 8 & 0xff), int(v & 0xff), true
}

// DarkBG reports whether a background color reads as dark (relative luminance
// below half) — how a file theme's absent "dark:" is auto-filled. Unparseable
// values count as dark, matching the app's dark-first history.
func DarkBG(bg string) bool {
	r, g, b, ok := parseHex(bg)
	if !ok {
		return true
	}
	// Rec. 601 luma is plenty for a binary light/dark call.
	return (299*r+587*g+114*b)/1000 < 128
}
