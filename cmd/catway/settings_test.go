//go:build ghostty

package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/theme"
)

// expectOK reads the next down message as a successful CmdResult.
func expectOK(t *testing.T, c *client, what string) *browserproto.CmdResult {
	t.Helper()
	r, ok := recvDown(t, c).(*browserproto.CmdResult)
	if !ok || !r.Ok {
		t.Fatalf("%s should ack ok, got %#v", what, r)
	}
	return r
}

// expectThemePush reads the next down message as the post-save theme broadcast.
func expectThemePush(t *testing.T, c *client) *browserproto.Theme {
	t.Helper()
	m, ok := recvDown(t, c).(*browserproto.Theme)
	if !ok {
		t.Fatalf("expected a theme broadcast, got %#v", m)
	}
	return m
}

// decodeData unmarshals a CmdResult's payload into the expected result type.
func decodeData[T any](t *testing.T, r *browserproto.CmdResult) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(r.Data, &v); err != nil {
		t.Fatalf("decode result data: %v", err)
	}
	return v
}

// config.set merges the live-appliable sections onto the current config,
// persists YAML that reloads to the same state (sparse overrides — unnamed
// colors are NOT baked into the file), replies, then broadcasts the effective
// theme. config.get then reflects the saved state.
func TestConfigSetPersists(t *testing.T) {
	o, c := newPendingHarness()
	path := filepath.Join(t.TempDir(), "config.yaml")
	o.cfg = config.Default()
	o.cfgPath = path

	o.handleCmd(c, cmd(t, "c1", browserproto.CmdConfigSet, browserproto.ConfigSetParams{
		Theme:    &browserproto.ConfigTheme{Colors: map[string]string{"bg": "#000000"}, Font: "monospace"},
		CopyMode: map[string][]string{"yank": {"y", "c"}},
	}))
	expectOK(t, c, "config.set")
	push := expectThemePush(t, c)
	if push.Colors["bg"] != "#000000" || push.Font != "monospace" {
		t.Fatalf("theme broadcast should carry the override: %+v", push)
	}

	got, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload saved config: %v", err)
	}
	if got.Theme.Colors["bg"] != "#000000" || got.Theme.Font != "monospace" {
		t.Fatalf("theme not persisted: %+v", got.Theme)
	}
	if len(got.Theme.Colors) != 1 {
		t.Fatalf("file should store only the override, got %v", got.Theme.Colors)
	}
	if !reflect.DeepEqual(got.Keybindings.CopyMode["yank"], []string{"y", "c"}) {
		t.Fatalf("copy-mode rebind not persisted: %v", got.Keybindings.CopyMode["yank"])
	}

	// config.get reflects the applied state: effective palette + raw overrides.
	o.handleCmd(c, cmd(t, "c2", browserproto.CmdConfigGet, nil))
	r := expectOK(t, c, "config.get")
	res := decodeData[browserproto.ConfigGetResult](t, r)
	if res.Theme.Name != theme.DefaultName || res.Theme.Colors["bg"] != "#000000" {
		t.Fatalf("config.get theme = %s/%s", res.Theme.Name, res.Theme.Colors["bg"])
	}
	if res.Theme.Colors["accent"] != "#4db380" {
		t.Fatalf("effective palette should keep theme values: accent=%q", res.Theme.Colors["accent"])
	}
	if !reflect.DeepEqual(res.ThemeOverrides, map[string]string{"bg": "#000000"}) {
		t.Fatalf("overrides = %v", res.ThemeOverrides)
	}
	if len(res.Themes) < 10 {
		t.Fatalf("themes registry looks empty: %d entries", len(res.Themes))
	}
}

