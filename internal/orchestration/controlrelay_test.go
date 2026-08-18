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

// The control relay carries a whole connection rather than a message pair,
// because the control protocol has a streaming half: one request, an ack, then
// events for as long as the caller stays listening. These hold that shape from
// the daemon's side — open, data both ways, and a close from either end.

func TestWelcomeCarriesTheControlRelayPath(t *testing.T) {
	c := dialHost(t, nil)
	w := handshake(t, c, NewHello())
	if w.ControlSocket == "" {
		t.Fatal("welcome carried no control socket")
	}
	fi, err := os.Stat(w.ControlSocket)
	if err != nil {
		t.Fatalf("stat %s: %v", w.ControlSocket, err)
	}
	// Owner-only, like the orchestrator's own control socket — anything that can
	// open this can run every command the session has, on every host it holds.
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("control relay mode = %o, want 600", perm)
	}
	// Advertising it is not permission: the orchestrator decides per host. The
	// daemon has no way to express, or to know, that decision.
	if !contains(w.Features, FeatureControlRelay) {
		t.Errorf("features = %v, want %q among them", w.Features, FeatureControlRelay)
	}
}

// A connection on the relay becomes an open, its bytes become data, and the
// orchestrator's answer comes back out of the socket.
func TestControlRelayCarriesAConversation(t *testing.T) {
	c := dialHost(t, nil)
	w := handshake(t, c, NewHello())

	client, err := net.Dial("unix", w.ControlSocket)
	if err != nil {
		t.Fatalf("dial the relay: %v", err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))

	typ, payload := readEvent(t, c)
	if typ != MsgControlOpen {
		t.Fatalf("first event = %q, want control_open", typ)
	}
	var open ControlOpen
	if err := json.Unmarshal(payload, &open); err != nil {
		t.Fatalf("decode open: %v", err)
	}

	const req = `{"id":"1","method":"ping"}`
	if _, err := client.Write([]byte(req + "\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	typ, payload = readEvent(t, c)
	if typ != MsgControlData {
		t.Fatalf("event = %q, want control_data", typ)
	}
	var data ControlData
	if err := json.Unmarshal(payload, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data.ID != open.ID {
		t.Errorf("data id = %d, want the open's %d", data.ID, open.ID)
	}
	// Verbatim: the daemon does not parse the control protocol, which is what
	// lets a method added to it work through the relay with no daemon release.
	if got := strings.TrimSpace(string(data.Payload)); got != req {
		t.Fatalf("relayed payload = %q, want the request unchanged", got)
	}

	// Two replies on one connection, because a subscription's ack and events
	// arrive that way and the relay must not assume a single answer.
	if err := WriteMessage(c, NewControlReply(open.ID, []byte("{\"ok\":true}\n"))); err != nil {
		t.Fatalf("send reply: %v", err)
	}
	if err := WriteMessage(c, NewControlReply(open.ID, []byte("{\"event\":\"x\"}\n"))); err != nil {
		t.Fatalf("send event: %v", err)
	}
	br := bufio.NewReader(client)
	for _, want := range []string{`{"ok":true}`, `{"event":"x"}`} {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read reply: %v", err)
		}
		if strings.TrimSpace(line) != want {
			t.Fatalf("client read %q, want %q", strings.TrimSpace(line), want)
		}
	}

	// The orchestrator finishing closes the client's connection.
	if err := WriteMessage(c, NewControlClose(open.ID)); err != nil {
		t.Fatalf("send close: %v", err)
	}
	if _, err := br.ReadString('\n'); err == nil {
		t.Fatal("the relayed connection should have been closed")
	}
}

// The client hanging up is how a subscription is cancelled — a streaming client
// says nothing else — so it has to reach the orchestrator as a close.
func TestControlRelayReportsAClientHangingUp(t *testing.T) {
	c := dialHost(t, nil)
	w := handshake(t, c, NewHello())

	client, err := net.Dial("unix", w.ControlSocket)
	if err != nil {
		t.Fatalf("dial the relay: %v", err)
	}
	typ, payload := readEvent(t, c)
	if typ != MsgControlOpen {
		t.Fatalf("first event = %q, want control_open", typ)
	}
	var open ControlOpen
	_ = json.Unmarshal(payload, &open)

	client.Close()
	typ, payload = readEvent(t, c)
	if typ != MsgControlClose {
		t.Fatalf("event after the client left = %q, want control_close", typ)
	}
	var cl ControlClose
	if err := json.Unmarshal(payload, &cl); err != nil {
		t.Fatalf("decode close: %v", err)
	}
	if cl.ID != open.ID {
		t.Errorf("close id = %d, want %d", cl.ID, open.ID)
	}
}

// With no client attached there is nothing to relay to, so the connection is
// closed rather than left hanging on an answer that cannot come.
func TestControlRelayClosesWhenNoClientIsAttached(t *testing.T) {
	h := NewHost()
	h.FlushInterval = 5 * time.Millisecond
	h.Start(t.Context())
	defer h.Stop()

	client, err := net.Dial("unix", h.controlSocketPath())
	if err != nil {
		t.Fatalf("dial the relay: %v", err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := bufio.NewReader(client).ReadString('\n'); err == nil {
		t.Fatal("a relay connection with no orchestrator attached should be closed")
	}
}

// "-" is how a machine says from its own side that it must never be able to
// drive the session, whatever the orchestrator's config says.
func TestControlRelayCanBeDisabled(t *testing.T) {
	h := NewHost()
	h.ControlSocketPath = "-"
	h.Start(t.Context())
	defer h.Stop()
	if got := h.controlSocketPath(); got != "" {
		t.Fatalf("relay path = %q with the relay disabled", got)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
