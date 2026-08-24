package main

// shellinit.go emits the shell-startup half of the cats tool story: a guarded
// PATH prepend of the cats bin dir (~/.cats/bin, the symlink farm where the
// plugin host exposes plugin binaries), plus one source line per installed
// plugin that declares a [shell] snippet for the running shell.
//
// Like `catctl completion`, the script is regenerated at every shell startup
// (an eval in the rc file, installed for you by `catctl integration install
// shell`), which is the whole trick: installing a plugin makes its tool and
// its shell functions appear in the next terminal, and uninstalling needs no
// rc-file surgery — the source line simply stops being emitted. Nothing
// plugin-specific is ever written into the user's rc files.
//
// The PATH line spells the directory as $HOME/.cats/bin rather than the
// expanded absolute path so the emitted text is stable across machines — it
// survives a dotfile setup that captures eval output, and reads the same in
// every shell. Only an explicit $CATS_BIN_DIR override is emitted literally,
// since it is this machine's deliberate choice.

import (
	"fmt"
	"os"
	"strings"

	"github.com/rohanthewiz/cats/internal/plugin"
	"github.com/rohanthewiz/cats/internal/shellenv"
)

// runShellinit implements `catctl shellinit <shell>`.
func runShellinit(args []string) int {
	if len(args) != 1 {
		printShellinitHelp()
		return 2
	}
	shell := args[0]
	switch shell {
	case "bash", "zsh", "fish":
	case "help", "--help", "-h":
		printShellinitHelp()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "catctl: unknown shell %q\n", shell)
		printShellinitHelp()
		return 2
	}
	os.Stdout.WriteString(shellinitScript(shell))
	return 0
}

// shellinitScript assembles the PATH guard and the plugin source lines for one
// shell. A plugins root that cannot be read degrades to just the PATH guard —
// shell startup is the worst place to fail loudly.
func shellinitScript(shell string) string {
	var b strings.Builder

	// The PATH prepend, guarded so re-evaluation (shellint emits one too, as a
	// bootstrap) never stacks duplicate entries. The dir leads the PATH so a
	// plugin-managed tool beats a stale hand-installed copy in ~/bin — the same
	// ordering shellenv.Merge gives spawned panes.
	//
	// The directory lands inside double-quoted shell text, where single-quote
	// wrapping would become literal apostrophes — so an override is escaped for
	// that context instead, while the default stays a raw $HOME reference the
	// shell expands.
	binDir := "$HOME/.cats/bin"
	if override := os.Getenv(shellenv.CatsBinEnvVar); override != "" {
		binDir = dqEscape(override, shell == "fish")
	}
	if shell == "fish" {
		fmt.Fprintf(&b, "if not contains -- \"%s\" $PATH\n    set -gx PATH \"%s\" $PATH\nend\n", binDir, binDir)
	} else {
		fmt.Fprintf(&b, "case \":$PATH:\" in *\":%s:\"*) ;; *) export PATH=\"%s:$PATH\" ;; esac\n", binDir, binDir)
	}

	plugins, err := plugin.List()
	if err != nil {
		return b.String()
	}
	for _, p := range plugins {
		if p.Err != nil {
			continue // never parsed, so it declares nothing
		}
		path, ok := plugin.ShellSnippetPath(p, shell)
		if !ok {
			continue
		}
		// Guarded per line: a snippet deleted from a linked checkout must not
		// make every new shell print an error until the plugin is unlinked.
		fmt.Fprintf(&b, "# from plugin %s\n", p.ID)
		if shell == "fish" {
			fmt.Fprintf(&b, "test -f %s; and source %s\n", shellQuote(path), shellQuote(path))
		} else {
			fmt.Fprintf(&b, "[ -f %s ] && . %s\n", shellQuote(path), shellQuote(path))
		}
	}
	return b.String()
}

// dqEscape escapes s for interpolation inside a double-quoted shell string.
// POSIX shells give four characters meaning there (\ " $ `); fish only three,
// and escaping a backtick in fish would leave a stray backslash behind, so the
// backtick is escaped only where it is live.
func dqEscape(s string, fish bool) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(s)
	if !fish {
		s = strings.ReplaceAll(s, "`", "\\`")
	}
	return s
}

func printShellinitHelp() {
	fmt.Fprint(os.Stderr, `catctl shellinit — print cats shell setup (PATH + plugin shell hooks)

Usage:
  catctl shellinit <bash|zsh|fish>

Emits a guarded prepend of the cats bin dir (~/.cats/bin, where plugin
binaries are exposed; override with $CATS_BIN_DIR) and a source line for every
installed plugin that declares a [shell] snippet for that shell.

Install (each shell re-runs it at startup, so plugins installed later take
effect in the next terminal — and uninstalled ones vanish without touching
your rc files):

  catctl integration install shell     # recommended: sets this up (and more)

or add the eval yourself:

  bash   echo 'eval "$(catctl shellinit bash)"' >> ~/.bashrc
  zsh    echo 'eval "$(catctl shellinit zsh)"' >> ~/.zshrc
  fish   echo 'catctl shellinit fish | source' >> ~/.config/fish/config.fish
`)
}
