//go:build ghostty

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/layout"
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

// assistantUsage is an assistant record carrying the API's usage block, split
// across the three input buckets the way a cached request reports them.
func assistantUsage(model, effort string, input, cacheWrite, cacheRead int64) string {
	return `{"type":"assistant","isSidechain":false,"effort":"` + effort +
		`","message":{"role":"assistant","model":"` + model + `","usage":{` +
		`"input_tokens":` + itoa(input) +
		`,"cache_creation_input_tokens":` + itoa(cacheWrite) +
		`,"cache_read_input_tokens":` + itoa(cacheRead) +
		`,"output_tokens":9999}}}`
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// The context segment sums all three input buckets (and not the output), and
// comes from the same last main-thread record as the model — a sub-agent's
// usage is its own context, not the pane's.
func TestLastAssistantModelCarriesContext(t *testing.T) {
	projects := t.TempDir()
	path := writeTranscript(t, projects, "-p", "s.jsonl", 0,
		assistantUsage("claude-opus-5-5", "high", 2, 3635, 39935),
		`{"type":"assistant","isSidechain":true,"message":{"model":"claude-haiku-4-5","usage":{"input_tokens":150000}}}`)
	if got, want := lastAssistantModel(path), "claude-opus-5-5 · high · 44k/1M"; got != want {
		t.Fatalf("model = %q, want %q", got, want)
	}

	// No usable effort still yields the context segment, with one separator.
	noEffort := writeTranscript(t, projects, "-p", "ne.jsonl", 0,
		assistantUsage("claude-haiku-4-5", "", 800, 0, 0))
	if got, want := lastAssistantModel(noEffort), "claude-haiku-4-5 · 800/200k"; got != want {
		t.Fatalf("no effort: model = %q, want %q", got, want)
	}
}

func TestClaudeContextWindow(t *testing.T) {
	for _, tc := range []struct {
		model string
		used  int64
		want  int64
	}{
		{"claude-opus-5-5", 1, 1_000_000},
		{"claude-fable-5-1", 1, 1_000_000},
		{"claude-sonnet-5", 1, 1_000_000},
		{"claude-opus-4-8", 1, 1_000_000},
		{"claude-sonnet-4-6", 1, 1_000_000},
		{"claude-opus-4-5-20251101", 1, 200_000},
		{"claude-sonnet-4-5-20250929", 1, 200_000},
		{"claude-haiku-4-5", 1, 200_000},
		{"claude-3-5-sonnet-20241022", 1, 200_000},
		// The 1M opt-in, named outright or given away by the request's size.
		{"claude-sonnet-4-5-20250929[1m]", 1, 1_000_000},
		{"claude-sonnet-4-5-20250929", 250_000, 1_000_000},
		// A model newer than the table follows the current line.
		{"claude-opus-6", 1, 1_000_000},
	} {
		if got := claudeContextWindow(tc.model, tc.used); got != tc.want {
			t.Errorf("claudeContextWindow(%q, %d) = %d, want %d", tc.model, tc.used, got, tc.want)
		}
	}
}

func TestCompactTokens(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1k"},
		{43_572, "44k"},
		{999_400, "999k"},
		{999_600, "1M"}, // rounds across the boundary, never "1000k"
		{1_000_000, "1M"},
		{1_240_000, "1.2M"},
		{200_000, "200k"},
	} {
		if got := compactTokens(tc.n); got != tc.want {
			t.Errorf("compactTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
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
	o.modelRoots["claude"] = projects
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
	o.modelRoots["claude"] = projects
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
		t.Fatalf("rollup model = %q for an unreadable agent, want empty", got)
	}
}

// --- copilot ------------------------------------------------------------------

// writeCopilotSession lays down one copilot session directory — the workspace.yaml
// header naming the directory the session started in, plus events.jsonl — and
// stamps the event file's mtime, since that (not the directory's) is what "most
// recently active" is judged on.
func writeCopilotSession(t *testing.T, root, id, cwd string, age time.Duration, events ...string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ws := "id: " + id + "\ncwd: " + cwd + "\nclient_name: github/cli\n"
	if err := os.WriteFile(filepath.Join(dir, copilotWorkspaceFile), []byte(ws), 0o644); err != nil {
		t.Fatalf("write workspace: %v", err)
	}
	path := filepath.Join(dir, copilotEventsFile)
	if err := os.WriteFile(path, []byte(strings.Join(events, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return dir
}

func copilotAssistant(model string) string {
	return `{"type":"assistant.message","data":{"model":"` + model + `","content":""}}`
}

// copilotModelChange is what an explicitly chosen model writes: the effort rides
// on the change event itself.
func copilotModelChange(model, effort string) string {
	return `{"type":"session.model_change","data":{"newModel":"` + model +
		`","reasoningEffort":"` + effort + `"}}`
}

// copilotAutoChange is what auto mode writes instead — no real model, and a null
// effort, both of which the router supplies afterwards.
func copilotAutoChange() string {
	return `{"type":"session.model_change","data":{"newModel":"auto","reasoningEffort":null}}`
}

func copilotAutoResolved(model, bucket string) string {
	return `{"type":"session.auto_mode_resolved","data":{"chosenModel":"` + model +
		`","reasoningBucket":"` + bucket + `","candidateModels":["` + model + `"]}}`
}

// A hook-reported session id names the session directory outright, wherever
// copilot started it — the pane's cwd does not have to agree.
func TestCopilotModelBySessionID(t *testing.T) {
	root := t.TempDir()
	writeCopilotSession(t, root, "sess-1", "/elsewhere", 0,
		`{"type":"user.message","data":{"content":"hi"}}`,
		copilotAssistant("claude-haiku-4.5"))

	if got := copilotModel(root, "/Users/x/proj", "sess-1"); got != "claude-haiku-4.5" {
		t.Fatalf("model = %q, want claude-haiku-4.5", got)
	}
}

// Without a session id the pane's cwd picks the session and the most recently
// active one wins. Sessions started elsewhere are never candidates, because
// copilot records the directory it started in.
func TestCopilotModelByCwd(t *testing.T) {
	root := t.TempDir()
	cwd := "/Users/x/projs/lezen"
	writeCopilotSession(t, root, "old", cwd, time.Hour, copilotAssistant("gpt-5-mini"))
	writeCopilotSession(t, root, "new", cwd, time.Minute, copilotAssistant("claude-sonnet-4.6"))
	writeCopilotSession(t, root, "other", "/Users/x/projs/elsewhere", 0, copilotAssistant("gpt-5.4"))

	if got := copilotModel(root, cwd, ""); got != "claude-sonnet-4.6" {
		t.Fatalf("model = %q, want the newest matching session's claude-sonnet-4.6", got)
	}
}

// Where the effort comes from depends on the session's mode, so both spellings
// have to land, and neither may invent one when the session does not say.
func TestCopilotModelEffort(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
		want   string
	}{
		{
			"explicit model reports it on the change",
			[]string{copilotModelChange("gpt-5.4", "medium"), copilotAssistant("gpt-5.4")},
			"gpt-5.4 · medium",
		},
		{
			"auto mode reports it as the router's bucket",
			[]string{copilotAutoChange(), copilotAutoResolved("claude-haiku-4.5", "low"),
				copilotAssistant("claude-haiku-4.5")},
			"claude-haiku-4.5 · low",
		},
		{
			"no usable effort leaves the model unadorned",
			[]string{copilotAutoChange(), copilotAssistant("claude-haiku-4.5")},
			"claude-haiku-4.5",
		},
		{
			// The newest effort wins even when it postdates the last answer: it is
			// what the next turn will run under, which is what a row read between
			// turns should say.
			"a switch after the last answer still counts",
			[]string{copilotModelChange("gpt-5.4", "low"), copilotAssistant("gpt-5.4"),
				copilotModelChange("gpt-5.4", "high")},
			"gpt-5.4 · high",
		},
		{
			// The model always comes from the answer, never from the configured
			// model, which in auto mode is the literal string "auto".
			"the model comes from the answer, not the setting",
			[]string{copilotAutoChange(), copilotAssistant("gpt-5-mini")},
			"gpt-5-mini",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeCopilotSession(t, root, "s", "/Users/x/proj", 0, tc.events...)
			if got := copilotModel(root, "/Users/x/proj", "s"); got != tc.want {
				t.Fatalf("model = %q, want %q", got, tc.want)
			}
		})
	}
}

// A session long past the tail budget still resolves: only the tail is read, the
// partial record it opens on is dropped rather than failing the whole parse, and
// an answer left behind off the end cannot win.
func TestCopilotModelBeyondTailBudget(t *testing.T) {
	root := t.TempDir()
	filler := `{"type":"tool.execution_start","data":{"name":"bash","command":"` +
		strings.Repeat("x", 4096) + `"}}`
	events := []string{copilotAssistant("gpt-5.4")}
	for len(events)*len(filler) < modelTailBytes*2 {
		events = append(events, filler)
	}
	events = append(events, copilotAssistant("claude-sonnet-4.6"))
	writeCopilotSession(t, root, "s", "/Users/x/proj", 0, events...)

	if got := copilotModel(root, "/Users/x/proj", "s"); got != "claude-sonnet-4.6" {
		t.Fatalf("model = %q, want claude-sonnet-4.6", got)
	}
}

// A session that has not answered yet resolves to no model rather than to an
// invented one — the ordinary state of a pane in its first turn.
func TestCopilotModelNoAnswerYet(t *testing.T) {
	root := t.TempDir()
	writeCopilotSession(t, root, "s", "/Users/x/proj", 0,
		`{"type":"session.start","data":{"sessionId":"s","producer":"copilot-agent"}}`,
		copilotAutoChange(),
		`{"type":"user.message","data":{"content":"hi"}}`)

	if got := copilotModel(root, "/Users/x/proj", "s"); got != "" {
		t.Fatalf("model = %q, want empty", got)
	}
}

// The session ref becomes a path element, so it is gated before it is joined onto
// the root: a corrupted or hostile ref must not walk out of the session tree.
func TestCopilotSessionRejectsBadRefs(t *testing.T) {
	root := t.TempDir()
	writeCopilotSession(t, root, "s", "/Users/x/proj", 0, copilotAssistant("gpt-5.4"))

	for _, ref := range []string{"../s", "..", "s/../s", "*", "/etc"} {
		if got := copilotSession(root, "", ref); got != "" {
			t.Fatalf("copilotSession(ref=%q) = %q, want empty", ref, got)
		}
	}
}

// copilot quotes the cwd only when the path forces it, so both spellings have to
// resolve — and only the top-level key counts, so a quoted value that happens to
// contain "cwd:" cannot be mistaken for one.
func TestCopilotWorkspaceCwd(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ yaml, want string }{
		{"id: s\ncwd: /Users/x/proj\nname: hi\n", "/Users/x/proj"},
		{"cwd: '/Users/x/it: proj'\n", "/Users/x/it: proj"},
		{"cwd: \"/Users/x/proj\"\n", "/Users/x/proj"},
		{"cwd: '/Users/x/o''brien'\n", "/Users/x/o'brien"},
		{"cwd: /Users/x/proj\r\n", "/Users/x/proj"},
		{"id: s\nname: 'cwd: not this'\n", ""},
		{"  cwd: /nested\n", ""},
		{"id: s\n", ""},
	} {
		path := filepath.Join(dir, copilotWorkspaceFile)
		if err := os.WriteFile(path, []byte(tc.yaml), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := copilotWorkspaceCwd(path); got != tc.want {
			t.Fatalf("cwd for %q = %q, want %q", tc.yaml, got, tc.want)
		}
	}
}

// The rollup names a copilot pane by its model just as it does a claude one: the
// resolver table, not a claude special case, is what decides who can be read.
func TestAgentsRollupCarriesCopilotModel(t *testing.T) {
	o, rt, _ := hookOrch(t)
	root := t.TempDir()
	o.modelRoots["copilot"] = root
	rt.cwd = "/Users/x/proj"
	writeCopilotSession(t, root, "sess-1", rt.cwd, 0,
		copilotAutoChange(), copilotAutoResolved("claude-haiku-4.5", "low"),
		copilotAssistant("claude-haiku-4.5"))

	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "copilot", State: "working"})
	waitFor(t, o, func() bool { return rt.agentModel != "" }) // the read is off-loop
	if got := rollupItem(t, o, rt.id).Model; got != "claude-haiku-4.5 · low" {
		t.Fatalf("rollup model = %q, want claude-haiku-4.5 · low", got)
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

// The model readers walk this machine's agent state (~/.claude and friends),
// keyed by the pane's cwd. A pane on another host has neither there — the
// transcripts live beside the agent — and the cwd-slug match would happily land
// on a same-named project here and report a model from someone else's session.
// So a remote pane carries no model, and drops one it was holding.
func TestRefreshAgentModelSkipsRemotePanes(t *testing.T) {
	o, localPane, remotePane, _, _ := twoHostOrch(t)
	projects := t.TempDir()
	o.modelRoots = map[string]string{"claude": projects}

	remote := o.panes[remotePane]
	remote.cwd, remote.agentModel = "/srv/app", "opus"
	o.refreshAgentModel(remote, "claude")
	if remote.agentModel != "" {
		t.Fatalf("remote pane model = %q, want it cleared", remote.agentModel)
	}
	if remote.modelBusy {
		t.Fatal("a remote pane must not start a transcript read")
	}

	local := o.panes[localPane]
	local.cwd = t.TempDir()
	o.refreshAgentModel(local, "claude")
	if !local.modelBusy {
		t.Fatal("a local pane should have started a transcript read")
	}
}

// --- the pane a transcript belongs to -----------------------------------------

// Two panes running claude in one repository share a working directory, so the
// cwd fallback resolves both to whichever transcript was written last: one pane's
// model on both rows, and both flipping together whenever either pane switches
// models. The session id the host traces off each pane's own process is what
// separates them.
func TestDetectedSessionSeparatesPanesSharingACwd(t *testing.T) {
	o, first, _ := hookOrch(t)
	id, err := o.session.SplitPane(nil, layout.Horizontal)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	o.syncDaemon()
	second := o.panes[uint32(id)]
	if second == nil {
		t.Fatal("the split pane has no runtime")
	}

	projects := t.TempDir()
	o.modelRoots["claude"] = projects
	cwd := "/Users/x/proj"
	first.cwd, second.cwd = cwd, cwd
	writeTranscript(t, projects, "-Users-x-proj", "sess-a.jsonl", time.Minute, assistant("claude-fable-5"))
	writeTranscript(t, projects, "-Users-x-proj", "sess-b.jsonl", 0, assistant("claude-opus-5"))

	// Before the host says anything, both panes can only guess — and they guess
	// the same, which is the bug this exists to fix.
	o.onPaneAgent(orchestration.PaneAgent{PaneID: first.id, Agent: "claude", State: "working"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: second.id, Agent: "claude", State: "working"})
	waitFor(t, o, func() bool { return first.agentModel != "" && second.agentModel != "" })
	if first.agentModel != "claude-opus-5" || second.agentModel != "claude-opus-5" {
		t.Fatalf("cwd fallback: models = %q / %q, want the newest transcript on both",
			first.agentModel, second.agentModel)
	}

	// The host traces each pane's process to its own conversation.
	o.applyPaneAgentSession(orchestration.PaneAgentSession{PaneID: first.id, Agent: "claude", SessionID: "sess-a"})
	o.applyPaneAgentSession(orchestration.PaneAgentSession{PaneID: second.id, Agent: "claude", SessionID: "sess-b"})
	waitFor(t, o, func() bool { return first.agentModel == "claude-fable-5" })
	if second.agentModel != "claude-opus-5" {
		t.Fatalf("second pane model = %q, want claude-opus-5", second.agentModel)
	}
	// And the rollup — the sidebar's agents list — names the rows apart.
	if got := rollupItem(t, o, first.id).Model; got != "claude-fable-5" {
		t.Fatalf("rollup model = %q, want claude-fable-5", got)
	}

	// A new conversation in the same pane is a new id, and the model moves with
	// it without waiting on the periodic refresh.
	writeTranscript(t, projects, "-Users-x-proj", "sess-c.jsonl", 2*time.Hour, assistant("claude-sonnet-5"))
	o.applyPaneAgentSession(orchestration.PaneAgentSession{PaneID: first.id, Agent: "claude", SessionID: "sess-c"})
	waitFor(t, o, func() bool { return first.agentModel == "claude-sonnet-5" })
}

// What the host reports is gated on this end too: an id that is not a bare token
// would travel into a glob pattern, and a report naming no agent (the agent left
// the pane) retracts the identity rather than leaving the pane keyed on it.
func TestDetectedSessionRejectsUnusableRefs(t *testing.T) {
	o, rt, _ := hookOrch(t)
	projects := t.TempDir()
	o.modelRoots["claude"] = projects
	rt.cwd = "/Users/x/proj"
	writeTranscript(t, projects, "-Users-x-proj", "cwd.jsonl", 0, assistant("claude-opus-5"))
	writeTranscript(t, projects, "-elsewhere", "sess-a.jsonl", 0, assistant("claude-fable-5"))

	for _, bad := range []orchestration.PaneAgentSession{
		{PaneID: rt.id, Agent: "claude", SessionID: "../../etc/passwd"},
		{PaneID: rt.id, Agent: "claude", SessionID: "sess-*"},
		{PaneID: rt.id, Agent: "claude", SessionID: ""},
		{PaneID: rt.id, Agent: "", SessionID: "sess-a"},
	} {
		o.applyPaneAgentSession(bad)
		if rt.detectedSession != nil {
			t.Fatalf("%+v: kept ref %+v, want none", bad, rt.detectedSession)
		}
	}

	// A ref for an agent that is not the one running is not used either: the id
	// names a history only its own agent wrote.
	o.applyPaneAgentSession(orchestration.PaneAgentSession{PaneID: rt.id, Agent: "copilot", SessionID: "sess-a"})
	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "working"})
	waitFor(t, o, func() bool { return rt.agentModel != "" })
	if rt.agentModel != "claude-opus-5" {
		t.Fatalf("model = %q, want the cwd fallback's claude-opus-5", rt.agentModel)
	}
}

// A read is in flight against the identity the pane had when it started. If the
// pane's conversation moves under it — the ordinary case, since the host's report
// is what corrects a cwd guess — the answer that comes back names a history that
// is no longer this pane's, and publishing it would leave the wrong model up
// until the next sweep.
func TestModelReadOvertakenByAnIdentityChangeIsDiscarded(t *testing.T) {
	o, rt, _ := hookOrch(t)
	projects := t.TempDir()
	o.modelRoots["claude"] = projects
	rt.cwd = "/Users/x/proj"
	writeTranscript(t, projects, "-Users-x-proj", "sess-a.jsonl", 0, assistant("claude-opus-5"))
	writeTranscript(t, projects, "-elsewhere", "sess-b.jsonl", 0, assistant("claude-fable-5"))

	o.onPaneAgent(orchestration.PaneAgent{PaneID: rt.id, Agent: "claude", State: "working"})
	waitFor(t, o, func() bool { return rt.agentModel != "" })

	// A read in flight, and the host names the pane's real conversation while it
	// is out.
	rt.modelBusy = true
	o.applyPaneAgentSession(orchestration.PaneAgentSession{PaneID: rt.id, Agent: "claude", SessionID: "sess-b"})
	if !rt.modelDirty {
		t.Fatal("an identity change during a read must mark the read stale")
	}

	// The overtaken read lands: its answer is dropped, and the pane re-reads.
	o.setAgentModel(rt.id, "claude-sonnet-5")
	if rt.agentModel == "claude-sonnet-5" {
		t.Fatal("published the overtaken read's model")
	}
	waitFor(t, o, func() bool { return rt.agentModel == "claude-fable-5" })
}

// A changed sweep period wakes the sweep goroutine (so a shorter one applies
// now, not after the old wait), an unchanged one does not, and nudges never
// pile up — the goroutine re-reads the period on waking, so one is enough.
func TestSetModelSweepNudgesOnChangeOnly(t *testing.T) {
	o := &orch{modelSweepNudge: make(chan struct{}, 1)}
	o.modelSweep.Store(int64(time.Minute))
	nudged := func() bool {
		select {
		case <-o.modelSweepNudge:
			return true
		default:
			return false
		}
	}

	o.setModelSweep(time.Minute)
	if nudged() {
		t.Fatal("unchanged period nudged the sweep")
	}
	o.setModelSweep(2 * time.Minute)
	o.setModelSweep(0) // a second change before the goroutine wakes must not block
	if !nudged() || nudged() {
		t.Fatal("want exactly one pending nudge after two changes")
	}
	if got := time.Duration(o.modelSweep.Load()); got != 0 {
		t.Fatalf("period = %v, want 0 (off)", got)
	}
}
