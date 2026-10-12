//go:build ghostty

package main

import (
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/orchestration"
)

// The session origin reaches the browser on both agent messages, and — unlike
// the blank word — stays there once the pane has worked.
func TestOriginOutlastsTheFirstPrompt(t *testing.T) {
	o, rt, _ := hookOrch(t)
	began := time.Now().Add(-5 * time.Minute)
	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "idle"})
	o.applyPaneAgentSession(orchestration.PaneAgentSession{PaneID: rt.id, Agent: "claude", SessionID: "sess-c",
		Origin: "clear", BeganMs: began.UnixMilli()})

	check := func(when string) {
		t.Helper()
		it := rollupItem(t, o, rt.id)
		if it.Origin != "clear" {
			t.Fatalf("%s: rollup origin = %q, want clear", when, it.Origin)
		}
		if d := time.Duration(it.OriginMs) * time.Millisecond; d < 5*time.Minute || d > 6*time.Minute {
			t.Fatalf("%s: rollup origin age = %v, want about 5m", when, d)
		}
		m := o.paneAgentMsg(rt, "claude", "working", true)
		if m.Origin != "clear" || m.OriginMs < it.OriginMs {
			t.Fatalf("%s: pane_agent origin = %q (%dms)", when, m.Origin, m.OriginMs)
		}
	}
	check("idle")
	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "working"})
	check("working")

	// The agent left: nothing to say about a conversation the pane no longer has.
	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "", State: "unknown"})
	if m := o.paneAgentMsg(rt, "", "unknown", true); m.Origin != "" || m.OriginMs != 0 {
		t.Fatalf("agentless pane carries an origin: %q (%dms)", m.Origin, m.OriginMs)
	}
}

// A hook ref is stamped when it first names a new conversation; a re-report of
// the same one (a compact) keeps that stamp; and where detection knows an
// earlier moment for the same id, the earlier one wins.
func TestHookOriginBeganStamp(t *testing.T) {
	o, rt, pub := hookOrch(t)
	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "idle"})
	report := func(n uint64, id, source string) {
		t.Helper()
		if herr := o.applyHookReport(methodReportAgentSession, hookReportParams{
			PaneID: pub, Source: "cats:claude", Agent: "claude", Seq: seq(n), AgentSessionID: id, SessionSource: source,
		}); herr != nil {
			t.Fatalf("report: %+v", herr)
		}
	}

	before := time.Now()
	report(1, "sess-1", "startup")
	stamped := rt.agentSession.began
	if stamped.Before(before) || time.Since(stamped) > time.Second {
		t.Fatalf("new conversation stamped %v, want about now", stamped)
	}
	if rt.pubOrigin.origin != "startup" || !rt.pubOrigin.began.Equal(stamped) {
		t.Fatalf("origin not published on the move: %+v", rt.pubOrigin)
	}

	time.Sleep(5 * time.Millisecond)
	report(2, "sess-1", "compact")
	if !rt.agentSession.began.Equal(stamped) || rt.agentSession.origin != "startup" {
		t.Fatalf("re-report restamped: %+v", rt.agentSession)
	}

	// Detection traced the same conversation back to the process start.
	early := stamped.Add(-90 * time.Second).Truncate(time.Millisecond) // the wire carries ms
	o.applyPaneAgentSession(orchestration.PaneAgentSession{PaneID: rt.id, Agent: "claude", SessionID: "sess-1",
		Origin: "startup", BeganMs: early.UnixMilli()})
	if _, origin, began := rt.sessionOrigin("claude"); origin != "startup" || !began.Equal(early) {
		t.Fatalf("merged = %s at %v, want startup at %v", origin, began, early)
	}
	// A later detected moment does not displace the hook's.
	rt.detectedSession.began = stamped.Add(time.Minute)
	if _, _, began := rt.sessionOrigin("claude"); !began.Equal(stamped) {
		t.Fatalf("later detected time won: %v", began)
	}
}

// A ref restored across a restart knows neither origin nor time; the host's
// replayed detection supplies both.
func TestRestoredRefBorrowsDetectedBegan(t *testing.T) {
	o, rt, _ := hookOrch(t)
	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "idle"})
	rt.agentSession = &agentSessionRef{source: "cats:claude", agent: "claude", kind: "id", value: "sess-r"}
	if _, origin, began := rt.sessionOrigin("claude"); origin != "" || !began.IsZero() {
		t.Fatalf("restored ref already knows %q at %v", origin, began)
	}
	began := time.Now().Add(-2 * time.Hour).Truncate(time.Millisecond)
	o.applyPaneAgentSession(orchestration.PaneAgentSession{PaneID: rt.id, Agent: "claude", SessionID: "sess-r",
		Origin: "resume", BeganMs: began.UnixMilli()})
	if _, origin, got := rt.sessionOrigin("claude"); origin != "resume" || !got.Equal(began) {
		t.Fatalf("after replay: %q at %v, want resume at %v", origin, got, began)
	}
	if it := rollupItem(t, o, rt.id); it.Origin != "resume" || it.OriginMs < (2*time.Hour).Milliseconds() {
		t.Fatalf("rollup = %q (%dms)", it.Origin, it.OriginMs)
	}
}
