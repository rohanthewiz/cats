package acpchat

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/acp"
	"github.com/rohanthewiz/cats/internal/browserproto"
)

// harness runs a Manager against a real actor loop (a pump goroutine over a
// channel — the same shape as catway's) and a scripted fake agent on the far
// side of real pipes, so the full concurrency picture is exercised: handshake
// goroutine, blocking prompt call, read-loop callbacks, permission goroutines.
type harness struct {
	t    *testing.T
	m    *Manager
	loop chan func()

	mu   sync.Mutex
	msgs []any // every emit, in order

	startCalls int
	agent      *fakeAgent // the most recently started fake agent
	onPrompt   func(a *fakeAgent, id int64, text string)
	startErr   error // when set, the start seam fails outright
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, loop: make(chan func(), 256)}
	go func() {
		for f := range h.loop {
			f()
		}
	}()
	post := func(f func()) { h.loop <- f }
	emit := func(msg any) {
		h.mu.Lock()
		h.msgs = append(h.msgs, msg)
		h.mu.Unlock()
	}
	h.m = New(Backends()[0], post, emit)
	h.m.start = h.startSeam
	prevLook := LookPath
	LookPath = func(string) (string, error) { return "/fake/bin", nil }
	t.Cleanup(func() {
		LookPath = prevLook
		h.run(func() { h.m.Shutdown() })
	})
	return h
}

// run executes f on the loop and waits — the tests' stand-in for "called on
// the owner's loop goroutine".
func (h *harness) run(f func()) {
	done := make(chan struct{})
	h.loop <- func() { f(); close(done) }
	<-done
}

// waitFor polls cond (evaluated on the loop) until it holds or the deadline
// hits.
func (h *harness) waitFor(desc string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ok := false
		h.run(func() { ok = cond() })
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", desc)
}

// emitted returns a snapshot of all broadcast messages.
func (h *harness) emitted() []any {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]any, len(h.msgs))
	copy(out, h.msgs)
	return out
}

func (h *harness) startSeam(dir, bin string, args, env, dropEnv []string,
	onNotify func(string, json.RawMessage),
	onRequest func(string, json.RawMessage) (any, error),
	onExit func(error)) (*acp.Client, error) {
	h.mu.Lock()
	h.startCalls++
	fail := h.startErr
	h.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	clientReads, agentWrites := io.Pipe()
	agentReads, clientWrites := io.Pipe()
	a := &fakeAgent{h: h, r: bufio.NewReader(agentReads), w: agentWrites}
	h.mu.Lock()
	h.agent = a
	h.mu.Unlock()
	go a.run()
	return acp.NewClient(clientReads, clientWrites, onNotify, onRequest, onExit), nil
}

// fakeAgent speaks just enough ACP: it answers the handshake itself and hands
// prompts to the test's script. Prompt scripts run on their own goroutine —
// the read loop must stay free to deliver session/cancel and the client's
// permission answers while a script blocks on them (the same rule the real
// client enforces for its own request handlers).
type fakeAgent struct {
	h *harness
	r *bufio.Reader

	wmu sync.Mutex
	w   *io.PipeWriter

	cancelled chan struct{} // closed when session/cancel arrives
	responses chan rpcMsg   // client's answers to agent→client requests
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func (a *fakeAgent) run() {
	a.cancelled = make(chan struct{})
	a.responses = make(chan rpcMsg, 8)
	for {
		line, err := a.r.ReadBytes('\n')
		if err != nil {
			a.wmu.Lock()
			_ = a.w.Close()
			a.wmu.Unlock()
			return
		}
		var m rpcMsg
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if m.Method == "" && m.ID != nil {
			// A response to one of the agent's own requests (a permission
			// answer) — route it to whichever script is waiting.
			a.responses <- m
			continue
		}
		switch m.Method {
		case "initialize":
			a.respond(*m.ID, `{"protocolVersion":1}`)
		case "session/new":
			a.respond(*m.ID, `{"sessionId":"s1","models":{"currentModelId":"gpt-5-mini"}}`)
		case "session/prompt":
			var p acp.PromptParams
			_ = json.Unmarshal(m.Params, &p)
			text := ""
			if len(p.Prompt) > 0 {
				text = p.Prompt[0].Text
			}
			a.h.mu.Lock()
			onPrompt := a.h.onPrompt
			a.h.mu.Unlock()
			id := *m.ID
			if onPrompt != nil {
				go onPrompt(a, id, text)
			} else {
				a.respond(id, `{"stopReason":"end_turn"}`)
			}
		case "session/cancel":
			close(a.cancelled)
		}
	}
}

func (a *fakeAgent) write(raw string) {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	_, _ = a.w.Write([]byte(raw + "\n"))
}

func (a *fakeAgent) respond(id int64, result string) {
	a.write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, id, result))
}

