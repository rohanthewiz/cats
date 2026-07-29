//go:build ghostty

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
)

// A pane's current model — the LLM its coding agent is running under — surfaced
// beside the agent's identity and state in the sidebar's pane hover card.
//
// Neither of the two agent-identity channels carries it: detection only ever
// yields (agent, state) from the screen, and the hook seam (hooks.go) fires for
// claude once per session, so a model reported there would go stale the moment
// the user switches with /model. What is live is the agent's own transcript —
// claude appends one JSONL record per message under
// ~/.claude/projects/<slugified cwd>/<session id>.jsonl, and every assistant
// record names the model that produced it. So the model is read from that tail:
// exact when the pane's claude hook has reported its resumable session id,
// best-effort from the pane's cwd otherwise (a pane whose integration is not
// installed still gets an answer, which is the common case).
//
// Only claude is wired up; every other agent keeps its history somewhere else in
// its own shape, so their panes simply show no model.
//
// The transcript read runs off the loop goroutine (refreshAgentModel spawns it
// and posts the result back); everything else here is loop-goroutine only. It
// also assumes the agent's files are on this machine, which the hook seam
// already assumes — panes reach catway over a unix socket.

// claudeAgent is the agent label whose transcripts this file knows how to read.
const claudeAgent = "claude"

const (
	// modelRefreshInterval is the minimum gap between transcript reads for one
	// pane. Every publishAgent calls in, and a pane flips state several times a
	// turn; the model only ever changes between turns.
	modelRefreshInterval = 20 * time.Second
	// modelSweepInterval paces the background refresh, which is what catches a
	// /model switch on a pane that then sits idle — with no state transition,
	// nothing else would re-read its transcript.
	modelSweepInterval = 30 * time.Second
	// modelTailBytes bounds the tail read. Transcripts run to megabytes; the
	// last assistant record is all but always within a few KB of the end.
	modelTailBytes = 256 << 10
)

// refreshAgentModel resolves rt's model in the background when one is due,
// posting the result back onto the loop. agent is the pane's arbitrated agent
// label: a pane not running claude drops any model it was carrying (the agent
// exited, or another one took the pane over).
func (o *orch) refreshAgentModel(rt *paneRuntime, agent string) {
	if agent != claudeAgent || o.claudeProjects == "" {
		rt.agentModel = ""
		rt.modelAt = time.Time{}
		return
	}
	if rt.modelBusy || time.Since(rt.modelAt) < modelRefreshInterval {
		return
	}
	rt.modelBusy = true
	pid, cwd, projects := rt.id, rt.cwd, o.claudeProjects
	session := ""
	if s := rt.agentSession; s != nil && s.agent == claudeAgent && s.kind == "id" {
		session = s.value
	}
	go func() {
		model := claudeModel(projects, cwd, session)
		o.post(func() { o.setAgentModel(pid, model) })
	}()
}

// setAgentModel records a resolved model and republishes the pane's agent chrome
// when it actually changed. The pane may have gone — or claude may have left it —
// while the read was in flight, and a read that raced the agent out must not put
// a model back on a pane that no longer has one.
func (o *orch) setAgentModel(pid uint32, model string) {
	rt := o.panes[pid]
	if rt == nil {
		return
	}
	rt.modelBusy = false
	rt.modelAt = time.Now()
	agent, state := rt.effectiveAgent()
	if agent != claudeAgent {
		model = ""
	}
	if model == rt.agentModel {
		return
	}
	rt.agentModel = model
	if agent != "" && o.visible[pid] {
		o.broadcast(browserproto.NewPaneAgent(pid, agent, state, model, !rt.unseen))
	}
}

// runAgentModels is the periodic refresh pacer (own goroutine, started by main),
// bounding how stale a quiet pane's model can get. Each pass is throttled per
// pane by refreshAgentModel, so it costs nothing on panes that just refreshed.
func (o *orch) runAgentModels() {
	t := time.NewTicker(modelSweepInterval)
	defer t.Stop()
	for range t.C {
		o.post(func() {
			for _, rt := range o.panes {
				agent, _ := rt.effectiveAgent()
				o.refreshAgentModel(rt, agent)
			}
		})
	}
}

