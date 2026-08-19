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
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// A pane's current model — the LLM its coding agent is running under — surfaced
// beside the agent's identity and state in the sidebar's pane hover card.
//
// Neither of the two agent-identity channels carries it: detection only ever
// yields (agent, state) from the screen, and the hook seam (hooks.go) fires once
// per session, so a model reported there would go stale the moment the user
// switches with /model. What is live is the agent's own on-disk history, which it
// appends to as it answers and which names the model that produced each message.
// So the model is read from that history's tail.
//
// Which history is the pane's is the whole problem, and there are three answers,
// in falling order of confidence:
//
//	hook       the pane's own agent reported its session id (hooks.go). Exact,
//	           but only once `catctl integration install` has been run.
//	detected   the host traced the pane's foreground process to the agent's
//	           pid-keyed registry and read the session id out of it
//	           (detect.AgentSessionID, arriving as pane_agent_session). Exact
//	           too, and needs nothing installed — it is what covers the ordinary
//	           pane.
//	cwd        the most recently written history under the pane's working
//	           directory. A guess, and the reason the first two exist: two panes
//	           running one agent in one repository share a cwd, so this answers
//	           both with whichever session wrote last — both rows name one pane's
//	           model, and both flip together when either pane switches.
//
// Which agents can be read is a table — modelResolvers — rather than a chain of
// comparisons, because the two entries have the same shape: a root directory
// resolved once at startup, plus a read that turns (root, cwd, session) into a
// model string. A third agent is then an entry, not another branch. Agents absent
// from the table keep their history somewhere else in its own shape, so their
// panes simply show no model.
//
//	claude   ~/.claude/projects/<slugified cwd>/<session id>.jsonl
//	         one JSON record per message; assistant records name the model, and a
//	         top-level "effort" names the reasoning effort the turn ran at.
//	copilot  ~/.copilot/session-state/<session id>/events.jsonl
//	         one typed event per line; assistant.message events name the model,
//	         and the effort comes from whichever of session.model_change or
//	         session.auto_mode_resolved spoke most recently (see copilotModel).
//
// The effort rides along inside the one model string ("claude-opus-5 · high")
// rather than in a field of its own: the hover card shows the pair as one line, so
// nothing between here and there — pane state, the wire protocol, the pane.list
// snapshot — has to learn about it.
//
// The history read runs off the loop goroutine (refreshAgentModel spawns it and
// posts the result back); everything else here is loop-goroutine only. It also
// assumes the agent's files are on this machine, which the hook seam already
// assumes — panes reach catway over a unix socket.

// modelResolver reads one agent's on-disk history.
//
// root is resolved once at startup (modelRootsFor, called from newOrch) rather
// than per read: it is a fixed function of the environment, while a pane's model
// is re-read every 20 seconds. "" means the agent's home could not be located,
// which disables that agent — and it is also the seam tests use to point an entry
// at a fixture tree.
//
// read runs off the loop goroutine with that root, the pane's cwd, and the
// hook-reported session id ("" when no integration is installed). It returns ""
// for "no answer" and never an error: a missing or half-written history is the
// ordinary state of a pane whose agent has not answered yet, not a fault.
type modelResolver struct {
	root func() string
	read func(root, cwd, session string) string
}

// modelResolvers is the set of agents whose model can be named, keyed by the
// agent label detect.IdentifyAgent yields (and that the hook seam reports).
var modelResolvers = map[string]modelResolver{
	"claude":  {root: claudeProjectsDir, read: claudeModel},
	"copilot": {root: copilotStateDir, read: copilotModel},
}

// modelRootsFor resolves every entry's root once, dropping the agents whose home
// is not on this machine. The result is what orch carries; a missing key and an
// empty root mean the same thing to refreshAgentModel.
func modelRootsFor() map[string]string {
	roots := make(map[string]string, len(modelResolvers))
	for agent, r := range modelResolvers {
		if root := r.root(); root != "" {
			roots[agent] = root
		}
	}
	return roots
}

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
	// modelEffortSep joins the model with the effort it ran at, matching the
	// hover card's own "agent · state" spelling.
	modelEffortSep = " · "
	// sessionSourceDetect labels a session ref the host traced rather than one an
	// agent reported, so the two are distinguishable on a pane that has both.
	// It is deliberately not one of the hook seam's "cats:<agent>" sources: those
	// name an installed integration, and this is the absence of one.
	sessionSourceDetect = "detect"
	// maxEffortLen bounds what is accepted as an effort label — the values are
	// words ("low", "medium", "high", "xhigh", "max").
	maxEffortLen = 16
)

