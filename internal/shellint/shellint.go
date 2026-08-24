// Package shellint installs cats's shell integration — cats's general setup
// for the user's own shell. Two things ride in it:
//
//   - the OSC 133 marks a shell prints around its prompt and each command,
//     which are what the command ledger is built on;
//   - cats tool setup: the cats bin dir (~/.cats/bin, where the plugin host
//     exposes plugin binaries) on PATH, plus an eval of `catctl shellinit`,
//     which emits a source line per installed plugin's [shell] snippet. The
//     eval-at-startup indirection is what keeps plugin installs and
//     uninstalls out of rc files entirely.
//
// It is deliberately NOT one of internal/integration's Targets. Those wire a
// coding agent to a running cats server by editing that agent's own config
// tree; this edits the user's SHELL, in a file they wrote by hand and read
// often, for three different shells with three different hook mechanisms. The
// two share a CLI verb and nothing else.
//
// # What gets written
//
// A script per shell under the cats config directory, and one guarded block in
// the shell's rc file that sources it:
//
//	# >>> cats shell integration >>>
//	[ -f "$HOME/.config/cats/shell/cats.zsh" ] && . "$HOME/.config/cats/shell/cats.zsh"
//	# <<< cats shell integration <<<
//
// The block is what makes install idempotent and uninstall exact. Rewriting a
// user's rc file with anything less precise than "the lines between these two
// markers" is how a tool eats a config somebody spent years on — so nothing
// here ever edits a line it did not write, and an uninstall that finds no
// markers changes nothing rather than guessing.
//
// The indirection through a sourced file is what makes an UPDATE not touch the
// rc file at all: the next release rewrites the script, and the one line
// pointing at it is already correct.
package shellint

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed assets/cats.bash
var bashAsset string

//go:embed assets/cats.zsh
var zshAsset string

//go:embed assets/cats.fish
var fishAsset string

// Version is stamped in every asset as CATS_INTEGRATION_VERSION and is what
// Status compares against, mirroring internal/integration's marker scheme.
//
// 2: the assets grew the cats tool setup (PATH + `catctl shellinit` eval), so
// v1 installs correctly report Outdated until re-installed.
const Version = 2

const (
	beginMarker = "# >>> cats shell integration >>>"
	endMarker   = "# <<< cats shell integration <<<"
)

// Shell is one supported shell.
type Shell int

const (
	Bash Shell = iota
	Zsh
	Fish
)

// AllShells is the canonical order, used by listings and by "install the one
// you are running" detection.
func AllShells() []Shell { return []Shell{Bash, Zsh, Fish} }

// Label is the lowercase CLI name.
func (s Shell) Label() string {
	switch s {
	case Bash:
		return "bash"
	case Zsh:
		return "zsh"
	case Fish:
		return "fish"
	}
	return "unknown"
}

// ParseShell resolves a CLI name.
func ParseShell(name string) (Shell, bool) {
	for _, s := range AllShells() {
		if s.Label() == name {
			return s, true
		}
	}
	return 0, false
}

// asset is the script contents for a shell.
func (s Shell) asset() string {
	switch s {
	case Bash:
		return bashAsset
	case Zsh:
		return zshAsset
	case Fish:
		return fishAsset
	}
	return ""
}

// scriptName is the file the rc block sources.
func (s Shell) scriptName() string {
	switch s {
	case Bash:
		return "cats.bash"
	case Zsh:
		return "cats.zsh"
	case Fish:
		return "cats.fish"
	}
	return ""
}

// Detect picks the shell to act on when the user named none: the one behind
// $SHELL. It is a guess, but the right one — $SHELL is the login shell, which
// is the one whose rc file a person means by "my shell".
func Detect() (Shell, bool) {
	base := filepath.Base(os.Getenv("SHELL"))
	return ParseShell(base)
}

