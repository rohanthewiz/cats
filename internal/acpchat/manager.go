package acpchat

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/acp"
	"github.com/rohanthewiz/cats/internal/browserproto"
)

// Manager is the chat panel's state machine: one ACP agent subprocess, one
// conversation, one transcript, fanned out to every connected client.
//
// Owning a subprocess breaks catway's usual rule (plugins.go deliberately
// shells install/update into a tab so "the server stays out of the subprocess
// business"). Chat is the exception because its subprocess is not a stream of
// output to watch but a JSON-RPC peer with agent→client requests — permission
// prompts that any connected client may answer — which a pane cannot mediate.
//
// Threading contract, exactly ced's events-only rule translated to catway's
// actor loop: every exported method and every field access runs on the
// owner's loop goroutine. The manager's own goroutines (the spawn handshake,
// the blocking prompt call, the acp.Client callbacks) reach that goroutine
// exclusively through post, and every browser-bound message leaves through
// emit. Nothing here needs a mutex; the loop is the mutex.
//
//	loop:  Send/Cancel/Permission/Clear/Snapshot ──► fields, rows, emit(...)
//	         ▲                                          │
//	         │ post(closure)                            ▼
//	goroutines: handshake, session/prompt, onNotify, onRequest, onExit
type Manager struct {
	def   BackendDef
	post  func(func())  // hand a closure to the owner's loop
	emit  func(msg any) // broadcast one browserproto message to all clients
	start StartFunc     // acp.Start normally; tests wire pipes to a fake agent

	// Engine state. connSeq is the generation counter: bumped on every
	// teardown, captured by every goroutine at spawn, checked by every posted
	// callback — so an event from a killed agent can never mutate the state
	// of its successor.
	status    string // "idle" | "starting" | "ready" | "turn" | "dead"
	detail    string // last error / note, mirrored into chat_state
	client    *acp.Client
	sessionID string
	modelID   string
	cwd       string
	connSeq   int

	// queued holds prompts typed before the agent is ready (mid-handshake or
	// mid-turn). The first Enter must never vanish: it is flushed as one
	// prompt the moment the session can take it.
	queued     string
	turnActive bool
	cancelSent bool

	// Transcript model. openRow is the trailing agent row a delta stream is
	// appending to (0 = none); deltaBuf holds text appended to the model but
	// not yet broadcast — the coalescer below.
	rows     []browserproto.ChatRow
	nextRow  int64
	openRow  int64
	deltaBuf string
	deltaOn  bool // a flush timer is armed

	toolRows map[string]int64 // ACP toolCallId → transcript row id

	perms    map[string]*pendingPerm
	nextPerm int
}

// StartFunc is the subprocess seam, acp.Start's signature.
type StartFunc func(dir, bin string, args, env, dropEnv []string,
	onNotify func(string, json.RawMessage),
	onRequest func(string, json.RawMessage) (any, error),
	onExit func(error)) (*acp.Client, error)

// pendingPerm is one unanswered session/request_permission. reply is buffered
// so a late resolution (turn already flushed it) never blocks the resolver.
type pendingPerm struct {
	reqID   string
	title   string
	kind    string
	options []acp.PermOption
	reply   chan any
}

const (
	// initTimeout budgets the spawn→initialize→session/new handshake. Looser
	// than a network call deserves because the first credential read can
	// stall on a macOS keychain unlock prompt.
	initTimeout = 60 * time.Second
	// turnTimeout bounds one session/prompt. Turns legitimately run for many
	// minutes (the agent edits, builds, retries); the bound only exists so a
	// wedged agent eventually surfaces instead of spinning forever.
	turnTimeout = 30 * time.Minute

	// Delta coalescing: streamed text is appended to the model immediately
	// but broadcast on a cadence, so a fast token stream cannot flood the
	// per-connection send buffer (catway drops clients whose buffer fills).
	// 100ms reads as live; 8KB bounds the burst a single flush can carry.
	deltaFlushEvery = 100 * time.Millisecond
	deltaFlushBytes = 8 << 10

	// transcriptMax caps the retained transcript. The cap exists for the
	// snapshot (every new client receives the whole thing), not for memory.
	transcriptMax = 500
)

