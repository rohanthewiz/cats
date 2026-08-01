//go:build ghostty

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/push"
)

// notifyKind ports cats's toast classification: attention on any change into
// blocked, finished on completion transitions, nothing otherwise.
func TestNotifyKind(t *testing.T) {
	cases := []struct {
		name                               string
		prevState, prevAgent, state, agent string
		want                               string
	}{
		{"working to blocked", "working", "claude", "blocked", "claude", "attention"},
		{"idle to blocked", "idle", "claude", "blocked", "claude", "attention"},
		{"working to idle", "working", "claude", "idle", "claude", "finished"},
		{"blocked to idle", "blocked", "claude", "idle", "claude", "finished"},
		{"unknown to idle same agent", "unknown", "claude", "idle", "claude", "finished"},
		{"unknown to idle new agent", "unknown", "", "idle", "claude", ""},
		{"unknown to idle agent swap", "unknown", "codex", "idle", "claude", ""},
		{"idle to working", "idle", "claude", "working", "claude", ""},
		{"same state", "blocked", "claude", "blocked", "claude", ""},
		{"no agent", "working", "", "blocked", "", ""},
	}
	for _, c := range cases {
		if got := notifyKind(c.prevState, c.prevAgent, c.state, c.agent); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// drainDown decodes every queued down-message on a client.
func drainDown(t *testing.T, c *client) []any {
	t.Helper()
	var out []any
	for {
		select {
		case b := <-c.out:
			m, err := browserproto.DecodeDown(b)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			out = append(out, m)
		default:
			return out
		}
	}
}

// A working→blocked pane_agent event must reach browsers as a notify carrying
// the pane, its public handle, and the workspace context — and reach control-API
// subscribers as a pane_notify event. A repeat of the same state must not.
func TestOnPaneAgentEmitsNotify(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	c := &client{o: o, out: make(chan []byte, 32), trans: map[uint32]*browserproto.FrameTranslator{}}
	o.conns[c] = struct{}{}
	pid := uint32(o.session.AllPaneIDs()[0])

	rec := &recSub{}
	o.subs[&ctlSubscriber{sub: rec, filter: app.EventsSubscribeParams{Events: []string{app.EventPaneNotify}}}] = struct{}{}

	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "working"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "blocked"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "blocked"}) // resync replay

	var notifies []*browserproto.Notify
	for _, m := range drainDown(t, c) {
		if n, ok := m.(*browserproto.Notify); ok {
			notifies = append(notifies, n)
		}
	}
	if len(notifies) != 1 {
		t.Fatalf("notify count: got %d want 1 (transition dedupe)", len(notifies))
	}
	n := notifies[0]
	if n.Kind != "attention" || n.Message != "claude needs attention" || n.Pane != pid {
		t.Fatalf("notify: %+v", n)
	}
	pub, _ := o.session.PublicPaneID(o.session.AllPaneIDs()[0])
	if n.Pub != pub || n.Body == "" {
		t.Fatalf("notify pub/context: %+v", n)
	}

	if len(rec.names) != 1 || rec.names[0] != app.EventPaneNotify {
		t.Fatalf("pane_notify events: %v", rec.names)
	}
	ev := rec.datas[0].(app.PaneNotifyEvent)
	if ev.Kind != "attention" || ev.Pane != pid || ev.Agent != "claude" {
		t.Fatalf("pane_notify payload: %+v", ev)
	}
}

// recPush records push deliveries in place of a real *push.Bridge, so the
// notify→push wiring is testable without an HTTP server (internal/push covers
// the delivery itself).
type recPush struct{ evs []push.Event }

func (r *recPush) Send(ev push.Event) { r.evs = append(r.evs, ev) }

// The push bridge hangs off the same choke point as the browser broadcast: one
// notification, one push, carrying the context a lock screen needs (which agent,
// which pane, where). The transition dedupe upstream applies to it too — a
// resync replay must not re-push.
func TestOnPaneAgentPushesNotification(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	c := &client{o: o, out: make(chan []byte, 32), trans: map[uint32]*browserproto.FrameTranslator{}}
	o.conns[c] = struct{}{}
	rec := &recPush{}
	o.push = rec
	pid := uint32(o.session.AllPaneIDs()[0])
	o.panes[pid].cwd = "/tmp/somewhere"

	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "working"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "blocked"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "blocked"}) // resync replay

	if len(rec.evs) != 1 {
		t.Fatalf("push count: got %d want 1", len(rec.evs))
	}
	ev := rec.evs[0]
	pub, _ := o.session.PublicPaneID(o.session.AllPaneIDs()[0])
	if ev.Kind != push.KindAttention || ev.Title != "claude needs attention" {
		t.Errorf("push kind/title: %+v", ev)
	}
	if ev.Pane != pid || ev.Pub != pub || ev.Agent != "claude" {
		t.Errorf("push addressing: %+v (want pane %d pub %q)", ev, pid, pub)
	}
	if ev.Body == "" || ev.Cwd != "/tmp/somewhere" {
		t.Errorf("push context: body=%q cwd=%q", ev.Body, ev.Cwd)
	}

	// The browser still gets its toast — the bridge is additive, never a
	// replacement.
	var notifies int
	for _, m := range drainDown(t, c) {
		if _, ok := m.(*browserproto.Notify); ok {
			notifies++
		}
	}
	if notifies != 1 {
		t.Fatalf("browser notify count: got %d want 1", notifies)
	}
}