// refreshAgentModel resolves rt's model in the background when one is due,
// posting the result back onto the loop. agent is the pane's arbitrated agent
// label: a pane running an agent this cannot read drops any model it was carrying
// (the agent exited, or another one took the pane over).
func (o *orch) refreshAgentModel(rt *paneRuntime, agent string) {
	resolver, known := modelResolvers[agent]
	root := o.modelRoots[agent]
	// The readers walk this machine's ~/.claude and copilot state, keyed by the
	// pane's cwd. For a pane on another host both halves are wrong — the
	// transcripts live beside the agent, on that box — and the slug match would
	// happily land on a same-named project here and report someone else's model.
	// A remote pane therefore carries no model at all (see orch.paneIsLocal);
	// the hook relay that would carry the real one is Phase 6.
	if !known || root == "" || !o.paneIsLocal(rt.id) {
		rt.agentModel = ""
		rt.modelAt = time.Time{}
		return
	}
	if rt.modelBusy || time.Since(rt.modelAt) < modelRefreshInterval {
		return
	}
	rt.modelBusy = true
	pid, cwd := rt.id, rt.cwd
	// The session ref only names a history belonging to the agent that reported
	// it. A pane that ran claude and then copilot still carries the older agent's
	// ref until the new one's hook fires, and handing claude's id to copilot's
	// reader would at best miss and at worst name someone else's directory. So
	// both channels are filtered on the agent actually running, and the hook wins
	// where they disagree: it is the agent speaking for itself, while the detected
	// one is read from the outside off a pid that may have just been replaced.
	session := ""
	if s := rt.agentSession; s != nil && s.agent == agent && s.kind == "id" {
		session = s.value
	} else if s := rt.detectedSession; s != nil && s.agent == agent {
		session = s.value
	}
	read := resolver.read
	go func() {
		model := read(root, cwd, session)
		o.post(func() { o.setAgentModel(pid, model) })
	}()
}

// setAgentModel records a resolved model and republishes the pane's agent chrome
// when it actually changed. The pane may have gone — or the agent may have left
// it — while the read was in flight, and a read that raced the agent out must not
// put a model back on a pane that no longer has one.
func (o *orch) setAgentModel(pid uint32, model string) {
	rt := o.panes[pid]
	if rt == nil {
		return
	}
	rt.modelBusy = false
	rt.modelAt = time.Now()
	if rt.modelDirty {
		// The pane's conversation changed under this read (applyPaneAgentSession),
		// so what came back names the model of a history that is no longer the
		// pane's. Publish nothing and read the new one — the throttle goes with it,
		// since what it would be pacing is a correction.
		rt.modelDirty = false
		rt.modelAt = time.Time{}
		agent, _ := rt.effectiveAgent()
		o.refreshAgentModel(rt, agent)
		return
	}
	agent, state := rt.effectiveAgent()
	if _, known := modelResolvers[agent]; !known {
		model = ""
	}
	if model == rt.agentModel {
		return
	}
	rt.agentModel = model
	if agent == "" {
		return
	}
	if o.visible[pid] {
		o.broadcast(browserproto.NewPaneAgent(pid, agent, state, model, !rt.unseen))
	}
	// The sidebar's agents rollup names each row by its model, and it otherwise
	// only ships on a state change — which is precisely the moment this read was
	// kicked off from, so the rollup that went out then carried the *previous*
	// model. Without this, a row would name the model one turn behind, and a pane
	// that resolves a model while sitting idle (the periodic sweep catching a
	// /model switch) would never correct itself. The rebuild is session-wide but
	// only happens when the model actually moved, which is rare.
	o.broadcast(o.agentsMsg())
}

// applyPaneAgentSession records the conversation the host traced the pane's agent
// process to, and re-reads the model straight away when it moved.
//
// The re-read cannot wait for the ordinary refresh: until this arrives the pane
// was resolving its model by cwd, which for two panes in one repository is a
// neighbour's transcript. The stale-throttle is cleared rather than respected for
// the same reason — what it is throttling is a read of the wrong file.
//
// A report naming no session (or no agent) drops the ref. That is not "keep what
// we had": the pane's agent left, or the host can no longer trace it, and the cwd
// fallback is a better answer than an identity that has stopped being this pane's.
func (o *orch) applyPaneAgentSession(ev orchestration.PaneAgentSession) {
	rt := o.panes[ev.PaneID]
	if rt == nil {
		return
	}
	var ref *agentSessionRef
	// isTranscriptID gates what the host sent for the same reason a hook report is
	// gated: the value goes into a glob pattern, and this end does not get to
	// assume the other end validated it.
	if ev.Agent != "" && isTranscriptID(ev.SessionID) {
		ref = &agentSessionRef{source: sessionSourceDetect, agent: ev.Agent, kind: "id", value: ev.SessionID}
	}
	if sameSessionRef(rt.detectedSession, ref) {
		return
	}
	rt.detectedSession = ref
	// A read already in flight is reading the previous identity's history; its
	// answer must not settle as this one's (see setAgentModel).
	rt.modelDirty = rt.modelBusy
	rt.modelAt = time.Time{}
	agent, _ := rt.effectiveAgent()
	o.refreshAgentModel(rt, agent)
}

