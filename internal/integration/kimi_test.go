package integration

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func assertKimiHook(t *testing.T, config, hookPath, event, action string) {
	t.Helper()
	table := fmt.Sprintf("[[hooks]]\nevent = %s\ncommand = %s\ntimeout = 10\n",
		tomlBasicString(event), tomlBasicString(hookCommand(hookPath, action, true)))
	if !strings.Contains(config, table) {
		t.Errorf("config missing %s hook table:\n%s", event, table)
	}
}

func TestInstallKimiWritesHookAndUpdatesConfig(t *testing.T) {
	home := testHome(t)
	kimiDir := filepath.Join(home, ".kimi-code")
	mustWriteFile(t, filepath.Join(kimiDir, "config.toml"),
		"default_model = \"moonshot\"\n\n[[hooks]]\nevent = \"Notification\"\nmatcher = \"task.completed\"\ncommand = \"echo keep\"\ntimeout = 3\n")

	installed, err := InstallKimi()
	if err != nil {
		t.Fatalf("InstallKimi: %v", err)
	}

	if installed.HookPath != filepath.Join(kimiDir, "hooks", kimiHookInstallName) {
		t.Fatalf("hook path = %s", installed.HookPath)
	}
	if mustReadFile(t, installed.HookPath) != kimiHookAsset {
		t.Fatal("hook content differs from embedded asset")
	}
	assertFileMode(t, installed.HookPath, 0o755)

	config := mustReadFile(t, installed.ConfigPath)
	if got := strings.Count(config, "[[hooks]]"); got != len(kimiHookEvents)+1 {
		t.Fatalf("[[hooks]] count = %d, want %d", got, len(kimiHookEvents)+1)
	}
	if !strings.Contains(config, "default_model = \"moonshot\"") ||
		!strings.Contains(config, "command = \"echo keep\"") {
		t.Fatalf("user config lost:\n%s", config)
	}
	if !strings.Contains(config, kimiConfigBlockBegin) || !strings.Contains(config, kimiConfigBlockEnd) {
		t.Fatalf("sentinels missing:\n%s", config)
	}
	// All 10 lifecycle events must be present with their exact action.
	for _, he := range kimiHookEvents {
		assertKimiHook(t, config, installed.HookPath, he.event, he.action)
	}
}

func TestInstallKimiUsesKimiCodeHomeEnv(t *testing.T) {
	testHome(t)
	kimiDir := t.TempDir()
	t.Setenv(kimiCodeHomeEnvVar, kimiDir)

	installed, err := InstallKimi()
	if err != nil {
		t.Fatal(err)
	}
	if installed.HookPath != filepath.Join(kimiDir, "hooks", kimiHookInstallName) {
		t.Fatalf("env override not honored: %s", installed.HookPath)
	}
	if installed.ConfigPath != filepath.Join(kimiDir, "config.toml") {
		t.Fatalf("config path = %s", installed.ConfigPath)
	}
}

func TestInstallKimiIsIdempotentForConfigBlock(t *testing.T) {
	home := testHome(t)
	kimiDir := filepath.Join(home, ".kimi-code")
	mustMkdirAll(t, kimiDir)

	if _, err := InstallKimi(); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallKimi(); err != nil {
		t.Fatal(err)
	}

	config := mustReadFile(t, filepath.Join(kimiDir, "config.toml"))
	if got := strings.Count(config, kimiConfigBlockBegin); got != 1 {
		t.Fatalf("begin sentinel count = %d", got)
	}
	if got := strings.Count(config, kimiConfigBlockEnd); got != 1 {
		t.Fatalf("end sentinel count = %d", got)
	}
	if got := strings.Count(config, "[[hooks]]"); got != len(kimiHookEvents) {
		t.Fatalf("[[hooks]] count = %d, want %d (block replaced, not duplicated)", got, len(kimiHookEvents))
	}
}

func TestUninstallKimiRemovesHookAndConfigBlockPreservesOtherHooks(t *testing.T) {
	home := testHome(t)
	kimiDir := filepath.Join(home, ".kimi-code")
	mustMkdirAll(t, kimiDir)

	installed, err := InstallKimi()
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, installed.ConfigPath,
		"default_model = \"moonshot\"\n\n[[hooks]]\nevent = \"Notification\"\ncommand = \"echo keep\"\n\n"+
			mustReadFile(t, installed.ConfigPath))

	result, err := UninstallKimi()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedHookFile || !result.UpdatedConfig {
		t.Fatalf("result = %+v", result)
	}
	if isFile(result.HookPath) {
		t.Fatal("hook still present")
	}
	config := mustReadFile(t, filepath.Join(kimiDir, "config.toml"))
	if !strings.Contains(config, "default_model = \"moonshot\"") ||
		!strings.Contains(config, "command = \"echo keep\"") {
		t.Fatalf("user config lost:\n%s", config)
	}
	if strings.Contains(config, kimiConfigBlockBegin) || strings.Contains(config, kimiConfigBlockEnd) {
		t.Fatalf("sentinels still present:\n%s", config)
	}
	if got := strings.Count(config, "[[hooks]]"); got != 1 {
		t.Fatalf("[[hooks]] count = %d, want 1 (only user hook)", got)
	}
}

func TestInstallKimiErrorsWhenConfigDirMissing(t *testing.T) {
	testHome(t)
	_, err := InstallKimi()
	if err == nil || !strings.Contains(err.Error(), "kimi code config directory not found") {
		t.Fatalf("err = %v", err)
	}
}

// TestInstallTargetKimiVersionGate exercises the full InstallTarget path: an
// old kimi binary is a hard error, an unrunnable one degrades to a warning
// line appended to the messages.
func TestInstallTargetKimiVersionGate(t *testing.T) {
	home := testHome(t)
	mustMkdirAll(t, filepath.Join(home, ".kimi-code"))

	fakeBinary(t, "kimi", "#!/bin/sh\necho 'kimi-code 0.13.9'\n")
	_, err := InstallTarget(TargetKimi)
	want := "kimi code 0.13.9 is too old: cats hooks require kimi code 0.14.0 or newer. upgrade kimi code, then re-run install"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}

	t.Setenv("PATH", t.TempDir()) // no kimi binary at all
	messages, err := InstallTarget(TargetKimi)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 {
		t.Fatalf("messages = %v", messages)
	}
	if messages[2] != "requires kimi code 0.14.0 or newer" {
		t.Fatalf("messages[2] = %q", messages[2])
	}
	if !strings.HasPrefix(messages[3], InstallWarningPrefix) {
		t.Fatalf("expected trailing warning line, got %q", messages[3])
	}

	fakeBinary(t, "kimi", "#!/bin/sh\necho 'kimi-code 0.15.2'\n")
	messages, err = InstallTarget(TargetKimi)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("no warning expected for current version: %v", messages)
	}
}
