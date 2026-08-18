//go:build ghostty

package orchestration

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// The hook relay exists because a pane on another machine used to be told the
// ORCHESTRATOR's hook socket path — a file in a filesystem it cannot see, and on
// a box that runs cats itself, a different server's socket. These hold the two
// halves of the fix: the daemon advertises a socket of its own, and what arrives
// there is carried across the seam verbatim and answered by the orchestrator.

func TestWelcomeCarriesTheHookRelayPath(t *testing.T) {
	c := dialHost(t, nil)
	w := handshake(t, c, NewHello())
	if w.HookSocket == "" {
		t.Fatal("welcome carried no hook socket; remote panes would get no hook environment")
	}
	fi, err := os.Stat(w.HookSocket)
	if err != nil {
		t.Fatalf("stat %s: %v", w.HookSocket, err)
	}
	// Owner-only: the path is the capability, and anything that can open it can
	// move an agent's state on the orchestrator's model.
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("hook relay mode = %o, want 600", perm)
	}
}

// One request in on the daemon's socket, one report across the seam, one reply
// back, and the hook client reads it. The daemon parses none of it.
func TestHookRelayCarriesRequestAndReply(t *testing.T) {
	c := dialHost(t, nil)
	w := handshake(t, c, NewHello())

	hook, err := net.Dial("unix", w.HookSocket)
	if err != nil {
		t.Fatalf("dial the relay: %v", err)
	}
	defer hook.Close()
	_ = hook.SetDeadline(time.Now().Add(10 * time.Second))
	const request = `{"id":"h1","method":"pane.report_agent","params":{"pane_id":"w1:p1","state":"working"}}`
	if _, err := hook.Write([]byte(request + "\n")); err != nil {
		t.Fatalf("write hook request: %v", err)
	}

	typ, payload := readEvent(t, c)
	if typ != MsgHookReport {
		t.Fatalf("event = %q, want hook_report", typ)
	}
	var rep HookReport
	if err := json.Unmarshal(payload, &rep); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	// Verbatim: the daemon does not decode the hook API and must not start,
	// or the next field added to it would need a daemon release too.
	if got := strings.TrimSpace(string(rep.Payload)); got != request {
		t.Fatalf("relayed payload = %q, want the request unchanged", got)
	}

	const reply = `{"id":"h1","result":{"type":"ok"}}`
	if err := WriteMessage(c, NewHookReply(rep.ID, []byte(reply+"\n"))); err != nil {
		t.Fatalf("send reply: %v", err)
	}
	line, err := bufio.NewReader(hook).ReadString('\n')
	if err != nil {
		t.Fatalf("read hook reply: %v", err)
	}
	if strings.TrimSpace(line) != reply {
		t.Fatalf("hook read %q, want %q", strings.TrimSpace(line), reply)
	}
}

// A report nobody is attached to answer is closed rather than answered. The
// hooks ignore replies, and fabricating an "ok" for a report that went nowhere
// would tell the CLI equivalents their transition landed.
func TestHookRelayClosesWhenNoClientIsAttached(t *testing.T) {
	h := NewHost()
	h.FlushInterval = 5 * time.Millisecond
	h.Start(t.Context())
	defer h.Stop()

	hook, err := net.Dial("unix", h.hookSocketPath())
	if err != nil {
		t.Fatalf("dial the relay: %v", err)
	}
	defer hook.Close()
	_ = hook.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := hook.Write([]byte("{\"id\":\"x\",\"method\":\"pane.report_agent\"}\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := bufio.NewReader(hook).ReadString('\n'); err == nil {
		t.Fatal("a report with no client attached should end in a close, not a reply")
	}
}

// The socket is the daemon's for its lifetime and is removed with it: panes
// outlive a reconnect, and their environment cannot be rewritten afterwards, so
// a path that moved per connection would strand every surviving pane's hooks.
func TestHookRelayPathOutlivesASessionAndIsCleanedUp(t *testing.T) {
	h := NewHost()
	h.FlushInterval = 5 * time.Millisecond
	h.Start(t.Context())
	path := h.hookSocketPath()
	if path == "" {
		t.Fatal("no relay socket")
	}

	// Serially, the way the daemon's own accept loop attaches clients: Attach
	// clears the session's outbound sink on the way out, so overlapping the two
	// would let the first one's teardown swallow the second one's welcome.
	attach := func(step string) Welcome {
		t.Helper()
		server, client := net.Pipe()
		done := make(chan struct{})
		go func() { defer close(done); _ = h.Attach(t.Context(), server) }()
		w := handshakeOn(t, client)
		client.Close()
		<-done
		if w.HookSocket != path {
			t.Fatalf("%s welcome path = %q, want the daemon's %q", step, w.HookSocket, path)
		}
		return w
	}
	attach("first")
	// A second client is told the same path — the panes the first one created
	// are still running with it in their environment.
	attach("second")

	h.Stop()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("%s survived Stop", path)
	}
}

// "-" is how an operator says a daemon's panes must report nowhere. No socket,
// and a welcome that advertises none, so the orchestrator injects no hook
// environment at all rather than a path that cannot work.
func TestHookRelayCanBeDisabled(t *testing.T) {
	h := NewHost()
	h.HookSocketPath = "-"
	h.Start(t.Context())
	defer h.Stop()
	if got := h.hookSocketPath(); got != "" {
		t.Fatalf("relay path = %q with the relay disabled", got)
	}
}

// handshakeOn is handshake for a connection the test drives itself.
func handshakeOn(t *testing.T, c net.Conn) Welcome {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	return handshake(t, c, NewHello())
}