// sameSessionRef compares two optional session refs, nil (no identity) included.
func sameSessionRef(a, b *agentSessionRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
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
func isTranscriptID(s string) bool { return isBareToken(s) }

// isEffortLabel gates the transcript's effort value before it is shown: the field
// is whatever claude wrote there, and only a short bare word belongs in a one-line
// model string beside the model itself.
func isEffortLabel(s string) bool { return len(s) <= maxEffortLen && isBareToken(s) }

// isBareToken reports whether s is a non-empty run of alphanumerics, '-' and '_' —
// no whitespace, no glob metacharacters, nothing needing escaping downstream.
func isBareToken(s string) bool {
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
	Effort      string `json:"effort"` // top-level, not part of the message
	Message     struct {
		Model string `json:"model"`
	} `json:"message"`
}

// lastAssistantModel is the model named by the transcript's last assistant
// record, suffixed with the effort that record ran at when it names a usable one
// ("claude-opus-5 · high"). Only main-thread records count: a sidechain record
// names the model a sub-agent ran under, and claude stamps "<synthetic>" on
// messages it fabricated (an API error, an interrupt) rather than sampled.
func lastAssistantModel(path string) string {
	lines := tailLines(path)
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
			if isEffortLabel(rec.Effort) {
				return m + modelEffortSep + rec.Effort
			}
			return m
		}
	}
	return ""
}

// tailLines is the last modelTailBytes of a line-oriented history file, split
// into lines, with a leading partial record dropped when the read was truncated.
// Both agents' histories are append-only and run to megabytes while the records
// worth reading sit within a few KB of the end, so neither is ever read whole.
// nil for anything unreadable or empty — callers treat that as "no answer".
func tailLines(path string) [][]byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return nil
	}
	off := int64(0)
	if fi.Size() > modelTailBytes {
		off = fi.Size() - modelTailBytes
	}
	buf := make([]byte, fi.Size()-off)
	if _, err := io.ReadFull(io.NewSectionReader(f, off, int64(len(buf))), buf); err != nil {
		return nil
	}
	lines := bytes.Split(buf, []byte("\n"))
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // a truncated read lands mid-record
	}
	return lines
}

// --- copilot (no orch state; runs off the loop goroutine) ---------------------

const (
	// copilotEventsFile is the session's append-only event log, and
	// copilotWorkspaceFile the small header naming the directory it started in.
	copilotEventsFile    = "events.jsonl"
	copilotWorkspaceFile = "workspace.yaml"

	// The events.jsonl types that name a model or the effort behind one.
	copilotAssistantEvent    = "assistant.message"
	copilotModelChangeEvent  = "session.model_change"
	copilotAutoResolvedEvent = "session.auto_mode_resolved"

	// copilotWorkspaceMaxBytes bounds the workspace.yaml read. The file is a
	// handful of scalar keys; anything larger is not one, and this runs over every
	// session directory on the machine when a pane has no session id.
	copilotWorkspaceMaxBytes = 64 << 10
)

// copilotStateDir is where copilot keeps one directory per session. COPILOT_HOME
// overrides the ~/.copilot default, as copilot itself honours and as
// internal/integration does when it installs the hook. "" (no resolvable home)
// disables model resolution for copilot.
//
// The CLI and the copilot language server share this tree — both drive the same
// embedded agent — so a session started from an editor is readable here too.
func copilotStateDir() string {
	root := os.Getenv("COPILOT_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		root = filepath.Join(home, ".copilot")
	}
	return filepath.Join(root, "session-state")
}

// copilotModel is the model behind the pane's most recent answer, "" when no
// session can be pinned down or it holds no usable record.
func copilotModel(root, cwd, session string) string {
	dir := copilotSession(root, cwd, session)
	if dir == "" {
		return ""
	}
	return lastCopilotModel(filepath.Join(dir, copilotEventsFile))
}

