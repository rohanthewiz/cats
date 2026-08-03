//go:build ghostty

package main

import (
	"testing"

	"github.com/rohanthewiz/cats/internal/acpchat"
)

// The chat panel's model comes from the same on-disk history the sidebar reads,
// keyed by the chat session's own ACP id — which for copilot is literally its
// ~/.copilot/session-state directory name (verified live: the directory appears
// at session/new, named for the id the agent just returned).
func TestChatModelResolver(t *testing.T) {
	root := t.TempDir()
	writeCopilotSession(t, root, "chat-sess", "/Users/x/proj", 0,
		copilotAutoChange(),
		copilotAutoResolved("gpt-5-mini", "medium"),
		copilotAssistant("gpt-5-mini"))

	o := &orch{modelRoots: map[string]string{"copilot": root}}
	read := o.chatModelResolver(acpchat.Backends()[0])
	if read == nil {
		t.Fatal("copilot backend should resolve a model reader")
	}
	if got := read("chat-sess"); got != "gpt-5-mini · medium" {
		t.Fatalf("model = %q, want gpt-5-mini · medium", got)
	}
}

// The readers fall back to "newest session started in this cwd" when the id
// misses. That fallback is right for a pane (whose agent may have no hook
// installed) and wrong for chat, which always knows its own session id: taking
// it would name some pane's agent running in the same project. Chat therefore
// passes no cwd, so a miss stays a miss.
func TestChatModelResolverNeverBorrowsAPaneSession(t *testing.T) {
	root := t.TempDir()
	// A pane's copilot session, live in the directory chat is anchored to.
	writeCopilotSession(t, root, "some-pane-session", "/Users/x/proj", 0,
		copilotAssistant("claude-sonnet-4.6"))

	o := &orch{modelRoots: map[string]string{"copilot": root}}
	read := o.chatModelResolver(acpchat.Backends()[0])
	if got := read("chat-sess-not-on-disk"); got != "" {
		t.Fatalf("model = %q, want empty — the chat session has no history yet", got)
	}
}

// A backend whose history cannot be read here (no ModelAgent, or that agent's
// home is not on this machine) resolves to nil rather than to a reader that
// always answers "" — the manager then never polls at all.
func TestChatModelResolverAbsent(t *testing.T) {
	o := &orch{modelRoots: map[string]string{}}
	if read := o.chatModelResolver(acpchat.Backends()[0]); read != nil {
		t.Fatal("no resolvable copilot home should disable model resolution")
	}
	o = &orch{modelRoots: map[string]string{"copilot": t.TempDir()}}
	if read := o.chatModelResolver(acpchat.BackendDef{ID: "x", Name: "X"}); read != nil {
		t.Fatal("a backend with no ModelAgent should disable model resolution")
	}
}
