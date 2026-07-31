package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every built-in validates, normalizes to the full canonical key set, and the
// set carries no duplicates — the picker and the settings modal key off this.
func TestBuiltinsComplete(t *testing.T) {
	keys := Keys()
	seen := map[string]bool{}
	for _, b := range BuiltIns() {
		if seen[b.Name] {
			t.Errorf("duplicate builtin name %q", b.Name)
		}
		seen[b.Name] = true
		if err := b.Validate(); err != nil {
			t.Errorf("builtin %s: %v", b.Name, err)
		}
		n := Normalize(b)
		for _, k := range keys {
			if strings.TrimSpace(n.Colors[k]) == "" {
				t.Errorf("builtin %s: normalized palette missing %q", b.Name, k)
			}
		}
		// Authored keys must all be canonical — a typo'd key would silently
		// never render (the stylesheet has no var for it).
		canon := map[string]bool{}
		for _, k := range keys {
			canon[k] = true
		}
		for k := range b.Colors {
			if !canon[k] {
				t.Errorf("builtin %s: unknown color key %q", b.Name, k)
			}
		}
	}
	if !seen[DefaultName] {
		t.Fatalf("no builtin named DefaultName %q", DefaultName)
	}
}

// The default theme's normalized palette is exactly its authored palette: it is
// fully hand-authored (pre-themes fidelity), so derivation must add nothing.
func TestDefaultFullyAuthored(t *testing.T) {
	d, ok := byName(BuiltIns(), DefaultName)
	if !ok {
		t.Fatal("default builtin missing")
	}
	if len(d.Colors) != len(Keys()) {
		t.Fatalf("default authors %d keys, canon has %d", len(d.Colors), len(Keys()))
	}
}

// Normalize fills a sparse theme: chained surface derivation, translucent
// accents, and label defaulting. The input map is not mutated.
func TestNormalizeDerivation(t *testing.T) {
	in := Theme{Name: "min", Colors: map[string]string{
		"bg": "#102030", "fg": "#d0d0d0", "muted": "#808080", "line": "#404040",
		"accent": "#4080ff", "ok": "#00aa00", "warn": "#aaaa00", "err": "#aa0000",
	}}
	n := Normalize(in)
	if got := n.Colors["chrome"]; got != "#102030" { // chrome ← panel2 ← panel ← bg
		t.Errorf("chrome = %q, want bg", got)
	}
	if got := n.Colors["sel-fill"]; got != "rgba(64,128,255,0.3)" {
		t.Errorf("sel-fill = %q", got)
	}
	if got := n.Colors["todo"]; got != "#aaaa00" {
		t.Errorf("todo = %q, want warn", got)
	}
	if n.Label != "min" {
		t.Errorf("label = %q", n.Label)
	}
	if len(in.Colors) != 8 {
		t.Error("Normalize mutated its input")
	}
}

// Resolve: named theme + overrides; unknown names fall back to the default and
// report !ok; empty name is the default silently.
func TestResolve(t *testing.T) {
	themes := BuiltIns()
	got, ok := Resolve("tokyo-night", map[string]string{"accent": "#ffffff"}, themes)
	if !ok || got.Name != "tokyo-night" || got.Colors["accent"] != "#ffffff" {
		t.Fatalf("resolve tokyo-night: ok=%v name=%s accent=%s", ok, got.Name, got.Colors["accent"])
	}
	if got.Colors["bg"] != "#1a1b26" {
		t.Errorf("non-overridden key lost: bg=%q", got.Colors["bg"])
	}
	got, ok = Resolve("no-such-theme", nil, themes)
	if ok || got.Name != DefaultName {
		t.Fatalf("unknown theme: ok=%v name=%s (want fallback to default)", ok, got.Name)
	}
	if got, ok = Resolve("", nil, themes); !ok || got.Name != DefaultName {
		t.Fatalf("empty name: ok=%v name=%s", ok, got.Name)
	}
}

