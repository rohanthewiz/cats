package main

// The `integration` verb family is handled entirely offline — it edits agent
// config trees on this machine and never dials the control socket. Ported
// from cats's cli/integration.rs: same subcommands, usage strings, exit
// codes (0 ok / 1 error / 2 usage) and stdout/stderr split.

import (
	"fmt"
	"os"

	"github.com/rohanthewiz/cats/internal/integration"
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
	return 0
}

func integrationInstall(args []string) int {
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
	fmt.Fprintln(os.Stderr, "  catctl integration status [--outdated-only]")
}
