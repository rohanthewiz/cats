//go:build ghostty

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/push"
)

// newActionOrch builds an orch with the inbound half of the push bridge turned
// on, a recording push sink, and a pipe daemon so a capture can be answered.
func newActionOrch(t *testing.T) (*orch, *recPush, *pipeDaemon) {
	t.Helper()
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	rec := &recPush{}
	o.push = rec
	o.pushActions = true
	o.pushActionBase = "https://cats.example"
	return o, rec, newPipeDaemon(t, o)
}

// answerCapture plays the daemon's side of an in-flight capture: read the
// request off the pipe, then hand catway the screen text.
func answerCapture(t *testing.T, o *orch, pd *pipeDaemon, pane uint32, screen string) {
	t.Helper()
	pd.expect(t, orchestration.MsgRequestText)
	o.resolvePending(paneKey(pane, reqText), browserproto.CaptureResult{Text: screen})
}

// The headline path: an agent blocks, catway reads its screen, and the phone
// gets the agent's own menu as buttons — each pointing at a distinct token
// under the configured base URL.
func TestAttentionPushCarriesTheAgentsMenu(t *testing.T) {
	o, rec, pd := newActionOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	o.onPaneAgent(orchestration.PaneAgent{PaneID: pane, Agent: "claude", State: "working"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pane, Agent: "claude", State: "blocked"})

	// Nothing has been pushed yet: the buttons are still being read off the
	// pane. The browser's toast, by contrast, has already gone.
	if len(rec.evs) != 0 {
		t.Fatalf("pushed before the screen was read: %+v", rec.evs)
	}
	answerCapture(t, o, pd, pane, "Do you want to proceed?\n❯ 1. Yes\n  2. No\n")

	if len(rec.evs) != 1 {
		t.Fatalf("pushed %d times, want 1", len(rec.evs))
	}
	acts := rec.evs[0].Actions
	if len(acts) != 2 || acts[0].Label != "Yes" || acts[1].Label != "No" {
		t.Fatalf("actions = %+v", acts)
	}
	for i, a := range acts {
		if !strings.HasPrefix(a.URL, "https://cats.example"+notifyActionPath) {
			t.Errorf("actions[%d].URL = %q; want the configured base", i, a.URL)
		}
	}
	if acts[0].URL == acts[1].URL {
		t.Error("both buttons share a token; one would answer for the other")
	}
}

// Tapping a button on the phone answers the prompt: the token resolves, the
// agent's key lands in the pane, and the ui_action event says where it came
// from. The second tap — the one a lock screen invites — is refused.
func TestNotifyActionTokenAnswersOnceFromThePhone(t *testing.T) {
	o, rec, pd := newActionOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])
	sub := &recSub{}
	o.subs[&ctlSubscriber{sub: sub, filter: app.EventsSubscribeParams{Events: []string{app.EventUIAction}}}] = struct{}{}

	o.onPaneAgent(orchestration.PaneAgent{PaneID: pane, Agent: "claude", State: "working"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pane, Agent: "claude", State: "blocked"})
	answerCapture(t, o, pd, pane, " 1. Yes\n 2. No\n")

	tok := strings.TrimPrefix(rec.evs[0].Actions[1].URL, "https://cats.example"+notifyActionPath)
	if msg := o.takeTokenAction(tok); msg != "" {
		t.Fatalf("token refused: %s", msg)
	}
	if msg := o.takeTokenAction(tok); msg == "" {
		t.Fatal("the same token answered twice")
	}

	var sent []byte
	for {
		payload := pd.expect(t, orchestration.MsgInput)
		var in orchestration.Input
		if err := json.Unmarshal(payload, &in); err != nil {
			t.Fatalf("decode input: %v", err)
		}
		sent = append(sent, in.Data...)
		if len(sent) > 0 {
			break
		}
	}
	if string(sent) != "2" {
		t.Errorf("pane received %q; want the second choice %q", sent, "2")
	}

	act, _ := findEvent(sub, app.EventUIAction).(app.UIActionEvent)
	if act.Source != "push" {
		t.Errorf("ui_action source = %q; want push", act.Source)
	}
}

// A screen with no menu on it is not guessed at: the notification still reaches
// the phone, carrying no buttons. A wrong button types a real keystroke into a
// real terminal, so "you have to go and look" is the correct failure.
func TestUnparseableScreenPushesWithoutButtons(t *testing.T) {
	o, rec, pd := newActionOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	o.onPaneAgent(orchestration.PaneAgent{PaneID: pane, Agent: "claude", State: "working"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pane, Agent: "claude", State: "blocked"})
	answerCapture(t, o, pd, pane, "waiting for the network…\n")

	if len(rec.evs) != 1 {
		t.Fatalf("pushed %d times, want 1", len(rec.evs))
	}
	if len(rec.evs[0].Actions) != 0 {
		t.Errorf("guessed %d buttons off a screen with no menu", len(rec.evs[0].Actions))
	}
	if len(o.actionTokens) != 0 {
		t.Errorf("minted %d tokens with nothing to answer", len(o.actionTokens))
	}
}

