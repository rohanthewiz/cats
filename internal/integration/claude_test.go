package integration

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallClaudeWritesHookAndUpdatesSettings(t *testing.T) {
	home := testHome(t)
	claudeDir := filepath.Join(home, ".claude")
	mustWriteFile(t, filepath.Join(claudeDir, "settings.json"),
		`{"permissions":{"allow":["Read"]},"hooks":{}}`)

	installed, err := InstallClaude()
	if err != nil {
		t.Fatalf("InstallClaude: %v", err)
	}

	wantHook := filepath.Join(claudeDir, "hooks", claudeHookInstallName)
	if installed.HookPath != wantHook {
		t.Fatalf("hook path = %s, want %s", installed.HookPath, wantHook)
	}
	if mustReadFile(t, installed.HookPath) != claudeHookAsset {
		t.Fatal("hook content differs from embedded asset")
	}
	assertFileMode(t, installed.HookPath, 0o755)

	settings := readJSONFile(t, installed.SettingsPath)
	if jsonAt(settings, "permissions", "allow") == nil {
		t.Fatal("pre-existing settings keys must be preserved")
	}
	if got := jsonStringAt(t, settings, "hooks", "SessionStart", 0, "matcher"); got != "*" {
		t.Fatalf("matcher = %q", got)
	}
	command := jsonStringAt(t, settings, "hooks", "SessionStart", 0, "hooks", 0, "command")
	if !strings.HasSuffix(command, " session") || !strings.Contains(command, claudeHookInstallName) {
		t.Fatalf("command = %q", command)
	}
	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "PermissionRequest",
		"PostToolUse", "PostToolUseFailure", "SubagentStop", "Stop", "SessionEnd"} {
		if jsonAt(settings, "hooks", event) != nil {
			t.Errorf("deprecated event %s present", event)
		}
	}
}

func TestInstallClaudeUsesClaudeConfigDirEnv(t *testing.T) {
	testHome(t)
	claudeDir := t.TempDir()
	t.Setenv(claudeConfigDirEnvVar, claudeDir)

	installed, err := InstallClaude()
	if err != nil {
		t.Fatal(err)
	}
	if installed.SettingsPath != filepath.Join(claudeDir, "settings.json") {
		t.Fatalf("settings path = %s", installed.SettingsPath)
	}
	if installed.HookPath != filepath.Join(claudeDir, "hooks", claudeHookInstallName) {
		t.Fatalf("hook path = %s", installed.HookPath)
	}
}

func TestInstallClaudeIsIdempotentForHookEntries(t *testing.T) {
	home := testHome(t)
	mustMkdirAll(t, filepath.Join(home, ".claude"))

	if _, err := InstallClaude(); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallClaude(); err != nil {
		t.Fatal(err)
	}

	settings := readJSONFile(t, filepath.Join(home, ".claude", "settings.json"))
	entries, ok := jsonAt(settings, "hooks", "SessionStart").([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("SessionStart entries = %v", jsonAt(settings, "hooks", "SessionStart"))
	}
}

func TestInstallClaudeRemovesDeprecatedHooksAndPreservesUserHooks(t *testing.T) {
	home := testHome(t)
	claudeDir := filepath.Join(home, ".claude")
	hookPath := filepath.Join(claudeDir, "hooks", claudeHookInstallName)
	herdr := func(action string) string {
		return fmt.Sprintf(`{"type":"command","command":"bash '%s' %s","timeout":10}`, hookPath, action)
	}
	settings := fmt.Sprintf(`{"hooks":{
		"PostToolUse":[{"matcher":"*","hooks":[%s,{"type":"command","command":"echo keep-post","timeout":10}]}],
		"PostToolUseFailure":[{"matcher":"*","hooks":[%s,{"type":"command","command":"echo keep-failure","timeout":10}]}],
		"SubagentStop":[{"matcher":"*","hooks":[%s,{"type":"command","command":"echo keep-subagent","timeout":10}]}],
		"SessionEnd":[{"matcher":"*","hooks":[%s,{"type":"command","command":"echo keep-session-end","timeout":10}]}]
	}}`, herdr("working"), herdr("working"), herdr("working"), herdr("release"))
	mustWriteFile(t, filepath.Join(claudeDir, "settings.json"), settings)

	if _, err := InstallClaude(); err != nil {
		t.Fatal(err)
	}

	got := readJSONFile(t, filepath.Join(claudeDir, "settings.json"))
	wantKept := map[string]string{
		"PostToolUse":        "echo keep-post",
		"PostToolUseFailure": "echo keep-failure",
		"SubagentStop":       "echo keep-subagent",
		"SessionEnd":         "echo keep-session-end",
	}
	for event, keep := range wantKept {
		if cmd := jsonStringAt(t, got, "hooks", event, 0, "hooks", 0, "command"); cmd != keep {
			t.Errorf("%s kept command = %q, want %q", event, cmd, keep)
		}
	}
	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "Stop"} {
		if jsonAt(got, "hooks", event) != nil {
			t.Errorf("event %s should be absent", event)
		}
	}
}

func TestUninstallClaudeRemovesHerdrHooksAndPreservesOthers(t *testing.T) {
	home := testHome(t)
	claudeDir := filepath.Join(home, ".claude")
	mustMkdirAll(t, claudeDir)

	installed, err := InstallClaude()
	if err != nil {
		t.Fatal(err)
	}
	// A user hook alongside the herdr entry must survive uninstall.
	settings := fmt.Sprintf(`{"hooks":{
		"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":"bash '%s' session","timeout":10}]}],
		"UserPromptSubmit":[{"hooks":[{"type":"command","command":"echo keep","timeout":10}]}]
	}}`, installed.HookPath)
	mustWriteFile(t, installed.SettingsPath, settings)

	result, err := UninstallClaude()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedHookFile || !result.UpdatedSettings {
		t.Fatalf("result = %+v", result)
	}
	if isFile(installed.HookPath) {
		t.Fatal("hook file still present")
	}
	got := readJSONFile(t, installed.SettingsPath)
	if jsonAt(got, "hooks", "SessionStart") != nil {
		t.Fatal("herdr SessionStart entry still present")
	}
	if cmd := jsonStringAt(t, got, "hooks", "UserPromptSubmit", 0, "hooks", 0, "command"); cmd != "echo keep" {
		t.Fatalf("user hook lost: %q", cmd)
	}
}

func TestInstallClaudeErrorsWhenClaudeDirMissing(t *testing.T) {
	testHome(t)
	_, err := InstallClaude()
	if err == nil || !strings.Contains(err.Error(), "claude directory not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallClaudeErrorsOnUnparsableSettings(t *testing.T) {
	home := testHome(t)
	claudeDir := filepath.Join(home, ".claude")
	mustWriteFile(t, filepath.Join(claudeDir, "settings.json"), "{not json")

	_, err := InstallClaude()
	if err == nil || !strings.Contains(err.Error(), "failed to parse ") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallClaudeErrorsWhenSettingsNotObject(t *testing.T) {
	home := testHome(t)
	claudeDir := filepath.Join(home, ".claude")
	mustWriteFile(t, filepath.Join(claudeDir, "settings.json"), `[1,2,3]`)

	_, err := InstallClaude()
	if err == nil || !strings.Contains(err.Error(), "claude settings at ") ||
		!strings.Contains(err.Error(), "must be a JSON object") {
		t.Fatalf("err = %v", err)
	}
}
