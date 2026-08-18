package ctlproto

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"time"
)

// DefaultSocket is the control socket path used when neither a flag nor the
// CATS_CONTROL_SOCKET env var overrides it. It mirrors the cathost socket
// convention (/tmp/cats-*.sock) so server and client agree by default.
const DefaultSocket = "/tmp/cats-control.sock"

// SocketEnvVar overrides the control socket path for both server and client.
const SocketEnvVar = "CATS_CONTROL_SOCKET"

// SocketNone is the value of SocketEnvVar meaning "this process can reach no
// control socket" — distinct from the variable being unset, which falls through
// to DefaultSocket.
//
// It exists because "unset" is the wrong answer in one real case: a pane running
// on a remote cathost that the session has not been told to trust with the
// control API. catway cannot export its own path there (it names a file in a
// filesystem the pane cannot see), and unless that host has the relay enabled
// there is nothing on that machine to dial. Leaving the variable alone is worse
// than it sounds — the pane inherits whatever the CATHOST's environment had,
// which on a box where somebody launched it from inside another cats session is
// a DIFFERENT session's control socket. Falling back to DefaultSocket is the
// same hazard by a more predictable route. Either way an in-pane catctl would
// drive the wrong terminals; this makes it say so instead.
//
// A host with control_relay enabled gets its cathost's relay path here instead,
// and the API works as it does locally.
const SocketNone = "-"

// ResolveSocket picks the control socket path with the standard precedence: an
// explicit non-empty override (a CLI flag) wins; else CATS_CONTROL_SOCKET; else
// DefaultSocket. Server and client both call it so they agree by default.
//
// Returns "" for SocketNone, which Call reports as an error naming the reason.
func ResolveSocket(override string) string {
	if override == SocketNone {
		return ""
	}
	if override != "" {
		return override
	}
	switch v := os.Getenv(SocketEnvVar); v {
	case "":
		return DefaultSocket
	case SocketNone:
		return ""
	default:
		return v
	}
}

// Call dials the control socket, sends one Request, and returns the server's
// Response. It is the whole client transport: connect, write one framed request,
// read one framed response, close. timeout bounds the dial and the round trip;
// use 0 for no deadline.
func Call(socket string, req Request, timeout time.Duration) (Response, error) {
	if socket == "" {
		// Only ResolveSocket produces this, and only from SocketNone. Naming the
		// variable is the whole point: "connection refused" would send someone
		// looking for a dead server instead of telling them this pane is on a
		// machine the session's control socket does not reach.
		return Response{}, fmt.Errorf("no control socket reachable from here (%s=%s): "+
			"this pane runs on a remote cathost that the session does not relay the "+
			"control API to (set control_relay on that host to allow it)",
			SocketEnvVar, SocketNone)
	}
	dialTimeout := timeout
	if dialTimeout == 0 {
		dialTimeout = 5 * time.Second // still bound the dial even with no round-trip deadline
	}
	conn, err := net.DialTimeout("unix", socket, dialTimeout)
	if err != nil {
		return Response{}, fmt.Errorf("dial control socket %s: %w", socket, err)
	}
	defer conn.Close()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	if err := writeMessage(conn, req); err != nil {
		return Response{}, fmt.Errorf("send request: %w", err)
	}
	resp, err := readResponse(bufio.NewReader(conn))
	if err != nil {
		return Response{}, fmt.Errorf("read response: %w", err)
	}
	return resp, nil
}
