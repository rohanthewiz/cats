package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAgent is the far side of an in-memory connection: it reads ndjson
// requests off what the client writes and scripts responses back. Runs its
// handler per received message on the test's goroutine budget.
type fakeAgent struct {
	in  *io.PipeReader // what the client wrote (agent's stdin)
	out *io.PipeWriter // what the agent writes (client reads this)
}

// newPair wires a Client to a fakeAgent over in-memory pipes.
func newPair(t *testing.T,
	onNotify func(string, json.RawMessage),
	onRequest func(string, json.RawMessage) (any, error),
	onExit func(error)) (*Client, *fakeAgent) {
	t.Helper()
	clientReads, agentWrites := io.Pipe()
	agentReads, clientWrites := io.Pipe()
	c := NewClient(clientReads, clientWrites, onNotify, onRequest, onExit)
	return c, &fakeAgent{in: agentReads, out: agentWrites}
}

func (a *fakeAgent) readMsg(t *testing.T, r *bufio.Reader) message {
	t.Helper()
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("agent read: %v", err)
	}
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("agent decode %q: %v", line, err)
	}
	return m
}

func (a *fakeAgent) write(t *testing.T, raw string) {
	t.Helper()
	if _, err := a.out.Write([]byte(raw + "\n")); err != nil {
		t.Fatalf("agent write: %v", err)
	}
}

func TestCallResponse(t *testing.T) {
	c, a := newPair(t, nil, nil, nil)
	defer c.Close()

	done := make(chan error, 1)
	var res struct {
		Ok bool `json:"ok"`
	}
	go func() { done <- c.Call("ping", map[string]any{"x": 1}, &res) }()

	r := bufio.NewReader(a.in)
	m := a.readMsg(t, r)
	if m.Method != "ping" || m.ID == nil {
		t.Fatalf("expected ping request, got %+v", m)
	}
	a.write(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"ok":true}}`, *m.ID))

	if err := <-done; err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.Ok {
		t.Fatalf("result not unmarshalled: %+v", res)
	}
}

func TestCallErrorMapping(t *testing.T) {
	c, a := newPair(t, nil, nil, nil)
	defer c.Close()

	done := make(chan error, 1)
	go func() { done <- c.Call("boom", nil, nil) }()

	r := bufio.NewReader(a.in)
	m := a.readMsg(t, r)
	a.write(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"kaput"}}`, *m.ID))

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "kaput") || !strings.Contains(err.Error(), "-32000") {
		t.Fatalf("error not mapped: %v", err)
	}
}

