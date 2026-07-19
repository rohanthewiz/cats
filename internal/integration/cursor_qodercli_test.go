package integration

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallCursorWritesHookAndUpdatesHooksJSON(t *testing.T) {
	home := testHome(t)
	cursorDir := filepath.Join(home, ".cursor")
	mustWriteFile(t, filepath.Join(cursorDir, "hooks.json"),
		`{"hooks":{"sessionStart":[{"command":"echo keep"}]}}`)

	installed, err := InstallCursor()
	if err != nil {
		t.Fatalf("InstallCursor: %v", err)
	}

	// Cursor's hook lives at the config-dir root, not under hooks/.
	if installed.HookPath != filepath.Join(cursorDir, cursorHookInstallName) {
		t.Fatalf("hook path = %s", installed.HookPath)
	}
	if mustReadFile(t, installed.HookPath) != cursorHookAsset {
		t.Fatal("hook content differs from embedded asset")
	}
	assertFileMode(t, installed.HookPath, 0o755)

	hooks := readJSONFile(t, installed.HooksPath)
	// version: 1 is ensured on files that lack it.
	if version, _ := jsonAt(hooks, "version").(float64); version != 1 {
		t.Fatalf("version = %v", jsonAt(hooks, "version"))
	}
	entries, ok := jsonAt(hooks, "hooks", "sessionStart").([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("sessionStart entries = %v", entries)
	}
	if cmd := jsonStringAt(t, hooks, "hooks", "sessionStart", 0, "command"); cmd != "echo keep" {
		t.Fatalf("user hook lost: %q", cmd)
	}
	wantCommand := "bash " + shellSingleQuote(installed.HookPath) + " session"
	if cmd := jsonStringAt(t, hooks, "hooks", "sessionStart", 1, "command"); cmd != wantCommand {
		t.Fatalf("herdr command = %q, want %q", cmd, wantCommand)
	}
	// Simple shape: no type/timeout fields.
	entry := entries[1].(map[string]any)
	if len(entry) != 1 {
		t.Fatalf("simple entry has extra fields: %v", entry)
	}
}

func TestInstallCursorFreshHooksFile(t *testing.T) {
	home := testHome(t)
	mustMkdirAll(t, filepath.Join(home, ".cursor"))

	installed, err := InstallCursor()
	if err != nil {
		t.Fatal(err)
	}
	hooks := readJSONFile(t, installed.HooksPath)
	if version, _ := jsonAt(hooks, "version").(float64); version != 1 {
		t.Fatalf("version = %v", jsonAt(hooks, "version"))
	}
}

func TestInstallCursorIsIdempotentForHookEntries(t *testing.T) {
	home := testHome(t)
	mustMkdirAll(t, filepath.Join(home, ".cursor"))

	if _, err := InstallCursor(); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCursor(); err != nil {
		t.Fatal(err)
	}

	hooks := readJSONFile(t, filepath.Join(home, ".cursor", "hooks.json"))
	entries, ok := jsonAt(hooks, "hooks", "sessionStart").([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("sessionStart entries = %v", entries)
	}
}

func TestInstallCursorUsesCursorConfigDirEnv(t *testing.T) {
	testHome(t)
	cursorDir := t.TempDir()
	t.Setenv(cursorConfigDirEnvVar, cursorDir)

	installed, err := InstallCursor()
	if err != nil {
		t.Fatal(err)
	}
	if installed.HookPath != filepath.Join(cursorDir, cursorHookInstallName) {
		t.Fatalf("env override not honored: %s", installed.HookPath)
	}
}

func TestUninstallCursorRemovesHerdrHooksAndPreservesOthers(t *testing.T) {
	home := testHome(t)
	mustMkdirAll(t, filepath.Join(home, ".cursor"))

	installed, err := InstallCursor()
	if err != nil {
		t.Fatal(err)
	}
	sessionCommand := "bash " + shellSingleQuote(installed.HookPath) + " session"
	mustWriteFile(t, installed.HooksPath, fmt.Sprintf(
		`{"version":1,"hooks":{"sessionStart":[{"command":%q},{"command":"echo keep"}],"stop":[{"command":%q}]}}`,
		sessionCommand, sessionCommand))

	result, err := UninstallCursor()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedHookFile || !result.UpdatedHooks {
		t.Fatalf("result = %+v", result)
	}
	if isFile(installed.HookPath) {
		t.Fatal("hook still present")
	}
	hooks := readJSONFile(t, installed.HooksPath)
	if jsonAt(hooks, "hooks", "stop") != nil {
		t.Fatal("herdr stop entry still present")
	}
	entries, ok := jsonAt(hooks, "hooks", "sessionStart").([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	if cmd := jsonStringAt(t, hooks, "hooks", "sessionStart", 0, "command"); cmd != "echo keep" {
		t.Fatalf("user hook lost: %q", cmd)
	}
}

func TestCursorV1IntegrationStatusIsCurrent(t *testing.T) {
	home := testHome(t)
	mustWriteFile(t, filepath.Join(home, ".cursor", cursorHookInstallName), cursorHookAsset)

	for _, status := range InstalledIntegrationStatuses() {
		if status.Target != TargetCursor {
			continue
		}
		if status.State != StatusCurrent || status.InstalledVersion != cursorIntegrationVersion {
			t.Fatalf("cursor status = %+v", status)
		}
		return
	}
	t.Fatal("cursor status missing")
}

func TestInstallCursorErrorsWhenConfigDirMissing(t *testing.T) {
	testHome(t)
	_, err := InstallCursor()
	if err == nil || !strings.Contains(err.Error(), "cursor config directory not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallQodercliWritesHookAndUpdatesSettings(t *testing.T) {
	testHome(t)
	qoderDir := t.TempDir()
	mustWriteFile(t, filepath.Join(qoderDir, "settings.json"),
		`{"permissions":{"allow":["Read"]},"hooks":{}}`)
	t.Setenv(qodercliConfigDirEnvVar, qoderDir)

	installed, err := InstallQodercli()
	if err != nil {
		t.Fatalf("InstallQodercli: %v", err)
	}
	if installed.HookPath != filepath.Join(qoderDir, "hooks", qodercliHookInstallName) {
		t.Fatalf("hook path = %s", installed.HookPath)
	}
	if !isFile(installed.HookPath) {
		t.Fatal("hook not written")
	}
	assertFileMode(t, installed.HookPath, 0o755)

	settings := readJSONFile(t, installed.SettingsPath)
	for _, he := range qodercliHookEvents {
		if got := jsonStringAt(t, settings, "hooks", he.event, 0, "matcher"); got != "*" {
			t.Fatalf("%s matcher = %q", he.event, got)
		}
		command := jsonStringAt(t, settings, "hooks", he.event, 0, "hooks", 0, "command")
		if !strings.Contains(command, qodercliHookInstallName) || !strings.HasSuffix(command, he.action) {
			t.Fatalf("%s command = %q", he.event, command)
		}
	}
	if jsonAt(settings, "permissions") == nil {
		t.Fatal("pre-existing settings keys must be preserved")
	}
}

func TestInstallQodercliIsIdempotentForHookEntries(t *testing.T) {
	testHome(t)
	qoderDir := t.TempDir()
	t.Setenv(qodercliConfigDirEnvVar, qoderDir)

	if _, err := InstallQodercli(); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallQodercli(); err != nil {
		t.Fatal(err)
	}

	settings := readJSONFile(t, filepath.Join(qoderDir, "settings.json"))
	for _, he := range qodercliHookEvents {
		entries, ok := jsonAt(settings, "hooks", he.event).([]any)
		if !ok || len(entries) != 1 {
			t.Fatalf("hooks.%s not idempotent: %v", he.event, entries)
		}
	}
}

func TestUninstallQodercliRemovesHerdrHooksAndPreservesOthers(t *testing.T) {
	home := testHome(t)
	mustMkdirAll(t, filepath.Join(home, ".qoder"))

	installed, err := InstallQodercli()
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, installed.SettingsPath, fmt.Sprintf(
		`{"hooks":{"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":%q,"timeout":10}]}],"Stop":[{"hooks":[{"type":"command","command":"echo keep","timeout":10}]}]}}`,
		hookCommand(installed.HookPath, "session", true)))

	result, err := UninstallQodercli()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedHookFile || !result.UpdatedSettings {
		t.Fatalf("result = %+v", result)
	}
	settings := readJSONFile(t, installed.SettingsPath)
	if jsonAt(settings, "hooks", "SessionStart") != nil {
		t.Fatal("herdr entry still present")
	}
	if cmd := jsonStringAt(t, settings, "hooks", "Stop", 0, "hooks", 0, "command"); cmd != "echo keep" {
		t.Fatalf("user hook lost: %q", cmd)
	}
}

func TestInstallQodercliErrorsWhenConfigDirMissing(t *testing.T) {
	testHome(t)
	_, err := InstallQodercli()
	if err == nil || !strings.Contains(err.Error(), "qodercli config directory not found") {
		t.Fatalf("err = %v", err)
	}
}
