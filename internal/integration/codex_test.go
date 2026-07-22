package integration

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallCodexWritesHookAndUpdatesHooksAndConfig(t *testing.T) {
	home := testHome(t)
	codexDir := filepath.Join(home, ".codex")
	mustMkdirAll(t, codexDir)

	installed, err := InstallCodex()
	if err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}

	if installed.HookPath != filepath.Join(codexDir, codexHookInstallName) {
		t.Fatalf("hook path = %s", installed.HookPath)
	}
	if mustReadFile(t, installed.HookPath) != codexHookAsset {
		t.Fatal("hook content differs from embedded asset")
	}
	assertFileMode(t, installed.HookPath, 0o755)

	hooks := readJSONFile(t, installed.HooksPath)
	command := jsonStringAt(t, hooks, "hooks", "SessionStart", 0, "hooks", 0, "command")
	if !strings.HasSuffix(command, " session") {
		t.Fatalf("command = %q", command)
	}
	if jsonAt(hooks, "hooks", "SessionStart", 0, "matcher") != nil {
		t.Fatal("codex entries must not carry a matcher")
	}

	config := mustReadFile(t, installed.ConfigPath)
	if config != "[features]\nhooks = true\n" {
		t.Fatalf("config = %q", config)
	}
}

func TestInstallCodexUsesCodexHomeEnv(t *testing.T) {
	testHome(t)
	codexDir := t.TempDir()
	t.Setenv(codexHomeEnvVar, codexDir)

	installed, err := InstallCodex()
	if err != nil {
		t.Fatal(err)
	}
	if installed.HookPath != filepath.Join(codexDir, codexHookInstallName) {
		t.Fatalf("env override not honored: %s", installed.HookPath)
	}
}

func TestInstallCodexIsIdempotentForHookEntriesAndFeatureFlag(t *testing.T) {
	home := testHome(t)
	codexDir := filepath.Join(home, ".codex")
	mustWriteFile(t, filepath.Join(codexDir, "config.toml"),
		"[features]\ncodex_hooks = false\nother = true\n")

	if _, err := InstallCodex(); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodex(); err != nil {
		t.Fatal(err)
	}

	hooks := readJSONFile(t, filepath.Join(codexDir, "hooks.json"))
	entries, ok := jsonAt(hooks, "hooks", "SessionStart").([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("SessionStart count = %v", jsonAt(hooks, "hooks", "SessionStart"))
	}
	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "PermissionRequest", "Stop"} {
		if jsonAt(hooks, "hooks", event) != nil {
			t.Errorf("event %s should be absent", event)
		}
	}

	config := mustReadFile(t, filepath.Join(codexDir, "config.toml"))
	if got := strings.Count(config, "hooks = true"); got != 1 {
		t.Fatalf("hooks = true count = %d in %q", got, config)
	}
	if strings.Contains(config, "codex_hooks") {
		t.Fatalf("deprecated codex_hooks kept: %q", config)
	}
	if !strings.Contains(config, "other = true") {
		t.Fatalf("user feature flag lost: %q", config)
	}
}

func TestInstallCodexOnlyMigratesTopLevelFeatureFlags(t *testing.T) {
	home := testHome(t)
	codexDir := filepath.Join(home, ".codex")
	mustWriteFile(t, filepath.Join(codexDir, "config.toml"),
		"profile = \"work\"\n\n[profiles.work.features]\nhooks = false\ncodex_hooks = false\n\n[features]\ncodex_hooks = true\nother = true\n")

	if _, err := InstallCodex(); err != nil {
		t.Fatal(err)
	}

	config := mustReadFile(t, filepath.Join(codexDir, "config.toml"))
	if !strings.Contains(config, "[profiles.work.features]\nhooks = false\ncodex_hooks = false") {
		t.Fatalf("profile table modified: %q", config)
	}
	if !strings.Contains(config, "[features]\nhooks = true\nother = true") {
		t.Fatalf("top-level table wrong: %q", config)
	}
}

func TestUninstallCodexRemovesCatsHooksAndLeavesConfigAlone(t *testing.T) {
	home := testHome(t)
	codexDir := filepath.Join(home, ".codex")
	hookPath := filepath.Join(codexDir, codexHookInstallName)
	mustWriteFile(t, hookPath, codexHookAsset)
	cats := func(action string) string {
		return fmt.Sprintf(`{"type":"command","command":"bash '%s' %s","timeout":10}`, hookPath, action)
	}
	mustWriteFile(t, filepath.Join(codexDir, "hooks.json"), fmt.Sprintf(`{"hooks":{
		"SessionStart":[{"hooks":[%s]}],
		"UserPromptSubmit":[{"hooks":[%s,{"type":"command","command":"echo keep","timeout":10}]}],
		"PreToolUse":[{"hooks":[%s]}],
		"PermissionRequest":[{"hooks":[%s]}],
		"Stop":[{"hooks":[%s]}]
	}}`, cats("idle"), cats("working"), cats("working"), cats("blocked"), cats("idle")))
	mustWriteFile(t, filepath.Join(codexDir, "config.toml"), "[features]\nhooks = true\nother = true\n")

	result, err := UninstallCodex()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedHookFile || !result.UpdatedHooks {
		t.Fatalf("result = %+v", result)
	}
	if isFile(hookPath) {
		t.Fatal("hook file still present")
	}

	hooks := readJSONFile(t, filepath.Join(codexDir, "hooks.json"))
	for _, event := range []string{"SessionStart", "PreToolUse", "PermissionRequest", "Stop"} {
		if jsonAt(hooks, "hooks", event) != nil {
			t.Errorf("event %s should be absent", event)
		}
	}
	kept, ok := jsonAt(hooks, "hooks", "UserPromptSubmit", 0, "hooks").([]any)
	if !ok || len(kept) != 1 {
		t.Fatalf("user hooks = %v", kept)
	}
	if cmd := jsonStringAt(t, hooks, "hooks", "UserPromptSubmit", 0, "hooks", 0, "command"); cmd != "echo keep" {
		t.Fatalf("kept command = %q", cmd)
	}

	// Uninstall never touches config.toml.
	config := mustReadFile(t, filepath.Join(codexDir, "config.toml"))
	if !strings.Contains(config, "hooks = true") || !strings.Contains(config, "other = true") {
		t.Fatalf("config modified: %q", config)
	}
}

func TestInstallCodexErrorsWhenConfigDirMissing(t *testing.T) {
	testHome(t)
	_, err := InstallCodex()
	if err == nil || !strings.Contains(err.Error(), "codex config directory not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildCodexConfigWithHooksAppendVariants(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty file", "", "[features]\nhooks = true\n"},
		{"content without features", "model = \"gpt\"\n", "model = \"gpt\"\n\n[features]\nhooks = true\n"},
		{"features without hooks", "[features]\nother = true\n", "[features]\nhooks = true\nother = true\n"},
		{"hooks false flipped", "[features]\nhooks = false\n", "[features]\nhooks = true\n"},
		{"already correct", "[features]\nhooks = true\n", "[features]\nhooks = true\n"},
	}
	for _, tc := range cases {
		if got := buildCodexConfigWithHooks(tc.in); got != tc.want {
			t.Errorf("%s:\ngot:  %q\nwant: %q", tc.name, got, tc.want)
		}
	}
}
