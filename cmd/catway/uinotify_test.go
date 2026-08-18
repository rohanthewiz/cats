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
)

// notifyResponder records one command's terminal resolution.
type notifyResponder struct {
	ok   bool
	data any
	fail bool
	err  string
}

func (r *notifyResponder) WantsReply() bool { return true }
func (r *notifyResponder) OK(data any)      { r.ok, r.data = true, data }
func (r *notifyResponder) Fail(msg string)  { r.fail, r.err = true, msg }

// newNotifyOrch builds an orch with one client attached and one event
// subscriber watching both notification events.
func newNotifyOrch(t *testing.T) (*orch, *client, *recSub) {
	t.Helper()
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	c := &client{o: o, out: make(chan []byte, 32), trans: map[uint32]*browserproto.FrameTranslator{}}
	o.conns[c] = struct{}{}
	rec := &recSub{}
	o.subs[&ctlSubscriber{sub: rec, filter: app.EventsSubscribeParams{
		Events: []string{app.EventPaneNotify, app.EventUIAction},
	}}] = struct{}{}
	return o, c, rec
}

// firstNotify returns the first notify down-message on the client.
func firstNotify(t *testing.T, c *client) *browserproto.Notify {
	t.Helper()
	for _, m := range drainDown(t, c) {
		if n, ok := m.(*browserproto.Notify); ok {
			return n
		}
	}
	t.Fatal("no notify broadcast")
	return nil
}

// A plain ui.notify reaches all three destinations at once — that is the whole
// reason it is routed through notifyAll rather than broadcast directly — and
// declares no id when it has no buttons to answer.
func TestUINotifyReachesBrowserAndEventStream(t *testing.T) {
	o, c, rec := newNotifyOrch(t)

	var r notifyResponder
	o.UINotify(&r, app.UINotifyParams{Title: "build finished", Body: "cats · 1", Kind: app.NotifyKindInfo})
	if !r.ok {
		t.Fatalf("ui.notify failed: %v", r.err)
	}
	if got := r.data.(app.UINotifyResult).ID; got != "" {
		t.Errorf("id %q for a notification with no actions; want none", got)
	}

	n := firstNotify(t, c)
	if n.Kind != "info" || n.Message != "build finished" || n.Body != "cats · 1" {
		t.Errorf("notify = %+v", n)
	}
	if len(n.Actions) != 0 {
		t.Errorf("actions = %+v; want none", n.Actions)
	}

	ev, ok := findEvent(rec, app.EventPaneNotify).(app.PaneNotifyEvent)
	if !ok {
		t.Fatal("no pane_notify event")
	}
	if ev.Kind != "info" || ev.Message != "build finished" {
		t.Errorf("event = %+v", ev)
	}
	// Agent stays empty: the notification is the caller's, and the push
	// bridge's per-agent mute rule must not be handed a plugin's name.
	if ev.Agent != "" {
		t.Errorf("agent = %q; want empty", ev.Agent)
	}
}

// An action's Send is injected into the pane exactly as pane.send_input would
// inject it, and the ui_action event lands after the input rather than before.
func TestUIActionSendsInputThenAnnounces(t *testing.T) {
	o, c, rec := newNotifyOrch(t)
	pd := newPipeDaemon(t, o)
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.UINotify(&r, app.UINotifyParams{
		Title: "claude needs attention",
		Kind:  app.NotifyKindAttention,
		Pane:  &pane,
		Actions: []app.NotifyAction{
			{ID: "yes", Label: "Yes", Send: "1", Submit: true},
			{ID: "no", Label: "No", Send: "2", Submit: true},
		},
	})
	if !r.ok {
		t.Fatalf("ui.notify failed: %v", r.err)
	}
	id := r.data.(app.UINotifyResult).ID
	if id == "" {
		t.Fatal("no id for a notification with actions")
	}

	n := firstNotify(t, c)
	if n.ID != id || len(n.Actions) != 2 || n.Actions[0].Label != "Yes" {
		t.Fatalf("broadcast notify = %+v", n)
	}
	if n.Pub == "" {
		t.Error("no public handle on a pane-attributed notification")
	}

	var ar notifyResponder
	o.UIAction(&ar, app.UIActionParams{ID: id, Action: "yes"})
	if !ar.ok {
		t.Fatalf("ui.action failed: %v", ar.err)
	}

	// The daemon saw the answer.
	payload := pd.expect(t, orchestration.MsgInput)
	var in orchestration.Input
	if err := json.Unmarshal(payload, &in); err != nil {
		t.Fatalf("decode input: %v", err)
	}
	if in.PaneID != pane || string(in.Data) != "1" {
		t.Errorf("input = pane %d %q; want pane %d %q", in.PaneID, in.Data, pane, "1")
	}

	act, ok := findEvent(rec, app.EventUIAction).(app.UIActionEvent)
	if !ok {
		t.Fatal("no ui_action event")
	}
	if act.ID != id || act.Action != "yes" || act.Pane != pane || act.Source != "control" {
		t.Errorf("ui_action = %+v", act)
	}
}