// Install writes the script and adds (or refreshes) the rc block. It returns the
// paths it touched, in the order it touched them.
func Install(s Shell) (script, rc string, err error) {
	script, err = ScriptPath(s)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		return "", "", fmt.Errorf("shell integration: %w", err)
	}
	if err := os.WriteFile(script, []byte(s.asset()), 0o644); err != nil {
		return "", "", fmt.Errorf("shell integration: %w", err)
	}
	rc, err = RCPath(s)
	if err != nil {
		return script, "", err
	}
	if err := writeRCBlock(rc, s, script); err != nil {
		return script, rc, err
	}
	return script, rc, nil
}

// Uninstall removes the rc block and the script. A missing rc block is not an
// error: uninstalling something that was never installed is a no-op, not a
// failure, and the caller reports what it actually removed.
func Uninstall(s Shell) (removedBlock bool, removedScript bool, err error) {
	rc, err := RCPath(s)
	if err != nil {
		return false, false, err
	}
	removedBlock, err = stripRCBlock(rc)
	if err != nil {
		return false, false, err
	}
	script, err := ScriptPath(s)
	if err != nil {
		return removedBlock, false, err
	}
	switch err := os.Remove(script); {
	case err == nil:
		removedScript = true
	case errors.Is(err, os.ErrNotExist):
	default:
		return removedBlock, false, fmt.Errorf("shell integration: %w", err)
	}
	return removedBlock, removedScript, nil
}

// State is what Status reports.
type State int

const (
	NotInstalled State = iota
	Current
	Outdated // the script is ours but from an older release
	Orphaned // the rc block is there but the script it sources is not
)

// Status describes one shell's installation.
type Status struct {
	Shell            Shell
	State            State
	InstalledVersion int // -1 when the script carries no marker
	ScriptPath       string
	RCPath           string
}

// Statuses reports every shell, in canonical order — including the ones that
// are not installed, so `status` answers "what could I turn on here" as well as
// "what is on".
func Statuses() []Status {
	out := make([]Status, 0, len(AllShells()))
	for _, s := range AllShells() {
		out = append(out, StatusOf(s))
	}
	return out
}

// StatusOf inspects one shell.
func StatusOf(s Shell) Status {
	st := Status{Shell: s, State: NotInstalled, InstalledVersion: -1}
	st.ScriptPath, _ = ScriptPath(s)
	st.RCPath, _ = RCPath(s)

	sourced := st.RCPath != "" && rcHasBlock(st.RCPath)
	data, err := os.ReadFile(st.ScriptPath)
	if err != nil {
		if sourced {
			// A block pointing at a script that is gone is worse than nothing:
			// the shell prints an error on every login, or (with the guard in
			// the block) silently does nothing while status claims it is on.
			st.State = Orphaned
		}
		return st
	}
	st.InstalledVersion = assetVersion(string(data))
	switch {
	case !sourced:
		// The script exists but nothing sources it — an rc file edited by hand,
		// or a home directory restored from a backup.
		st.State = Orphaned
	case st.InstalledVersion == Version:
		st.State = Current
	default:
		st.State = Outdated
	}
	return st
}

// ScriptPath is where a shell's script lives: $XDG_CONFIG_HOME/cats/shell/, the
// same config-home chain the config file uses, because an integration script is
// configuration (what you chose to have) rather than state.
func ScriptPath(s Shell) (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("shell integration: no home directory: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cats", "shell", s.scriptName()), nil
}

// RCPath is the file the block goes in.
//
// bash reads .bashrc for interactive non-login shells, which is what a terminal
// starts; a macOS user whose login shell reads .bash_profile still gets there,
// because the conventional .bash_profile sources .bashrc. Guessing between them
// per platform would put the block in the file the user does not read.
func RCPath(s Shell) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("shell integration: no home directory: %w", err)
	}
	switch s {
	case Bash:
		return filepath.Join(home, ".bashrc"), nil
	case Zsh:
		// $ZDOTDIR when set: a user who moved their zsh config has moved it, and
		// writing to ~/.zshrc would create a second file nothing reads.
		if zdot := os.Getenv("ZDOTDIR"); zdot != "" {
			return filepath.Join(zdot, ".zshrc"), nil
		}
		return filepath.Join(home, ".zshrc"), nil
	case Fish:
		return filepath.Join(home, ".config", "fish", "config.fish"), nil
	}
	return "", fmt.Errorf("shell integration: unknown shell")
}

