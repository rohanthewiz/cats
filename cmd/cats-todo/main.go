// Command cats-todo is a prompt-backlog manager for cats: save prompts of
// future work (per-project and globally), then "drop" one into a Claude Code
// session — an existing agent pane or a freshly launched one — either staged
// for review or submitted to run.
//
// It is the cats port of herdr-todo (github.com/rohanthewiz/herdr-todo), which
// ran as a herdr plugin; cats has no plugin host, so cats-todo is a standalone
// TUI you run directly in any cats shell pane:
//
//	cats-todo                     open the manager in this pane
//	cats-todo add [-g] [-t] ...   quick-capture a prompt without the manager
//	cats-todo version
//
// The manager talks to the cats server over the local control socket
// (internal/ctlproto, CATS_CONTROL_SOCKET) — the same §7 command table catctl
// drives: pane.list to find agent panes, tab.create to open a new session,
// pane.wait_for_output to pace launches, and pane.send_input to deliver the
// prompt. Run outside cats it still manages backlogs; only drops need the
// socket.
package main

import (
	"fmt"
	"os"
)

// version is the binary's version.
const version = "0.1.0"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "add":
			addFromCLI(os.Args[2:])
			return
		case "version", "--version", "-v", "-V":
			fmt.Println("cats-todo", version)
			return
		case "help", "--help", "-h":
			fmt.Println("usage: cats-todo [add [-g] [-t title] [prompt...] | version]")
			fmt.Println("  with no arguments, opens the manager TUI in the current pane")
			return
		default:
			errExit(fmt.Sprintf("unknown subcommand %q — run `cats-todo help`", os.Args[1]))
		}
	}
	runTodoUI()
}
