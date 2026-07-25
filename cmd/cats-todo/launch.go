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

	// Hand the title back on the way out. The title we set is a property of the
	// terminal, not of this process, so without an explicit clear it outlives the
	// TUI and keeps naming a pane that is a plain shell again. OSC 2 with an empty
	// payload is the standard clear — cats reads it as "no title" and the pane
	// falls back to whatever the shell sets next. Written directly rather than via
	// tea.SetWindowTitle because the renderer is already stopped here; it prints
	// nothing visible, so it is safe after the alt screen is torn down.
	fmt.Print("\x1b]2;\a")
}