// copilotSession locates the pane's session directory. A hook-reported id names
// it outright — it is the directory name — and is gated by isBareToken first, so
// a corrupted or hostile ref cannot walk out of the tree with "..".
//
// Without one the pane's cwd picks it: every session records the directory it
// started in, so they are scanned and the most recently active match wins. That
// is right for one copilot per directory and a coin flip between two panes
// sharing one, the same tradeoff claudeTranscript already makes.
func copilotSession(root, cwd, session string) string {
	if root == "" {
		return ""
	}
	if isBareToken(session) {
		dir := filepath.Join(root, session)
		if fi, err := os.Stat(filepath.Join(dir, copilotEventsFile)); err == nil && !fi.IsDir() {
			return dir
		}
	}
	if cwd == "" {
		return ""
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var newest string
	var newestAt time.Time
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if copilotWorkspaceCwd(filepath.Join(dir, copilotWorkspaceFile)) != cwd {
			continue
		}
		// The events file's mtime, not the directory's: copilot also writes
		// checkpoints/, files/ and research/ under the session, so the directory
		// is touched for reasons that have nothing to do with a new answer.
		fi, err := os.Stat(filepath.Join(dir, copilotEventsFile))
		if err != nil {
			continue
		}
		if newest == "" || fi.ModTime().After(newestAt) {
			newest, newestAt = dir, fi.ModTime()
		}
	}
	return newest
}

// copilotWorkspaceCwd reads the working directory a session started in out of its
// workspace.yaml. The file is a flat map of scalars written by copilot itself, so
// it is scanned line by line rather than through a YAML dependency: only one key
// is needed, and matching at column 0 means a nested key spelled "cwd" under some
// future block cannot be mistaken for the top-level one.
func copilotWorkspaceCwd(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) > copilotWorkspaceMaxBytes {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(line, "cwd:")
		if !ok {
			continue
		}
		v := strings.TrimSpace(strings.TrimSuffix(rest, "\r"))
		// copilot quotes the value only when it has to — a path holding ": " or
		// leading with an indicator character provokes it — so both spellings turn
		// up. Within single quotes YAML escapes a quote by doubling it.
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		}
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			return v[1 : len(v)-1]
		}
		return v
	}
	return ""
}

// copilotEvent is the slice of an events.jsonl line this cares about. One struct
// covers all three event types: their payloads are disjoint, so the fields that
// do not apply simply stay zero.
type copilotEvent struct {
	Type string `json:"type"`
	Data struct {
		Model string `json:"model"` // assistant.message
		// session.model_change carries the effort of an explicitly chosen model;
		// session.auto_mode_resolved carries the router's for an auto one.
		ReasoningEffort string `json:"reasoningEffort"`
		ReasoningBucket string `json:"reasoningBucket"`
	} `json:"data"`
}

// lastCopilotModel is the model named by the session's last assistant.message,
// suffixed with the effort it ran at when one is known ("gpt-5.4 · medium").
//
// The model comes from the message rather than from the session's configured
// model because the configured model is very often the literal "auto": copilot's
// router picks per turn, and what a row should name is what actually answered.
//
// The effort comes from whichever of the two events spoke most recently, because
// which one carries it depends on the mode the session is in — an explicitly
// chosen model reports reasoningEffort on session.model_change and leaves auto
// mode's field null, while auto mode reports the router's pick as
// session.auto_mode_resolved's reasoningBucket.
//
// The two are searched for independently rather than in one pass that stops at
// the answer, because an effort event sits on either side of the message it
// applies to: the change that configured the turn precedes it, while a switch
// made after it has not been used yet but is what the next turn will run under —
// and that is what a row read between turns should say. So the walk runs
// backwards, keeps the first of each kind it meets, and stops as soon as it holds
// both.
func lastCopilotModel(path string) string {
	lines := tailLines(path)
	model, effort := "", ""
	for i := len(lines) - 1; i >= 0 && (model == "" || effort == ""); i-- {
		// Cheap gate: tool traffic and user turns are most of the file, and none
		// of the three events of interest can match without one of these keys.
		// "chosenModel" deliberately does not satisfy the first — the quote is
		// part of the needle — so an auto-resolved line is admitted by the second.
		if !bytes.Contains(lines[i], []byte(`"model"`)) &&
			!bytes.Contains(lines[i], []byte(`"reasoning`)) {
			continue
		}
		var ev copilotEvent
		if err := json.Unmarshal(lines[i], &ev); err != nil {
			continue
		}
		switch ev.Type {
		case copilotModelChangeEvent, copilotAutoResolvedEvent:
			if effort != "" {
				continue // an older event must not overwrite the newest
			}
			if e := ev.Data.ReasoningEffort; isEffortLabel(e) {
				effort = e
			} else if b := ev.Data.ReasoningBucket; isEffortLabel(b) {
				effort = b
			}
		case copilotAssistantEvent:
			if model == "" {
				model = ev.Data.Model
			}
		}
	}
	if model == "" {
		return ""
	}
	if effort != "" {
		return model + modelEffortSep + effort
	}
	return model
}
