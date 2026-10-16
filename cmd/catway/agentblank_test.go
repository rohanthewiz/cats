//go:build ghostty

package main

import (
	"strconv"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/orchestration"
)

// The records claude opens a /clear'd transcript with: bookkeeping, the local
// command caveat (isMeta), and the /clear command itself.
var clearedHead = []string{
	`{"type":"mode","mode":"default","sessionId":"x"}`,
	`{"type":"file-history-snapshot","messageId":"m","snapshot":{}}`,
	`{"type":"user","isMeta":true,"timestamp":"2026-09-27T02:45:25.693Z","message":{"role":"user","content":"<local-command-caveat>Caveat</local-command-caveat>"}}`,
	userPrompt("<command-name>/clear</command-name>\n<command-message>clear</command-message>\n<command-args></command-args>", "2026-09-27T02:45:25.684Z"),
}

// A transcript is blank until the model is asked something: slash commands and
// shell escapes do not count, a real prompt or any main-chain answer does, and
// a sub-agent's answer alone does not.
func TestClaudeBlank(t *testing.T) {
	projects := t.TempDir()
	cases := []struct {
		name  string
		lines []string
		blank bool
	}{
		{"cleared", clearedHead, true},
		{"cleared then /model", append(append([]string{}, clearedHead...),
			userPrompt("<command-name>/model</command-name>\n<command-args>opus</command-args>", "2026-09-27T02:46:00Z")), true},
		{"shell escape", []string{userPrompt("<bash-input>ls</bash-input>", "2026-09-27T02:46:00Z")}, true},
		{"sidechain answer only", []string{`{"type":"assistant","isSidechain":true,"message":{"role":"assistant","model":"m"}}`}, true},
		{"prompted", append(append([]string{}, clearedHead...), userPrompt("fix the bug", "2026-09-27T02:46:00Z")), false},
		{"answered", []string{assistant("claude-opus-5")}, false},
	}
	for i, c := range cases {
		id := "sess-" + strconv.Itoa(i) // names hold spaces and slashes; ids must be bare tokens
		writeTranscript(t, projects, "-Users-x-proj", id+".jsonl", 0, c.lines...)
		blank, found := claudeBlank(projects, id)
		if !found || blank != c.blank {
			t.Errorf("%s: blank=%v found=%v, want blank=%v", c.name, blank, found, c.blank)
		}
	}
	if _, found := claudeBlank(projects, "sess-missing"); found {
		t.Error("a missing transcript must report not found")
	}
	// Never off the cwd fallback: no session, no answer.
	if _, found := claudeBlank(projects, ""); found {
		t.Error("an empty session must not resolve a transcript")
	}
}

func TestCopilotBlank(t *testing.T) {
	root := t.TempDir()
	writeCopilotSession(t, root, "fresh", "/p", 0, `{"type":"session.start","data":{}}`)
	writeCopilotSession(t, root, "asked", "/p", 0, `{"type":"session.start","data":{}}`,
		`{"type":"user.message","data":{"content":"hi"}}`)
	if blank, found := copilotBlank(root, "fresh"); !found || !blank {
		t.Errorf("fresh: blank=%v found=%v", blank, found)
	}
	if blank, found := copilotBlank(root, "asked"); !found || blank {
		t.Errorf("asked: blank=%v found=%v", blank, found)
	}
}

// A missing history is blank only for a conversation known to have just begun;
// the word follows the origin.
func TestResolveBlank(t *testing.T) {
	for _, c := range []struct {
		blank, found bool
		origin, want string
	}{
		{true, true, "clear", "cleared"},
		{true, true, "startup", "new"},
		{true, true, "", "new"},
		{false, true, "clear", ""},
		{false, false, "clear", "cleared"},
		{false, false, "startup", "new"},
		{false, false, "resume", ""},
		{false, false, "", ""},
	} {
		if got := resolveBlank(c.blank, c.found, c.origin); got != c.want {
			t.Errorf("resolveBlank(%v, %v, %q) = %q, want %q", c.blank, c.found, c.origin, got, c.want)
		}
	}
}

// End to end on the orchestrator: a /clear'd session reads as "cleared" in the
// rollup, going working drops it at once, and the history that then has a turn
// is never read for blankness again.
func TestBlankFollowsTheConversation(t *testing.T) {
	o, rt, _ := hookOrch(t)
	projects := t.TempDir()
	o.modelRoots["claude"] = projects
	rt.cwd = "/Users/x/proj"
	writeTranscript(t, projects, "-Users-x-proj", "sess-c.jsonl", 0, clearedHead...)

	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "idle"})
	o.applyPaneAgentSession(orchestration.PaneAgentSession{PaneID: rt.id, Agent: "claude", SessionID: "sess-c", Origin: "clear"})
	waitFor(t, o, func() bool { return rt.blank != "" })
	if rt.blank != "cleared" {
		t.Fatalf("blank = %q, want cleared", rt.blank)
	}
	if got := rollupItem(t, o, rt.id).Blank; got != "cleared" {
		t.Fatalf("rollup blank = %q, want cleared", got)
	}
	// pane.list carries the same word, for control-API clients.
	if got := o.PaneMeta(rt.id).AgentBlank; got != "cleared" {
		t.Fatalf("pane meta blank = %q, want cleared", got)
	}

	// Asked something: the dot goes the moment the pane works, no read needed.
	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "working"})
	if rt.blank != "" {
		t.Fatalf("working pane still blank: %q", rt.blank)
	}
	if got := rollupItem(t, o, rt.id).Blank; got != "" {
		t.Fatalf("rollup blank while working = %q", got)
	}
	if got := o.PaneMeta(rt.id).AgentBlank; got != "" {
		t.Fatalf("pane meta blank while working = %q", got)
	}

	// The turn lands in the history; the next read records it as non-blank.
	writeTranscript(t, projects, "-Users-x-proj", "sess-c.jsonl", 0,
		append(append([]string{}, clearedHead...), userPrompt("go", "2026-09-27T02:46:00Z"), assistant("claude-opus-5"))...)
	rt.modelAt = time.Time{}
	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "idle"})
	waitFor(t, o, func() bool { return rt.nonBlankSession == "sess-c" })
	if rt.blank != "" {
		t.Fatalf("answered conversation reads blank: %q", rt.blank)
	}
}

// A blank answer from a read that started before the pane went working is
// stale on arrival and must not put the dot back.
func TestStaleBlankReadIsDropped(t *testing.T) {
	o, rt, _ := hookOrch(t)
	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "idle"})
	rt.workingAt = time.Now()
	o.setAgentRead(rt.id, agentReadResult{session: "s", blank: "new", blankRead: true, started: rt.workingAt.Add(-time.Second)})
	if rt.blank != "" {
		t.Fatalf("stale blank read applied: %q", rt.blank)
	}
	o.setAgentRead(rt.id, agentReadResult{session: "s", blank: "new", blankRead: true, started: rt.workingAt.Add(time.Millisecond)})
	if rt.blank != "new" {
		t.Fatalf("fresh blank read dropped: %q", rt.blank)
	}
}
