//go:build darwin

package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// A double-clicked .app is launched by launchd, not by a shell, so it inherits
// the bare system PATH (/usr/bin:/bin:/usr/sbin:/sbin plus whatever
// /etc/paths.d contributes) — none of the user's .zprofile/.zshrc additions.
// Every process we spawn inherits that: the daemons, and through them every
// pane and every `catctl plugin install` build step. The visible symptom is a
// plugin whose manifest builds itself failing with "sh: go: command not found"
// in the app while the identical install works from a terminal.
//
// The fix is the one WebKit-based desktop apps have converged on: ask the
// user's login shell what PATH it would set up, once at startup, and adopt it.
// Interactive rc files print banners and prompts, so the shell is asked to
// fence the value in markers rather than to echo it bare.
const (
	shellEnvMarker  = "__CATS_PATH__"
	shellEnvTimeout = 5 * time.Second
)

// hydratePATH replaces our PATH with the user's login-shell PATH when we were
// launched from the Finder/Dock. It is best-effort: any failure leaves the
// inherited PATH in place, since a bare PATH still runs the bundled daemons
// (they are resolved next to the executable, not via PATH).
func hydratePATH() {
	// __CFBundleIdentifier is set by LaunchServices, so it marks a GUI launch —
	// double-click, Dock, or `open -a`. A launch from a terminal (`go run
	// ./cmd/catapp`, or the binary directly) leaves it unset, and there the
	// inherited PATH is already the user's; re-deriving it would be wasted
	// startup latency at best and a surprise override at worst.
	if os.Getenv("__CFBundleIdentifier") == "" {
		return
	}
	shellPath := loginShellPATH()
	if shellPath == "" {
		return
	}
	if err := os.Setenv("PATH", mergePATH(shellPath, os.Getenv("PATH"))); err != nil {
		log.Printf("could not adopt login shell PATH: %v", err)
	}
}

// loginShellPATH runs the user's shell as a login + interactive shell and reads
// back the PATH it ends up with. Both flags matter: -l picks up .zprofile /
// .bash_profile, -i picks up .zshrc / .bashrc, and users put PATH edits in
// either. Returns "" if the shell can't be run, times out, or prints nothing
// recognisable.
func loginShellPATH() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh" // the macOS default since Catalina
	}
	ctx, cancel := context.WithTimeout(context.Background(), shellEnvTimeout)
	defer cancel()

	// stdin is /dev/null (exec's default for a nil Stdin), so an interactive rc
	// file that tries to read from the terminal gets EOF instead of hanging; the
	// context is the backstop for one that blocks anyway.
	cmd := exec.CommandContext(ctx, shell, "-ilc", `printf '`+shellEnvMarker+`%s`+shellEnvMarker+`' "$PATH"`)
	// Output() is used for its stdout capture only — an rc file that exits
	// nonzero or writes to stderr is normal noise, and the markers tell us
	// whether the part we care about made it out.
	out, err := cmd.Output()
	value := betweenMarkers(string(out), shellEnvMarker)
	if value == "" {
		log.Printf("could not read PATH from %s (err: %v); using the inherited PATH", shell, err)
		return ""
	}
	return value
}

// betweenMarkers extracts the text fenced by the first two occurrences of
// marker, which is how the shell's PATH is separated from any banner an
// interactive rc file printed around it.
func betweenMarkers(s, marker string) string {
	start := strings.Index(s, marker)
	if start < 0 {
		return ""
	}
	rest := s[start+len(marker):]
	end := strings.Index(rest, marker)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// mergePATH puts the login shell's entries first and appends any inherited
// entry the shell didn't mention. The inherited PATH is almost always a subset,
// but a managed Mac can inject an entry at the launchd level that no shell
// startup file repeats, and dropping it would be a regression.
func mergePATH(shellPath, inherited string) string {
	seen := make(map[string]bool)
	var merged []string
	for _, list := range []string{shellPath, inherited} {
		for _, entry := range strings.Split(list, ":") {
			if entry == "" || seen[entry] {
				continue
			}
			seen[entry] = true
			merged = append(merged, entry)
		}
	}
	return strings.Join(merged, ":")
}
