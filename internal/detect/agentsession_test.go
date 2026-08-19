package detect

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeClaudeSession lays down one entry in claude's live-process registry, the
// shape claude itself writes: a flat object keyed on disk by the pid.
func writeClaudeSession(t *testing.T, root string, pid int, body string) {
	t.Helper()
	dir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, strconv.Itoa(pid)+".json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// The registry turns a pid into the conversation that process is in — the answer
// two panes running claude in one directory cannot get any other way without an
// installed hook.
func TestAgentSessionIDReadsClaudeRegistry(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	writeClaudeSession(t, root, 4242, `{"pid":4242,"sessionId":"ef004484-48d7-4967-ae7f-7b1819f5f290",`+
		`"cwd":"/Users/x/proj","status":"busy"}`)

	if got := AgentSessionID("claude", []int{4242}); got != "ef004484-48d7-4967-ae7f-7b1819f5f290" {
		t.Fatalf("session = %q, want the registry's id", got)
	}
	// A pid with no entry, and a pid that is not one, are both "no answer".
	if got := AgentSessionID("claude", []int{4243}); got != "" {
		t.Fatalf("missing entry: session = %q, want empty", got)
	}
	if got := AgentSessionID("claude", []int{0}); got != "" {
		t.Fatalf("no pid: session = %q, want empty", got)
	}
	if got := AgentSessionID("claude", nil); got != "" {
		t.Fatalf("no pids at all: session = %q, want empty", got)
	}
	// A wrapper carrying the name in front of the process that keeps the state:
	// the pids are tried in order and the first that answers wins.
	if got := AgentSessionID("claude", []int{4241, 4242}); got != "ef004484-48d7-4967-ae7f-7b1819f5f290" {
		t.Fatalf("wrapper first: session = %q, want the child's id", got)
	}
	// An agent that keeps no such registry costs a map lookup and answers nothing.
	if got := AgentSessionID("copilot", []int{4242}); got != "" {
		t.Fatalf("copilot: session = %q, want empty", got)
	}
}

// What is read is refused unless it is usable: an entry whose own pid disagrees
// with the file's is a dead session's leftovers under a reused pid, and an id
// that is not a bare token would travel into the glob that names a transcript.
func TestAgentSessionIDRejectsUnusableEntries(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	for name, body := range map[string]string{
		"pid mismatch": `{"pid":9999,"sessionId":"sess-a"}`,
		"glob in id":   `{"pid":4242,"sessionId":"sess-*"}`,
		"path in id":   `{"pid":4242,"sessionId":"../../etc/passwd"}`,
		"empty id":     `{"pid":4242,"sessionId":""}`,
		"not json":     `{"pid":4242,`,
	} {
		writeClaudeSession(t, root, 4242, body)
		if got := AgentSessionID("claude", []int{4242}); got != "" {
			t.Fatalf("%s: session = %q, want empty", name, got)
		}
	}
}
