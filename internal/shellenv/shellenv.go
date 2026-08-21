// Package shellenv answers one question: what PATH would the user's login
// shell set up?
//
// It exists because a process started by anything other than a shell — a
// double-clicked .app (launchd), a LaunchAgent, a systemd unit — inherits a
// bare system PATH (/usr/bin:/bin:/usr/sbin:/sbin on macOS) that has none of
// the user's .zprofile/.zshrc additions. Everything spawned downstream inherits
// it too, so a tool the user installed under ~/.local/bin, ~/go/bin or a
// version manager's shim directory is simply not there:
//
//	catapp (launchd, bare PATH)
//	  └─ cathost            ← resolves every pane's program with THIS PATH
//	       ├─ /bin/zsh      ← fine: an interactive shell rebuilds PATH from rc files
//	       └─ claude        ← "executable file not found in $PATH"
//
// A shell pane papers over the problem by sourcing the rc files itself, which
// is why the gap only shows up for panes exec'd straight into a program
// (tab.create's Command: agent launches, plugin actions, prompt drops).
//
// Two consumers, deliberately at different levels: catapp hydrates its own
// process PATH at startup so every child is born with it, and cathost uses
// LoginPATH per command pane as the backstop for the launches catapp's
// hydration did not cover (a probe that failed or timed out, a cathost started
// by something else entirely, a remote host's daemon).
package shellenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// marker fences the PATH in the shell's stdout. Interactive rc files print
	// banners, set prompts, and generally write things we did not ask for, so
	// the value has to be delimited rather than assumed to be the whole output.
	marker = "__CATS_PATH__"
	// probeTimeout bounds the shell run. Generous next to the ~1s a normal rc
	// chain takes, because the failure it guards against is an rc file that
	// blocks forever, not one that is merely slow.
	probeTimeout = 5 * time.Second
)

// loginPATH memoises the probe: the answer is a property of the user's rc
// files, not of the caller, and running a full interactive shell startup once
// per pane would be a visible cost on every agent launch. A failed probe is
// cached as "" too — a shell that could not answer once is not going to answer
// differently on the next pane, and retrying would pay the timeout again.
var loginPATH = sync.OnceValue(probeLoginPATH)

// LoginPATH returns the PATH the user's login shell sets up, or "" if the shell
// cannot be run, times out, or prints nothing recognisable. Computed once per
// process; safe for concurrent use.
func LoginPATH() string { return loginPATH() }

// probeLoginPATH runs the user's shell as a login + interactive shell and reads
// back the PATH it ends up with. Both flags matter: -l picks up .zprofile /
// .bash_profile, -i picks up .zshrc / .bashrc, and users put PATH edits in
// either.
func probeLoginPATH() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh" // the macOS default since Catalina
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	// stdin is /dev/null (exec's default for a nil Stdin), so an interactive rc
	// file that tries to read from the terminal gets EOF instead of hanging; the
	// context is the backstop for one that blocks anyway.
	cmd := exec.CommandContext(ctx, shell, "-ilc", `printf '`+marker+`%s`+marker+`' "$PATH"`)
	// Output() is used for its stdout capture only — an rc file that exits
	// nonzero or writes to stderr is normal noise, and the markers tell us
	// whether the part we care about made it out.
	out, _ := cmd.Output()
	return Between(string(out), marker)
}

// Between extracts the text fenced by the first two occurrences of marker,
// which is how the shell's PATH is separated from any banner an interactive rc
// file printed around it. Exported for the probe's tests and for callers that
// run the same trick with their own shell invocation.
func Between(s, marker string) string {
	_, rest, found := strings.Cut(s, marker)
	if !found {
		return ""
	}
	value, _, closed := strings.Cut(rest, marker)
	if !closed {
		return "" // an unterminated fence means the output was cut short
	}
	return value
}

// Marker is the fence probeLoginPATH asks the shell to print around the value.
// Exported so tests can build sample output without restating the literal.
const Marker = marker

// Merge puts the login shell's entries first and appends any inherited entry
// the shell didn't mention. The inherited PATH is almost always a subset, but a
// managed Mac can inject an entry at the launchd level that no shell startup
// file repeats, and dropping it would be a regression.
func Merge(shellPath, inherited string) string {
	seen := make(map[string]bool)
	var merged []string
	for _, list := range []string{shellPath, inherited} {
		for entry := range strings.SplitSeq(list, ":") {
			if entry == "" || seen[entry] {
				continue
			}
			seen[entry] = true
			merged = append(merged, entry)
		}
	}
	return strings.Join(merged, ":")
}

// Lookup resolves a program name the way exec.LookPath does, but against the
// login shell's PATH as well as our own — and reports the PATH the caller
// should hand the spawned process.
//
// The two returns are separate on purpose, because they fix two distinct
// halves of the same failure:
//
//   - path is what actually gets exec'd. Go resolves a bare program name with
//     the *current process's* PATH, never with cmd.Env, so a daemon holding a
//     bare PATH cannot start `claude` no matter what environment it prepares
//     for the child. Handing exec an absolute path takes that resolution away
//     from the daemon's environment entirely.
//   - envPATH is what the child then works with. An agent shells out constantly
//     (git, gh, node, the language toolchain), so launching it with a PATH that
//     could not even find the agent itself just moves the failure one level
//     down.
//
// A name that already contains a separator is a path, not a lookup, and is
// returned untouched — but it still gets the hydrated envPATH, for the same
// subprocess reason. envPATH is "" when there is nothing better to offer than
// what the caller already has (no probe result, or a probe that adds nothing).
func Lookup(name string) (path string, envPATH string) {
	inherited := os.Getenv("PATH")
	login := LoginPATH()

	envPATH = ""
	if login != "" {
		if merged := Merge(login, inherited); merged != inherited {
			envPATH = merged
		}
	}

	if name == "" || strings.ContainsRune(name, os.PathSeparator) {
		return name, envPATH
	}
	if p, err := exec.LookPath(name); err == nil {
		// Resolvable already: still absolutise it, so the child process's own
		// PATH (which we may be about to change) cannot alter which binary the
		// daemon decided on.
		return p, envPATH
	}
	if envPATH == "" {
		return name, "" // nothing new to search; let exec produce the real error
	}
	if p, err := lookIn(envPATH, name); err == nil {
		return p, envPATH
	}
	return name, envPATH
}

// lookIn is exec.LookPath against an explicit PATH rather than the process's.
// os/exec offers no such entry point, so the search list is swapped in around
// the call. The swap is guarded by a mutex because PATH is process-global state
// and two panes can be created concurrently; it is held for microseconds and
// restored unconditionally.
var lookMu sync.Mutex

func lookIn(path, name string) (string, error) {
	lookMu.Lock()
	defer lookMu.Unlock()
	saved, had := os.LookupEnv("PATH")
	if err := os.Setenv("PATH", path); err != nil {
		return "", err
	}
	defer func() {
		if had {
			_ = os.Setenv("PATH", saved)
		} else {
			_ = os.Unsetenv("PATH")
		}
	}()
	return exec.LookPath(name)
}
