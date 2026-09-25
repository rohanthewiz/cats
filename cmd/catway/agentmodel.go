//go:build ghostty

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/dlog"
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
// The effort, and for claude how full the context window is, ride along inside
// the one model string ("claude-opus-5 · high · 43k/1M") rather than in fields of
// their own: the hover card shows the pair as one line, so
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
	// modelSweepInterval is the built-in period of the background refresh,
	// which is what catches a /model switch on a pane that then sits idle — with
	// no state transition, nothing else would re-read its transcript. The live
	// period is panes.agent_refresh (orch.modelSweep); this is what an orch uses
	// before, or without, a config, and it matches config's own default.
	//
	// A minute rather than anything tighter because the string also carries the
	// context size, which grows on every tool round-trip of a working pane: each
	// sweep that lands mid-turn is a change, and each change re-broadcasts the
	// agents rollup session-wide (see setAgentModel). State transitions still
	// read sooner (bounded by modelRefreshInterval), so a pane that finishes a
	// turn shows its final figure promptly; the sweep only bounds how stale a
	// pane sitting in one state can get.
	modelSweepInterval = time.Minute
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
	o.sendVisible(pid, browserproto.NewPaneAgent(pid, agent, state, model, !rt.unseen))
	// The sidebar's agents rollup names each row by its model, and it otherwise
	// only ships on a state change — which is precisely the moment this read was
	// kicked off from, so the rollup that went out then carried the *previous*
	// model. Without this, a row would name the model one turn behind, and a pane
	// that resolves a model while sitting idle (the periodic sweep catching a
	// /model switch) would never correct itself. The rebuild is session-wide and,
	// now that the string carries the context size, happens about once per turn
	// on a busy claude pane — bounded by modelRefreshInterval per pane, and by
	// compactTokens rounding to whole thousands so small moves do not count.
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
//
// The period is re-read before every wait rather than fixed at start, because
// panes.agent_refresh is live-reloadable. A plain ticker would need resetting
// from the loop goroutine; instead the loop only stores the new period and
// pokes modelSweepNudge (setModelSweep), and this goroutine — the only one that
// owns the timer — starts a fresh wait with it:
//
//	wait(period) ──fires──▶ post sweep ──▶ wait(period)
//	      │
//	      └──nudged──▶ (no sweep) ──▶ wait(new period)
//
// A nudge does not sweep: the period changing is not a reason to read every
// transcript right now. A period of 0 (agent_refresh: off) waits on the nudge
// alone, so turning the sweep back on needs no restart.
func (o *orch) runAgentModels() {
	for {
		d := time.Duration(o.modelSweep.Load())
		if d <= 0 {
			<-o.modelSweepNudge
			continue
		}
		t := time.NewTimer(d)
		select {
		case <-t.C:
			o.post(func() {
				for _, rt := range o.panes {
					agent, _ := rt.effectiveAgent()
					o.refreshAgentModel(rt, agent)
				}
			})
		case <-o.modelSweepNudge:
			t.Stop()
		}
	}
}

// setModelSweep adopts a new sweep period (0 = off) and wakes the sweep so it
// takes effect now. An unchanged period is not a nudge: a config save that
// touched some other section should not restart the current wait. The send is
// non-blocking — a nudge already pending carries the same message, since the
// goroutine re-reads the period when it wakes. Loop goroutine (or main, before
// the loop starts).
func (o *orch) setModelSweep(d time.Duration) {
	if o.modelSweep.Swap(int64(d)) == int64(d) {
		return
	}
	select {
	case o.modelSweepNudge <- struct{}{}:
	default:
	}
}

// agentRefreshFromConfig resolves panes.agent_refresh, falling back to the
// built-in period if the value is unparseable — Config.Validate has already
// refused that at load, so this is belt-and-braces plus a log line, as with
// reapAfterFromConfig.
func agentRefreshFromConfig(p config.Panes) time.Duration {
	d, err := p.AgentRefreshEvery()
	if err != nil {
		dlog.Warnf("catway: panes.%v — using %s", err, modelSweepInterval)
		return modelSweepInterval
	}
	return d
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
		// Usage is the API's own accounting for the request that produced this
		// message, copied into the transcript verbatim. Only the input side is
		// read (see claudeContextUsed).
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// lastAssistantModel is the model named by the transcript's last assistant
// record, suffixed with the effort that record ran at when it names a usable one,
// and then with how full the context window was for that request
// ("claude-opus-5 · high · 43k/1M"). Only main-thread records count: a sidechain
// record names the model a sub-agent ran under (and a sub-agent's context is its
// own, not the pane's), and claude stamps "<synthetic>" on messages it
// fabricated (an API error, an interrupt) rather than sampled.
//
// The context figure rides in the same string as the effort, for the same
// reason the effort does (see modelEffortSep): the hover card and the rollup
// already carry this one string end to end, so nothing downstream has to learn
// a new field. The browser's modelLabel recognises the segment by its shape.
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
			out := m
			if isEffortLabel(rec.Effort) {
				out += modelEffortSep + rec.Effort
			}
			if used := claudeContextUsed(rec); used > 0 {
				out += modelEffortSep + formatContext(used, claudeContextWindow(m, used))
			}
			return out
		}
	}
	return ""
}