// New builds a manager for one backend. post and emit bind it to the owner's
// loop and broadcast fan-out; the zero-value start seam is the real
// subprocess launcher.
func New(def BackendDef, post func(func()), emit func(msg any)) *Manager {
	return &Manager{
		def:      def,
		post:     post,
		emit:     emit,
		start:    acp.Start,
		status:   "idle",
		toolRows: map[string]int64{},
		perms:    map[string]*pendingPerm{},
	}
}

// --- Queries ------------------------------------------------------------------

// Snapshot is the full client model: state, transcript, and still-open
// permission prompts. Sent to each client on connect so joining
// mid-conversation needs no request/reply choreography.
func (m *Manager) Snapshot() browserproto.ChatSnapshot {
	var perms []browserproto.ChatPerm
	for _, p := range m.perms {
		perms = append(perms, permMsg(p))
	}
	rows := make([]browserproto.ChatRow, len(m.rows))
	copy(rows, m.rows)
	return browserproto.NewChatSnapshot(m.stateInfo(), rows, perms)
}

func (m *Manager) stateInfo() browserproto.ChatStateInfo {
	return browserproto.ChatStateInfo{
		Status:  m.status,
		Backend: m.def.Name,
		Model:   m.modelID,
		Cwd:     m.cwd,
		Detail:  m.detail,
	}
}

// --- Commands (loop goroutine) ------------------------------------------------

// Send handles one chat.send: echo the user's text, then route it by state —
// prompt now, queue for the pending handshake/turn, or (re)start the agent.
// cwd only matters when a start happens; an established session keeps its own.
func (m *Manager) Send(cwd, text string) {
	m.appendRow(browserproto.ChatRow{Role: "user", Text: text})
	switch m.status {
	case "ready":
		m.prompt(text)
	case "starting", "turn":
		// One session/prompt may be in flight per session; later sends ride
		// along when the current one lands.
		m.queued = joinPrompts(m.queued, text)
	default: // idle, dead — a send is also the retry gesture
		m.queued = joinPrompts(m.queued, text)
		m.startAgent(cwd)
	}
}

// Cancel handles chat.cancel: ask the agent to stop the turn, and resolve
// every pending permission as cancelled — the spec requires it, and a
// well-behaved agent may be unable to end the turn while one is open.
// The turn itself ends when session/prompt responds with stopReason
// "cancelled"; sending the notification twice would be noise.
func (m *Manager) Cancel() {
	if m.status != "turn" || m.cancelSent {
		return
	}
	m.cancelSent = true
	_ = m.client.Notify("session/cancel", acp.CancelParams{SessionID: m.sessionID})
	m.flushPerms(acp.CancelledOutcome(), "cancelled")
}

// Permission handles chat.permission: route a client's answer to the blocked
// request handler. Errors on an unknown id — usually the benign race of two
// clients answering the same prompt — so the loser gets a failed cmd_result
// instead of silence.
func (m *Manager) Permission(reqID, optionID string, cancel bool) error {
	p := m.perms[reqID]
	if p == nil {
		return fmt.Errorf("permission %s already resolved", reqID)
	}
	delete(m.perms, reqID)
	outcome, verdict := acp.SelectedOutcome(optionID), "allowed"
	if cancel {
		outcome, verdict = acp.CancelledOutcome(), "cancelled"
	} else if kindOf(p.options, optionID) == "reject_once" || kindOf(p.options, optionID) == "reject_always" {
		verdict = "rejected"
	}
	p.reply <- outcome
	m.emit(browserproto.NewChatPermResolved(reqID, verdict))
	return nil
}

// Clear handles chat.clear: drop the conversation and the agent with it, and
// tell every client to start blank. The next send starts fresh.
func (m *Manager) Clear() {
	m.teardown()
	m.rows = nil
	m.status, m.detail, m.modelID, m.cwd = "idle", "", "", ""
	m.emit(m.Snapshot())
}

// Shutdown tears the agent down without emitting — the server is exiting and
// the sockets are about to close anyway.
func (m *Manager) Shutdown() {
	m.teardown()
}

