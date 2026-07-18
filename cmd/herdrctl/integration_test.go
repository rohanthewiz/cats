package main

import (
	"os"
	"path/filepath"
	"testing"
)

// setTestHome isolates the integration verb from the developer's real agent
// config trees (t.Setenv also serializes these tests).
func setTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, envVar := range []string{
		"PI_CODING_AGENT_DIR", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "KIMI_CODE_HOME",
		"COPILOT_HOME", "QODER_CONFIG_DIR", "CURSOR_CONFIG_DIR",
	} {
		t.Setenv(envVar, "")
	}
	return home
}

func TestRunIntegrationExitCodes(t *testing.T) {
	setTestHome(t)

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"bare integration prints help, usage error", nil, 2},
		{"help", []string{"help"}, 0},
		{"--help", []string{"--help"}, 0},
		{"-h", []string{"-h"}, 0},
		{"unknown subcommand", []string{"frobnicate"}, 2},
		{"install without target", []string{"install"}, 2},
		{"install extra args", []string{"install", "pi", "omp"}, 2},
		{"install unknown target", []string{"install", "nope"}, 2},
		{"uninstall unknown target", []string{"uninstall", "nope"}, 2},
		{"status bad flag", []string{"status", "--wat"}, 2},
		{"status", []string{"status"}, 0},
		{"status outdated only", []string{"status", "--outdated-only"}, 0},
		{"install pi fails offline with missing dir", []string{"install", "pi"}, 1},
		{"uninstall pi succeeds when nothing installed", []string{"uninstall", "pi"}, 0},
	}
	for _, tc := range cases {
		if got := runIntegration(tc.args); got != tc.want {
			t.Errorf("%s: runIntegration(%v) = %d, want %d", tc.name, tc.args, got, tc.want)
		}
	}
}

func TestRunIntegrationInstallSucceedsOffline(t *testing.T) {
	home := setTestHome(t)
	// opencode's config dir existing is the only prerequisite; no server or
	// socket is involved.
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := runIntegration([]string{"install", "opencode"}); got != 0 {
		t.Fatalf("install opencode = %d, want 0", got)
	}
	if got := runIntegration([]string{"uninstall", "opencode"}); got != 0 {
		t.Fatalf("uninstall opencode = %d, want 0", got)
	}
}
