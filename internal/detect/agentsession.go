package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// Which conversation a running agent process is in.
//
// Identity detection answers "this pane is running claude"; it cannot answer
// "which claude", and that second question is the one a per-pane reading of the
// agent's own history needs. Two panes running claude in one repository share a
// working directory, so anything keyed on the directory — as the model readout
// in cmd/catway/agentmodel.go was — resolves both panes to whichever transcript
// was written last: both rows name one pane's model, and both flip together when
// either pane switches models.
//
// The hook seam is the exact answer (claude's SessionStart reports its session
// id), but it only exists once the user has run `catctl integration install`,
// which most panes have not. What is always there is the agent's own registry of
// live processes, keyed by pid:
//
//	claude  ~/.claude/sessions/<pid>.json  {"pid":…,"sessionId":…,"cwd":…,…}
//
// A terminal holds the pid — it owns the PTY, so the foreground process group is
// one syscall away (ForegroundAgentPID) — which makes the registry a lookup with
// no cooperation from the agent required. That is why this lives in detect and
// runs on the host: the pids and the registry are both on the machine the agent
// runs on, which for a remote pane is not the machine drawing the sidebar.
//
// The answer is a session *identity*, not a model: what it is worth is naming the
// pane's own history file, wherever that history is then read from.

// sessionProbe reads one agent's registry entry for a live pid, "" when there is
// no usable answer (no registry, no entry, a half-written file). Never an error:
// an agent that has not written its entry yet is an ordinary state, not a fault.
type sessionProbe func(pid int) string

// sessionProbes is the set of agents whose live processes can be traced back to a
// conversation, keyed by the label IdentifyAgent yields. A table rather than a
// switch for the same reason modelResolvers is one: a second agent that keeps a
// pid-keyed registry is an entry, not another branch.
var sessionProbes = map[string]sessionProbe{
	"claude": claudeSessionID,
}

// AgentSessionID names the conversation one of pids is in — the first that
// answers — and "" when the agent keeps no readable registry, no pid is given, or
// none of them has an entry. Cheap for every agent but the ones in the table: an
// unknown label costs one map lookup.
//
// Several pids because the caller cannot know which process in the pane's
// foreground group is the one holding the state (ForegroundAgentPIDs): a wrapper
// script carries the name, the binary it started keeps the registry entry. They
// are tried in the caller's order, which puts the likeliest first.
func AgentSessionID(agent string, pids []int) string {
	probe := sessionProbes[agent]
	if probe == nil {
		return ""
	}
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if id := probe(pid); id != "" {
			return id
		}
	}
	return ""
}

const (
	// claudeSessionMaxBytes bounds the registry read. The file is a flat object of
	// a dozen scalars; anything larger is not one.
	claudeSessionMaxBytes = 64 << 10
	// maxSessionIDLen bounds what is accepted as a session id — claude's are
	// UUIDs, and the value ends up in a file path built by the reader.
	maxSessionIDLen = 128
)

// claudeSessionID reads claude's registry entry for pid. claude writes one at
// startup and rewrites it as the session changes (a new conversation under the
// same process gets a new id there), so a re-read is what catches a /clear as
// well as a fresh start.
//
// The entry names its own pid; a mismatch means the file is not this process's —
// the only way that happens is a stale entry a dead claude never cleaned up and a
// pid the OS has since reused — and is refused rather than believed.
func claudeSessionID(pid int) string {
	dir := claudeConfigDir()
	if dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil || len(b) > claudeSessionMaxBytes {
		return ""
	}
	var rec struct {
		PID       int    `json:"pid"`
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return ""
	}
	if rec.PID != pid || !isSessionID(rec.SessionID) {
		return ""
	}
	return rec.SessionID
}

// claudeConfigDir is claude's state directory: CLAUDE_CONFIG_DIR when set (as
// claude itself honours), else ~/.claude. "" when no home can be resolved, which
// disables the probe.
//
// Read from this process's environment rather than the pane's: a shell that
// exports its own CLAUDE_CONFIG_DIR would be read against the wrong tree, but a
// pane env is not available here, and the reader that consumes the id
// (cmd/catway/agentmodel.go) makes exactly the same assumption — so the two
// agree, which is what matters.
func claudeConfigDir() string {
	if root := os.Getenv("CLAUDE_CONFIG_DIR"); root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// isSessionID gates a registry-reported id before it travels: it is whatever the
// agent wrote there, and it ends up in a glob pattern naming a transcript file on
// the machine that reads it. Only a short bare token — alphanumerics, '-' and
// '_' — can do that safely.
func isSessionID(s string) bool {
	if s == "" || len(s) > maxSessionIDLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