// An orch with no bridge configured — the default — must not panic on the
// unconditional Send in notifyAll. This is the case every existing test and
// every unconfigured deployment takes.
func TestNotifyWithoutPushBridge(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	pid := uint32(o.session.AllPaneIDs()[0])
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "blocked"})
}

func TestShortenHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	if got := shortenHome(home); got != "~" {
		t.Errorf("shortenHome(home) = %q, want ~", got)
	}
	if got := shortenHome(filepath.Join(home, "projs")); got != filepath.Join("~", "projs") {
		t.Errorf("shortenHome under home = %q", got)
	}
	// A path that merely shares a prefix with home is not under it.
	if got := shortenHome(home + "-backup"); got != home+"-backup" {
		t.Errorf("shortenHome(sibling) = %q, want unchanged", got)
	}
	if got := shortenHome(""); got != "" {
		t.Errorf("shortenHome(\"\") = %q", got)
	}
}

// A background completion (working → idle) notifies "finished".
func TestOnPaneAgentFinished(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	c := &client{o: o, out: make(chan []byte, 32), trans: map[uint32]*browserproto.FrameTranslator{}}
	o.conns[c] = struct{}{}
	pid := uint32(o.session.AllPaneIDs()[0])

	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "codex", State: "working"})
	drainDown(t, c) // discard the working-state chrome
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "codex", State: "idle"})

	for _, m := range drainDown(t, c) {
		if n, ok := m.(*browserproto.Notify); ok {
			if n.Kind != "finished" || n.Message != "codex finished" {
				t.Fatalf("notify: %+v", n)
			}
			return
		}
	}
	t.Fatal("no notify for the completion transition")
}

// The attention marker (cats's pane.seen): a completion while the pane is off
// the viewport flags it unseen in the rollup; re-entering the viewport clears
// it. On-viewport completions stay seen.
func TestAgentSeenMarkers(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	c := &client{o: o, out: make(chan []byte, 256), trans: map[uint32]*browserproto.FrameTranslator{}}
	o.conns[c] = struct{}{}
	pid := uint32(o.session.AllPaneIDs()[0])

	// An on-viewport completion stays seen.
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "working"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "idle"})
	if item := rollupItem(t, o, pid); !item.Seen {
		t.Fatal("visible completion should stay seen")
	}

	// Move the pane off the viewport (a second tab becomes active), then
	// complete a run there: the rollup flags it unseen.
	o.handleCmd(c, cmd(t, "t1", browserproto.CmdTabCreate, nil))
	if o.visible[pid] {
		t.Fatal("test setup: pane still visible after tab.create")
	}
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "working"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "idle"})
	item := rollupItem(t, o, pid)
	if item.Seen {
		t.Fatal("off-viewport completion should be unseen")
	}
	if item.Tab != 1 {
		t.Fatalf("rollup tab: got %d want 1", item.Tab)
	}

	// A new run clears the marker even off-viewport (cats: state != idle).
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "working"})
	if item := rollupItem(t, o, pid); !item.Seen {
		t.Fatal("leaving idle should clear unseen")
	}
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "idle"})
	if item := rollupItem(t, o, pid); item.Seen {
		t.Fatal("second off-viewport completion should be unseen again")
	}

	// Switching back to the pane's tab marks it seen.
	o.handleCmd(c, cmd(t, "t2", browserproto.CmdTabFocus, browserproto.TabParams{Num: 1}))
	if item := rollupItem(t, o, pid); !item.Seen {
		t.Fatal("entering the viewport should mark the pane seen")
	}
}

// The rollup's state age: unknown until the first publish, restamped only when
// the state actually moves (so a republish of the same state doesn't reset it).
func TestAgentStateAge(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	pid := uint32(o.session.AllPaneIDs()[0])

	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "idle"})
	if got := rollupItem(t, o, pid).SinceMs; got < 0 {
		t.Fatalf("first publish should stamp an age, got %d", got)
	}

	// Hold the state and republish: the age keeps counting from the change.
	o.panes[pid].stateAt = time.Now().Add(-90 * time.Second)
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "idle"})
	if got := rollupItem(t, o, pid).SinceMs; got < 89_000 {
		t.Fatalf("unchanged state should keep its age, got %d", got)
	}

	// A real change restarts it.
	o.onPaneAgent(orchestration.PaneAgent{PaneID: pid, Agent: "claude", State: "working"})
	if got := rollupItem(t, o, pid).SinceMs; got > 5_000 {
		t.Fatalf("state change should restamp the age, got %d", got)
	}
}

// rollupItem finds a pane's row in the agents rollup.
func rollupItem(t *testing.T, o *orch, pid uint32) browserproto.AgentItem {
	t.Helper()
	for _, it := range o.agentsMsg().Items {
		if it.Pane == pid {
			return it
		}
	}
	t.Fatalf("pane %d not in agents rollup", pid)
	return browserproto.AgentItem{}
}
