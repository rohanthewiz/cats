package detect

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// How the conversation a running agent is in came to be.
//
// A pane's session id changing says "a different conversation now", and nothing
// about why. The three answers look different to a user and call for different
// things downstream (a cleared pane has thrown its history away; a resumed one
// has picked an old history back up; a fresh one has none yet):
//
//	startup  a new agent process with a new conversation
//	clear    the running agent dropped its conversation for a fresh one (/clear)
//	resume   an earlier conversation picked back up, in a new process
//	         (`claude --resume`, `--continue`) or inside the running one (/resume)
//
// The vocabulary is claude's own: its SessionStart hook reports exactly these as
// `source`, plus "compact", which keeps the session id and so never starts a
// conversation — it is dropped (NormalizeSessionOrigin). The hook is the exact
// answer, but only where `catctl integration install` has been run. Everywhere
// else the answer is inferred from two things that are always on disk:
//
//   - the pid registry (AgentSessionOf), which says whether the process holding
//     the new conversation is the one that held the previous one, and when it
//     started;
//   - the head of the new conversation's own transcript, which says whether its
//     history predates the switch (a resume) and whether it opens with a /clear.
//
// The decision, in order — the first rule that holds wins:
//
//	history predates the switch          → resume
//	transcript opens with a /clear       → clear
//	same pid as the previous conversation → clear
//	otherwise                            → startup
//
// "The switch" is the latest moment the conversation is known to have begun
// after: for the same pid, the last time the previous id was still seen (the
// boundary fell between that read and this one); for a new pid, the process's
// start. A resumed history carries records from before either; a cleared or
// fresh one cannot.
//
// The /clear marker exists for the case where there is no previous observation
// to compare pids against — the host restarted, or the pane is seen for the first
// time — and a long-running process is found in a conversation it started with a
// /clear: its records postdate the process start, so without the marker it would
// read as a fresh launch.
const (
	SessionOriginStartup = "startup"
	SessionOriginClear   = "clear"
	SessionOriginResume  = "resume"
)

// NormalizeSessionOrigin passes the three origins through and maps everything
// else to "" (unknown). "compact" lands there on purpose: it is a SessionStart
// source, but the conversation it reports is the one already held, so it says
// nothing about how that conversation began and must not overwrite the answer.
func NormalizeSessionOrigin(s string) string {
	switch s {
	case SessionOriginStartup, SessionOriginClear, SessionOriginResume:
		return s
	}
	return ""
}

// originProbe classifies cur, a conversation that has just replaced prev (the
// zero value when there was none to compare with), last seen at prevSeen.
type originProbe func(cur, prev AgentSession, prevSeen time.Time) string

// originProbes mirrors sessionProbes: an agent whose conversations can be traced
// can, given a readable history, have their origins told apart too.
var originProbes = map[string]originProbe{
	"claude": claudeSessionOrigin,
}

// AgentSessionOrigin says how cur began, "" when the agent has no probe or cur
// names no conversation. Call it once per change of id, not per poll: it reads
// the head of a transcript, and the answer for a given id never changes.
//
// prev is the conversation the pane was last seen in (zero when none), and
// prevSeen the last time it was seen there.
func AgentSessionOrigin(agent string, cur, prev AgentSession, prevSeen time.Time) string {
	probe := originProbes[agent]
	if probe == nil || cur.ID == "" {
		return ""
	}
	return probe(cur, prev, prevSeen)
}

// AgentSessionBegan says when the pane entered cur, a conversation first seen at
// now that replaced prev (the zero value when there was none to compare with).
// It is the moment the conversation became the pane's, not the age of its
// history: a resumed conversation began in this pane when it was resumed, even
// though its first records are older.
//
//	same pid as prev  → now        the switch happened inside the running agent
//	                                (/clear, /resume), somewhere since the last
//	                                read — now is late by at most one poll
//	new pid, start known → StartedAt  a launch (fresh or `--resume`) enters its
//	                                conversation as the process starts
//	otherwise         → now        nothing better is known
//
// The process start is preferred over now for a launch because a host does not
// always see a launch promptly: the registry entry is written a moment after the
// process starts, and the session read is paced. Agnostic of the agent, unlike
// the origin: it reads only what AgentSessionOf already returned.
func AgentSessionBegan(cur, prev AgentSession, now time.Time) time.Time {
	samePID := prev.ID != "" && prev.ID != cur.ID && prev.PID == cur.PID && cur.PID > 0
	if !samePID && !cur.StartedAt.IsZero() && !cur.StartedAt.After(now) {
		return cur.StartedAt
	}
	return now
}

