package shellint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandbox points the installer at a throwaway home and config root, so no test
// can touch a real ~/.zshrc.
func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("ZDOTDIR", "")
	return dir
}

// The whole cycle for every shell: install writes a script and one guarded
// block, status sees it, and uninstall leaves the rc file byte-identical to
// what it was before.
func TestInstallUninstallRoundTrip(t *testing.T) {
	for _, sh := range AllShells() {
		t.Run(sh.Label(), func(t *testing.T) {
			sandbox(t)
			rcPath, err := RCPath(sh)
			if err != nil {
				t.Fatalf("rc path: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(rcPath), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			original := "# my shell config\nexport EDITOR=ced\n"
			if err := os.WriteFile(rcPath, []byte(original), 0o644); err != nil {
				t.Fatalf("seed rc: %v", err)
			}

			script, rc, err := Install(sh)
			if err != nil {
				t.Fatalf("install: %v", err)
			}
			if rc != rcPath {
				t.Fatalf("wrote to %s, want %s", rc, rcPath)
			}
			if _, err := os.Stat(script); err != nil {
				t.Fatalf("script not written: %v", err)
			}
			body := readFile(t, rcPath)
			if !strings.HasPrefix(body, original) {
				t.Errorf("the user's own lines were disturbed:\n%s", body)
			}
			if strings.Count(body, beginMarker) != 1 {
				t.Errorf("want exactly one block:\n%s", body)
			}
			if !strings.Contains(body, script) {
				t.Errorf("the block does not source the script:\n%s", body)
			}

			if st := StatusOf(sh); st.State != Current || st.InstalledVersion != Version {
				t.Errorf("status = %+v, want current v%d", st, Version)
			}

			removedBlock, removedScript, err := Uninstall(sh)
			if err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			if !removedBlock || !removedScript {
				t.Errorf("uninstall reported block=%v script=%v", removedBlock, removedScript)
			}
			if got := readFile(t, rcPath); got != original {
				t.Errorf("rc file not restored:\n%q\nwant\n%q", got, original)
			}
			if st := StatusOf(sh); st.State != NotInstalled {
				t.Errorf("status after uninstall = %+v", st)
			}
		})
	}
}

// Installing twice must not stack blocks, and an update must replace the block
// WHERE IT IS — a user who put ours before their prompt framework did it for a
// reason.
func TestInstallIsIdempotentAndInPlace(t *testing.T) {
	sandbox(t)
	rcPath, _ := RCPath(Zsh)
	script, _ := ScriptPath(Zsh)
	head := "# top\n"
	tail := "\n# a prompt framework that also touches PS1\neval \"$(starship init zsh)\"\n"
	seed := head + blockFor(Zsh, "/an/old/path/cats.zsh") + tail
	if err := os.WriteFile(rcPath, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, _, err := Install(Zsh); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, _, err := Install(Zsh); err != nil {
		t.Fatalf("second install: %v", err)
	}

	body := readFile(t, rcPath)
	if n := strings.Count(body, beginMarker); n != 1 {
		t.Fatalf("%d blocks after two installs:\n%s", n, body)
	}
	if !strings.HasPrefix(body, head) || !strings.HasSuffix(body, tail) {
		t.Errorf("the block moved:\n%s", body)
	}
	if strings.Contains(body, "/an/old/path/") || !strings.Contains(body, script) {
		t.Errorf("the block was not refreshed:\n%s", body)
	}
}

// Nothing here may edit a line it did not write. A file with no block of ours
// comes back untouched from an uninstall, and one whose block was hand-mangled
// is left alone rather than guessed at.
func TestUninstallNeverGuesses(t *testing.T) {
	t.Run("no block", func(t *testing.T) {
		sandbox(t)
		rcPath, _ := RCPath(Bash)
		original := "export PATH=$PATH:/opt/bin\n. ~/.other-integration.sh\n"
		if err := os.WriteFile(rcPath, []byte(original), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		removed, _, err := Uninstall(Bash)
		if err != nil {
			t.Fatalf("uninstall: %v", err)
		}
		if removed {
			t.Error("reported removing a block that was never there")
		}
		if got := readFile(t, rcPath); got != original {
			t.Errorf("rc file changed:\n%q", got)
		}
	})

	t.Run("unterminated block", func(t *testing.T) {
		// Half an edit: the begin marker with no end. Swallowing the rest of the
		// file is the disaster this guards against.
		sandbox(t)
		rcPath, _ := RCPath(Bash)
		original := beginMarker + "\n. /somewhere\nexport KEEP=me\n"
		if err := os.WriteFile(rcPath, []byte(original), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, _, err := Uninstall(Bash); err != nil {
			t.Fatalf("uninstall: %v", err)
		}
		if got := readFile(t, rcPath); got != original {
			t.Errorf("an unterminated block was acted on:\n%q", got)
		}
	})

	t.Run("no rc file at all", func(t *testing.T) {
		sandbox(t)
		removed, _, err := Uninstall(Bash)
		if err != nil || removed {
			t.Errorf("uninstall with no rc file: removed=%v err=%v", removed, err)
		}
	})
}

// The two half-installed states are distinguished, because they need different
// fixes: an old script wants an update, a block with no script behind it wants
// a reinstall.
func TestStatusDetectsHalfInstalls(t *testing.T) {
	t.Run("outdated script", func(t *testing.T) {
		sandbox(t)
		if _, _, err := Install(Zsh); err != nil {
			t.Fatalf("install: %v", err)
		}
		script, _ := ScriptPath(Zsh)
		// Rewrite whatever stamp the current asset carries, so the simulated
		// downgrade survives future version bumps.
		body := strings.Replace(readFile(t, script),
			fmt.Sprintf("CATS_INTEGRATION_VERSION=%d", Version), "CATS_INTEGRATION_VERSION=0", 1)
		if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
			t.Fatalf("downgrade: %v", err)
		}
		if st := StatusOf(Zsh); st.State != Outdated || st.InstalledVersion != 0 {
			t.Errorf("status = %+v, want outdated v0", st)
		}
	})

	t.Run("block with no script", func(t *testing.T) {
		sandbox(t)
		if _, _, err := Install(Zsh); err != nil {
			t.Fatalf("install: %v", err)
		}
		script, _ := ScriptPath(Zsh)
		if err := os.Remove(script); err != nil {
			t.Fatalf("remove script: %v", err)
		}
		if st := StatusOf(Zsh); st.State != Orphaned {
			t.Errorf("status = %+v, want orphaned", st)
		}
	})

	t.Run("script with no block", func(t *testing.T) {
		sandbox(t)
		if _, _, err := Install(Zsh); err != nil {
			t.Fatalf("install: %v", err)
		}
		rcPath, _ := RCPath(Zsh)
		if err := os.WriteFile(rcPath, []byte("# hand-edited\n"), 0o644); err != nil {
			t.Fatalf("rewrite rc: %v", err)
		}
		if st := StatusOf(Zsh); st.State != Orphaned {
			t.Errorf("status = %+v, want orphaned", st)
		}
	})
}

// $ZDOTDIR wins for zsh: a user who moved their zsh config has moved it, and
// writing to ~/.zshrc would create a second file nothing reads.
func TestZshHonoursZdotdir(t *testing.T) {
	home := sandbox(t)
	zdot := filepath.Join(home, "cfg", "zsh")
	if err := os.MkdirAll(zdot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("ZDOTDIR", zdot)
	_, rc, err := Install(Zsh)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rc != filepath.Join(zdot, ".zshrc") {
		t.Errorf("wrote to %s, want the ZDOTDIR rc", rc)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); err == nil {
		t.Error("also created ~/.zshrc")
	}
}

// Every asset carries the marker Status reads, emits all four OSC 133 marks and
// the command line, and never emits from a non-interactive shell — where these
// bytes would corrupt whatever is reading the pipe.
func TestAssetsAreWellFormed(t *testing.T) {
	for _, sh := range AllShells() {
		t.Run(sh.Label(), func(t *testing.T) {
			a := sh.asset()
			if assetVersion(a) != Version {
				t.Errorf("version marker = %d, want %d", assetVersion(a), Version)
			}
			for _, want := range []string{"133;A", "133;B", "133;C", "133;D", "633;E", "7;file://"} {
				if !strings.Contains(a, want) {
					t.Errorf("asset does not emit %s", want)
				}
			}
			if !strings.Contains(a, "interactive") && !strings.Contains(a, "case \"$-\"") {
				t.Error("asset has no interactive-shell guard")
			}
		})
	}
}

// Detect follows $SHELL, which is the shell whose rc file a person means.
func TestDetect(t *testing.T) {
	t.Setenv("SHELL", "/opt/homebrew/bin/fish")
	if s, ok := Detect(); !ok || s != Fish {
		t.Errorf("Detect = %v/%v, want fish", s.Label(), ok)
	}
	t.Setenv("SHELL", "/usr/bin/nu")
	if _, ok := Detect(); ok {
		t.Error("Detect claimed an unsupported shell")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// v2's addition: every asset carries the cats tool setup — the guarded
// ~/.cats/bin PATH prepend and the shellinit eval — so a shell that installed
// the integration needs nothing else for plugin tools to work.
func TestAssetsCarryToolSetup(t *testing.T) {
	for sh, asset := range map[string]string{
		"bash": bashAsset, "zsh": zshAsset, "fish": fishAsset,
	} {
		if !strings.Contains(asset, `.cats/bin`) {
			t.Errorf("%s asset lacks the cats bin PATH prepend", sh)
		}
		if !strings.Contains(asset, "catctl shellinit "+sh) {
			t.Errorf("%s asset lacks the shellinit eval", sh)
		}
	}
}
