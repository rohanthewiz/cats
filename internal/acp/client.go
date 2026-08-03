// Package acp is a JSON-RPC 2.0 client speaking the Agent Client Protocol's
// wire dialect: newline-delimited JSON over a child process's stdio, with the
// client as the spawning side. Ported from ced's internal/lsp transport with
// the Content-Length (LSP) framing dropped — cats has no LSP consumer, and a
// dead dialect is worse than none.
//
//	owner loop ──Call/Notify──► Client ──stdin──► agent (copilot --acp, …)
//	     ▲                         │
//	     │ onNotify / onRequest    │◄──stdout── readLoop goroutine
//	     └── callbacks post back; only the owner's loop touches shared state
//
// Thread model: Call and Notify are safe from any goroutine (writes are
// mutex-serialised). The read loop runs on its own goroutine and calls
// onNotify / resolves pending Calls from there — so onNotify must never touch
// shared state directly; hand off to the owner's event loop instead. onRequest
// invocations each get their OWN goroutine (see respondToServer) because ACP
// agents send the client real requests — permission prompts — that block on a
// human while streamed notifications keep arriving.
package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// callTimeout bounds how long a Call waits for the agent's response. ACP
// handshake calls (initialize, session/new) can stall on a keychain unlock
// prompt, so this is far looser than an LSP's five seconds; session/prompt
// legitimately runs for minutes and must use CallWithTimeout with a turn
// budget instead.
const callTimeout = 30 * time.Second

// message is the JSON-RPC envelope, used for both directions. Which fields
// are set determines the shape: ID+Method = request, ID+Result/Error =
// response, Method alone = notification.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *respError      `json:"error,omitempty"`
}

// respError is the JSON-RPC error object of a failed response.
type respError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Client is a JSON-RPC connection to one ACP agent process.
type Client struct {
	writeMu sync.Mutex
	w       io.Writer
	r       *bufio.Reader

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan *message
	closed  bool

	// onNotify receives agent→client notifications (method + raw params).
	// Called on the read-loop goroutine — implementations must hand off to
	// their own event loop, not mutate shared state.
	onNotify func(method string, params json.RawMessage)

	// onRequest answers agent→client REQUESTS (id + method): its return value
	// is marshalled into the response, or its error into a JSON-RPC error
	// object. Each invocation runs on its OWN goroutine (never the read loop),
	// so a handler may block while it waits for an answer — a permission
	// prompt waits on the user — without stalling notification delivery or the
	// pending session/prompt response. It still must not mutate owner state
	// directly; post events and wait on a reply channel.
	onRequest func(method string, params json.RawMessage) (any, error)

	// onExit fires once when the read loop ends (agent exited, pipe closed,
	// or protocol error). Also called from the read-loop goroutine.
	onExit func(err error)

	// cmd is the spawned agent process when the client came from Start; nil
	// for clients built over arbitrary pipes (tests).
	cmd *exec.Cmd
}

// NewClient wraps an existing reader/writer pair (the agent's stdout / stdin)
// and starts the read loop. Split from Start so tests can drive the protocol
// over in-memory pipes without spawning a process.
func NewClient(r io.Reader, w io.Writer,
	onNotify func(string, json.RawMessage),
	onRequest func(string, json.RawMessage) (any, error),
	onExit func(error)) *Client {
	c := &Client{
		w:         w,
		r:         bufio.NewReader(r),
		pending:   map[int64]chan *message{},
		onNotify:  onNotify,
		onRequest: onRequest,
		onExit:    onExit,
	}
	go c.readLoop()
	return c
}

// Start launches the agent binary with args in dir and returns a Client wired
// to its stdio. The caller should have verified the binary exists
// (exec.LookPath) — a missing binary errors here too, but checking first lets
// the caller shape a friendlier "not installed" message than an exec error.
//
// env holds extra "K=V" entries appended to the inherited environment.
// dropEnv names variables STRIPPED from the inherited environment before the
// extras are appended: an agent's credentials live in its own store, and a
// stray token in our environment (GH_TOKEN for copilot) would silently
// override that store with a different identity — a failure that reads as a
// broken agent rather than a wrong account.
func Start(dir, bin string, args, env, dropEnv []string,
	onNotify func(string, json.RawMessage),
	onRequest func(string, json.RawMessage) (any, error),
	onExit func(error)) (*Client, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = buildEnv(os.Environ(), env, dropEnv)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Unlike ced (whose tty tcell owns), catway has a real log — and the
	// agent's stderr is the only diagnostic when a handshake dies before the
	// protocol starts. Drain it line by line rather than inheriting the fd so
	// each line lands as one tagged log record.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go drainStderr(bin, stderr)
	c := NewClient(stdout, stdin, onNotify, onRequest, onExit)
	c.cmd = cmd
	return c, nil
}

// buildEnv is Start's environment assembly, split out for testing: base minus
// dropEnv names, plus extra entries.
func buildEnv(base, extra, dropEnv []string) []string {
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(dropEnv, name) {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}

// drainStderr copies the agent's stderr into the log, one line per record.
// Scanner truncation is acceptable here (unlike the protocol reader): a log
// line over 64KB has no diagnostic value past its prefix.
func drainStderr(bin string, r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			log.Printf("acp: %s stderr: %s", bin, line)
		}
	}
}

// Call sends a request and blocks until the response arrives, then unmarshals
// its result into result (skipped when result is nil). Times out after
// callTimeout so a wedged agent can't hang the calling goroutine forever.
// Safe from any goroutine.
func (c *Client) Call(method string, params, result any) error {
	return c.CallWithTimeout(method, params, result, callTimeout)
}

