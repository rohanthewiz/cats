//go:build ghostty

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/rohanthewiz/cats/internal/detect"
)

// Blank conversations: an agent pane whose current conversation has had
// nothing asked of it yet — a fresh launch, or a /clear — sitting at its prompt.
//
// To the state machine such a pane is just "idle", the same as one that has
// finished a long turn and is waiting for the next. The two mean different
// things at a glance: one has context and answers worth reading, the other is a
// blank page. So the sidebar draws a blank one with a dot of its own (light
// brown, theme key "blank"), and says which kind of blank it is: "new" or
// "cleared", from the session origin (detect.SessionOrigin*).
//
// Blankness is read from the agent's own history, in the same background read
// that resolves the model (refreshAgentModel), and only for a conversation that
// is pinned to an id — never off the cwd fallback, which may be a neighbour's
// history. It is cleared without a read the moment the pane goes working
// (publishAgent): an agent working is an agent that was asked something, and
// the throttled re-read would otherwise leave the dot up for a whole turn.
//
//	session moves ──▶ read: blank? ──yes──▶ rt.blank = "new"|"cleared" ──▶ ● light brown
//	                        │                     │
//	                        no                pane goes working
//	                        ▼                     ▼
//	               rt.nonBlankSession = id    rt.blank = ""  (next read confirms)
//
// A conversation only ever grows, so once one has been seen non-blank it is
// never read for blankness again (nonBlankSession) — the blank read is a whole-
// file scan, cheap only because a blank history is a few KB.

// blankMaxBytes is the largest history that can still be blank. A conversation
// with nothing asked holds a handful of bookkeeping records; anything bigger
// has had a turn, and is not read.
const blankMaxBytes = 1 << 20

// Display words for a blank conversation, as AgentItem.Blank / PaneAgent.Blank
// carry them.
const (
	blankNew     = "new"
	blankCleared = "cleared"
)

// blankWord names a blank conversation by how it began: "cleared" for a /clear,
// "new" for everything else — a fresh launch, or a start that could not be told.
func blankWord(origin string) string {
	if origin == detect.SessionOriginClear {
		return blankCleared
	}
	return blankNew
}

// resolveBlank turns a reader's answer into the pane's blank word. A history
// that exists says for itself. A missing one is blank only when the
// conversation is known to have just begun (startup or clear): claude writes its
// transcript lazily, so a fresh conversation may have no file yet — but a
// missing file is also what a history kept somewhere this machine does not look
// (another CLAUDE_CONFIG_DIR) looks like, and that must not read as blank
// forever.
func resolveBlank(blank, found bool, origin string) string {
	switch {
	case found && blank:
		return blankWord(origin)
	case !found && (origin == detect.SessionOriginStartup || origin == detect.SessionOriginClear):
		return blankWord(origin)
	}
	return ""
}

// readSmallHistory reads a history file whole when it is small enough to be
// blank. found reports whether the file exists at all; nil content with found
// set means it is too big to be blank.
func readSmallHistory(path string) (content []byte, found bool) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return nil, false
	}
	if fi.Size() > blankMaxBytes {
		return nil, true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return b, true
}

// --- claude (no orch state; runs off the loop goroutine) ----------------------

// claudeBlank reports whether claude's transcript for session holds no turn:
// no main-chain assistant record (the model never answered anything) and no
// prompt addressed to the model. Slash commands and "!" shell escapes are not
// such prompts — /clear itself opens every cleared transcript, and /model or
// "! ls" run locally without a turn. A skill invoked as a slash command does
// reach the model, and its answer is an assistant record, which is what
// catches it.
func claudeBlank(projects, session string) (blank, found bool) {
	// cwd "" pins the lookup to the id: blankness must never be read off the
	// newest-under-cwd guess (see the file comment).
	path := claudeTranscript(projects, "", session)
	if path == "" {
		return false, false
	}
	b, found := readSmallHistory(path)
	if b == nil {
		return false, found
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		var rec claudeUserRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		switch rec.Type {
		case "assistant":
			if !rec.IsSidechain {
				return false, true
			}
		case "user":
			if text, ok := claudeContentText(rec.Message.Content); ok {
				t := strings.TrimSpace(text)
				if strings.HasPrefix(t, "<command-") || strings.HasPrefix(t, "<bash-input>") {
					continue
				}
			}
			if _, ok := claudePromptText(rec); ok {
				return false, true
			}
		}
	}
	return true, true
}

// --- copilot (no orch state; runs off the loop goroutine) ---------------------

// copilotBlank reports whether copilot's session holds neither a prompt nor an
// answer. Its event log opens with session bookkeeping either way; the first
// user.message is the first thing anyone asked.
func copilotBlank(root, session string) (blank, found bool) {
	dir := copilotSession(root, "", session)
	if dir == "" {
		return false, false
	}
	b, found := readSmallHistory(filepath.Join(dir, copilotEventsFile))
	if b == nil {
		return false, found
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Type == copilotUserEvent || ev.Type == "assistant.message" {
			return false, true
		}
	}
	return true, true
}
