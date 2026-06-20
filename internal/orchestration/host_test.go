//go:build ghostty

package orchestration

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// startTestHost spins up a Host serving one end of an in-memory pipe and returns
// the client end, with the Hello/Welcome handshake already done.
func startTestHost(t *testing.T) net.Conn {
	t.Helper()
	serverEnd, clientEnd := net.Pipe()

	h := NewHost()
	h.FlushInterval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	go h.Serve(ctx, serverEnd)

	t.Cleanup(func() {
		cancel()
		clientEnd.Close()
	})

	// Overall safety deadline so a stuck test fails instead of hanging.
	_ = clientEnd.SetDeadline(time.Now().Add(15 * time.Second))

	if err := WriteMessage(clientEnd, NewHello()); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	typ, _ := readEvent(t, clientEnd)
	if typ != MsgWelcome {
		t.Fatalf("first event = %q, want welcome", typ)
	}
	return clientEnd
}

func readEvent(t *testing.T, c net.Conn) (MessageType, []byte) {
	t.Helper()
	typ, payload, err := ReadMessage(c)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	return typ, payload
}

func frameText(f *Frame) string {
	var b strings.Builder
	for _, c := range f.Cells {
		b.WriteString(c.Symbol)
	}
	return b.String()
}

func TestHostRunsCommandAndReportsFrames(t *testing.T) {
	c := startTestHost(t)

	cp := NewCreatePane(1, 40, 5)
	cp.Command = "/bin/sh"
	cp.Args = []string{"-c", "printf HELLO"}
	if err := WriteMessage(c, cp); err != nil {
		t.Fatalf("create_pane: %v", err)
	}

	var transcript strings.Builder
	for {
		typ, payload := readEvent(t, c)
		switch typ {
		case MsgPaneFrame:
			var pf PaneFrame
			if err := json.Unmarshal(payload, &pf); err != nil {
				t.Fatalf("decode pane_frame: %v", err)
			}
			if pf.PaneID != 1 {
				t.Fatalf("frame for pane %d, want 1", pf.PaneID)
			}
			transcript.WriteString(frameText(pf.Frame))
		case MsgPaneExited:
			var pe PaneExited
			if err := json.Unmarshal(payload, &pe); err != nil {
				t.Fatalf("decode pane_exited: %v", err)
			}
			if pe.PaneID != 1 {
				t.Fatalf("exited for pane %d, want 1", pe.PaneID)
			}
			if pe.ExitCode != 0 {
				t.Errorf("exit code = %d, want 0", pe.ExitCode)
			}
			if !strings.Contains(transcript.String(), "HELLO") {
				t.Fatalf("never saw HELLO in frames; transcript=%q", transcript.String())
			}
			return
		case MsgError:
			t.Fatalf("unexpected error event: %s", string(payload))
		}
	}
}

func TestHostInputEchoAndClose(t *testing.T) {
	c := startTestHost(t)

	cp := NewCreatePane(2, 40, 5)
	cp.Command = "/bin/cat" // PTY line discipline echoes input back
	if err := WriteMessage(c, cp); err != nil {
		t.Fatalf("create_pane: %v", err)
	}

	if err := WriteMessage(c, NewInput(2, []byte("ping\r"))); err != nil {
		t.Fatalf("input: %v", err)
	}

	// Read frames until the echoed input shows up.
	sawEcho := false
	deadline := time.Now().Add(10 * time.Second)
	for !sawEcho && time.Now().Before(deadline) {
		typ, payload := readEvent(t, c)
		switch typ {
		case MsgPaneFrame:
			var pf PaneFrame
			if err := json.Unmarshal(payload, &pf); err != nil {
				t.Fatalf("decode pane_frame: %v", err)
			}
			if strings.Contains(frameText(pf.Frame), "ping") {
				sawEcho = true
			}
		case MsgError:
			t.Fatalf("unexpected error event: %s", string(payload))
		}
	}
	if !sawEcho {
		t.Fatal("never saw echoed input 'ping'")
	}

	// Close the pane; expect a pane_exited for it.
	if err := WriteMessage(c, NewClosePane(2)); err != nil {
		t.Fatalf("close_pane: %v", err)
	}
	for {
		typ, payload := readEvent(t, c)
		if typ == MsgPaneExited {
			var pe PaneExited
			if err := json.Unmarshal(payload, &pe); err != nil {
				t.Fatalf("decode pane_exited: %v", err)
			}
			if pe.PaneID != 2 {
				t.Fatalf("exited for pane %d, want 2", pe.PaneID)
			}
			return
		}
	}
}
