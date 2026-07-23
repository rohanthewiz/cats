package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// runTodoUI renders the manager TUI in the current terminal — normally a cats
// shell pane, where the drop machinery has a control socket to talk to. It
// gathers the launch context (cwd, own pane, workspace), loads the project and
// global backlogs, and runs the manager. Drops happen in-loop, off the UI
// thread (see chooseTarget), so one manager pane serves many drops; Run() only
// returns when the user quits.
func runTodoUI() {
	// The socket client drives session drops; the manager still works without it
	// (you can add/edit/organize prompts), so a missing/unresponsive control
	// socket is not fatal here — startDrop reports it when a drop is attempted.
	client, _ := newCatsClient()

	ctx := gatherRunContext(client)

	project, global, err := loadStores(ctx)
	if err != nil {
		errExit(err)
	}

	p := tea.NewProgram(newModel(ctx, project, global, client), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "cats-todo:", err)
	}
}