// A notification is answered once. The second take is refused by name, and no
// second input reaches the pane — the case this guards is a browser toast and a
// lock screen showing the same buttons.
func TestUIActionIsSingleUse(t *testing.T) {
	o, _, _ := newNotifyOrch(t)
	pd := newPipeDaemon(t, o)
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.UINotify(&r, app.UINotifyParams{Title: "t", Pane: &pane, Kind: app.NotifyKindAttention,
		Actions: []app.NotifyAction{{ID: "a", Label: "A", Send: "y"}}})
	id := r.data.(app.UINotifyResult).ID

	var first, second notifyResponder
	o.UIAction(&first, app.UIActionParams{ID: id, Action: "a"})
	o.UIAction(&second, app.UIActionParams{ID: id, Action: "a"})
	if !first.ok {
		t.Fatalf("first action failed: %v", first.err)
	}
	if !second.fail {
		t.Fatal("second action succeeded; want a refusal")
	}
	if second.err == "" || !strings.Contains(second.err, "no longer answerable") {
		t.Errorf("second refusal = %q", second.err)
	}

	pd.expect(t, orchestration.MsgInput)
	select {
	case m := <-pd.msgs:
		t.Fatalf("a second message reached the daemon: %v", m.mt)
	case <-time.After(50 * time.Millisecond):
	}
}

// An action whose pane exited is still SPENT: the send fails, the caller is
// told, and the entry is gone — a phone retrying a "yes" over a flaky link must
// not be able to land it twice because the first attempt reported an error.
func TestUIActionIsSpentEvenWhenTheSendFails(t *testing.T) {
	o, _, _ := newNotifyOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	var r notifyResponder
	o.UINotify(&r, app.UINotifyParams{Title: "t", Pane: &pane, Kind: app.NotifyKindAttention,
		Actions: []app.NotifyAction{{ID: "a", Label: "A", Send: "y"}}})
	id := r.data.(app.UINotifyResult).ID

	exit := 0
	o.panes[pane].exited = &exit

	var first, second notifyResponder
	o.UIAction(&first, app.UIActionParams{ID: id, Action: "a"})
	o.UIAction(&second, app.UIActionParams{ID: id, Action: "a"})
	if !first.fail {
		t.Fatal("action on an exited pane succeeded")
	}
	if !second.fail || !strings.Contains(second.err, "no longer answerable") {
		t.Errorf("retry = %q; want the entry to have been spent", second.err)
	}
}

// Refusals that happen before anything is registered: an unknown pane, and a
// button that says it will send text but names no pane to send it to.
func TestUINotifyRefusals(t *testing.T) {
	o, _, _ := newNotifyOrch(t)
	ghost := uint32(9999)

	var r notifyResponder
	o.UINotify(&r, app.UINotifyParams{Title: "t", Pane: &ghost})
	if !r.fail || !strings.Contains(r.err, "not found") {
		t.Errorf("unknown pane: %v / %q", r.fail, r.err)
	}

	var r2 notifyResponder
	o.UINotify(&r2, app.UINotifyParams{Title: "t",
		Actions: []app.NotifyAction{{ID: "a", Label: "A", Send: "y"}}})
	if !r2.fail || !strings.Contains(r2.err, "needs a pane") {
		t.Errorf("paneless send: %v / %q", r2.fail, r2.err)
	}
	if len(o.notifs) != 0 {
		t.Errorf("a refused notification left %d registry entries", len(o.notifs))
	}
}

// An announcement-only action (no Send) needs no pane and reaches subscribers
// as a ui_action — that is the shape a live caller uses to do the work itself.
func TestUIActionWithoutSendIsAnnouncementOnly(t *testing.T) {
	o, _, rec := newNotifyOrch(t)

	var r notifyResponder
	o.UINotify(&r, app.UINotifyParams{Title: "deploy?",
		Actions: []app.NotifyAction{{ID: "go", Label: "Ship it"}}})
	if !r.ok {
		t.Fatalf("ui.notify failed: %v", r.err)
	}
	var ar notifyResponder
	o.UIAction(&ar, app.UIActionParams{ID: r.data.(app.UINotifyResult).ID, Action: "go"})
	if !ar.ok {
		t.Fatalf("ui.action failed: %v", ar.err)
	}
	act, ok := findEvent(rec, app.EventUIAction).(app.UIActionEvent)
	if !ok || act.Action != "go" || act.Pane != 0 {
		t.Errorf("ui_action = %+v (ok=%v)", act, ok)
	}
}

// The registry is bounded and self-pruning: expired entries go, and a caller in
// a loop evicts its own oldest rather than growing catway's memory.
func TestNotifyRegistryIsBounded(t *testing.T) {
	o, _, _ := newNotifyOrch(t)
	pane := uint32(o.session.AllPaneIDs()[0])

	var stale notifyResponder
	o.UINotify(&stale, app.UINotifyParams{Title: "old", Pane: &pane,
		Actions: []app.NotifyAction{{ID: "a", Label: "A"}}})
	staleID := stale.data.(app.UINotifyResult).ID
	o.notifs[staleID].at = time.Now().Add(-2 * notifyActionTTL)

	for i := 0; i < maxLiveNotifies+5; i++ {
		var r notifyResponder
		o.UINotify(&r, app.UINotifyParams{Title: "n", Pane: &pane,
			Actions: []app.NotifyAction{{ID: "a", Label: "A"}}})
		if !r.ok {
			t.Fatalf("notify %d failed: %v", i, r.err)
		}
	}
	if len(o.notifs) >= maxLiveNotifies+1 {
		t.Errorf("registry holds %d entries; want under %d", len(o.notifs), maxLiveNotifies+1)
	}
	if o.notifs[staleID] != nil {
		t.Error("an expired notification is still answerable")
	}
	if len(o.notifOrder) > maxLiveNotifies {
		t.Errorf("notifOrder holds %d ids; the prune is not compacting", len(o.notifOrder))
	}
}

// findEvent returns the payload of the first event named name, or nil.
func findEvent(rec *recSub, name string) any {
	for i, n := range rec.names {
		if n == name {
			return rec.datas[i]
		}
	}
	return nil
}