func TestNotificationDispatch(t *testing.T) {
	got := make(chan string, 1)
	c, a := newPair(t, func(method string, params json.RawMessage) {
		var p struct {
			V string `json:"v"`
		}
		_ = json.Unmarshal(params, &p)
		got <- method + ":" + p.V
	}, nil, nil)
	defer c.Close()

	a.write(t, `{"jsonrpc":"2.0","method":"session/update","params":{"v":"hi"}}`)
	select {
	case s := <-got:
		if s != "session/update:hi" {
			t.Fatalf("got %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification never dispatched")
	}
}

// TestInboundRequestDuringPendingCall is the deadlock regression test: the
// agent sends a request (a permission prompt) while our Call is still
// pending, and the handler blocks until the test releases it. The read loop
// must keep running — answer the request, then deliver the Call response.
func TestInboundRequestDuringPendingCall(t *testing.T) {
	release := make(chan struct{})
	c, a := newPair(t, nil, func(method string, params json.RawMessage) (any, error) {
		<-release // block like a real permission prompt waiting on a user
		return map[string]any{"outcome": "ok"}, nil
	}, nil)
	defer c.Close()

	done := make(chan error, 1)
	go func() { done <- c.CallWithTimeout("session/prompt", nil, nil, 5*time.Second) }()

	r := bufio.NewReader(a.in)
	prompt := a.readMsg(t, r)

	// Agent asks for permission mid-turn.
	a.write(t, `{"jsonrpc":"2.0","id":900,"method":"session/request_permission","params":{}}`)
	close(release)

	// The permission answer must come back even though session/prompt is
	// still unanswered.
	perm := a.readMsg(t, r)
	if perm.ID == nil || *perm.ID != 900 || perm.Error != nil {
		t.Fatalf("expected reply to request 900, got %+v", perm)
	}

	// Now finish the turn; the pending Call must resolve.
	a.write(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"stopReason":"end_turn"}}`, *prompt.ID))
	if err := <-done; err != nil {
		t.Fatalf("prompt call: %v", err)
	}
}

func TestInboundRequestErrorBecomesJSONRPCError(t *testing.T) {
	c, a := newPair(t, nil, func(method string, params json.RawMessage) (any, error) {
		return nil, fmt.Errorf("not handled: %s", method)
	}, nil)
	defer c.Close()

	a.write(t, `{"jsonrpc":"2.0","id":7,"method":"fs/read_text_file","params":{}}`)
	r := bufio.NewReader(a.in)
	m := a.readMsg(t, r)
	if m.Error == nil || m.Error.Code != -32601 || !strings.Contains(m.Error.Message, "fs/read_text_file") {
		t.Fatalf("expected -32601 error reply, got %+v", m)
	}
}

func TestNoHandlerDeclinesRequests(t *testing.T) {
	c, a := newPair(t, nil, nil, nil)
	defer c.Close()

	a.write(t, `{"jsonrpc":"2.0","id":3,"method":"terminal/create","params":{}}`)
	r := bufio.NewReader(a.in)
	m := a.readMsg(t, r)
	if m.Error == nil || m.Error.Code != -32601 {
		t.Fatalf("expected decline, got %+v", m)
	}
}

// TestHugeLine exercises the framing on a single record far past
// bufio.Scanner's 64KB token cap, both directions.
func TestHugeLine(t *testing.T) {
	c, a := newPair(t, nil, nil, nil)
	defer c.Close()

	big := strings.Repeat("x", 1<<20) // 1 MiB of payload
	done := make(chan error, 1)
	var res struct {
		Text string `json:"text"`
	}
	go func() { done <- c.CallWithTimeout("echo", map[string]string{"text": big}, &res, 10*time.Second) }()

	r := bufio.NewReader(a.in)
	m := a.readMsg(t, r)
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil || len(p.Text) != len(big) {
		t.Fatalf("request payload mangled: err=%v len=%d", err, len(p.Text))
	}
	reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *m.ID,
		"result": map[string]string{"text": big}})
	a.write(t, string(reply))

	if err := <-done; err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Text) != len(big) {
		t.Fatalf("response payload mangled: len=%d", len(res.Text))
	}
}

func TestBlankLinesSkipped(t *testing.T) {
	got := make(chan string, 1)
	c, a := newPair(t, func(method string, _ json.RawMessage) { got <- method }, nil, nil)
	defer c.Close()

	if _, err := a.out.Write([]byte("\n\n  \n{\"jsonrpc\":\"2.0\",\"method\":\"tick\"}\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m != "tick" {
			t.Fatalf("got %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("message after blank lines never arrived")
	}
}

// TestEOFFailsPendingAndFiresOnExit: closing the agent's write side must
// resolve in-flight Calls immediately (not after their timeout) and fire
// onExit exactly once.
func TestEOFFailsPendingAndFiresOnExit(t *testing.T) {
	exits := make(chan error, 2)
	c, a := newPair(t, nil, nil, func(err error) { exits <- err })
	defer c.Close()

	done := make(chan error, 1)
	go func() { done <- c.CallWithTimeout("hang", nil, nil, 30*time.Second) }()

	r := bufio.NewReader(a.in)
	a.readMsg(t, r) // ensure the request is in flight
	_ = a.out.Close()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("expected connection-closed error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending call not failed on EOF")
	}
	select {
	case err := <-exits:
		if err != nil {
			t.Fatalf("clean EOF should report nil, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onExit never fired")
	}
	select {
	case <-exits:
		t.Fatal("onExit fired twice")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCallAfterCloseFailsFast(t *testing.T) {
	c, _ := newPair(t, nil, nil, nil)
	c.Close()
	c.Close() // idempotent

	start := time.Now()
	err := c.Call("ping", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("expected closed error, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("Call after Close should fail fast, not wait for a timeout")
	}
}

func TestConcurrentCallsCorrelate(t *testing.T) {
	c, a := newPair(t, nil, nil, nil)
	defer c.Close()

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	vals := make([]int, n)
	for i := range n {
		wg.Go(func() {
			var res struct {
				N int `json:"n"`
			}
			errs[i] = c.CallWithTimeout("mirror", map[string]int{"n": i}, &res, 5*time.Second)
			vals[i] = res.N
		})
	}

	// Collect all requests, then answer them in reverse order — correlation
	// by id must route each response to its caller regardless.
	r := bufio.NewReader(a.in)
	reqs := make([]message, n)
	for i := range n {
		reqs[i] = a.readMsg(t, r)
	}
	for i := n - 1; i >= 0; i-- {
		var p struct {
			N int `json:"n"`
		}
		_ = json.Unmarshal(reqs[i].Params, &p)
		a.write(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"n":%d}}`, *reqs[i].ID, p.N))
	}

	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if vals[i] != i {
			t.Fatalf("call %d got %d — responses cross-wired", i, vals[i])
		}
	}
}

func TestBuildEnv(t *testing.T) {
	base := []string{"PATH=/bin", "GH_TOKEN=secret", "HOME=/u", "GITHUB_TOKEN=also"}
	got := buildEnv(base, []string{"EXTRA=1"}, []string{"GH_TOKEN", "GITHUB_TOKEN"})
	want := []string{"PATH=/bin", "HOME=/u", "EXTRA=1"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}