func (a *fakeAgent) update(update string) {
	a.write(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":%s}}`, update))
}

func (a *fakeAgent) chunk(text string) {
	b, _ := json.Marshal(text)
	a.update(fmt.Sprintf(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%s}}`, b))
}

func (a *fakeAgent) requestPermission(id int64) {
	a.write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"session/request_permission","params":{`+
		`"sessionId":"s1","toolCall":{"title":"Run: rm -rf junk","kind":"execute"},`+
		`"options":[{"optionId":"ok","name":"Allow","kind":"allow_once"},{"optionId":"no","name":"Deny","kind":"reject_once"}]}}`, id))
}

// rowsByRole collects transcript rows of one role, in order.
func (h *harness) rowsByRole(role string) []browserproto.ChatRow {
	var out []browserproto.ChatRow
	h.run(func() {
		for _, r := range h.m.rows {
			if r.Role == role {
				out = append(out, r)
			}
		}
	})
	return out
}

// --- Tests --------------------------------------------------------------------

func TestLazyStartQueuedPromptAndStreaming(t *testing.T) {
	h := newHarness(t)
	h.onPrompt = func(a *fakeAgent, id int64, text string) {
		if text != "hello agent" {
			t.Errorf("prompt text %q", text)
		}
		a.chunk("Hi ")
		a.chunk("there.")
		a.respond(id, `{"stopReason":"end_turn"}`)
	}

	h.run(func() { h.m.Send("/w", "hello agent") })
	h.waitFor("turn to finish", func() bool { return h.m.status == "ready" && h.m.openRow == 0 })

	h.run(func() {
		if h.m.modelID != "gpt-5-mini" {
			t.Errorf("model %q", h.m.modelID)
		}
		if h.m.cwd != "/w" {
			t.Errorf("cwd %q", h.m.cwd)
		}
	})
	users, agents := h.rowsByRole("user"), h.rowsByRole("agent")
	if len(users) != 1 || users[0].Text != "hello agent" {
		t.Fatalf("user rows: %+v", users)
	}
	if len(agents) != 1 || agents[0].Text != "Hi there." {
		t.Fatalf("agent rows: %+v", agents)
	}
	// The full text must also have been broadcast (row + deltas), not just
	// stored: reassemble what a client would render.
	var got strings.Builder
	for _, msg := range h.emitted() {
		switch v := msg.(type) {
		case browserproto.ChatRowMsg:
			if v.Row.Role == "agent" {
				got.WriteString(v.Row.Text)
			}
		case browserproto.ChatDelta:
			got.WriteString(v.Text)
		}
	}
	if got.String() != "Hi there." {
		t.Fatalf("client-visible agent text %q", got.String())
	}
}

func TestDeltaCoalescing(t *testing.T) {
	h := newHarness(t)
	const chunks = 200
	h.onPrompt = func(a *fakeAgent, id int64, text string) {
		for range chunks {
			a.chunk("x")
		}
		a.respond(id, `{"stopReason":"end_turn"}`)
	}
	h.run(func() { h.m.Send("/w", "go") })
	h.waitFor("turn to finish", func() bool { return h.m.status == "ready" && h.m.openRow == 0 })

	deltas := 0
	total := 0
	for _, msg := range h.emitted() {
		if d, ok := msg.(browserproto.ChatDelta); ok {
			deltas++
			total += len(d.Text)
		}
	}
	if total != chunks {
		t.Fatalf("broadcast %d bytes, want %d", total, chunks)
	}
	// 200 chunks in well under 100ms must coalesce into a handful of deltas —
	// the coalescer is the panel's protection against the per-connection
	// buffer drop, so a regression here is a correctness bug, not a nit.
	if deltas > 10 {
		t.Fatalf("%d deltas for %d chunks — coalescing broken", deltas, chunks)
	}
}

func TestToolRowUpsert(t *testing.T) {
	h := newHarness(t)
	h.onPrompt = func(a *fakeAgent, id int64, text string) {
		a.chunk("Let me look. ")
		a.update(`{"sessionUpdate":"tool_call","toolCallId":"t1","title":"Read main.go","kind":"read","status":"in_progress"}`)
		a.update(`{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed"}`)
		a.chunk("Done.")
		a.respond(id, `{"stopReason":"end_turn"}`)
	}
	h.run(func() { h.m.Send("/w", "look") })
	h.waitFor("turn to finish", func() bool { return h.m.status == "ready" && h.m.openRow == 0 })

	tools := h.rowsByRole("tool")
	if len(tools) != 1 || tools[0].Status != "completed" || tools[0].Text != "Read main.go" {
		t.Fatalf("tool rows: %+v", tools)
	}
	// Interleaving must hold: prose row, then tool row, then a second prose
	// row — not one merged blob.
	agents := h.rowsByRole("agent")
	if len(agents) != 2 || agents[0].Text != "Let me look. " || agents[1].Text != "Done." {
		t.Fatalf("agent rows around the tool call: %+v", agents)
	}
}