// A capture that never comes back (a host that went away mid-prompt) must not
// swallow the notification: the pending times out and the push goes without
// buttons.
func TestCaptureFailurePushesAnyway(t *testing.T) {
	o, rec, pd := newActionOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	o.onPaneAgent(orchestration.PaneAgent{PaneID: pane, Agent: "claude", State: "working"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pane, Agent: "claude", State: "blocked"})
	pd.expect(t, orchestration.MsgRequestText)
	o.flushPendingFor(o.defaultHost, "cathost connection lost")

	if len(rec.evs) != 1 || len(rec.evs[0].Actions) != 0 {
		t.Fatalf("push after a failed capture = %+v", rec.evs)
	}
}

// With the feature off nothing changes: the push goes immediately, no screen is
// read, and the endpoint has nothing to resolve.
func TestPushActionsOffIsTheOldBehaviour(t *testing.T) {
	o, rec, pd := newActionOrch(t)
	o.pushActions = false
	pane := uint32(o.session.AllPaneIDs()[0])

	o.onPaneAgent(orchestration.PaneAgent{PaneID: pane, Agent: "claude", State: "working"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pane, Agent: "claude", State: "blocked"})

	if len(rec.evs) != 1 || len(rec.evs[0].Actions) != 0 {
		t.Fatalf("push = %+v; want one plain delivery", rec.evs)
	}
	select {
	case m := <-pd.msgs:
		t.Fatalf("read the pane's screen with actions disabled: %v", m.mt)
	case <-time.After(50 * time.Millisecond):
	}
	if msg := o.takeTokenAction("anything"); msg == "" {
		t.Error("a token resolved with no notifications registered")
	}
}

// A ui.notify that declared its own buttons is pushed with them straight away:
// the caller knows what it is asking better than the screen does, so there is
// nothing to read.
func TestDeclaredActionsSkipTheScreenRead(t *testing.T) {
	o, rec, pd := newActionOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.UINotify(&r, app.UINotifyParams{Title: "deploy?", Kind: app.NotifyKindAttention, Pane: &pane,
		Actions: []app.NotifyAction{{ID: "go", Label: "Ship it", Send: "y", Submit: true}}})
	if !r.ok {
		t.Fatalf("ui.notify failed: %v", r.err)
	}
	if len(rec.evs) != 1 {
		t.Fatalf("pushed %d times, want 1", len(rec.evs))
	}
	if len(rec.evs[0].Actions) != 1 || rec.evs[0].Actions[0].Label != "Ship it" {
		t.Errorf("actions = %+v", rec.evs[0].Actions)
	}
	select {
	case m := <-pd.msgs:
		t.Fatalf("read the screen for a notification that declared its buttons: %v", m.mt)
	case <-time.After(50 * time.Millisecond):
	}
}

// Tokens do not outlive the notification they answer: pruning drops the ones
// whose entry is gone, so a spent notification leaves no live credential.
func TestActionTokensDieWithTheirNotification(t *testing.T) {
	o, rec, _ := newActionOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.UINotify(&r, app.UINotifyParams{Title: "t", Kind: app.NotifyKindAttention, Pane: &pane,
		Actions: []app.NotifyAction{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}})
	if len(o.actionTokens) != 2 {
		t.Fatalf("minted %d tokens, want 2", len(o.actionTokens))
	}
	tok := strings.TrimPrefix(rec.evs[0].Actions[0].URL, "https://cats.example"+notifyActionPath)
	if msg := o.takeTokenAction(tok); msg != "" {
		t.Fatalf("token refused: %s", msg)
	}
	// One was spent; the other is orphaned because its notification is gone.
	o.pruneActionTokens()
	if len(o.actionTokens) != 0 {
		t.Errorf("%d tokens outlived their notification", len(o.actionTokens))
	}
}

// A notification kind that is not "attention" is pushed as it always was: only
// a blocked agent has a menu on screen, and reading one for a completion would
// be a round trip that can never find anything.
func TestOnlyAttentionReadsTheScreen(t *testing.T) {
	o, rec, pd := newActionOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.UINotify(&r, app.UINotifyParams{Title: "done", Kind: app.NotifyKindFinished, Pane: &pane})
	if !r.ok {
		t.Fatalf("ui.notify failed: %v", r.err)
	}
	if len(rec.evs) != 1 || rec.evs[0].Kind != push.KindFinished {
		t.Fatalf("push = %+v", rec.evs)
	}
	select {
	case m := <-pd.msgs:
		t.Fatalf("read the screen for a %q notification: %v", push.KindFinished, m.mt)
	case <-time.After(50 * time.Millisecond):
	}
}
