//go:build ghostty

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/orchestration"
)

// writeTranscript lays down one transcript file under projects/<slug>/, with the
// given lines joined as JSONL, and stamps its mtime so "newest wins" is testable.
func writeTranscript(t *testing.T, projects, slug, name string, age time.Duration, lines ...string) string {
	t.Helper()
	dir := filepath.Join(projects, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

func assistant(model string) string {
	return `{"type":"assistant","isSidechain":false,"message":{"role":"assistant","model":"` + model + `"}}`
}

// assistantEffort is an assistant record as current claude writes it: the effort
// sits at the top level, beside the message rather than in it.
func assistantEffort(model, effort string) string {
	return `{"type":"assistant","isSidechain":false,"effort":"` + effort +
		`","message":{"role":"assistant","model":"` + model + `"}}`
}

// A hook-reported session id names the transcript outright, wherever claude
// slugged it — the pane's cwd does not have to agree.
func TestClaudeModelBySessionID(t *testing.T) {
	projects := t.TempDir()
	writeTranscript(t, projects, "-elsewhere", "sess-1.jsonl", 0,
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		assistant("claude-opus-5"))

	if got := claudeModel(projects, "/Users/x/proj", "sess-1"); got != "claude-opus-5" {
		t.Fatalf("model = %q, want claude-opus-5", got)
	}
}

// Without a session id the pane's cwd picks the project directory and the most
// recently written transcript in it wins. Both slug spellings are searched, so a
// directory written by an older claude (which kept '_') still resolves.
func TestClaudeModelByCwd(t *testing.T) {
	projects := t.TempDir()
	cwd := "/Users/x/cbre_projs/lezen"
	writeTranscript(t, projects, "-Users-x-cbre-projs-lezen", "old.jsonl", time.Hour, assistant("claude-sonnet-5"))
	writeTranscript(t, projects, "-Users-x-cbre-projs-lezen", "new.jsonl", time.Minute, assistant("claude-opus-5"))

	if got := claudeModel(projects, cwd, ""); got != "claude-opus-5" {
		t.Fatalf("model = %q, want the newest transcript's claude-opus-5", got)
	}

	// Legacy directory only (underscores preserved), and a session id that names
	// no file falls back to the cwd search rather than giving up.
	legacy := t.TempDir()
	writeTranscript(t, legacy, "-Users-x-cbre_projs-lezen", "old.jsonl", time.Minute, assistant("claude-fable-5"))
	if got := claudeModel(legacy, cwd, "no-such-session"); got != "claude-fable-5" {
		t.Fatalf("legacy slug: model = %q, want claude-fable-5", got)
	}

	// A cwd with no project directory at all resolves to nothing.
	if got := claudeModel(projects, "/Users/x/untouched", ""); got != "" {
		t.Fatalf("unknown cwd: model = %q, want empty", got)
	}
}

// The reported model is the last *main-thread, sampled* assistant record: a
// sub-agent's record names the sub-agent's model, and "<synthetic>" is a message
// claude fabricated rather than sampled.
func TestLastAssistantModelSkipsSidechainAndSynthetic(t *testing.T) {
	projects := t.TempDir()
	path := writeTranscript(t, projects, "-p", "s.jsonl", 0,
		assistant("claude-sonnet-5"),
		assistant("claude-opus-5"),
		`{"type":"assistant","isSidechain":true,"message":{"model":"claude-haiku-4-5"}}`,
		`{"type":"assistant","isSidechain":false,"message":{"model":"<synthetic>"}}`,
		`{"type":"user","message":{"role":"user","content":"next"}}`,
		`{"type":"file-history-snapshot","snapshot":{}}`)

	if got := lastAssistantModel(path); got != "claude-opus-5" {
		t.Fatalf("model = %q, want claude-opus-5", got)
	}
	if got := lastAssistantModel(filepath.Join(projects, "-p", "missing.jsonl")); got != "" {
		t.Fatalf("missing file: model = %q, want empty", got)
	}
}

// The effort the last main-thread record ran at is appended to the model, and it
// comes from that same record — an earlier turn's effort does not leak forward.
// A record naming no effort (or junk where the word should be) reports the model
// alone rather than a dangling separator.
func TestLastAssistantModelCarriesEffort(t *testing.T) {
	projects := t.TempDir()
	path := writeTranscript(t, projects, "-p", "s.jsonl", 0,
		assistantEffort("claude-opus-5", "medium"),
		assistantEffort("claude-opus-5", "high"),
		`{"type":"assistant","isSidechain":true,"effort":"low","message":{"model":"claude-haiku-4-5"}}`)
	if got := lastAssistantModel(path); got != "claude-opus-5 · high" {
		t.Fatalf("model = %q, want \"claude-opus-5 · high\"", got)
	}

	bare := writeTranscript(t, projects, "-p", "bare.jsonl", 0, assistant("claude-opus-5"))
	if got := lastAssistantModel(bare); got != "claude-opus-5" {
		t.Fatalf("no effort: model = %q, want claude-opus-5", got)
	}

	junk := writeTranscript(t, projects, "-p", "junk.jsonl", 0,
		assistantEffort("claude-opus-5", "a very wordy effort"))
	if got := lastAssistantModel(junk); got != "claude-opus-5" {
		t.Fatalf("junk effort: model = %q, want claude-opus-5", got)
	}
}

// Only the tail of a long transcript is read, and the record it lands mid-way
// through is dropped rather than half-parsed.
func TestLastAssistantModelReadsTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.jsonl")
	// A padded record whose model must never be reported (it is scrolled out),
	// followed by enough filler to push the read window past it.
	var b strings.Builder
	b.WriteString(assistant("claude-scrolled-out") + "\n")
	filler := `{"type":"user","message":{"role":"user","content":"` + strings.Repeat("x", 1023) + `"}}` + "\n"
	for b.Len() < modelTailBytes+len(filler) {
		b.WriteString(filler)
	}
	b.WriteString(assistant("claude-opus-5") + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := lastAssistantModel(path); got != "claude-opus-5" {
		t.Fatalf("model = %q, want claude-opus-5", got)
	}
}

// A session id is only trusted in the glob when it looks like an id — a value
// carrying glob metacharacters would otherwise match another pane's transcript.
func TestTranscriptIDGate(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"f54261c5-3dbc-45b6-99bc-c243efda40e8", true},
		{"sess_1", true},
		{"", false},
		{"*", false},
		{"../../etc/passwd", false},
	} {
		if got := isTranscriptID(tc.id); got != tc.want {
			t.Fatalf("isTranscriptID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// The pane's model tracks the arbitrated agent: it is resolved for a claude pane
// and dropped the moment claude is no longer what is running there.
func TestRefreshAgentModelFollowsAgent(t *testing.T) {
	o, rt, _ := hookOrch(t)
	projects := t.TempDir()
	o.claudeProjects = projects
	rt.cwd = "/Users/x/proj"
	writeTranscript(t, projects, "-Users-x-proj", "s.jsonl", 0, assistant("claude-opus-5"))

	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "working"})
	waitFor(t, o, func() bool { return rt.agentModel != "" }) // the read is off-loop
	if rt.agentModel != "claude-opus-5" {
		t.Fatalf("model = %q, want claude-opus-5", rt.agentModel)
	}

	// The agent leaves: the model goes with it, without waiting on a re-read.
	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "", State: "unknown"})
	if rt.agentModel != "" {
		t.Fatalf("model = %q after the agent left, want empty", rt.agentModel)
	}
}