func claudeSessionOrigin(cur, prev AgentSession, prevSeen time.Time) string {
	samePID := prev.ID != "" && prev.ID != cur.ID && prev.PID == cur.PID && cur.PID > 0
	switchAt := cur.StartedAt
	if samePID && !prevSeen.IsZero() {
		switchAt = prevSeen
	}
	head := readClaudeTranscriptHead(cur.ID)
	switch {
	case !switchAt.IsZero() && !head.earliest.IsZero() && head.earliest.Before(switchAt):
		return SessionOriginResume
	case head.cleared, samePID:
		return SessionOriginClear
	}
	return SessionOriginStartup
}

// claudeTranscriptHead is what the first records of a transcript say about its
// beginning.
type claudeTranscriptHead struct {
	earliest time.Time // oldest record timestamp seen; zero when none (or no file)
	cleared  bool      // a /clear command record is among them
}

const (
	// transcriptHeadBytes bounds the head read. The records a /clear writes, and
	// the first timestamped record of any history, sit within the first few KB;
	// the rest of a transcript (megabytes) is never needed here.
	transcriptHeadBytes = 64 << 10
)

// clearCommandMarkers are the command records that open a conversation started
// by /clear. claude writes the command into the NEW session's transcript, as a
// user record whose content is `<command-name>/clear</command-name>…`; /reset
// and /new are its aliases and are accepted in case they log under their own name.
var clearCommandMarkers = []string{
	"<command-name>/clear</command-name>",
	"<command-name>/reset</command-name>",
	"<command-name>/new</command-name>",
}

// readClaudeTranscriptHead scans the head of the transcript for session id. A
// missing file is the zero value, which is a real answer: claude creates the file
// lazily, so a fresh conversation may have none yet — and one that has none
// certainly has no history from before the switch.
//
// The transcript lives under ~/.claude/projects/<slugified cwd>/. It is found by
// glob on the id rather than by recomputing the slug, so this does not depend on
// claude's slug rules; id has been through isSessionID, so it cannot widen the
// pattern.
func readClaudeTranscriptHead(id string) claudeTranscriptHead {
	dir := claudeConfigDir()
	if dir == "" || !isSessionID(id) {
		return claudeTranscriptHead{}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "projects", "*", id+".jsonl"))
	if len(matches) == 0 {
		return claudeTranscriptHead{}
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return claudeTranscriptHead{}
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, transcriptHeadBytes))
	if err != nil {
		return claudeTranscriptHead{}
	}
	return parseClaudeTranscriptHead(b)
}

// parseClaudeTranscriptHead reads complete JSONL records out of b. A last line
// cut off by the byte bound fails to decode and is skipped, like any other line
// that is not a record.
//
// The earliest timestamp is a minimum, not the first one seen: records are
// appended in roughly but not strictly time order (a /clear's caveat record is
// written a few ms after the command record it precedes in the file).
func parseClaudeTranscriptHead(b []byte) claudeTranscriptHead {
	var head claudeTranscriptHead
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), transcriptHeadBytes)
	for sc.Scan() {
		var rec struct {
			Type      string          `json:"type"`
			Timestamp string          `json:"timestamp"`
			Message   json.RawMessage `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
			if head.earliest.IsZero() || ts.Before(head.earliest) {
				head.earliest = ts
			}
		}
		if rec.Type == "user" && !head.cleared {
			head.cleared = isClearCommand(rec.Message)
		}
	}
	return head
}

// isClearCommand reports whether a user record's message is a /clear command.
// Command records carry their content as a plain string; a message whose content
// is a block list is a real prompt (or a tool result) and never one.
func isClearCommand(msg json.RawMessage) bool {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msg, &m) != nil {
		return false
	}
	var content string
	if json.Unmarshal(m.Content, &content) != nil {
		return false
	}
	for _, marker := range clearCommandMarkers {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}
