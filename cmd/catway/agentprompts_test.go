//go:build ghostty

package main

import (
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// userPrompt is a typed prompt as current claude writes it.
func userPrompt(text, at string) string {
	return `{"type":"user","isSidechain":false,"timestamp":"` + at +
		`","origin":{"kind":"human"},"message":{"role":"user","content":` + jsonString(text) + `}}`
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Only what the user sent counts as a prompt. A claude transcript records far
// more as "user" turns — tool results, harness-injected text, sub-agent turns,
// slash-command output, notifications — and every one of those would read as a
// prompt nobody typed. Slash commands and "!" escapes are kept, respelled the
// way they were typed.
func TestLastClaudePromptsFiltersAndRespells(t *testing.T) {
	projects := t.TempDir()
	path := writeTranscript(t, projects, "-p", "s.jsonl", 0,
		userPrompt("first ask", "2026-10-10T10:00:00Z"),
		assistant("claude-opus-5"),
		`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"t1","type":"tool_result","content":"file body"}]},"toolUseResult":{}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>Caveat</local-command-caveat>"}}`,
		`{"type":"user","message":{"role":"user","content":"<command-name>/model</command-name>\n<command-message>model</command-message>\n<command-args>opus</command-args>"}}`,
		`{"type":"user","message":{"role":"user","content":"<local-command-stdout>Set model to opus</local-command-stdout>"}}`,
		`{"type":"user","isSidechain":true,"message":{"role":"user","content":"a sub-agent's brief"}}`,
		`{"type":"user","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>done</task-notification>"}}`,
		`{"type":"user","origin":{"kind":"peer"},"message":{"role":"user","content":"Another Claude session sent a message"}}`,
		`{"type":"user","message":{"role":"user","content":"<bash-input>git status</bash-input>"}}`,
		`{"type":"user","message":{"role":"user","content":"<bash-stdout>clean</bash-stdout><bash-stderr></bash-stderr>"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`,
		`{"type":"user","origin":{"kind":"human"},"message":{"role":"user","content":[{"type":"text","text":"why is this red?"},{"type":"image","source":{}}]}}`,
		userPrompt("  commit and push  \n", "2026-10-10T10:05:00Z"),
	)

	got := lastClaudePrompts(path, 10)
	want := []string{"commit and push", "why is this red?\n[image]", "! git status", "/model opus", "first ask"}
	if len(got) != len(want) {
		t.Fatalf("prompts = %+v, want %q", got, want)
	}
	for i := range want {
		if got[i].Text != want[i] {
			t.Errorf("prompt %d = %q, want %q", i, got[i].Text, want[i])
		}
	}
	if got[0].At != "2026-10-10T10:05:00Z" {
		t.Errorf("newest prompt's time = %q, want the record's timestamp", got[0].At)
	}

	// The limit counts prompts, not records: the walk keeps going past the
	// records it skips.
	if two := lastClaudePrompts(path, 2); len(two) != 2 || two[1].Text != "why is this red?\n[image]" {
		t.Fatalf("limit 2 = %+v", two)
	}
}

// The gate before the decode must not depend on JSON spacing: a history written
// as `"type": "user"` has to read the same as claude's compact form.
func TestLastClaudePromptsIgnoresJSONSpacing(t *testing.T) {
	projects := t.TempDir()
	path := writeTranscript(t, projects, "-p", "s.jsonl", 0,
		`{"type": "user", "isSidechain": false, "message": {"role": "user", "content": "spaced out"}}`)
	if got := lastClaudePrompts(path, 5); len(got) != 1 || got[0].Text != "spaced out" {
		t.Fatalf("prompts = %+v, want the spaced record", got)
	}
}

// A prompt is followed by a whole turn of tool traffic, so the prompts tail has
// to reach well past what the model read covers (modelTailBytes).
func TestLastClaudePromptsReachesPastTheModelTail(t *testing.T) {
	projects := t.TempDir()
	bulk := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"` +
		strings.Repeat("x", 64<<10) + `"}]}}`
	lines := []string{userPrompt("the old ask", "")}
	for i := 0; i < 8; i++ { // 512 KiB of tool results: twice the model tail
		lines = append(lines, bulk)
	}
	path := writeTranscript(t, projects, "-p", "s.jsonl", 0, lines...)

	if got := lastClaudePrompts(path, 5); len(got) != 1 || got[0].Text != "the old ask" {
		t.Fatalf("prompts = %+v, want the prompt behind the tool traffic", got)
	}
}

// A giant paste is cut, and the cut is marked so it never reads as the whole
// prompt.
func TestClipPrompt(t *testing.T) {
	long := strings.Repeat("é", maxPromptRunes+10)
	got := clipPrompt(long)
	if r := []rune(got); len(r) != maxPromptRunes+1 || r[len(r)-1] != '…' {
		t.Fatalf("clipped to %d runes ending %q", len(r), string(r[len(r)-1]))
	}
	if clipPrompt("  short \n") != "short" {
		t.Fatal("a short prompt is only trimmed")
	}
}

func TestLastCopilotPrompts(t *testing.T) {
	root := t.TempDir()
	writeCopilotSession(t, root, "sess-c", "/Users/x/proj", 0,
		`{"type":"user.message","timestamp":"2026-10-10T09:00:00Z","data":{"content":"add a test"}}`,
		copilotAssistant("gpt-5.4"),
		`{"type":"user.message","data":{"content":"  "}}`,
		`{"type":"user.message","data":{"content":"now run it","transformedContent":"<context/> now run it"}}`,
	)

	got := copilotPrompts(root, "/Users/x/proj", "", 5)
	if len(got) != 2 || got[0].Text != "now run it" || got[1].Text != "add a test" {
		t.Fatalf("prompts = %+v, want [now run it, add a test]", got)
	}
	if got[1].At != "2026-10-10T09:00:00Z" {
		t.Errorf("At = %q, want the event's timestamp", got[1].At)
	}
}

// promptsResponder captures the one answer PanePrompts gives.
type promptsResponder struct {
	done bool
	fail string
	res  app.PanePromptsResult
}

func (r *promptsResponder) WantsReply() bool { return true }
func (r *promptsResponder) OK(data any) {
	r.done = true
	r.res, _ = data.(app.PanePromptsResult)
}
func (r *promptsResponder) Fail(msg string) { r.done, r.fail = true, msg }

// End to end through the orch: the pane's agent picks the reader, the pane's cwd
// picks the transcript, and the answer arrives on the loop once the off-loop
// read posts it back.
func TestPanePromptsReadsThePaneAgentsHistory(t *testing.T) {
	o, rt, _ := hookOrch(t)
	projects := t.TempDir()
	o.modelRoots["claude"] = projects
	rt.cwd = "/Users/x/proj"
	writeTranscript(t, projects, "-Users-x-proj", "s.jsonl", 0,
		userPrompt("one", ""), assistant("claude-opus-5"), userPrompt("two", ""))

	// No agent yet: an answer, not an error, saying why it is empty.
	r := &promptsResponder{}
	o.PanePrompts(r, rt.id, 5)
	if !r.done || r.fail != "" || len(r.res.Prompts) != 0 || r.res.Note == "" {
		t.Fatalf("agentless pane: %+v", r)
	}

	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "idle"})
	r = &promptsResponder{}
	o.PanePrompts(r, rt.id, 5)
	waitFor(t, o, func() bool { return r.done })
	if r.fail != "" || r.res.Agent != "claude" || len(r.res.Prompts) != 2 || r.res.Prompts[0].Text != "two" {
		t.Fatalf("claude pane: %+v", r)
	}

	// An agent with no reader answers with a note, not a failure.
	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "codex", State: "idle"})
	r = &promptsResponder{}
	o.PanePrompts(r, rt.id, 5)
	if !r.done || r.fail != "" || !strings.Contains(r.res.Note, "codex") {
		t.Fatalf("codex pane: %+v", r)
	}

	r = &promptsResponder{}
	o.PanePrompts(r, 9999, 5)
	if r.fail == "" {
		t.Fatal("an unknown pane should fail")
	}
}

// A remote pane's history is on its own machine; reading by cwd here would land
// on a same-named local project, so it answers with a note and reads nothing.
func TestPanePromptsSkipsRemotePanes(t *testing.T) {
	o, _, remotePane, _, _ := twoHostOrch(t)
	o.modelRoots = map[string]string{"claude": t.TempDir()}
	rt := o.panes[remotePane]
	rt.cwd = "/srv/app"
	o.onPaneAgent(orchestration.PaneAgent{PaneID: remotePane, Agent: "claude", State: "idle"})

	r := &promptsResponder{}
	o.PanePrompts(r, remotePane, 5)
	if !r.done || r.fail != "" || !strings.Contains(r.res.Note, "another host") {
		t.Fatalf("remote pane: %+v", r)
	}
}