// --- transcript resolution (no orch state; runs off the loop goroutine) -------

// claudeProjectsDir is where claude keeps its per-project transcripts.
// CLAUDE_CONFIG_DIR overrides the ~/.claude default, as claude itself honours.
// "" (no resolvable home) disables model resolution.
func claudeProjectsDir() string {
	root := os.Getenv("CLAUDE_CONFIG_DIR")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		root = filepath.Join(home, ".claude")
	}
	return filepath.Join(root, "projects")
}

// claudeModel is the model behind the pane's most recent assistant message, ""
// when no transcript can be pinned down or it holds no usable record.
func claudeModel(projects, cwd, session string) string {
	path := claudeTranscript(projects, cwd, session)
	if path == "" {
		return ""
	}
	return lastAssistantModel(path)
}

// claudeTranscript locates the pane's transcript file. A hook-reported session
// id names it outright — the project directory is not implied by the pane's cwd
// (claude slugs the directory it started in), so the glob spans them all.
// Without one, the pane's cwd picks the project directory and the most recently
// written transcript in it wins: right for one claude per directory, a coin flip
// between two panes sharing one.
func claudeTranscript(projects, cwd, session string) string {
	if projects == "" {
		return ""
	}
	if isTranscriptID(session) {
		if hits, _ := filepath.Glob(filepath.Join(projects, "*", session+".jsonl")); len(hits) > 0 {
			return hits[0]
		}
	}
	if cwd == "" {
		return ""
	}
	var newest string
	var newestAt time.Time
	for _, slug := range claudeProjectSlugs(cwd) {
		dir := filepath.Join(projects, slug)
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			if newest == "" || fi.ModTime().After(newestAt) {
				newest, newestAt = filepath.Join(dir, e.Name()), fi.ModTime()
			}
		}
	}
	return newest
}

// isTranscriptID gates the session id before it goes into a glob pattern: hook
// reports are only validated for length and control characters (hooks.go), and
// a value carrying glob metacharacters would match files that are not its own.
func isTranscriptID(s string) bool {
	if s == "" {
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

// claudeProjectSlugs is the directory name(s) claude derives from a working
// directory. Current versions map every non-alphanumeric character to '-';
// older ones kept '_', and the directories they wrote are still on disk (claude
// does not migrate them), so both spellings are searched.
func claudeProjectSlugs(cwd string) []string {
	cur := claudeSlug(cwd, false)
	if legacy := claudeSlug(cwd, true); legacy != cur {
		return []string{cur, legacy}
	}
	return []string{cur}
}

func claudeSlug(cwd string, keepUnderscore bool) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case keepUnderscore && r == '_':
			return r
		}
		return '-'
	}, cwd)
}

// transcriptRecord is the slice of a transcript line this cares about.
type transcriptRecord struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Model string `json:"model"`
	} `json:"message"`
}

// lastAssistantModel is the model named by the transcript's last assistant
// record. Only main-thread records count: a sidechain record names the model a
// sub-agent ran under, and claude stamps "<synthetic>" on messages it fabricated
// (an API error, an interrupt) rather than sampled.
func lastAssistantModel(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return ""
	}
	off := int64(0)
	if fi.Size() > modelTailBytes {
		off = fi.Size() - modelTailBytes
	}
	buf := make([]byte, fi.Size()-off)
	if _, err := io.ReadFull(io.NewSectionReader(f, off, int64(len(buf))), buf); err != nil {
		return ""
	}
	lines := bytes.Split(buf, []byte("\n"))
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // a truncated read lands mid-record
	}
	for i := len(lines) - 1; i >= 0; i-- {
		// Cheap gate: user turns, snapshots and summaries have no model field,
		// and they are most of the file.
		if !bytes.Contains(lines[i], []byte(`"model"`)) {
			continue
		}
		var rec transcriptRecord
		if err := json.Unmarshal(lines[i], &rec); err != nil {
			continue
		}
		if rec.Type != "assistant" || rec.IsSidechain {
			continue
		}
		if m := rec.Message.Model; m != "" && m != "<synthetic>" {
			return m
		}
	}
	return ""
}