// Naming a theme in config.set is a switch: the stored overrides are replaced
// (here: cleared), and the broadcast carries the new theme's palette.
func TestConfigSetThemeSwitch(t *testing.T) {
	o, c := newPendingHarness()
	o.cfg = config.Default()
	o.cfg.Theme.Colors = map[string]string{"bg": "#000000"} // stale override
	o.cfgPath = filepath.Join(t.TempDir(), "config.yaml")

	o.handleCmd(c, cmd(t, "c1", browserproto.CmdConfigSet, browserproto.ConfigSetParams{
		Theme: &browserproto.ConfigTheme{Name: "tokyo-night"},
	}))
	r := expectOK(t, c, "config.set")
	res := decodeData[browserproto.ConfigGetResult](t, r)
	if res.Theme.Name != "tokyo-night" || res.Theme.Colors["bg"] != "#1a1b26" {
		t.Fatalf("switch not applied: %s bg=%s", res.Theme.Name, res.Theme.Colors["bg"])
	}
	if len(res.ThemeOverrides) != 0 {
		t.Fatalf("switch must shed stale overrides, kept %v", res.ThemeOverrides)
	}
	if push := expectThemePush(t, c); push.Name != "tokyo-night" {
		t.Fatalf("broadcast theme = %q", push.Name)
	}
	if o.cfg.Theme.Name != "tokyo-night" || len(o.cfg.Theme.Colors) != 0 {
		t.Fatalf("live config not switched: %+v", o.cfg.Theme)
	}
}

// theme.save writes a user theme file; with activate it becomes the config's
// theme (overrides cleared). theme.delete removes it and falls the config
// back to the default theme.
func TestThemeSaveActivateDelete(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	o, c := newPendingHarness()
	o.cfg = config.Default()
	o.cfg.Theme.Colors = map[string]string{"bg": "#123456"}
	o.cfgPath = filepath.Join(t.TempDir(), "config.yaml")

	o.handleCmd(c, cmd(t, "c1", browserproto.CmdThemeSave, browserproto.ThemeSaveParams{
		Name: "midnight", Colors: map[string]string{
			"bg": "#101020", "fg": "#e0e0f0", "muted": "#8888aa", "line": "#333355",
			"accent": "#66aaff", "ok": "#44cc88", "warn": "#ccaa44", "err": "#cc5555",
		}, Activate: true,
	}))
	r := expectOK(t, c, "theme.save")
	res := decodeData[browserproto.ConfigGetResult](t, r)
	if res.Theme.Name != "midnight" || len(res.ThemeOverrides) != 0 {
		t.Fatalf("activate: theme=%s overrides=%v", res.Theme.Name, res.ThemeOverrides)
	}
	found := false
	for _, ti := range res.Themes {
		if ti.Name == "midnight" {
			found = true
			if ti.Source != theme.SourceUser || ti.Colors["panel"] != "#101020" {
				t.Fatalf("saved theme entry: %+v", ti)
			}
		}
	}
	if !found {
		t.Fatal("saved theme missing from registry")
	}
	if push := expectThemePush(t, c); push.Colors["bg"] != "#101020" {
		t.Fatalf("broadcast after save: %+v", push.Colors["bg"])
	}

	o.handleCmd(c, cmd(t, "c2", browserproto.CmdThemeDelete, browserproto.ThemeDeleteParams{Name: "midnight"}))
	r = expectOK(t, c, "theme.delete")
	res = decodeData[browserproto.ConfigGetResult](t, r)
	if res.Theme.Name != theme.DefaultName {
		t.Fatalf("delete of the active theme should fall back to default, got %s", res.Theme.Name)
	}
	expectThemePush(t, c)

	// Deleting a built-in is refused.
	o.handleCmd(c, cmd(t, "c3", browserproto.CmdThemeDelete, browserproto.ThemeDeleteParams{Name: "darcula"}))
	if r, ok := recvDown(t, c).(*browserproto.CmdResult); !ok || r.Ok {
		t.Fatalf("deleting a builtin should fail, got %#v", r)
	}
}

// An invalid config.set (unknown copy-mode action) is rejected by the shared
// Validate path and writes nothing.
func TestConfigSetRejectsInvalid(t *testing.T) {
	o, c := newPendingHarness()
	path := filepath.Join(t.TempDir(), "config.yaml")
	o.cfg = config.Default()
	o.cfgPath = path

	o.handleCmd(c, cmd(t, "c1", browserproto.CmdConfigSet, browserproto.ConfigSetParams{
		CopyMode: map[string][]string{"teleport": {"t"}},
	}))
	if r, ok := recvDown(t, c).(*browserproto.CmdResult); !ok || r.Ok {
		t.Fatalf("invalid config.set should fail, got %#v", r)
	}
	if _, _, err := config.Load(path); err == nil {
		t.Fatal("a rejected config.set must not write the file")
	}
	if _, ok := o.cfg.Keybindings.CopyMode["teleport"]; ok {
		t.Fatal("a rejected config.set must not mutate the live config")
	}
}
