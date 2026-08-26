//go:build ghostty

package main

import (
	"testing"

	"github.com/rohanthewiz/cats/internal/browserproto"
)

// Cats-level navigation, runtime half: the post-dispatch hook records where a
// window's focus lands, nav.back walks that window — and only that window —
// back through it. The stack semantics themselves are tested in
// internal/app/nav_test.go; these tests are about which window's history a
// command reads and writes.

// A window that jumps to another workspace's pane can ⌘[ its way home: the
// registration seed is entry zero, the jump is entry one, and nav.back reveals
// the seed — moving the issuing view's workspace back with it.
func TestNavBackReturnsAcrossWorkspaces(t *testing.T) {
	o, ws1, _, _, pane2 := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	drain(a)

	// Jump to the pane in the other workspace (the pane.focus reveal path).
	o.handleCmd(a, cmd(t, "", browserproto.CmdPaneFocus, browserproto.PaneParams{Pane: pane2}))
	if a.view.ws == ws1 {
		t.Fatal("focusing another workspace's pane should have moved the view")
	}

	o.handleCmd(a, cmd(t, "", browserproto.CmdNavBack, nil))
	if a.view.ws != ws1 {
		t.Fatalf("nav.back left the view on %q, want %q", a.view.ws, ws1)
	}
	if got := activeWSOf(lastLayout(t, a)); got != ws1 {
		t.Fatalf("layout after nav.back says active workspace %q, want %q", got, ws1)
	}

	// And forward returns to the jump target.
	o.handleCmd(a, cmd(t, "", browserproto.CmdNavForward, nil))
	if a.view.ws == ws1 {
		t.Fatal("nav.forward should have moved the view forward again")
	}
}

// Each window walks its own trail: one window's nav.back must not move — or
// consume history from — another window on the same session.
func TestNavHistoriesAreIndependentPerWindow(t *testing.T) {
	o, ws1, ws2, _, pane2 := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40)
	drain(a)
	drain(b)

	o.handleCmd(a, cmd(t, "", browserproto.CmdPaneFocus, browserproto.PaneParams{Pane: pane2}))
	o.handleCmd(a, cmd(t, "", browserproto.CmdNavBack, nil))

	if b.view.ws != ws2 {
		t.Fatalf("window B moved to %q; A's navigation must not touch it", b.view.ws)
	}
	// B's own history is just its registration seed, so its back has nowhere
	// to go — proving A's walk did not spill into B's stack.
	o.handleCmd(b, cmd(t, "", browserproto.CmdNavBack, nil))
	if b.view.ws != ws2 {
		t.Fatalf("window B's empty history moved it to %q", b.view.ws)
	}
}

// A view-less caller (catctl, a hook, a runbook step) walks the PRIMARY
// window's history — the plain-orch NavHistory answer — so `catctl back`
// moves the window the user touched last.
func TestNavHistoryViewlessResolvesToPrimary(t *testing.T) {
	o, ws1, ws2, _, _ := twoWorkspaceOrch(t)
	a := openWindow(o, ws1, 200, 60)
	b := openWindow(o, ws2, 120, 40) // registered last ⇒ primary
	drain(a)
	drain(b)

	if got := o.NavHistory(); got != b.view.nav || got == nil {
		t.Fatalf("view-less history = %p, want primary window B's %p", got, b.view.nav)
	}
	if o.navHistoryFor(a) == o.navHistoryFor(b) {
		t.Fatal("two windows sharing one history")
	}
}
