package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/plugin"
	"github.com/rohanthewiz/cats/internal/shellenv"
)

// snippetPlugin lays out an installed plugin (a real dir in a scratch plugins
// root, no build needed) declaring a zsh snippet, and returns the root.
func snippetPlugin(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(plugin.DirEnvVar, root)
	dir := filepath.Join(root, "acme.tool")
	if err := os.MkdirAll(filepath.Join(dir, "shell"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "id = \"acme.tool\"\nversion = \"1.0\"\n\n[shell]\nzsh = \"shell/tool.zsh\"\n"
	if err := os.WriteFile(filepath.Join(dir, plugin.ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// The generated script is the whole contract: a guarded $HOME-relative PATH
// prepend, then one guarded source line per plugin snippet — and nothing for a
// shell the plugin says nothing about.
func TestShellinitScript(t *testing.T) {
	root := snippetPlugin(t)
	t.Setenv(shellenv.CatsBinEnvVar, "") // default: $HOME/.cats/bin, spelled symbolically

	zsh := shellinitScript("zsh")
	if !strings.Contains(zsh, `export PATH="$HOME/.cats/bin:$PATH"`) {
		t.Errorf("zsh script lacks the PATH prepend:\n%s", zsh)
	}
	if !strings.Contains(zsh, `case ":$PATH:"`) {
		t.Errorf("zsh PATH prepend is unguarded:\n%s", zsh)
	}
	snippet := filepath.Join(root, "acme.tool", "shell/tool.zsh")
	if !strings.Contains(zsh, "[ -f '"+snippet+"' ] && . '"+snippet+"'") {
		t.Errorf("zsh script lacks the guarded source line for %s:\n%s", snippet, zsh)
	}

	// No bash snippet declared: bash gets the PATH prepend only.
	if bash := shellinitScript("bash"); strings.Contains(bash, "acme.tool") {
		t.Errorf("bash script sources a zsh-only snippet:\n%s", bash)
	}

	// fish spells both halves in its own syntax.
	fish := shellinitScript("fish")
	if !strings.Contains(fish, "contains -- \"$HOME/.cats/bin\" $PATH") {
		t.Errorf("fish script lacks the PATH guard:\n%s", fish)
	}

	// Uninstalling (here: the entry vanishing) removes the line on the next
	// generation — the no-rc-surgery property the design leans on.
	if err := os.RemoveAll(filepath.Join(root, "acme.tool")); err != nil {
		t.Fatal(err)
	}
	if regen := shellinitScript("zsh"); strings.Contains(regen, "acme.tool") {
		t.Errorf("removed plugin still emitted:\n%s", regen)
	}
}

// An explicit $CATS_BIN_DIR is this machine's deliberate choice and is emitted
// literally (quoted), not symbolically.
func TestShellinitBinDirOverride(t *testing.T) {
	t.Setenv(plugin.DirEnvVar, t.TempDir())
	t.Setenv(shellenv.CatsBinEnvVar, "/opt/cats bin")
	zsh := shellinitScript("zsh")
	// The dir sits inside double-quoted shell text; a space must survive and
	// no quote characters may leak into the PATH value itself.
	if !strings.Contains(zsh, `export PATH="/opt/cats bin:$PATH"`) {
		t.Errorf("override not emitted into the double-quoted context:\n%s", zsh)
	}
	if strings.Contains(zsh, ".cats/bin") {
		t.Errorf("default dir emitted despite override:\n%s", zsh)
	}
}