// claudeContextUsed is how many tokens of context the record's request carried:
// everything the model was handed as input, whether it was billed fresh, written
// to the cache, or read from it. Those three are disjoint slices of one prompt
// (the API reports cached tokens *instead of* counting them in input_tokens), so
// the sum is the prompt's full size.
//
// The record's own output is left out. It does join the context on the next
// request, but most of it is thinking, which is dropped from later turns, so
// adding it would overstate the figure.
//
// 0 means the record carries no usage (an older transcript, a record written
// before its response finished) and the segment is simply omitted.
func claudeContextUsed(rec transcriptRecord) int64 {
	u := rec.Message.Usage
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

const (
	contextWindowStd  = 200_000
	contextWindowWide = 1_000_000
)

// claudeContextWindows maps a model id prefix to its context window, first match
// wins. The transcript names the model but not the window, so this is a table
// rather than a read.
//
// It is ordered narrow-to-broad on purpose: "claude-opus-4-6" has to be tried
// before "claude-opus-4", which would otherwise claim it (and Opus 4 / 4.1 / 4.5
// really are 200K while 4.6 and later are 1M). The table only needs to name the
// older generations explicitly — an id that matches nothing falls through to 1M
// in claudeContextWindow, because every current Claude model has a 1M window
// except Haiku, and a model released after this table was written is far likelier
// to follow the current line than the old one.
var claudeContextWindows = []struct {
	prefix string
	window int64
}{
	{"claude-opus-4-6", contextWindowWide},
	{"claude-opus-4-7", contextWindowWide},
	{"claude-opus-4-8", contextWindowWide},
	{"claude-sonnet-4-6", contextWindowWide},
	{"claude-opus-4", contextWindowStd},   // Opus 4, 4.1, 4.5
	{"claude-sonnet-4", contextWindowStd}, // Sonnet 4, 4.5 (1M only as an opt-in beta)
	{"claude-haiku-", contextWindowStd},
	{"claude-3", contextWindowStd}, // the older "claude-3-5-sonnet-…" ordering
}

// claudeContextWindow is the context window model ran with.
//
// Two corrections sit on top of the table:
//
//   - An id ending "[1m]" names the 1M opt-in on a model whose default is 200K
//     (the same spelling modelLabel already recognises in the browser).
//   - A request that carried more than the table's window can only have run on
//     the larger one. That is how a 200K-default model on the 1M beta gets the
//     right denominator even though its transcript id says nothing about it —
//     from the moment the conversation outgrows 200K, which is the moment the
//     difference starts to matter.
func claudeContextWindow(model string, used int64) int64 {
	id := strings.ToLower(model)
	if strings.HasSuffix(id, "[1m]") {
		return contextWindowWide
	}
	window := int64(contextWindowWide)
	for _, e := range claudeContextWindows {
		if strings.HasPrefix(id, e.prefix) {
			window = e.window
			break
		}
	}
	if used > window {
		window = contextWindowWide
	}
	return window
}

// formatContext renders "used/window" compactly for a one-line label: "43k/1M".
func formatContext(used, window int64) string {
	return compactTokens(used) + "/" + compactTokens(window)
}

// compactTokens renders a token count in at most four characters or so: "850",
// "43k", "1M", "1.2M". Thousands are whole — a sidebar label moving by a tenth
// of a k on every turn is noise, and every change re-broadcasts the agents rollup
// (see setAgentModel), so coarser is also cheaper.
//
// The k/M boundary is decided on the *rounded* value, so 999,600 reads "1M"
// rather than "1000k". Millions keep one decimal, dropped when it is ".0".
func compactTokens(n int64) string {
	switch k := (n + 500) / 1000; {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case k < 1000:
		return strconv.FormatInt(k, 10) + "k"
	default:
		m := strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64)
		return strings.TrimSuffix(m, ".0") + "M"
	}
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
