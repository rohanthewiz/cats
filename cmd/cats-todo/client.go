// client.go — a small client for the cats control API (internal/ctlproto).
//
// Ported from herdr-todo's herdr.go (a JSON-RPC plugin-socket client), itself
// adapted from herdr-plus (https://github.com/cloudmanic/herdr-plus),
// Copyright (c) 2026 Cloudmanic Labs, LLC, MIT License. See NOTICE.

package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/ctlproto"
)

// catsClient talks to the running cats server (cmd/catway) over its control
// unix socket, driving the same §7 command table as the browser front-end and
// catctl. Each call is one short-lived connection: one newline-framed JSON
// Request in, one Response out (ctlproto.Call owns the transport). The socket
// resolves like catctl's: CATS_CONTROL_SOCKET when set, else the default
// /tmp/cats-control.sock — so cats-todo works from any pane of a default-config
// catway with no wiring.
type catsClient struct {
	socket string
}

// callTimeout bounds one ordinary control round trip. Only pane.wait_for_output
// legitimately outlives it, and waitForOutput sizes its own deadline.
const callTimeout = 10 * time.Second

// newCatsClient resolves the control socket and verifies a cats server is
// actually behind it with a ping, so the manager can degrade gracefully (todos
// still work, drops don't) when launched outside cats.
func newCatsClient() (*catsClient, error) {
	c := &catsClient{socket: ctlproto.ResolveSocket("")}
	var pong ctlproto.Pong
	if err := c.call(ctlproto.MethodPing, nil, &pong, callTimeout); err != nil {
		return nil, err
	}
	return c, nil
}

// call sends one request and decodes the response data into out (which may be
// nil when the caller does not care about the payload). A server-side failure
// (Response.OK false) surfaces as an error carrying the server's message.
func (c *catsClient) call(method string, params any, out any, timeout time.Duration) error {
	req := ctlproto.Request{ID: "cats-todo", Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("encode params: %w", err)
		}
		req.Params = raw
	}
	resp, err := ctlproto.Call(c.socket, req, timeout)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("cats error: %s", resp.Error)
	}
	if out != nil && len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, out); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}
	return nil
}

// paneList returns every pane cats currently knows about (across all workspaces
// and tabs), each carrying its runtime metadata — the Agent label is how we find
// Claude Code sessions to drop a prompt into.
func (c *catsClient) paneList() ([]app.PaneInfo, error) {
	var out app.PaneListResult
	if err := c.call(app.CmdPaneList, nil, &out, callTimeout); err != nil {
		return nil, err
	}
	return out.Panes, nil
}

// workspaceLabels returns a map of public workspace id → display name, for
// showing where a candidate pane lives (a pane's workspace id is the prefix of
// its "w1:p3" handle).
func (c *catsClient) workspaceLabels() (map[string]string, error) {
	var out app.WorkspaceListResult
	if err := c.call(app.CmdWorkspaceList, nil, &out, callTimeout); err != nil {
		return nil, err
	}
	labels := make(map[string]string, len(out.Workspaces))
	for _, ws := range out.Workspaces {
		labels[ws.ID] = ws.Name
	}
	return labels, nil
}

// sessionInfo returns the one-shot session snapshot (active workspace, counts).
func (c *catsClient) sessionInfo() (app.SessionInfoResult, error) {
	var out app.SessionInfoResult
	err := c.call(app.CmdSessionGet, nil, &out, callTimeout)
	return out, err
}

// sendInput types text into a pane as though at the keyboard. The server
// paste-encodes text against the pane's live modes, so a multi-line prompt
// lands intact in a TUI input (readline/Claude Code) instead of executing
// line-by-line; submit follows it with a real Enter keypress. To RUN a command,
// pass the text with submit=true — never a trailing newline in text, which the
// line editor would insert literally. submit with empty text sends just the
// Enter, firing previously staged input.
func (c *catsClient) sendInput(pane uint32, text string, submit bool) error {
	return c.call(app.CmdPaneSendInput,
		app.SendInputParams{Pane: pane, Text: text, Submit: submit}, nil, callTimeout)
}