// blockFor renders the guarded rc block. The guard on the file's existence is
// what keeps a removed script from breaking every new shell.
func blockFor(s Shell, script string) string {
	var body string
	if s == Fish {
		body = fmt.Sprintf("test -f %q; and source %q", script, script)
	} else {
		body = fmt.Sprintf("[ -f %q ] && . %q", script, script)
	}
	return beginMarker + "\n" + body + "\n" + endMarker + "\n"
}

// writeRCBlock adds the block, or replaces an existing one in place. In place
// matters: a user who put ours in the middle of their file for a reason (before
// a prompt framework that also touches PS1) must find it there after an update.
func writeRCBlock(rc string, s Shell, script string) error {
	data, err := os.ReadFile(rc)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("shell integration: %w", err)
	}
	block := blockFor(s, script)
	text := string(data)
	if before, after, found := cutBlock(text); found {
		text = before + block + after
	} else {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "\n" + block
	}
	if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
		return fmt.Errorf("shell integration: %w", err)
	}
	if err := os.WriteFile(rc, []byte(text), 0o644); err != nil {
		return fmt.Errorf("shell integration: %w", err)
	}
	return nil
}

// stripRCBlock removes the block, reporting whether one was there.
func stripRCBlock(rc string) (bool, error) {
	data, err := os.ReadFile(rc)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("shell integration: %w", err)
	}
	before, after, found := cutBlock(string(data))
	if !found {
		return false, nil
	}
	// The blank line install added before the block goes with it, so an
	// install/uninstall cycle leaves the file byte-identical. Only ONE newline
	// is taken, and only when there are two: a block the user placed by hand
	// against their own line has a single terminator before it, and eating that
	// would join two of their lines together.
	text := before
	if strings.HasSuffix(text, "\n\n") {
		text = text[:len(text)-1]
	}
	text += after
	if strings.TrimSpace(text) == "" {
		text = ""
	}
	if err := os.WriteFile(rc, []byte(text), 0o644); err != nil {
		return false, fmt.Errorf("shell integration: %w", err)
	}
	return true, nil
}

// cutBlock splits text around our guarded block. An unterminated begin marker —
// a file somebody edited half-way through — is treated as "not found", so the
// installer appends a fresh block rather than swallowing the rest of the file.
func cutBlock(text string) (before, after string, found bool) {
	i := strings.Index(text, beginMarker)
	if i < 0 {
		return text, "", false
	}
	j := strings.Index(text[i:], endMarker)
	if j < 0 {
		return text, "", false
	}
	end := i + j + len(endMarker)
	if end < len(text) && text[end] == '\n' {
		end++
	}
	return text[:i], text[end:], true
}

// rcHasBlock reports whether an rc file already sources us.
func rcHasBlock(rc string) bool {
	data, err := os.ReadFile(rc)
	if err != nil {
		return false
	}
	_, _, found := cutBlock(string(data))
	return found
}

// assetVersion reads the CATS_INTEGRATION_VERSION marker out of a script, or -1
// when there is none — which is how a hand-written or pre-marker file reads.
func assetVersion(text string) int {
	const marker = "CATS_INTEGRATION_VERSION="
	i := strings.Index(text, marker)
	if i < 0 {
		return -1
	}
	rest := text[i+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return -1
	}
	v := 0
	for _, c := range rest[:end] {
		v = v*10 + int(c-'0')
	}
	return v
}
