package main

import (
	"errors"
)

// performDrop carries out the chosen drop. For an existing pane it types the
// prompt straight in; for a new session it opens a tab, launches the agent, and
// feeds the prompt. In both cases dropRun submits with Enter while dropPaste
// leaves the text unsubmitted for the user to review.
func performDrop(client *catsClient, act pendingAction) error {
	if client == nil {
		return errors.New("cats control socket unavailable")
	}
	prompt := act.todo.Prompt
	switch act.target.kind {
	case targetExistingPane:
		if err := client.sendInput(act.target.pane, prompt, act.mode == dropRun); err != nil {
			return err
		}
		// Switch to the pane we just dropped into, mirroring how a new-session
		// drop focuses its freshly-created tab. Best effort: the prompt is
		// already delivered, so a focus failure must not fail the drop.
		_ = client.focusPane(act.target.pane)
		return nil
	case targetNewSession:
		return dropIntoNewSession(client, act, prompt)
	}
	return errors.New("unknown drop target")
}

// dropIntoNewSession opens a fresh tab (in the active workspace — the one the
// manager pane lives in), launches the target's agent (claude by default),
// waits for its input UI, and delivers the prompt as typed input. Run mode adds
// a real Enter so the agent starts working; paste mode stops short so the user
// can review and edit. One delivery path for both modes — and for any agent —
// with no shell quoting to get wrong and no prompt leaking into shell history
// or `ps` output.
//
// tab.create already returns the new tab's root pane id and leaves the tab
// focused, so unlike the herdr original there is no workspace resolution or
// pane discovery step — create, label, drive.
func dropIntoNewSession(client *catsClient, act pendingAction, prompt string) error {
	command := firstNonEmpty(act.target.command, "claude")
	label := command
	if t := firstNonEmpty(act.todo.Title, firstLine(prompt, 18)); t != "" {
		label = command + ": " + truncate(t, 18)
	}

	num, pane, err := client.tabCreate()
	if err != nil {
		return err
	}
	// Label the tab after the work it hosts. Best effort: an unlabeled tab must
	// not abort a drop that can still deliver.
	_ = client.tabRename(num, label)

	if err := client.runCommand(pane, command); err != nil {
		return err
	}
	client.waitForAgentReady(pane, command)
	return client.sendInput(pane, prompt, act.mode == dropRun)
}
