package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallHermesWritesPluginAndEnablesIt(t *testing.T) {
	home := testHome(t)
	hermesDir := filepath.Join(home, ".hermes")
	mustWriteFile(t, filepath.Join(hermesDir, "config.yaml"), "model:\n  provider: auto\n")

	installed, err := InstallHermes()
	if err != nil {
		t.Fatalf("InstallHermes: %v", err)
	}

	wantPluginDir := filepath.Join(hermesDir, "plugins", hermesPluginInstallName)
	if installed.PluginDir != wantPluginDir {
		t.Fatalf("plugin dir = %s, want %s", installed.PluginDir, wantPluginDir)
	}
	if got := mustReadFile(t, filepath.Join(installed.PluginDir, hermesPluginManifestInstallName)); got != hermesPluginManifestAsset {
		t.Error("manifest differs from embedded asset")
	}
	if got := mustReadFile(t, filepath.Join(installed.PluginDir, hermesPluginInitInstallName)); got != hermesPluginInitAsset {
		t.Error("__init__.py differs from embedded asset")
	}
	config := mustReadFile(t, installed.ConfigPath)
	if !strings.Contains(config, "plugins:\n  enabled:\n    - herdr-agent-state") {
		t.Fatalf("config not enabled:\n%s", config)
	}
	if !strings.Contains(config, "model:\n  provider: auto") {
		t.Fatalf("user config lost:\n%s", config)
	}
}

func TestInstallHermesIsIdempotentForEnabledEntry(t *testing.T) {
	home := testHome(t)
	hermesDir := filepath.Join(home, ".hermes")
	mustWriteFile(t, filepath.Join(hermesDir, "config.yaml"),
		"plugins:\n  enabled:\n    - herdr-agent-state\n")

	if _, err := InstallHermes(); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHermes(); err != nil {
		t.Fatal(err)
	}

	config := mustReadFile(t, filepath.Join(hermesDir, "config.yaml"))
	if got := strings.Count(config, "herdr-agent-state"); got != 1 {
		t.Fatalf("expected exactly one entry, got %d:\n%s", got, config)
	}
}

func TestInstallHermesPreservesFlatPluginList(t *testing.T) {
	home := testHome(t)
	hermesDir := filepath.Join(home, ".hermes")
	mustWriteFile(t, filepath.Join(hermesDir, "config.yaml"), "plugins:\n  - platforms/discord\n")

	if _, err := InstallHermes(); err != nil {
		t.Fatal(err)
	}

	config := mustReadFile(t, filepath.Join(hermesDir, "config.yaml"))
	if config != "plugins:\n  - herdr-agent-state\n  - platforms/discord\n" {
		t.Fatalf("flat list mishandled:\n%s", config)
	}
}

func TestInstallHermesConvertsFlowPluginListToBlockList(t *testing.T) {
	home := testHome(t)
	hermesDir := filepath.Join(home, ".hermes")
	mustWriteFile(t, filepath.Join(hermesDir, "config.yaml"), "plugins: [platforms/discord]\n")

	if _, err := InstallHermes(); err != nil {
		t.Fatal(err)
	}

	config := mustReadFile(t, filepath.Join(hermesDir, "config.yaml"))
	if config != "plugins:\n  - herdr-agent-state\n  - platforms/discord\n" {
		t.Fatalf("flow list mishandled:\n%s", config)
	}
}

func TestInstallHermesIsIdempotentForQuotedFlatPluginEntry(t *testing.T) {
	home := testHome(t)
	hermesDir := filepath.Join(home, ".hermes")
	content := "plugins:\n  - \"herdr-agent-state\" # installed by herdr\n"
	mustWriteFile(t, filepath.Join(hermesDir, "config.yaml"), content)

	if _, err := InstallHermes(); err != nil {
		t.Fatal(err)
	}

	if got := mustReadFile(t, filepath.Join(hermesDir, "config.yaml")); got != content {
		t.Fatalf("quoted entry not recognized:\n%s", got)
	}
}

func TestInstallHermesCreatesConfigWhenMissing(t *testing.T) {
	home := testHome(t)
	mustMkdirAll(t, filepath.Join(home, ".hermes"))

	installed, err := InstallHermes()
	if err != nil {
		t.Fatal(err)
	}
	if got := mustReadFile(t, installed.ConfigPath); got != "plugins:\n  enabled:\n    - herdr-agent-state\n" {
		t.Fatalf("fresh config wrong:\n%s", got)
	}
}