// A theme file round-trips through Save/LoadDir, keeping sparseness, and the
// dark flag auto-detects from bg when absent.
func TestSaveLoadRoundtrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	in := Theme{Name: "my-light", Label: "My Light", Colors: map[string]string{
		"bg": "#fafafa", "fg": "#202020", "muted": "#707070", "line": "#d0d0d0",
		"accent": "#0066cc", "ok": "#008800", "warn": "#886600", "err": "#bb2222",
	}}
	path, err := Save(in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// dark: false was authored (zero value) so it round-trips explicitly; strip
	// it from the file to exercise auto-detection off the light bg.
	data, _ := os.ReadFile(path)
	stripped := strings.ReplaceAll(string(data), "dark: false\n", "")
	if err := os.WriteFile(path, []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, warns := LoadDir(UserDir(), SourceUser)
	if len(warns) != 0 || len(ts) != 1 {
		t.Fatalf("LoadDir: themes=%d warns=%v", len(ts), warns)
	}
	got := ts[0]
	if got.Name != "my-light" || got.Dark || got.Source != SourceUser || len(got.Colors) != 8 {
		t.Fatalf("roundtrip: %+v", got)
	}

	if err := Delete("my-light"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ts, _ := LoadDir(UserDir(), SourceUser); len(ts) != 0 {
		t.Fatal("theme survived Delete")
	}
	if err := Delete("my-light"); err == nil {
		t.Fatal("double Delete should error")
	}
}

// A broken theme file is a warning, not a failure, and doesn't hide siblings.
func TestLoadDirSkipsBroken(t *testing.T) {
	dir := t.TempDir()
	good := "name: good\ncolors: {bg: '#111111', fg: '#eeeeee', muted: '#888888', line: '#333333', accent: '#44aa88', ok: '#44aa44', warn: '#aaaa44', err: '#aa4444'}\n"
	if err := os.WriteFile(filepath.Join(dir, "good.yaml"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("colors: {bg: '#111111'}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, warns := LoadDir(dir, SourceUser)
	if len(ts) != 1 || ts[0].Name != "good" || len(warns) != 1 {
		t.Fatalf("themes=%v warns=%v", ts, warns)
	}
	if !ts[0].Dark {
		t.Error("dark not auto-detected from #111111 bg")
	}
}

// Registry precedence: a user theme shadows a same-named builtin in place, and
// a plugin theme is discovered from <root>/<id>/themes.
func TestRegistryPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("CATS_PLUGINS_DIR", filepath.Join(home, "plugins"))

	pdir := filepath.Join(home, "plugins", "my-plug", "themes")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	plugTheme := "colors: {bg: '#010101', fg: '#efefef', muted: '#888888', line: '#333333', accent: '#5588ff', ok: '#44aa44', warn: '#aaaa44', err: '#aa4444'}\n"
	if err := os.WriteFile(filepath.Join(pdir, "plug-night.yaml"), []byte(plugTheme), 0o644); err != nil {
		t.Fatal(err)
	}

	// A user theme reusing the darcula name shadows the builtin.
	udir := filepath.Join(home, "cats", "themes")
	if err := os.MkdirAll(udir, 0o755); err != nil {
		t.Fatal(err)
	}
	userTheme := "colors: {bg: '#222222', fg: '#cccccc', muted: '#888888', line: '#333333', accent: '#ff8800', ok: '#44aa44', warn: '#aaaa44', err: '#aa4444'}\n"
	if err := os.WriteFile(filepath.Join(udir, "darcula.yaml"), []byte(userTheme), 0o644); err != nil {
		t.Fatal(err)
	}

	themes, warns := Registry()
	if len(warns) != 0 {
		t.Fatalf("warns: %v", warns)
	}
	byNameIdx := map[string]int{}
	for i, th := range themes {
		byNameIdx[th.Name] = i
	}
	if i, ok := byNameIdx["plug-night"]; !ok {
		t.Fatal("plugin theme not discovered")
	} else if themes[i].Source != "plugin:my-plug" {
		t.Errorf("plugin theme source = %q", themes[i].Source)
	}
	di, ok := byNameIdx["darcula"]
	if !ok {
		t.Fatal("darcula missing")
	}
	if themes[di].Source != SourceUser || themes[di].Colors["accent"] != "#ff8800" {
		t.Errorf("user theme did not shadow builtin: %+v", themes[di])
	}
	if di != 1 {
		t.Errorf("shadowed darcula moved from its builtin slot: index %d", di)
	}
	// No duplicate names survive.
	if len(byNameIdx) != len(themes) {
		t.Error("registry has duplicate names")
	}
}

func TestDarkBG(t *testing.T) {
	for bg, want := range map[string]bool{
		"#000000": true, "#ffffff": false, "#1f2420": true, "#fdf6e3": false,
		"#abc": false, "not-a-color": true,
	} {
		if got := DarkBG(bg); got != want {
			t.Errorf("DarkBG(%q) = %v, want %v", bg, got, want)
		}
	}
}