// --- Lifecycle ----------------------------------------------------------------

// startAgent spawns the backend and runs the ACP handshake off-loop. Every
// callback captures gen; a teardown while the handshake is in flight makes
// its completion a no-op.
func (m *Manager) startAgent(cwd string) {
	if _, err := LookPath(m.def.Binary); err != nil {
		m.setStatus("dead", m.def.Binary+" is not installed")
		m.appendRow(browserproto.ChatRow{Role: "info",
			Text: "The `" + m.def.Binary + "` binary is not on PATH. Install it, then send your message again."})
		return
	}
	m.cwd = cwd
	m.setStatus("starting", "")
	gen := m.connSeq
	def := m.def

	onNotify := func(method string, params json.RawMessage) {
		if method != "session/update" {
			return
		}
		var n acp.SessionNotification
		if json.Unmarshal(params, &n) != nil {
			return
		}
		var u acp.UpdateEnvelope
		if json.Unmarshal(n.Update, &u) != nil {
			return
		}
		m.post(func() {
			if gen == m.connSeq {
				m.handleUpdate(u)
			}
		})
	}
	onRequest := func(method string, params json.RawMessage) (any, error) {
		if method != "session/request_permission" {
			// We declared no fs/terminal capability, so anything else is the
			// agent overstepping; decline honestly.
			return nil, fmt.Errorf("unsupported client method %s", method)
		}
		return m.servePermission(gen, params), nil
	}
	onExit := func(err error) {
		m.post(func() {
			if gen != m.connSeq {
				return
			}
			reason := "the agent exited"
			if err != nil {
				reason = "the agent connection failed: " + err.Error()
			}
			m.die(reason)
		})
	}

	go func() {
		client, err := m.start("", def.Binary, def.Args, nil, def.DropEnv, onNotify, onRequest, onExit)
		if err == nil {
			var caps acp.ClientCapabilities // all false: no fs, no terminal
			err = client.CallWithTimeout("initialize", acp.InitializeParams{
				ProtocolVersion:    acp.ProtocolVersion,
				ClientCapabilities: caps,
				ClientInfo:         acp.ClientInfo{Name: "cats", Version: "1"},
			}, nil, initTimeout)
		}
		var sess acp.NewSessionResult
		if err == nil {
			err = client.CallWithTimeout("session/new", acp.NewSessionParams{
				Cwd: cwd, MCPServers: []any{},
			}, &sess, initTimeout)
		}
		m.post(func() {
			if gen != m.connSeq {
				if client != nil {
					client.Close()
				}
				return
			}
			if err != nil {
				// The handshake owns its failure report: a dead spawn may
				// never reach the read loop, so onExit cannot be relied on
				// to fire here.
				if client != nil {
					client.Close()
				}
				m.die("could not start " + def.Name + ": " + err.Error())
				return
			}
			m.client = client
			m.sessionID = sess.SessionID
			m.modelID = sess.Models.CurrentModelID
			m.setStatus("ready", "")
			if q := m.queued; q != "" {
				m.queued = ""
				m.prompt(q)
			}
		})
	}()
}

// die is the shared death path: tear down, report, and leave the sign-in
// remedy on the table — auth is the overwhelmingly likely cause of an agent
// dying before it served anything, and the hint costs nothing when it wasn't.
func (m *Manager) die(reason string) {
	m.teardown()
	m.setStatus("dead", reason)
	m.appendRow(browserproto.ChatRow{Role: "info", Text: reason})
	if m.def.AuthHint != "" {
		row := browserproto.ChatRow{Role: "info", Text: m.def.AuthHint}
		if len(m.def.AuthArgv) > 0 {
			row.Action = &browserproto.ChatAction{Label: "Sign in", Argv: m.def.AuthArgv}
		}
		m.appendRow(row)
	}
}

// teardown invalidates the generation, resolves every open permission as
// cancelled, and drops the subprocess. Callers decide what the next status is.
func (m *Manager) teardown() {
	m.connSeq++
	m.flushPerms(acp.CancelledOutcome(), "cancelled")
	if m.client != nil {
		m.client.Close()
		m.client = nil
	}
	m.sessionID = ""
	m.turnActive, m.cancelSent = false, false
	m.queued = ""
	m.openRow, m.deltaBuf = 0, ""
	m.toolRows = map[string]int64{}
}