func TestPermissionRoundTrip(t *testing.T) {
	h := newHarness(t)
	answered := make(chan string, 1)
	h.onPrompt = func(a *fakeAgent, id int64, text string) {
		a.requestPermission(700)
		resp := <-a.responses // the client's answer to request 700
		raw, _ := json.Marshal(resp.Result)
		answered <- string(raw)
		a.respond(id, `{"stopReason":"end_turn"}`)
	}
	h.run(func() { h.m.Send("/w", "do the thing") })

	var reqID string
	h.waitFor("permission prompt", func() bool {
		for id := range h.m.perms {
			reqID = id
			return true
		}
		return false
	})
	h.run(func() {
		if err := h.m.Permission(reqID, "ok", false); err != nil {
			t.Errorf("Permission: %v", err)
		}
		// A second answer must fail — the prompt is spent.
		if err := h.m.Permission(reqID, "ok", false); err == nil {
			t.Error("second answer should have errored")
		}
	})

	select {
	case raw := <-answered:
		if !strings.Contains(raw, `"selected"`) || !strings.Contains(raw, `"ok"`) {
			t.Fatalf("agent got %s", raw)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent never received the permission answer")
	}
	h.waitFor("turn to finish", func() bool { return h.m.status == "ready" })

	resolved := false
	for _, msg := range h.emitted() {
		if p, ok := msg.(browserproto.ChatPerm); ok && p.Resolved && p.Outcome == "allowed" {
			resolved = true
		}
	}
	if !resolved {
		t.Fatal("no resolved chat_perm broadcast")
	}
}

func TestCancelFlushesPermsAndStops(t *testing.T) {
	h := newHarness(t)
	h.onPrompt = func(a *fakeAgent, id int64, text string) {
		a.requestPermission(701)
		<-a.cancelled
		a.respond(id, `{"stopReason":"cancelled"}`)
	}
	h.run(func() { h.m.Send("/w", "long job") })
	h.waitFor("permission prompt", func() bool { return len(h.m.perms) > 0 })

	h.run(func() { h.m.Cancel() })
	h.waitFor("turn to finish", func() bool { return h.m.status == "ready" })

	var cancelledPerm, stoppedRow bool
	for _, msg := range h.emitted() {
		if p, ok := msg.(browserproto.ChatPerm); ok && p.Resolved && p.Outcome == "cancelled" {
			cancelledPerm = true
		}
		if r, ok := msg.(browserproto.ChatRowMsg); ok && r.Row.Role == "info" && r.Row.Text == "— stopped" {
			stoppedRow = true
		}
	}
	if !cancelledPerm {
		t.Fatal("pending permission not resolved as cancelled")
	}
	if !stoppedRow {
		t.Fatal("no '— stopped' row")
	}
}

// TestCancelWithNonCompliantAgent pins the copilot 1.0.77 quirk: the spec
// says a cancelled turn answers stopReason "cancelled", but copilot answers
// end_turn. The user still gets "— stopped" — cancelSent is the truth.
func TestCancelWithNonCompliantAgent(t *testing.T) {
	h := newHarness(t)
	h.onPrompt = func(a *fakeAgent, id int64, text string) {
		<-a.cancelled
		a.respond(id, `{"stopReason":"end_turn"}`)
	}
	h.run(func() { h.m.Send("/w", "long job") })
	h.waitFor("turn", func() bool { return h.m.status == "turn" })
	h.run(func() { h.m.Cancel() })
	h.waitFor("turn end", func() bool { return h.m.status == "ready" })

	stopped := false
	for _, r := range h.rowsByRole("info") {
		if r.Text == "— stopped" {
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("cancelSent must yield '— stopped' even on stopReason end_turn")
	}
}

func TestAgentDeathAndRestart(t *testing.T) {
	h := newHarness(t)
	h.onPrompt = func(a *fakeAgent, id int64, text string) {
		a.respond(id, `{"stopReason":"end_turn"}`)
	}
	h.run(func() { h.m.Send("/w", "hi") })
	h.waitFor("first turn", func() bool { return h.m.status == "ready" })

	// The agent dies out from under the session.
	h.mu.Lock()
	agent := h.agent
	h.mu.Unlock()
	agent.wmu.Lock()
	_ = agent.w.Close()
	agent.wmu.Unlock()

	h.waitFor("death noticed", func() bool { return h.m.status == "dead" })
	var hint, action bool
	for _, r := range h.rowsByRole("info") {
		if strings.Contains(r.Text, "not signed in") {
			hint = true
		}
		if r.Action != nil && r.Action.Argv[0] == "copilot" {
			action = true
		}
	}
	if !hint || !action {
		t.Fatalf("death rows missing hint/action (hint=%v action=%v)", hint, action)
	}

	// Typing again is the retry gesture: a fresh agent, and the prompt rides
	// the restart.
	h.run(func() { h.m.Send("/w2", "again") })
	h.waitFor("restarted turn", func() bool { return h.m.status == "ready" && h.startCalls == 2 })
	users := h.rowsByRole("user")
	if len(users) != 2 || users[1].Text != "again" {
		t.Fatalf("user rows after restart: %+v", users)
	}
}

func TestHandshakeFailure(t *testing.T) {
	h := newHarness(t)
	h.startErr = errors.New("spawn: no such device")
	h.run(func() { h.m.Send("/w", "hi") })
	h.waitFor("dead", func() bool { return h.m.status == "dead" })
	h.run(func() {
		if !strings.Contains(h.m.detail, "could not start") {
			t.Errorf("detail %q", h.m.detail)
		}
	})
}

func TestMissingBinary(t *testing.T) {
	h := newHarness(t)
	LookPath = func(string) (string, error) { return "", errors.New("not found") }
	h.run(func() { h.m.Send("/w", "hi") })
	h.waitFor("dead", func() bool { return h.m.status == "dead" })
	if h.startCalls != 0 {
		t.Fatal("start must not be attempted without the binary")
	}
	infos := h.rowsByRole("info")
	if len(infos) == 0 || !strings.Contains(infos[0].Text, "not on PATH") {
		t.Fatalf("info rows: %+v", infos)
	}
}

func TestClearResetsEverything(t *testing.T) {
	h := newHarness(t)
	h.onPrompt = func(a *fakeAgent, id int64, text string) {
		a.chunk("answer")
		a.respond(id, `{"stopReason":"end_turn"}`)
	}
	h.run(func() { h.m.Send("/w", "hi") })
	h.waitFor("turn", func() bool { return h.m.status == "ready" })

	h.run(func() { h.m.Clear() })
	h.run(func() {
		if h.m.status != "idle" || len(h.m.rows) != 0 || h.m.client != nil {
			t.Errorf("clear left state: status=%s rows=%d", h.m.status, len(h.m.rows))
		}
	})
	msgs := h.emitted()
	last, ok := msgs[len(msgs)-1].(browserproto.ChatSnapshot)
	if !ok || len(last.Rows) != 0 || last.State.Status != "idle" {
		t.Fatalf("last emit should be the empty snapshot, got %#v", msgs[len(msgs)-1])
	}
}

func TestTranscriptTrim(t *testing.T) {
	h := newHarness(t)
	h.run(func() {
		for i := range transcriptMax + 50 {
			h.m.appendRow(browserproto.ChatRow{Role: "info", Text: fmt.Sprintf("r%d", i)})
		}
	})
	h.run(func() {
		if len(h.m.rows) != transcriptMax {
			t.Errorf("rows %d, want %d", len(h.m.rows), transcriptMax)
		}
		if h.m.rows[0].Text != "r50" {
			t.Errorf("trim must drop from the front, first is %q", h.m.rows[0].Text)
		}
	})
}

func TestSnapshotMidTurn(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	h.onPrompt = func(a *fakeAgent, id int64, text string) {
		a.chunk("thinking")
		a.requestPermission(702)
		<-release
		a.respond(id, `{"stopReason":"end_turn"}`)
	}
	h.run(func() { h.m.Send("/w", "hi") })
	h.waitFor("mid-turn with prompt", func() bool { return len(h.m.perms) > 0 })

	var snap browserproto.ChatSnapshot
	h.run(func() { snap = h.m.Snapshot() })
	if snap.State.Status != "turn" || snap.State.Backend != "Copilot" {
		t.Fatalf("state: %+v", snap.State)
	}
	if len(snap.Perms) != 1 || len(snap.Perms[0].Options) != 2 {
		t.Fatalf("perms: %+v", snap.Perms)
	}
	found := false
	for _, r := range snap.Rows {
		if r.Role == "agent" && strings.Contains(r.Text, "thinking") {
			found = true
		}
	}
	if !found {
		t.Fatalf("mid-stream text missing from snapshot rows: %+v", snap.Rows)
	}
	var reqID string
	h.run(func() {
		for id := range h.m.perms {
			reqID = id
		}
		_ = h.m.Permission(reqID, "ok", false)
	})
	close(release)
	h.waitFor("turn to finish", func() bool { return h.m.status == "ready" })
}