// The sidebar's agents rollup names each row by its model, so a resolved model
// has to reach the rollup — including the resolution that lands *after* the
// state change that triggered it, which is the ordinary case.
func TestAgentsRollupCarriesModel(t *testing.T) {
	o, rt, _ := hookOrch(t)
	projects := t.TempDir()
	o.claudeProjects = projects
	rt.cwd = "/Users/x/proj"
	writeTranscript(t, projects, "-Users-x-proj", "s.jsonl", 0, assistant("claude-fable-5"))

	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "working"})
	waitFor(t, o, func() bool { return rt.agentModel != "" }) // the read is off-loop
	if got := rollupItem(t, o, rt.id).Model; got != "claude-fable-5" {
		t.Fatalf("rollup model = %q, want claude-fable-5", got)
	}

	// And a row whose agent has no resolvable model carries none, so the browser
	// falls back to the agent's own name rather than showing a stale one.
	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "codex", State: "working"})
	if got := rollupItem(t, o, rt.id).Model; got != "" {
		t.Fatalf("rollup model = %q for a non-claude agent, want empty", got)
	}
}

// waitFor drains the orchestrator mailbox until cond holds — these tests own the
// loop goroutine, so work posted by the off-loop read only runs when pumped.
func waitFor(t *testing.T, o *orch, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the model to resolve")
		}
		select {
		case fn := <-o.mailbox:
			fn()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
