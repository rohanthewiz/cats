//go:build ghostty

package main

import (
	"testing"

	"github.com/rohanthewiz/herdr-web/internal/app"
	"github.com/rohanthewiz/herdr-web/internal/browserproto"
)

// cmd builds a §7 command message for the dispatch tests.
func cmd(t *testing.T, id, name string, params any) *browserproto.Cmd {
	t.Helper()
	c, err := browserproto.NewCmd(id, name, params)
	if err != nil {
		t.Fatalf("NewCmd(%s): %v", name, err)
	}
	return &c
}

// recvDown pops one queued down-message off the client.
func recvDown(t *testing.T, c *client) any {
	t.Helper()
	select {
	case b := <-c.out:
		msg, err := browserproto.DecodeDown(b)
		if err != nil {
			t.Fatalf("decode down: %v", err)
		}
		return msg
	default:
		t.Fatal("no message queued")
		return nil
	}
}

// agent.focus for a pane not in the model fails synchronously (before any
// viewport reconciliation), so a bad id never reaches the daemon.
func TestAgentFocusUnknownPane(t *testing.T) {
	o, c := newReadHarness()
	sess, err := app.NewSession(modelSpawner{}, "/tmp")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	o.session = sess

	o.handleCmd(c, cmd(t, "a1", browserproto.CmdAgentFocus, browserproto.PaneParams{Pane: 9999}))
	if r, ok := recvDown(t, c).(*browserproto.CmdResult); !ok || r.Ok || r.Error == "" {
		t.Fatalf("agent.focus on unknown pane should fail with an error result, got %#v", recvDown(t, c))
	}
}
