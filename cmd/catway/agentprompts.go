//go:build ghostty

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rohanthewiz/cats/internal/app"
)

// pane.prompts: the last few prompts a pane's coding agent was sent, for the
// pane header's "recent prompts" popover.
//
// The answer is read from the agent's own history — the same file
// agentmodel.go reads the model from, located the same way (hook session id,
// then detected session id, then newest-under-cwd) — rather than recorded from
// the keystrokes catway forwards. That choice is the feature:
//
//	keystrokes   what was TYPED: every edit, every ↑-recall, every line
//	             abandoned with Esc, and nothing at all for a prompt that
//	             arrived by paste, by pane.send_input, or before catway started.
//	history      what the agent RECEIVED, from whoever sent it, already split
//	             into prompts, and there the first time anyone asks.
//
// It is a pull, not a push. The model rides the agents rollup because every
// sidebar row shows it all the time; prompts are looked at occasionally, on
// one pane, so reading them on demand costs nothing until someone opens the
// popover and is always current when they do.
//
//	browser ──pane.prompts──▶ dispatcher ──PanePrompts──▶ orch (loop)
//	                                                       │ resolve agent,
//	                                                       │ root, session, cwd
//	                                                       ▼
//	                                            goroutine: tail-read history
//	                                                       │
//	browser ◀──cmd_result── r.OK ◀──o.post────────────────┘

// promptTailBytes bounds the history read. A prompt is followed by a whole
// turn of tool calls and tool results — a single file read can be tens of KB —
// so the 256 KiB the model read uses would often hold only the newest prompt.
// 8 MiB covers several long turns, and the read only happens when someone opens
// the popover, off the loop goroutine. A history whose last n prompts lie
// further back than this answers with fewer than n.
const promptTailBytes = 8 << 20

// maxPromptRunes bounds one prompt's text in the reply. A pasted spec can run
// to tens of KB, and the popover shows a few lines of each; the cut keeps one
// giant paste from making the whole reply heavy, and is marked with "…" so it
// never reads as the complete prompt.
const maxPromptRunes = 4000

// PanePrompts implements app.Backend: answer pane.prompts.
//
// Every "cannot answer" case is an OK with a Note rather than a Fail: a pane
// running an agent cats has no reader for is an ordinary pane, and the popover
// should say why it is empty rather than show an error.
func (o *orch) PanePrompts(r app.Responder, pane uint32, limit int) {
	rt := o.panes[pane]
	if rt == nil {
		r.Fail(fmt.Sprintf("unknown pane %d", pane))
		return
	}
	agent, _ := rt.effectiveAgent()
	res := app.PanePromptsResult{Pane: pane, Agent: agent, Prompts: []app.AgentPrompt{}}
	resolver, known := modelResolvers[agent]
	root := o.modelRoots[agent]
	switch {
	case agent == "":
		res.Note = "no coding agent is running in this pane"
	// Checked before the reader, for the reason refreshAgentModel gives: a
	// remote pane's history is on its own machine, and the cwd fallback would
	// happily read a same-named project's history here instead.
	case !o.paneIsLocal(pane):
		res.Note = agent + " is running on another host, and its history is kept there"
	case !known || resolver.prompts == nil:
		res.Note = "cats cannot read " + agent + "'s prompt history"
	case root == "":
		res.Note = agent + "'s history directory was not found on this machine"
	}
	if res.Note != "" {
		r.OK(res)
		return
	}

	// Everything the read needs is copied out of rt here: rt is loop-owned,
	// and the goroutine below must not touch it.
	cwd, session, read := rt.cwd, rt.historySession(agent), resolver.prompts
	go func() {
		prompts := read(root, cwd, session, limit)
		o.post(func() {
			if len(prompts) > 0 {
				res.Prompts = prompts
			} else {
				res.Note = "no prompts found in " + agent + "'s history for this pane"
			}
			r.OK(res)
		})
	}()
}

// clipPrompt trims a prompt for the reply and caps its length (maxPromptRunes).
func clipPrompt(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxPromptRunes {
		return string(r[:maxPromptRunes]) + "…"
	}
	return s
}

// --- claude (no orch state; runs off the loop goroutine) ----------------------

// claudePrompts is the last n prompts in the pane's claude transcript, newest
// first; nil when no transcript can be pinned down.
func claudePrompts(projects, cwd, session string, n int) []app.AgentPrompt {
	path := claudeTranscript(projects, cwd, session)
	if path == "" {
		return nil
	}
	return lastClaudePrompts(path, n)
}

// claudeUserRecord is the slice of a transcript line a prompt is read from.
//
// Content is kept raw because claude writes it in two shapes: a bare string
// for a typed prompt, and an array of content blocks for anything carrying an
// image — and for tool results, which are "user" records too.
type claudeUserRecord struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Timestamp   string `json:"timestamp"`
	// Origin names who sent the record. Current claude stamps typed prompts
	// "human" and stamps what the harness injected as a user turn — a
	// background task finishing, a message from another session — with a kind
	// of its own. Older transcripts carry no origin at all, which is why its
	// absence admits a record rather than refusing it.
	Origin struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// claudeContentBlock is one block of an array-shaped content.
type claudeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// lastClaudePrompts walks the transcript tail backwards collecting prompts.
func lastClaudePrompts(path string, n int) []app.AgentPrompt {
	lines := tailLinesN(path, promptTailBytes)
	var out []app.AgentPrompt
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		// Cheap gate before the decode: most of a transcript is assistant
		// records, and none of those can match. It looks for the quoted value
		// alone rather than `"type":"user"`, so it does not depend on how the
		// writer spaces its JSON — claude writes compact JSON today, but a gate
		// that silently fails on `"type": "user"` would empty the list with no
		// sign of why. The decode decides; this only skips lines it would
		// reject.
		if !bytes.Contains(lines[i], []byte(`"user"`)) {
			continue
		}
		var rec claudeUserRecord
		if err := json.Unmarshal(lines[i], &rec); err != nil {
			continue
		}
		if text, ok := claudePromptText(rec); ok {
			out = append(out, app.AgentPrompt{Text: clipPrompt(text), At: rec.Timestamp})
		}
	}
	return out
}

// claudePromptText turns one user record into the prompt it carries, or
// reports false for a user record that is not a prompt.
//
// What claude records as a "user" turn is much more than prompts, and each
// exclusion below is one of those:
//
//	isSidechain           a sub-agent's conversation, not the pane's
//	isMeta                text the harness injected: a skill's expanded body,
//	                      image dimension notes, the local-command caveat
//	origin.kind ≠ human   a background task's notification, another session's
//	                      message
//	tool_result blocks    the answer to a tool call
//	<local-command-…>     the output of a slash command claude ran itself
//	<bash-stdout> etc.    the output of a "!" shell escape
//	[Request interrupted  the marker claude writes on Esc
//
// The two that are kept but respelled are a slash command and a "!" shell
// escape, which claude stores as markup: they are shown the way they were
// typed, since that is what the user would recognise and could re-send.
func claudePromptText(rec claudeUserRecord) (string, bool) {
	if rec.Type != "user" || rec.IsSidechain || rec.IsMeta {
		return "", false
	}
	if k := rec.Origin.Kind; k != "" && k != "human" {
		return "", false
	}
	text, ok := claudeContentText(rec.Message.Content)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", false
	case strings.HasPrefix(text, "<command-name>"), strings.HasPrefix(text, "<command-message>"):
		return claudeSlashCommand(text)
	case strings.HasPrefix(text, "<bash-input>"):
		if cmd := tagText(text, "bash-input"); cmd != "" {
			return "! " + cmd, true
		}
		return "", false
	case strings.HasPrefix(text, "<local-command-"),
		strings.HasPrefix(text, "<bash-stdout>"),
		strings.HasPrefix(text, "<bash-stderr>"),
		strings.HasPrefix(text, "<task-notification>"),
		strings.HasPrefix(text, "<system-reminder>"),
		strings.HasPrefix(text, "[Request interrupted"):
		return "", false
	}
	return text, true
}

// claudeContentText flattens a record's content to text. An array is joined
// text block by text block, an image standing in as "[image]" so a prompt that
// was a screenshot plus a sentence does not read as just the sentence. Any
// tool_result block marks the record as a tool answer, not a prompt.
func claudeContentText(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", false
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", false
		}
		return s, true
	case '[':
		var blocks []claudeContentBlock
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return "", false
		}
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				return "", false
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					parts = append(parts, t)
				}
			case "image":
				parts = append(parts, "[image]")
			}
		}
		return strings.Join(parts, "\n"), true
	}
	return "", false
}

// claudeSlashCommand respells claude's slash-command markup
//
//	<command-name>/model</command-name>
//	<command-message>model</command-message>
//	<command-args>opus</command-args>
//
// as what was typed: "/model opus". The name carries its own slash in current
// transcripts; one is added when it does not, so the two spellings read alike.
func claudeSlashCommand(text string) (string, bool) {
	name := tagText(text, "command-name")
	if name == "" {
		return "", false
	}
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	if args := tagText(text, "command-args"); args != "" {
		return name + " " + args, true
	}
	return name, true
}

// tagText is the trimmed text between <tag> and </tag>, "" when the pair is not
// there. It is a substring search rather than an XML parse because the values
// are free text the user typed — an argument holding "<" or "&" is not escaped
// in the transcript, and a parser would reject it.
func tagText(s, tag string) string {
	open, end := "<"+tag+">", "</"+tag+">"
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

// --- copilot (no orch state; runs off the loop goroutine) ---------------------

// copilotUserEvent is the events.jsonl type a prompt is recorded under.
const copilotUserEvent = "user.message"

// copilotPrompts is the last n prompts in the pane's copilot session, newest
// first; nil when no session can be pinned down.
func copilotPrompts(root, cwd, session string, n int) []app.AgentPrompt {
	dir := copilotSession(root, cwd, session)
	if dir == "" {
		return nil
	}
	return lastCopilotPrompts(filepath.Join(dir, copilotEventsFile), n)
}

// copilotPromptEvent is the slice of a user.message event a prompt is read
// from. Content is what the user sent; copilot also records a transformed
// copy with the context it injected, which is the agent's business, not the
// prompt.
type copilotPromptEvent struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Data      struct {
		Content string `json:"content"`
	} `json:"data"`
}

// lastCopilotPrompts walks the event log tail backwards collecting prompts.
func lastCopilotPrompts(path string, n int) []app.AgentPrompt {
	lines := tailLinesN(path, promptTailBytes)
	var out []app.AgentPrompt
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		// Cheap gate, as in lastClaudePrompts: the event type is a quoted
		// value, so a line without it cannot be one.
		if !bytes.Contains(lines[i], []byte(`"`+copilotUserEvent+`"`)) {
			continue
		}
		var ev copilotPromptEvent
		if err := json.Unmarshal(lines[i], &ev); err != nil || ev.Type != copilotUserEvent {
			continue
		}
		if text := clipPrompt(ev.Data.Content); text != "" {
			out = append(out, app.AgentPrompt{Text: text, At: ev.Timestamp})
		}
	}
	return out
}
