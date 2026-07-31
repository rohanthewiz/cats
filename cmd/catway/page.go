package main

import (
	"encoding/json"
	"log"
	"sort"
	"strings"

	"github.com/rohanthewiz/cats/internal/buildinfo"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/theme"
)

// renderPage bakes the front-end's server-side settings into the served HTML: a
// <style> that overrides the :root CSS custom properties (theme colours + font),
// a <script> that publishes the copy-mode keybindings as window.__catsKeys, and
// a <script> that publishes this binary's git identity as window.__catsBuild.
// All are injected just before </head> so they win the cascade / load before the
// app script runs. The base page keeps working with no injection (its stylesheet
// has fallback values, the JS has a default binding table, and the build badge
// is simply omitted), so this only ever narrows behaviour toward the config.
//
// The config file is operator-controlled and local, but we still sanitise the
// CSS pieces so a stray value can't break out of <style> into markup; the
// keybindings and build info ride through json.Marshal, whose default HTML
// escaping keeps a "</script>" in a value inert.
func renderPage(base []byte, cfg config.Config) []byte {
	inject := themeStyle(resolveTheme(cfg)) + keybindingsScript(cfg.Keybindings) + buildScript()
	html := string(base)
	if i := strings.LastIndex(html, "</head>"); i >= 0 {
		return []byte(html[:i] + inject + html[i:])
	}
	// No </head> (unexpected) — prepend so the settings still take effect.
	return []byte(inject + html)
}

// resolveTheme turns the config's theme *choices* (a name + sparse overrides +
// optional font) into the full effective palette, consulting the live theme
// registry (built-ins + plugin themes + user theme files). The registry is
// re-read on every call: renderPage runs only at startup, on config.set, and
// on reload, and re-reading is what lets a freshly installed theme plugin or a
// hand-edited theme file show up without a server restart. Problems degrade,
// never fail — a broken theme file is logged and skipped, an unknown name
// falls back to the default theme — because the page must always render.
func resolveTheme(cfg config.Config) theme.Theme {
	themes, warns := theme.Registry()
	for _, w := range warns {
		log.Printf("catway: theme: %v", w)
	}
	t, ok := theme.Resolve(cfg.Theme.Name, cfg.Theme.Colors, themes)
	if !ok {
		log.Printf("catway: theme %q not found — using %q", cfg.Theme.Name, t.Name)
	}
	// Font precedence: explicit config override > the theme's own > default.
	if cfg.Theme.Font != "" {
		t.Font = cfg.Theme.Font
	}
	if t.Font == "" {
		t.Font = theme.DefaultFont
	}
	return t
}

// themeStyle renders the resolved theme as a :root override plus a font rule.
// Keys are emitted sorted so the same theme always renders the same bytes
// (map order would otherwise shuffle the block between renders).
func themeStyle(t theme.Theme) string {
	names := make([]string, 0, len(t.Colors))
	for name := range t.Colors {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("<style id=\"cats-config-theme\">:root{")
	for _, name := range names {
		n := sanitizeCSSName(name)
		v := sanitizeCSSValue(t.Colors[name])
		if n == "" || v == "" {
			continue
		}
		b.WriteString("--")
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(v)
		b.WriteByte(';')
	}
	b.WriteString("}")
	if font := sanitizeCSSValue(t.Font); font != "" {
		b.WriteString("html,body{font-family:")
		b.WriteString(font)
		b.WriteString(";}")
	}
	b.WriteString("</style>\n")
	return b.String()
}

// keybindingsScript publishes the copy-mode bindings for the front-end.
func keybindingsScript(k config.Keybindings) string {
	// json.Marshal escapes <, >, & (SetEscapeHTML default), so a value can't
	// close the <script>. A marshal failure (impossible for a string map) drops
	// the block, and index.html falls back to its built-in table.
	data, err := json.Marshal(map[string]any{"copyMode": k.CopyMode})
	if err != nil {
		return ""
	}
	return "<script id=\"cats-config-keys\">window.__catsKeys=" + string(data) + ";</script>\n"
}

// buildScript publishes the binary's git identity for the sidebar's build badge.
// An unknown hash (built outside a git checkout) emits nothing at all, and the
// front end then leaves the badge off rather than showing a placeholder.
func buildScript() string {
	bi := buildinfo.Get()
	if bi.Hash == "" {
		return ""
	}
	data, err := json.Marshal(bi)
	if err != nil {
		return ""
	}
	return "<script id=\"cats-build\">window.__catsBuild=" + string(data) + ";</script>\n"
}

// sanitizeCSSName keeps a CSS custom-property name to [A-Za-z0-9-] so it stays a
// valid identifier and can't carry markup.
func sanitizeCSSName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		default:
			return -1
		}
	}, s)
}

// sanitizeCSSValue drops characters that could end the declaration, the rule, or
// the <style> element, leaving an ordinary colour/font value.
func sanitizeCSSValue(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', '{', '}', ';', '\n', '\r':
			return -1
		default:
			return r
		}
	}, s)
	return strings.TrimSpace(s)
}
