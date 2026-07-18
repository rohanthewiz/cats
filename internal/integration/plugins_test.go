package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallOpencodeWritesPluginToPluginsDir(t *testing.T) {
	home := testHome(t)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	mustMkdirAll(t, opencodeDir)

	installed, err := InstallOpencode()
	if err != nil {
		t.Fatalf("InstallOpencode: %v", err)
	}
	if installed.PluginPath != filepath.Join(opencodeDir, "plugins", opencodePluginInstallName) {
		t.Fatalf("plugin path = %s", installed.PluginPath)
	}
	if mustReadFile(t, installed.PluginPath) != opencodePluginAsset {
		t.Fatal("plugin content differs from embedded asset")
	}
	// Plugins are not hook scripts; no executable bit is set.
	info, err := os.Stat(installed.PluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 != 0 {
		t.Fatalf("plugin unexpectedly executable: %o", info.Mode().Perm())
	}
}

func TestUninstallOpencodeRemovesPluginWhenPresent(t *testing.T) {
	home := testHome(t)
	pluginPath := filepath.Join(home, ".config", "opencode", "plugins", opencodePluginInstallName)
	mustWriteFile(t, pluginPath, opencodePluginAsset)

	result, err := UninstallOpencode()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedPlugin {
		t.Fatalf("result = %+v", result)
	}
	if isFile(pluginPath) {
		t.Fatal("plugin still present")
	}
}

func TestInstallOpencodeErrorsWhenConfigDirMissing(t *testing.T) {
	testHome(t)
	_, err := InstallOpencode()
	if err == nil || !strings.Contains(err.Error(), "opencode config directory not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallKiloWritesPluginToPluginDir(t *testing.T) {
	home := testHome(t)
	kiloDir := filepath.Join(home, ".config", "kilo")
	mustMkdirAll(t, kiloDir)

	installed, err := InstallKilo()
	if err != nil {
		t.Fatalf("InstallKilo: %v", err)
	}
	if installed.PluginPath != filepath.Join(kiloDir, "plugin", kiloPluginInstallName) {
		t.Fatalf("plugin path = %s", installed.PluginPath)
	}
	if mustReadFile(t, installed.PluginPath) != kiloPluginAsset {
		t.Fatal("plugin content differs from embedded asset")
	}
}

func TestUninstallKiloRemovesPluginWhenPresent(t *testing.T) {
	home := testHome(t)
	pluginPath := filepath.Join(home, ".config", "kilo", "plugin", kiloPluginInstallName)
	mustWriteFile(t, pluginPath, kiloPluginAsset)

	result, err := UninstallKilo()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedPlugin {
		t.Fatalf("result = %+v", result)
	}
	if isFile(pluginPath) {
		t.Fatal("plugin still present")
	}
}

func TestInstallKiloErrorsWhenConfigDirMissing(t *testing.T) {
	testHome(t)
	_, err := InstallKilo()
	if err == nil || !strings.Contains(err.Error(), "kilo config directory not found") {
		t.Fatalf("err = %v", err)
	}
}

// TestInstallTargetMessages spot-checks the message-line contract of the
// dispatch layer for a plugin-style target.
func TestInstallTargetMessages(t *testing.T) {
	home := testHome(t)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	mustMkdirAll(t, opencodeDir)

	messages, err := InstallTarget(TargetOpencode)
	if err != nil {
		t.Fatal(err)
	}
	want := "installed opencode integration plugin to " +
		filepath.Join(opencodeDir, "plugins", opencodePluginInstallName)
	if len(messages) != 1 || messages[0] != want {
		t.Fatalf("messages = %v, want [%q]", messages, want)
	}

	messages, err = UninstallTarget(TargetOpencode)
	if err != nil {
		t.Fatal(err)
	}
	want = "removed opencode integration plugin at " +
		filepath.Join(opencodeDir, "plugins", opencodePluginInstallName)
	if len(messages) != 1 || messages[0] != want {
		t.Fatalf("messages = %v, want [%q]", messages, want)
	}

	messages, err = UninstallTarget(TargetOpencode)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || !strings.HasPrefix(messages[0], "no opencode integration plugin found at ") {
		t.Fatalf("messages = %v", messages)
	}
}
