package ctlproto

import (
	"strings"
	"testing"
)

// SocketNone is how a pane on a remote cathost is told there is nothing to
// dial. It must be distinguishable from the variable being unset, which falls
// through to the conventional path — on a machine that runs cats, that path is
// somebody else's live session.
func TestResolveSocketNone(t *testing.T) {
	t.Setenv(SocketEnvVar, "")
	if got := ResolveSocket(""); got != DefaultSocket {
		t.Fatalf("unset resolves to %q, want the default", got)
	}
	if got := ResolveSocket(SocketNone); got != "" {
		t.Fatalf("explicit none resolves to %q, want empty", got)
	}
	t.Setenv(SocketEnvVar, SocketNone)
	if got := ResolveSocket(""); got != "" {
		t.Fatalf("env none resolves to %q, want empty", got)
	}
	// A real flag still wins over a disabled environment: the operator on that
	// machine may know about a socket the session did not.
	if got := ResolveSocket("/tmp/x.sock"); got != "/tmp/x.sock" {
		t.Fatalf("flag override = %q", got)
	}

	// And the refusal names the variable, so nobody goes looking for a dead
	// server: "connection refused" would be the wrong diagnosis entirely.
	_, err := Call("", Request{}, 0)
	if err == nil || !strings.Contains(err.Error(), SocketEnvVar) {
		t.Fatalf("Call with no socket: %v", err)
	}
}