// waitForOutput blocks until the pane's output matches pattern (a substring, or
// a regexp when regex is set) or timeout elapses, reporting whether it matched.
// The server matches the live output stream seeded with the current screen, so
// it never misses fast-scrolling output. The round-trip deadline is sized past
// the wait's own timeout (catctl does the same) so the transport never gives up
// before the server answers.
func (c *catsClient) waitForOutput(pane uint32, pattern string, regex bool, timeout time.Duration) (bool, error) {
	p := app.WaitForOutputParams{
		Pane:      pane,
		Pattern:   pattern,
		Regex:     regex,
		TimeoutMs: uint32(timeout / time.Millisecond),
	}
	var out app.WaitForOutputResult
	if err := c.call(app.CmdWaitForOutput, p, &out, app.WaitTimeout(p.TimeoutMs)+10*time.Second); err != nil {
		return false, err
	}
	return out.Matched, nil
}

// tabCreate opens a new tab in the active workspace, returning the new tab's
// public number and its root pane's id. The server focuses the new tab, so the
// returned pane is immediately drivable.
func (c *catsClient) tabCreate() (num int, pane uint32, err error) {
	var out app.TabCreateResult
	if err = c.call(app.CmdTabCreate, nil, &out, callTimeout); err != nil {
		return 0, 0, err
	}
	return out.Num, out.Pane, nil
}

// tabRename gives a tab a human label (the new-session drop labels its tab
// after the agent + prompt title).
func (c *catsClient) tabRename(num int, label string) error {
	return c.call(app.CmdTabRename, app.RenameTabParams{Num: num, Name: label}, nil, callTimeout)
}

// focusPane reveals the pane into the viewport. agent.focus rather than
// pane.focus: the drop target may live in another workspace or tab, and
// agent.focus (like the agents sidebar it serves) crosses both, while
// pane.focus only moves focus within the pane's own tab.
func (c *catsClient) focusPane(pane uint32) error {
	return c.call(app.CmdAgentFocus, app.PaneParams{Pane: pane}, nil, callTimeout)
}

// runCommand types command into a freshly created pane and submits it, pacing
// itself to the shell's startup so the command actually runs instead of sitting
// unsubmitted at the prompt. It waits for the shell prompt to draw (any
// non-blank output), types the command (no trailing newline), waits for the
// command to echo, then submits with a real Enter key. Every wait is best
// effort: on timeout or error it proceeds.
func (c *catsClient) runCommand(pane uint32, command string) error {
	// `\S` = any non-whitespace character: the first sign the shell has drawn
	// its prompt. wait_for_output seeds with the current screen, so a prompt
	// that beat us here still matches immediately.
	_, _ = c.waitForOutput(pane, `\S`, true, 5*time.Second)
	if err := c.sendInput(pane, command, false); err != nil {
		return err
	}
	_, _ = c.waitForOutput(pane, commandEchoProbe(command), false, 5*time.Second)
	return c.sendInput(pane, "", true)
}

// commandEchoProbe returns a short, stable leading fragment of a command to look
// for when confirming it was typed at the prompt. A long command wraps across
// rows, so a short leading fragment is more reliable to match.
func commandEchoProbe(command string) string {
	probe := command
	if i := strings.IndexByte(probe, '\n'); i >= 0 {
		probe = probe[:i]
	}
	if len(probe) > 12 {
		probe = probe[:12]
	}
	return strings.TrimSpace(probe)
}

// claudeReadyProbes are substrings that signal Claude Code's input UI has drawn
// and is ready to receive a pasted prompt. We wait for any of them before
// pasting so keystrokes are not dropped into a half-started app. Matching is
// best effort — on timeout we paste anyway. These track Claude Code's
// footer/banner strings and may need refreshing as its UI evolves.
var claudeReadyProbes = []string{
	"for shortcuts",
	"Welcome to Claude",
	"/help for help",
	"esc to interrupt",
	"Bypassing Permissions",
}

// waitForAgentReady blocks until a freshly launched agent looks ready to accept
// a pasted prompt. Claude Code has known footer/banner strings to probe for —
// folded into one alternation regex so a single server-side waiter watches for
// all of them at once; other agents get a short fixed grace period instead.
// Best effort either way — on timeout we paste anyway.
func (c *catsClient) waitForAgentReady(pane uint32, command string) {
	if command == "claude" {
		quoted := make([]string, len(claudeReadyProbes))
		for i, p := range claudeReadyProbes {
			quoted[i] = regexp.QuoteMeta(p)
		}
		_, _ = c.waitForOutput(pane, strings.Join(quoted, "|"), true, 12*time.Second)
		return
	}
	time.Sleep(2500 * time.Millisecond)
}