// --- The prompt turn ----------------------------------------------------------

// prompt runs one session/prompt off-loop. The call blocks for the whole
// turn — streamed updates and permission requests arrive on other goroutines
// meanwhile — and its response is what ends the turn.
func (m *Manager) prompt(text string) {
	m.setStatus("turn", "")
	m.turnActive, m.cancelSent = true, false
	client, sid, gen := m.client, m.sessionID, m.connSeq
	go func() {
		var res acp.PromptResult
		err := client.CallWithTimeout("session/prompt", acp.PromptParams{
			SessionID: sid,
			Prompt:    []acp.ContentBlock{{Type: "text", Text: text}},
		}, &res, turnTimeout)
		m.post(func() {
			if gen != m.connSeq {
				return
			}
			m.turnDone(res, err)
		})
	}()
}

// turnDone closes out a turn: settle the streaming row, resolve anything the
// turn left open, report abnormal endings, and take the next queued prompt.
func (m *Manager) turnDone(res acp.PromptResult, err error) {
	m.closeOpenRow()
	// A turn that ends with prompts still open (the agent gave up waiting,
	// or errored) must not leave the clients staring at dead buttons.
	m.flushPerms(acp.CancelledOutcome(), "cancelled")
	m.turnActive = false
	m.setStatus("ready", "")
	switch {
	case m.cancelSent || res.StopReason == acp.StopCancelled:
		// cancelSent counts even when the stopReason disagrees: the spec says
		// a cancelled turn must answer stopReason "cancelled", but copilot
		// (1.0.77, verified live) answers end_turn — and the truth the user
		// cares about is that the turn stopped because they asked.
		m.appendRow(browserproto.ChatRow{Role: "info", Text: "— stopped"})
	case err != nil:
		m.appendRow(browserproto.ChatRow{Role: "info", Text: "turn failed: " + err.Error()})
	case res.StopReason == acp.StopEndTurn, res.StopReason == "":
	default:
		// refusal, max_tokens, max_turn_requests — the turn ended early for a
		// reason worth reading.
		m.appendRow(browserproto.ChatRow{Role: "info", Text: "turn ended: " + res.StopReason})
	}
	if q := m.queued; q != "" && m.status == "ready" {
		m.queued = ""
		m.prompt(q)
	}
}

// handleUpdate renders the session/update variants v1 acts on. Everything
// else (thoughts, plans, command/mode/config/usage updates) is dropped here
// on purpose: ChatRow.Role is an open enum, so rendering more later is a new
// row kind, not a protocol change.
func (m *Manager) handleUpdate(u acp.UpdateEnvelope) {
	switch u.SessionUpdate {
	case acp.UpdateAgentMessageChunk:
		text := acp.ContentText(u.Content)
		if text == "" {
			return
		}
		if m.openRow == 0 {
			row := m.appendRow(browserproto.ChatRow{Role: "agent"})
			m.openRow = row.ID
		}
		m.deltaBuf += text
		if r := m.findRow(m.openRow); r != nil {
			r.Text += text
		}
		if len(m.deltaBuf) >= deltaFlushBytes {
			m.flushDelta()
		} else if !m.deltaOn {
			m.deltaOn = true
			time.AfterFunc(deltaFlushEvery, func() {
				m.post(func() {
					m.deltaOn = false
					m.flushDelta()
				})
			})
		}

	case acp.UpdateToolCall:
		// A tool call interleaves with prose: settle the streaming row so the
		// transcript keeps the order the agent actually worked in.
		m.closeOpenRow()
		row := m.appendRow(browserproto.ChatRow{
			Role: "tool", Text: u.Title, Kind: u.Kind, Status: u.Status,
		})
		if u.ToolCallID != "" {
			m.toolRows[u.ToolCallID] = row.ID
		}

	case acp.UpdateToolCallUpdate:
		id := m.toolRows[u.ToolCallID]
		r := m.findRow(id)
		if r == nil {
			return
		}
		if u.Title != "" {
			r.Text = u.Title
		}
		if u.Status != "" {
			r.Status = u.Status
		}
		// Rows are small; re-broadcasting the whole row is the upsert.
		m.emit(browserproto.NewChatRow(*r))
	}
}

