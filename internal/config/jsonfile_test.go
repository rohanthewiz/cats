package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateConfigDir points DefaultPath/LegacyPath at a temp dir for one test.
func isolateConfigDir(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv(EnvVar, "")
	return filepath.Join(xdg, "cats")
}

func TestDefaultPathIsJSON(t *testing.T) {
	dir := isolateConfigDir(t)
	if got, want := DefaultPath(), filepath.Join(dir, "config.json"); got != want {
		t.Fatalf("DefaultPath = %q, want %q", got, want)
	}
	if got, want := LegacyPath(), filepath.Join(dir, "config.yaml"); got != want {
		t.Fatalf("LegacyPath = %q, want %q", got, want)
	}
}

func TestSaveLoadJSONRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Panes.ReapExited = "2h"
	cfg.Theme.Name = "darcula"
	cfg.UI = UI{FontPx: 16, SidebarWidth: 240}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !json.Valid(data) {
		t.Fatalf("saved file is not JSON:\n%s", data)
	}
	// Field order, not alphabetical: server is the first thing a reader sees.
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "{\n  \"server\"") {
		t.Errorf("saved file does not lead with server:\n%.80s", data)
	}
	back, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Panes.ReapExited != "2h" || back.Theme.Name != "darcula" || back.UI != cfg.UI {
		t.Fatalf("round trip lost values: %+v %+v %+v", back.Panes, back.Theme, back.UI)
	}
}

// A partial JSON document keeps defaults for everything it leaves out — the
// same contract the YAML file had.
func TestParseJSONPartialKeepsDefaults(t *testing.T) {
	cfg, err := parse([]byte(`{"panes":{"reap_exited":"1h"},"keybindings":{"copy_mode":{"yank":["Y"]}}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Panes.ReapExited != "1h" || cfg.Panes.AutocloseExited != defaultAutocloseExitedStr {
		t.Errorf("panes %+v", cfg.Panes)
	}
	if cfg.Server.Addr != ":8421" {
		t.Errorf("server.addr default lost: %q", cfg.Server.Addr)
	}
	if got := cfg.Keybindings.CopyMode["yank"]; len(got) != 1 || got[0] != "Y" {
		t.Errorf("yank = %v", got)
	}
	if got := cfg.Keybindings.CopyMode["exit"]; len(got) == 0 {
		t.Errorf("exit default lost")
	}
}

func TestLoadMigratesLegacyYAML(t *testing.T) {
	dir := isolateConfigDir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yamlDoc := "panes:\n  reap_exited: \"90m\"\ntheme:\n  name: tokyo-night\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, path, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "config.json") {
		t.Fatalf("loaded from %q, want the JSON path", path)
	}
	if cfg.Panes.ReapExited != "90m" || cfg.Theme.Name != "tokyo-night" {
		t.Fatalf("migrated values lost: %+v %+v", cfg.Panes, cfg.Theme)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err != nil {
		t.Errorf("the YAML file should be left in place: %v", err)
	}
	// The JSON now wins: an edit to the YAML is not read any more.
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("panes:\n  reap_exited: \"5m\"\n"), 0o644)
	again, _, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if again.Panes.ReapExited != "90m" {
		t.Errorf("YAML re-read after migration: %q", again.Panes.ReapExited)
	}
}

// An explicit .yaml path is still read (and written) as YAML.
func TestExplicitYAMLPathStaysYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mine.yaml")
	os.WriteFile(path, []byte("panes:\n  reap_exited: \"3h\"\n"), 0o644)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Panes.ReapExited != "3h" {
		t.Fatalf("reap_exited = %q", cfg.Panes.ReapExited)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if json.Valid(data) {
		t.Errorf("a .yaml path was rewritten as JSON")
	}
}

// The heart of the two-writer design: catway's Save must carry the Mac app's
// section through, and the app's WriteSection must leave catway's alone.
func TestSaveKeepsForeignSectionAndWriteSectionKeepsOurs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Panes.ReapExited = "7h"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	type app struct {
		Mode string `json:"mode"`
	}
	if err := WriteSection(path, "app", app{Mode: "remote"}); err != nil {
		t.Fatal(err)
	}
	// catway saves again from its in-memory copy, which has no "app".
	cfg.Panes.ReapExited = "8h"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	var got app
	found, err := ReadSection(path, "app", &got)
	if err != nil || !found || got.Mode != "remote" {
		t.Fatalf("app section after catway save: found=%v err=%v %+v", found, err, got)
	}
	back, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Panes.ReapExited != "8h" {
		t.Fatalf("catway's value lost: %q", back.Panes.ReapExited)
	}
}

// A section Config models is removed by omission, not resurrected from disk.
func TestSaveDropsRemovedKnownSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Hosts = []Host{{ID: "devbox", Addr: "unix:///tmp/d.sock"}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Hosts = nil
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	back, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Hosts) != 0 {
		t.Fatalf("removed hosts came back: %+v", back.Hosts)
	}
}

func TestWriteSectionRefusesCatwaySections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := WriteSection(path, "panes", map[string]string{}); err == nil {
		t.Fatal("WriteSection wrote a catway-owned section")
	}
}

// The Mac app saving first must not strand a legacy YAML config: the section
// write migrates before it creates config.json.
func TestWriteSectionMigratesFirst(t *testing.T) {
	dir := isolateConfigDir(t)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("panes:\n  reap_exited: \"11h\"\n"), 0o644)
	if err := WriteSection(DefaultPath(), "app", map[string]string{"mode": "local"}); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Panes.ReapExited != "11h" {
		t.Fatalf("YAML settings stranded by the app's first save: %q", cfg.Panes.ReapExited)
	}
}

func TestUIValidation(t *testing.T) {
	cases := []struct {
		ui UI
		ok bool
	}{
		{UI{}, true},
		{UI{FontPx: 14, SidebarWidth: 200}, true},
		{UI{FontPx: 4}, false},
		{UI{FontPx: 99}, false},
		{UI{SidebarWidth: 20}, false},
	}
	for _, c := range cases {
		cfg := Default()
		cfg.UI = c.ui
		if err := cfg.Validate(); (err == nil) != c.ok {
			t.Errorf("%+v: err=%v, want ok=%v", c.ui, err, c.ok)
		}
	}
}

func TestNewFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", st.Mode().Perm())
	}
}