// CallWithTimeout is Call with an explicit response deadline. It exists for
// requests that legitimately outlive the default budget — session/prompt
// blocks for the whole agent turn, which takes minutes. Callers that can't
// say why they need longer should use Call.
func (c *Client) CallWithTimeout(method string, params, result any, timeout time.Duration) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("acp: connection closed")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan *message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.send(&message{JSONRPC: "2.0", ID: &id, Method: method, Params: marshalParams(params)}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}

	select {
	case resp := <-ch:
		if resp == nil {
			return fmt.Errorf("acp: connection closed")
		}
		if resp.Error != nil {
			return fmt.Errorf("acp: %s: %s (code %d)", method, resp.Error.Message, resp.Error.Code)
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	case <-time.After(timeout):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("acp: %s timed out", method)
	}
}

// Notify sends a fire-and-forget notification. Safe from any goroutine.
func (c *Client) Notify(method string, params any) error {
	return c.send(&message{JSONRPC: "2.0", Method: method, Params: marshalParams(params)})
}

// Close tears the connection down: close stdin (an ACP agent exits on EOF),
// then kill after a grace period so a deaf agent can't outlive its owner.
// Idempotent.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	if wc, ok := c.w.(io.Closer); ok {
		_ = wc.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		// Reap on a goroutine so Close never blocks on a slow exit; kill
		// after a grace period if the stdin EOF wasn't enough.
		proc := c.cmd
		go func() {
			done := make(chan struct{})
			go func() { _ = proc.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = proc.Process.Kill()
				<-done
			}
		}()
	}
}

// send frames and writes one message: exactly one JSON object, one line.
// Serialised by writeMu so concurrent Calls/Notifies — and out-of-order
// onRequest replies — can't interleave their bytes.
func (c *Client) send(m *message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// json.Marshal never emits a raw newline inside an object, so body+"\n"
	// is exactly one well-formed ndjson record.
	if _, err := c.w.Write(body); err != nil {
		return err
	}
	_, err = c.w.Write([]byte("\n"))
	return err
}

// marshalParams pre-encodes params so the envelope marshal can't fail
// halfway. nil params stay nil (the field is omitempty).
func marshalParams(params any) json.RawMessage {
	if params == nil {
		return nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		// Params are always our own structs; a marshal failure is a
		// programming error. Sending null keeps the wire valid.
		return json.RawMessage("null")
	}
	return b
}

// readLoop drains agent messages until the pipe closes, routing each to the
// pending Call it answers, the notification callback, or the request handler.
func (c *Client) readLoop() {
	var loopErr error
	for {
		m, err := readLineMessage(c.r)
		if err != nil {
			if err != io.EOF {
				loopErr = err
			}
			break
		}
		switch {
		case m.ID != nil && m.Method != "":
			c.respondToServer(m)
		case m.ID != nil:
			c.mu.Lock()
			ch := c.pending[*m.ID]
			delete(c.pending, *m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.Method != "":
			if c.onNotify != nil {
				c.onNotify(m.Method, m.Params)
			}
		}
	}

	// Connection over — fail every in-flight Call so nothing blocks until its
	// timeout, and let the owner know.
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- nil
	}
	c.mu.Unlock()
	if c.onExit != nil {
		c.onExit(loopErr)
	}
}

// respondToServer answers an agent→client request. Each handler invocation
// gets its own goroutine: ACP request handlers legitimately block for a long
// time (a permission prompt waits on the user's answer), and the read loop
// must keep draining streamed notifications and Call responses meanwhile —
// this is the guard against the classic ACP deadlock where a blocked
// session/prompt and a blocked request handler wait on each other. JSON-RPC
// correlates responses by id, so replying out of order is legal — and send is
// writeMu-serialised, so concurrent replies can't interleave bytes.
func (c *Client) respondToServer(m *message) {
	if c.onRequest == nil {
		// No handler installed: decline honestly rather than answer null —
		// an ACP agent's requests carry real semantics a null can't satisfy.
		_ = c.send(&message{JSONRPC: "2.0", ID: m.ID,
			Error: &respError{Code: -32601, Message: "method not handled: " + m.Method}})
		return
	}
	go func() {
		res, err := c.onRequest(m.Method, m.Params)
		if err != nil {
			// -32601 (method not found) is the honest default for a request
			// this client declines to handle; the message says why.
			_ = c.send(&message{JSONRPC: "2.0", ID: m.ID,
				Error: &respError{Code: -32601, Message: err.Error()}})
			return
		}
		raw, _ := json.Marshal(res)
		_ = c.send(&message{JSONRPC: "2.0", ID: m.ID, Result: raw})
	}()
}

// readLineMessage parses one newline-delimited JSON-RPC message. Blank lines
// are skipped (harmless keep-alives some agents emit); a line that isn't
// valid JSON is a hard protocol error — there's no way to resynchronise once
// a record boundary is wrong.
//
// bufio.Reader.ReadBytes grows its buffer without bound, which is the point:
// ACP agents routinely emit single lines past 64KB (file contents inside tool
// results), so this must never be "simplified" to a bufio.Scanner and its
// fixed token limit.
func readLineMessage(r *bufio.Reader) (*message, error) {
	for {
		line, err := r.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if err != nil {
				return nil, err // EOF (or pipe error) with nothing pending
			}
			continue
		}
		// A final record may arrive without its trailing newline right
		// before EOF — parse what we have rather than dropping it.
		var m message
		if uerr := json.Unmarshal(trimmed, &m); uerr != nil {
			return nil, fmt.Errorf("acp: bad ndjson message: %w", uerr)
		}
		return &m, nil
	}
}