// flushDelta broadcasts the buffered tail of the streaming row.
func (m *Manager) flushDelta() {
	if m.openRow == 0 || m.deltaBuf == "" {
		m.deltaBuf = ""
		return
	}
	m.emit(browserproto.NewChatDelta(m.openRow, m.deltaBuf))
	m.deltaBuf = ""
}

// closeOpenRow settles the streaming row: flush what's buffered and stop
// appending to it.
func (m *Manager) closeOpenRow() {
	m.flushDelta()
	m.openRow = 0
}

// --- Permissions --------------------------------------------------------------

// servePermission runs on the acp.Client's per-request goroutine: it parks
// the request as a pendingPerm on the loop and blocks until some client
// answers (or the backstop gives the agent its own gentlest refusal). The
// blocking is the point — the read loop keeps streaming meanwhile, and the
// agent's turn waits exactly as long as the user does.
func (m *Manager) servePermission(gen int, params json.RawMessage) any {
	_, title, kind, options := acp.ParsePermission(params)
	reply := make(chan any, 1)
	m.post(func() {
		if gen != m.connSeq {
			reply <- acp.CancelledOutcome()
			return
		}
		m.nextPerm++
		p := &pendingPerm{
			reqID:   fmt.Sprintf("p%d", m.nextPerm),
			title:   title,
			kind:    kind,
			options: options,
			reply:   reply,
		}
		m.perms[p.reqID] = p
		m.emit(permMsg(p))
	})
	select {
	case out := <-reply:
		return out
	case <-time.After(turnTimeout):
		return acp.RejectOutcome(options)
	}
}

// flushPerms resolves every open prompt with one outcome and broadcasts the
// resolutions so no client is left holding live buttons.
func (m *Manager) flushPerms(outcome any, verdict string) {
	for id, p := range m.perms {
		delete(m.perms, id)
		p.reply <- outcome
		m.emit(browserproto.NewChatPermResolved(id, verdict))
	}
}

func permMsg(p *pendingPerm) browserproto.ChatPerm {
	opts := make([]browserproto.ChatPermOption, len(p.options))
	for i, o := range p.options {
		opts[i] = browserproto.ChatPermOption{ID: o.OptionID, Name: o.Name, Kind: o.Kind}
	}
	return browserproto.NewChatPerm(p.reqID, p.title, p.kind, opts)
}

func kindOf(options []acp.PermOption, optionID string) string {
	for _, o := range options {
		if o.OptionID == optionID {
			return o.Kind
		}
	}
	return ""
}

// --- Transcript ---------------------------------------------------------------

// appendRow assigns the next id, stores, trims, and broadcasts. Returns a
// pointer into rows valid until the next append.
func (m *Manager) appendRow(row browserproto.ChatRow) *browserproto.ChatRow {
	m.nextRow++
	row.ID = m.nextRow
	m.rows = append(m.rows, row)
	if len(m.rows) > transcriptMax {
		m.rows = m.rows[len(m.rows)-transcriptMax:]
	}
	m.emit(browserproto.NewChatRow(row))
	return &m.rows[len(m.rows)-1]
}

// findRow resolves a row id, scanning from the tail — the rows being patched
// (the streaming row, a running tool) are always recent.
func (m *Manager) findRow(id int64) *browserproto.ChatRow {
	if id == 0 {
		return nil
	}
	for i := len(m.rows) - 1; i >= 0; i-- {
		if m.rows[i].ID == id {
			return &m.rows[i]
		}
	}
	return nil
}

func (m *Manager) setStatus(status, detail string) {
	m.status, m.detail = status, detail
	m.emit(browserproto.NewChatState(m.stateInfo()))
}

// joinPrompts folds a second send into the queue; two thoughts typed while
// the agent was busy arrive as one prompt, in order.
func joinPrompts(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" {
		return b
	}
	return a + "\n" + b
}
