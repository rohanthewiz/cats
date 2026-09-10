//go:build darwin

package main

import (
	"log"
	"os"

	"github.com/rohanthewiz/cats/internal/shellenv"
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
// The shell probe itself lives in internal/shellenv, because cathost needs the
// same answer for its own spawns — this hydration is the first line of defence
// (get it right for every child at once), and cathost's per-command lookup is
// the backstop for the launches it misses.
//
// hydratePATH replaces our PATH with the user's login-shell PATH when we were
// launched from the Finder/Dock. It is best-effort: any failure leaves the
// inherited PATH in place, since a bare PATH still runs the bundled daemons
// (they are resolved next to the executable, not via PATH).
//
// It is also the first thing in startup that can block for a noticeable time,
// and occasionally forever: the probe runs the user's whole rc chain, and an
// rc file that waits on something (a network mount, a prompt) waits with our
// launch behind it. shellenv bounds that at 5s, and the step is recorded in the
// boot log so the startup window can show those seconds passing instead of
// leaving the app looking hung. Neither outcome stops the launch, so the
// failures are warnings, not failures.
func hydratePATH() {
	// __CFBundleIdentifier is set by LaunchServices, so it marks a GUI launch —
	// double-click, Dock, or `open -a`. A launch from a terminal (`go run
	// ./cmd/catapp`, or the binary directly) leaves it unset, and there the
	// inherited PATH is already the user's; re-deriving it would be wasted
	// startup latency at best and a surprise override at worst.
	if os.Getenv("__CFBundleIdentifier") == "" {
		boot.note("path", "launched from a shell — keeping the inherited PATH")
		return
	}
	step := boot.begin("reading PATH from the login shell")
	shellPath := shellenv.LoginPATH()
	if shellPath == "" {
		log.Printf("could not read PATH from the login shell; using the inherited PATH")
		boot.warn(step, "the login shell did not answer; using the inherited PATH")
		return
	}
	if err := os.Setenv("PATH", shellenv.Merge(shellPath, os.Getenv("PATH"))); err != nil {
		log.Printf("could not adopt login shell PATH: %v", err)
		boot.warn(step, "could not adopt the login shell PATH: "+err.Error())
		return
	}
	boot.ok(step)
}