// TestUpdateHermesEnabledPluginMatrix drives the YAML editor over the layout
// variants directly (empty flow list, bare plugins key, missing key removal).
func TestUpdateHermesEnabledPluginMatrix(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		enabled bool
		want    string
	}{
		{
			name: "enabled empty flow list", enabled: true,
			in:   "plugins:\n  enabled: []\n",
			want: "plugins:\n  enabled:\n    - herdr-agent-state\n",
		},
		{
			name: "enabled empty flow list herdr comment", enabled: true,
			in:   "plugins:\n  enabled: [] # herdr\n",
			want: "plugins:\n  enabled:\n    - herdr-agent-state\n",
		},
		{
			name: "bare plugins key grows enabled block", enabled: true,
			in:   "plugins:\n",
			want: "plugins:\n  enabled:\n    - herdr-agent-state\n",
		},
		{
			name: "existing enabled list gains entry at head", enabled: true,
			in:   "plugins:\n  enabled:\n    - other\n",
			want: "plugins:\n  enabled:\n    - herdr-agent-state\n    - other\n",
		},
		{
			name: "empty flow plugins list", enabled: true,
			in:   "plugins: []\n",
			want: "plugins:\n  - herdr-agent-state\n",
		},
		{
			name: "remove without plugins key is a no-op", enabled: false,
			in:   "model: auto\n",
			want: "model: auto\n",
		},
		{
			name: "remove absent entry is a no-op", enabled: false,
			in:   "plugins:\n  enabled:\n    - other\n",
			want: "plugins:\n  enabled:\n    - other\n",
		},
		{
			name: "remove from enabled block", enabled: false,
			in:   "plugins:\n  enabled:\n    - other-plugin\n    - herdr-agent-state\n",
			want: "plugins:\n  enabled:\n    - other-plugin\n",
		},
		{
			name: "remove last flow entry collapses to empty", enabled: false,
			in:   "plugins: [herdr-agent-state]\n",
			want: "plugins: []\n",
		},
		{
			name: "later top-level key bounds the plugins block", enabled: true,
			in:   "plugins:\n  enabled:\n    - other\nmodel: auto\n",
			want: "plugins:\n  enabled:\n    - herdr-agent-state\n    - other\nmodel: auto\n",
		},
		{
			name: "missing key appends block after content", enabled: true,
			in:   "model: auto\n",
			want: "model: auto\nplugins:\n  enabled:\n    - herdr-agent-state\n",
		},
	}
	for _, tc := range cases {
		if got := updateHermesEnabledPlugin(tc.in, tc.enabled); got != tc.want {
			t.Errorf("%s:\ngot:  %q\nwant: %q", tc.name, got, tc.want)
		}
	}
}

func TestUninstallHermesRemovesPluginAndEnabledEntry(t *testing.T) {
	home := testHome(t)
	hermesDir := filepath.Join(home, ".hermes")
	pluginDir := filepath.Join(hermesDir, "plugins", hermesPluginInstallName)
	mustWriteFile(t, filepath.Join(pluginDir, hermesPluginInitInstallName), hermesPluginInitAsset)
	mustWriteFile(t, filepath.Join(hermesDir, "config.yaml"),
		"plugins:\n  enabled:\n    - other-plugin\n    - herdr-agent-state\n")

	result, err := UninstallHermes()
	if err != nil {
		t.Fatal(err)
	}
	config := mustReadFile(t, filepath.Join(hermesDir, "config.yaml"))

	if !result.RemovedPluginDir || !result.UpdatedConfig {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(pluginDir); !os.IsNotExist(err) {
		t.Fatal("plugin dir still exists")
	}
	if !strings.Contains(config, "    - other-plugin") || strings.Contains(config, "herdr-agent-state") {
		t.Fatalf("config wrong:\n%s", config)
	}
}

func TestUninstallHermesPreservesFlatPluginList(t *testing.T) {
	home := testHome(t)
	hermesDir := filepath.Join(home, ".hermes")
	pluginDir := filepath.Join(hermesDir, "plugins", hermesPluginInstallName)
	mustWriteFile(t, filepath.Join(pluginDir, hermesPluginInitInstallName), hermesPluginInitAsset)
	mustWriteFile(t, filepath.Join(hermesDir, "config.yaml"),
		"plugins:\n  - other-plugin\n  - herdr-agent-state\n")

	result, err := UninstallHermes()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedPluginDir || !result.UpdatedConfig {
		t.Fatalf("result = %+v", result)
	}
	if got := mustReadFile(t, filepath.Join(hermesDir, "config.yaml")); got != "plugins:\n  - other-plugin\n" {
		t.Fatalf("config wrong:\n%s", got)
	}
}

func TestUninstallHermesRemovesFlowPluginListEntry(t *testing.T) {
	home := testHome(t)
	hermesDir := filepath.Join(home, ".hermes")
	pluginDir := filepath.Join(hermesDir, "plugins", hermesPluginInstallName)
	mustWriteFile(t, filepath.Join(pluginDir, hermesPluginInitInstallName), hermesPluginInitAsset)
	mustWriteFile(t, filepath.Join(hermesDir, "config.yaml"),
		"plugins: [other-plugin, herdr-agent-state]\n")

	result, err := UninstallHermes()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedPluginDir || !result.UpdatedConfig {
		t.Fatalf("result = %+v", result)
	}
	if got := mustReadFile(t, filepath.Join(hermesDir, "config.yaml")); got != "plugins:\n  - other-plugin\n" {
		t.Fatalf("config wrong:\n%s", got)
	}
}

func TestUninstallHermesRemovesCommentedFlatPluginEntry(t *testing.T) {
	home := testHome(t)
	hermesDir := filepath.Join(home, ".hermes")
	pluginDir := filepath.Join(hermesDir, "plugins", hermesPluginInstallName)
	mustWriteFile(t, filepath.Join(pluginDir, hermesPluginInitInstallName), hermesPluginInitAsset)
	mustWriteFile(t, filepath.Join(hermesDir, "config.yaml"),
		"plugins:\n  - other-plugin\n  - herdr-agent-state # installed by herdr\n")

	result, err := UninstallHermes()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedPluginDir || !result.UpdatedConfig {
		t.Fatalf("result = %+v", result)
	}
	if got := mustReadFile(t, filepath.Join(hermesDir, "config.yaml")); got != "plugins:\n  - other-plugin\n" {
		t.Fatalf("config wrong:\n%s", got)
	}
}

func TestUninstallHermesNoopWhenNothingInstalled(t *testing.T) {
	testHome(t)
	result, err := UninstallHermes()
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedPluginDir || result.UpdatedConfig {
		t.Fatalf("result = %+v", result)
	}
}

func TestInstallHermesErrorsWhenConfigDirMissing(t *testing.T) {
	testHome(t)
	_, err := InstallHermes()
	if err == nil || !strings.Contains(err.Error(), "hermes config directory not found") {
		t.Fatalf("err = %v", err)
	}
}
