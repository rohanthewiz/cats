//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/cats/internal/config"
)

// isolateHome points both the legacy app-data dir and the config dir at temp
// dirs, so no test can touch the real ~/Library or ~/.config.
func isolateHome(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(config.EnvVar, "")
	return home
}

// Upgrading must not lose the saved catways: a legacy app.json is imported
// into config.json's app section on first load, and left where it was.
func TestLoadAppConfigImportsLegacyAppJSON(t *testing.T) {
	home := isolateHome(t)
	legacyDir := filepath.Join(home, "Library", "Application Support", "cats")
	os.MkdirAll(legacyDir, 0o700)
	legacy := `{"mode":"remote","remote":{"url":"https://a","label":"a"},"presets":[{"url":"https://a","label":"a"}]}`
	if err := os.WriteFile(filepath.Join(legacyDir, "app.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := loadAppConfig()
	if cfg.Mode != "remote" || len(cfg.Presets) != 1 || cfg.Remote.URL != "https://a" {
		t.Fatalf("legacy settings not imported: %+v", cfg)
	}
	var onDisk appConfig
	found, err := config.ReadSection(config.DefaultPath(), "app", &onDisk)
	if err != nil || !found || onDisk.Mode != "remote" {
		t.Fatalf("import not written to config.json: found=%v err=%v %+v", found, err, onDisk)
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "app.json")); err != nil {
		t.Errorf("app.json should be left in place: %v", err)
	}
}

// The settings screen edits mode and presets only; the connection and the
// window layout already in the file survive a save.
func TestApplyAppSettingsKeepsState(t *testing.T) {
	isolateHome(t)
	start := appConfig{
		Mode:    "local",
		Remote:  remoteTarget{URL: "https://cur"},
		Windows: []savedWindow{{}},
	}
	if err := saveAppConfig(start); err != nil {
		t.Fatal(err)
	}
	msg := applyAppSettings(appSettingsView{
		Mode:    "remote",
		Presets: []remoteTarget{{URL: " https://x ", Label: "x"}, {URL: "https://x", Label: "renamed"}},
	})
	if msg != "" {
		t.Fatal(msg)
	}
	got := loadAppConfig()
	if got.Mode != "remote" || got.Remote.URL != "https://cur" || len(got.Windows) != 1 {
		t.Fatalf("state lost: %+v", got)
	}
	// upsertPreset's identity rule: trimmed, one entry per URL, last label wins.
	if len(got.Presets) != 1 || got.Presets[0].Label != "renamed" {
		t.Fatalf("presets = %+v", got.Presets)
	}

	if msg := applyAppSettings(appSettingsView{Mode: "sideways"}); msg == "" {
		t.Fatal("an unknown mode was accepted")
	}
}

// catway's own save must not erase the app section it does not model.
func TestCatwaySaveKeepsAppSection(t *testing.T) {
	isolateHome(t)
	if err := saveAppConfig(appConfig{Mode: "remote"}); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(config.DefaultPath(), config.Default()); err != nil {
		t.Fatal(err)
	}
	if got := loadAppConfig(); got.Mode != "remote" {
		t.Fatalf("app section clobbered by catway's save: %+v", got)
	}
}
