package integration

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallCopilotWritesHookAndUpdatesSettings(t *testing.T) {
	home := testHome(t)
	copilotDir := filepath.Join(home, ".copilot")
	mustWriteFile(t, filepath.Join(copilotDir, "settings.json"),
		`{"banner":"never","hooks":{"SessionStart":[{"type":"command","bash":"echo keep","timeoutSec":5}]}}`)

	installed, err := InstallCopilot()
	if err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	if installed.HookPath != filepath.Join(copilotDir, "hooks", copilotHookInstallName) {
		t.Fatalf("hook path = %s", installed.HookPath)
	}
	assertFileMode(t, installed.HookPath, 0o755)

	settings := readJSONFile(t, installed.SettingsPath)
	if jsonStringAt(t, settings, "banner") != "never" {
		t.Fatal("user key lost")
	}
	entries, ok := jsonAt(settings, "hooks", "SessionStart").([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("SessionStart entries = %v", entries)
	}
	// User entry preserved, herdr entry appended in the flat/direct shape.
	if cmd := jsonStringAt(t, settings, "hooks", "SessionStart", 0, "bash"); cmd != "echo keep" {
		t.Fatalf("user entry = %q", cmd)
	}
	herdrEntry := entries[1].(map[string]any)
	if herdrEntry["type"] != "command" {
		t.Fatalf("herdr entry = %v", herdrEntry)
	}
	command := herdrEntry["bash"].(string)
	if command != hookCommand(installed.HookPath, "", false) {
		t.Fatalf("herdr command = %q", command)
	}
	if timeout, _ := herdrEntry["timeoutSec"].(float64); timeout != 10 {
		t.Fatalf("timeoutSec = %v", herdrEntry["timeoutSec"])
	}
	if _, has := herdrEntry["matcher"]; has {
		t.Fatal("copilot entry must not carry a matcher")
	}
	if _, has := herdrEntry["command"]; has {
		t.Fatal("copilot entry must use bash, not command")
	}
}

func TestInstallCopilotUsesCopilotHomeEnvAndIsIdempotent(t *testing.T) {
	testHome(t)
	copilotDir := t.TempDir()
	t.Setenv(copilotHomeEnvVar, copilotDir)

	if _, err := InstallCopilot(); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCopilot(); err != nil {
		t.Fatal(err)
	}

	settings := readJSONFile(t, filepath.Join(copilotDir, "settings.json"))
	entries, ok := jsonAt(settings, "hooks", "SessionStart").([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("SessionStart entries = %v", entries)
	}
}

func TestInstallCopilotRemovesLegacyLifecycleEvents(t *testing.T) {
	home := testHome(t)
	copilotDir := filepath.Join(home, ".copilot")
	hookPath := filepath.Join(copilotDir, "hooks", copilotHookInstallName)
	command := hookCommand(hookPath, "", false)
	legacy := map[string]any{"hooks": map[string]any{}}
	hooks := legacy["hooks"].(map[string]any)
	for _, event := range copilotRemovedLifecycleHookEvents {
		hooks[event] = []any{map[string]any{"type": "command", "bash": command, "timeoutSec": 10}}
	}
	raw, _ := json.Marshal(legacy)
	mustWriteFile(t, filepath.Join(copilotDir, "settings.json"), string(raw))

	if _, err := InstallCopilot(); err != nil {
		t.Fatal(err)
	}

	settings := readJSONFile(t, filepath.Join(copilotDir, "settings.json"))
	for _, event := range copilotRemovedLifecycleHookEvents {
		if jsonAt(settings, "hooks", event) != nil {
			t.Errorf("legacy event %s still present", event)
		}
	}
	if entries, ok := jsonAt(settings, "hooks", "SessionStart").([]any); !ok || len(entries) != 1 {
		t.Fatalf("SessionStart entries = %v", jsonAt(settings, "hooks", "SessionStart"))
	}
}

func TestUninstallCopilotRemovesHerdrHooksAndPreservesOthers(t *testing.T) {
	home := testHome(t)
	copilotDir := filepath.Join(home, ".copilot")
	mustMkdirAll(t, copilotDir)

	installed, err := InstallCopilot()
	if err != nil {
		t.Fatal(err)
	}
	// Add a user hook next to the herdr one.
	raw := fmt.Sprintf(`{"hooks":{"SessionStart":[{"type":"command","bash":%q,"timeoutSec":10},{"type":"command","bash":"echo keep","timeoutSec":5}]}}`,
		hookCommand(installed.HookPath, "", false))
	mustWriteFile(t, installed.SettingsPath, raw)

	result, err := UninstallCopilot()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedHookFile || !result.UpdatedSettings {
		t.Fatalf("result = %+v", result)
	}
	got := readJSONFile(t, installed.SettingsPath)
	entries, ok := jsonAt(got, "hooks", "SessionStart").([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	if cmd := jsonStringAt(t, got, "hooks", "SessionStart", 0, "bash"); cmd != "echo keep" {
		t.Fatalf("user hook lost: %q", cmd)
	}
}

func TestInstallCopilotErrorsWhenConfigDirMissing(t *testing.T) {
	testHome(t)
	_, err := InstallCopilot()
	if err == nil || !strings.Contains(err.Error(), "copilot config directory not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallDroidWritesHookToSettingsAndCleansLegacyHooksJSON(t *testing.T) {
	home := testHome(t)
	droidDir := filepath.Join(home, ".factory")
	legacyHookPath := filepath.Join(droidDir, "hooks", droidHookInstallName)
	mustMkdirAll(t, filepath.Dir(legacyHookPath))
	legacyCommand := "bash " + shellSingleQuote(legacyHookPath)
	mustWriteFile(t, filepath.Join(droidDir, "hooks.json"), fmt.Sprintf(
		`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":%q,"timeout":10}]}],"PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"echo keep","timeout":10}]}]}}`,
		legacyCommand))
	mustWriteFile(t, filepath.Join(droidDir, "settings.json"), `{"theme":"factory-dark"}`)

	installed, err := InstallDroid()
	if err != nil {
		t.Fatalf("InstallDroid: %v", err)
	}

	if installed.HookPath != legacyHookPath {
		t.Fatalf("hook path = %s", installed.HookPath)
	}
	if !installed.UpdatedLegacyHooks {
		t.Fatal("legacy hooks.json should have been cleaned")
	}
	if mustReadFile(t, installed.HookPath) != droidHookAsset {
		t.Fatal("hook content differs from embedded asset")
	}
	assertFileMode(t, installed.HookPath, 0o755)

	settings := readJSONFile(t, installed.SettingsPath)
	if jsonStringAt(t, settings, "theme") != "factory-dark" {
		t.Fatal("user key lost")
	}
	for _, he := range droidHookEvents {
		command := jsonStringAt(t, settings, "hooks", he.event, 0, "hooks", 0, "command")
		if !strings.Contains(command, droidHookInstallName) || !strings.HasSuffix(command, he.action) {
			t.Fatalf("droid %s command = %q", he.event, command)
		}
	}
	if jsonAt(settings, "hooks", "SessionStart", 0, "matcher") != nil {
		t.Fatal("droid entries must not carry a matcher")
	}

	legacyHooks := readJSONFile(t, installed.HooksPath)
	if jsonStringAt(t, legacyHooks, "hooks", "PreToolUse", 0, "matcher") != "Read" {
		t.Fatal("user legacy hook lost")
	}
	if jsonAt(legacyHooks, "hooks", "SessionStart") != nil {
		t.Fatal("herdr legacy entry still present")
	}
}

func TestInstallDroidIsIdempotentForHookEntries(t *testing.T) {
	home := testHome(t)
	mustMkdirAll(t, filepath.Join(home, ".factory"))

	if _, err := InstallDroid(); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallDroid(); err != nil {
		t.Fatal(err)
	}

	settings := readJSONFile(t, filepath.Join(home, ".factory", "settings.json"))
	for _, he := range droidHookEvents {
		entries, ok := jsonAt(settings, "hooks", he.event).([]any)
		if !ok || len(entries) != 1 {
			t.Fatalf("hooks.%s not idempotent: %v", he.event, entries)
		}
	}
}

func TestUninstallDroidRemovesHerdrHooksAndPreservesOthers(t *testing.T) {
	home := testHome(t)
	droidDir := filepath.Join(home, ".factory")
	mustMkdirAll(t, droidDir)

	installed, err := InstallDroid()
	if err != nil {
		t.Fatal(err)
	}
	herdrCommand := hookCommand(installed.HookPath, "session", true)
	mustWriteFile(t, installed.SettingsPath, fmt.Sprintf(
		`{"theme":"factory-dark","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":%q,"timeout":10}]}],"Stop":[{"hooks":[{"type":"command","command":"echo keep","timeout":10}]}]}}`,
		herdrCommand))

	result, err := UninstallDroid()
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
	if jsonStringAt(t, settings, "theme") != "factory-dark" {
		t.Fatal("user key lost")
	}
}

func TestInstallDroidErrorsWhenConfigDirMissing(t *testing.T) {
	testHome(t)
	_, err := InstallDroid()
	if err == nil || !strings.Contains(err.Error(), "droid config directory not found") {
		t.Fatalf("err = %v", err)
	}
}
