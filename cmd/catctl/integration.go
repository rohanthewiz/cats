package main

// The `integration` verb family is handled entirely offline — it edits agent
// config trees on this machine and never dials the control socket. Ported
// from cats's cli/integration.rs: same subcommands, usage strings, exit
// codes (0 ok / 1 error / 2 usage) and stdout/stderr split.

import (
	"fmt"
	"os"

	"github.com/rohanthewiz/cats/internal/integration"
	"github.com/rohanthewiz/cats/internal/shellint"
)

const integrationTargetsUsage = "pi|omp|claude|codex|copilot|droid|kimi|opencode|kilo|hermes|qodercli|cursor"

// runIntegration dispatches `catctl integration <subcommand> ...` and
// returns the process exit code.
func runIntegration(args []string) int {
	if len(args) == 0 {
		printIntegrationHelp()
		return 2
	}

	switch args[0] {
	case "install":
		return integrationInstall(args[1:])
	case "uninstall":
		return integrationUninstall(args[1:])
	case "status":
		return integrationStatus(args[1:])
	case "help", "--help", "-h":
		printIntegrationHelp()
		return 0
	default:
		printIntegrationHelp()
		return 2
	}
}

func integrationStatus(args []string) int {
	outdatedOnly := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--outdated-only":
		outdatedOnly = true
	default:
		fmt.Fprintln(os.Stderr, "usage: catctl integration status [--outdated-only]")
		return 2
	}

	if outdatedOnly {
		if notice, ok := integration.OutdatedUpdateNotice(); ok {
			fmt.Fprintln(os.Stderr, notice)
		}
		return 0
	}

	for _, status := range integration.InstalledIntegrationStatuses() {
		version := "legacy"
		if status.InstalledVersion >= 0 {
			version = fmt.Sprintf("v%d", status.InstalledVersion)
		}
		var state string
		switch status.State {
		case integration.StatusNotInstalled:
			state = "not installed"
		case integration.StatusCurrent:
			state = fmt.Sprintf("current (%s)", version)
		case integration.StatusOutdated:
			state = fmt.Sprintf("outdated (%s < v%d)", version, status.ExpectedVersion)
		}
		fmt.Printf("%s: %s (%s)\n", status.Target.Label(), state, status.Path)
	}
	for _, line := range shellStatusLines() {
		fmt.Println(line)
	}
	return 0
}

// shellTargetName is the pseudo-target that installs the SHELL integration
// (internal/shellint) rather than an agent's hooks. It shares this verb because
// "wire the thing in my terminal to cats" is one idea to a user, and splits
// below it because a shell is not an agent: three shells, three hook
// mechanisms, and an rc file the user wrote by hand.
const shellTargetName = "shell"

func integrationInstall(args []string) int {
	if len(args) > 0 && args[0] == shellTargetName {
		return shellInstall(args[1:])
	}
	target, ok := parseIntegrationTarget(args, "install")
	if !ok {
		return 2
	}
	messages, err := integration.InstallTarget(target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	printIntegrationMessages(messages)
	return 0
}

func integrationUninstall(args []string) int {
	if len(args) > 0 && args[0] == shellTargetName {
		return shellUninstall(args[1:])
	}
	target, ok := parseIntegrationTarget(args, "uninstall")
	if !ok {
		return 2
	}
	messages, err := integration.UninstallTarget(target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	printIntegrationMessages(messages)
	return 0
}

// parseShellArg resolves the optional shell name, defaulting to $SHELL — which
// is the login shell, and therefore the one whose rc file a person means by
// "my shell".
func parseShellArg(args []string, action string) (shellint.Shell, bool) {
	switch len(args) {
	case 0:
		sh, ok := shellint.Detect()
		if !ok {
			fmt.Fprintf(os.Stderr, "cannot tell which shell you use ($SHELL=%q)\n", os.Getenv("SHELL"))
			fmt.Fprintf(os.Stderr, "usage: catctl integration %s shell <bash|zsh|fish>\n", action)
			return 0, false
		}
		return sh, true
	case 1:
		sh, ok := shellint.ParseShell(args[0])
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown shell: %s\n", args[0])
			fmt.Fprintln(os.Stderr, "currently supported: bash, zsh, fish")
			return 0, false
		}
		return sh, true
	}
	fmt.Fprintf(os.Stderr, "usage: catctl integration %s shell [bash|zsh|fish]\n", action)
	return 0, false
}

func shellInstall(args []string) int {
	sh, ok := parseShellArg(args, "install")
	if !ok {
		return 2
	}
	script, rc, err := shellint.Install(sh)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("installed %s shell integration to %s\n", sh.Label(), script)
	fmt.Printf("sourced from %s\n", rc)
	// The one thing that will otherwise look broken: nothing changes in the
	// shell that ran this, because its rc file was read before the edit.
	fmt.Println("open a new shell (or source the file above) for it to take effect")
	return 0
}

func shellUninstall(args []string) int {
	sh, ok := parseShellArg(args, "uninstall")
	if !ok {
		return 2
	}
	removedBlock, removedScript, err := shellint.Uninstall(sh)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !removedBlock && !removedScript {
		fmt.Printf("%s shell integration was not installed\n", sh.Label())
		return 0
	}
	rc, _ := shellint.RCPath(sh)
	if removedBlock {
		fmt.Printf("removed the cats block from %s\n", rc)
	}
	if removedScript {
		script, _ := shellint.ScriptPath(sh)
		fmt.Printf("removed %s\n", script)
	}
	return 0
}

// shellStatusLines renders the shell rows for `integration status`, in the same
// "<name>: <state> (<path>)" shape as the agent rows above them.
func shellStatusLines() []string {
	var out []string
	for _, st := range shellint.Statuses() {
		var state string
		switch st.State {
		case shellint.NotInstalled:
			state = "not installed"
		case shellint.Current:
			state = fmt.Sprintf("current (v%d)", st.InstalledVersion)
		case shellint.Outdated:
			state = fmt.Sprintf("outdated (v%d < v%d)", st.InstalledVersion, shellint.Version)
		case shellint.Orphaned:
			// Named precisely because the two halves need different fixes: an
			// rc block with no script behind it, or a script nothing sources.
			state = "half-installed — reinstall"
		}
		out = append(out, fmt.Sprintf("shell/%s: %s (%s)", st.Shell.Label(), state, st.ScriptPath))
	}
	return out
}

func printIntegrationMessages(messages []string) {
	for _, message := range messages {
		fmt.Println(message)
	}
}

func parseIntegrationTarget(args []string, action string) (integration.Target, bool) {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "usage: catctl integration %s <%s>\n", action, integrationTargetsUsage)
		return 0, false
	}
	target, ok := integration.ParseTarget(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown integration target: %s\n", args[0])
		fmt.Fprintln(os.Stderr,
			"currently supported: pi, omp, claude, codex, copilot, droid, kimi, opencode, kilo, hermes, qodercli, cursor")
		return 0, false
	}
	return target, true
}

func printIntegrationHelp() {
	fmt.Fprintln(os.Stderr, "catctl integration commands:")
	for _, action := range []string{"install", "uninstall"} {
		for _, target := range integration.AllTargets() {
			fmt.Fprintf(os.Stderr, "  catctl integration %s %s\n", action, target.Label())
		}
	}
	for _, action := range []string{"install", "uninstall"} {
		fmt.Fprintf(os.Stderr, "  catctl integration %s shell [bash|zsh|fish]\n", action)
	}
	fmt.Fprintln(os.Stderr, "  catctl integration status [--outdated-only]")
}
